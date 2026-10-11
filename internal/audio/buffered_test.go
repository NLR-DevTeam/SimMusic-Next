package audio

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
)

type stalledDecoder struct {
	position int
	reads    int
	blocked  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (d *stalledDecoder) Stream(samples [][2]float64) (int, bool) {
	d.reads++
	if d.reads == 2 {
		close(d.blocked)
		<-d.release
	}
	for i := range samples {
		samples[i] = [2]float64{float64(d.position), float64(d.position)}
		d.position++
	}
	return len(samples), true
}
func (d *stalledDecoder) Len() int                { return 1000000 }
func (d *stalledDecoder) Position() int           { return d.position }
func (d *stalledDecoder) Seek(position int) error { d.position = position; return nil }
func (d *stalledDecoder) Err() error              { return nil }
func (d *stalledDecoder) Close() error            { d.once.Do(func() { close(d.release) }); return nil }

func waitBuffered(t *testing.T, b *bufferedDecoder, minimum int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		count := b.count
		b.mu.Unlock()
		if count >= minimum {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("PCM producer did not fill")
}

func TestPCMStallDoesNotBlockSpeakerControlsSeekOrClose(t *testing.T) {
	d := &stalledDecoder{blocked: make(chan struct{}), release: make(chan struct{})}
	b := newBufferedDecoder(d, d, beep.SampleRate(48000))
	defer b.Close()
	select {
	case <-d.blocked:
	case <-time.After(time.Second):
		t.Fatal("decoder did not stall")
	}
	e := &Engine{dec: b, format: beep.Format{SampleRate: 48000}, res: beep.ResampleRatio(3, 1, b), playing: true, gain: 1, volume: .8, speed: 1, eq: NewEqualizer(48000)}
	finished := make(chan struct{})
	go func() {
		samples := make([][2]float64, 4096)
		for range 4 {
			e.Stream(samples)
		}
		e.SetVolume(.5)
		e.SetPlaying(false)
		if err := e.Seek(2); err != nil {
			t.Errorf("seek: %v", err)
		}
		e.Position()
		e.Unload()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("blocked decoder stalled speaker or UI controls")
	}
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("closing did not unblock decoder worker")
	}
	if e.Volume() != .5 {
		t.Fatal("volume changed without user request")
	}
}

func TestPCMSeekDiscardsAnInflightRead(t *testing.T) {
	d := &stalledDecoder{blocked: make(chan struct{}), release: make(chan struct{})}
	b := newBufferedDecoder(d, d, beep.SampleRate(48000))
	defer b.Close()
	<-d.blocked
	if err := b.Seek(50000); err != nil {
		t.Fatal(err)
	}
	d.once.Do(func() { close(d.release) })
	waitBuffered(t, b, 2048)
	samples := make([][2]float64, 32)
	n, ok := b.Stream(samples)
	if !ok || n != len(samples) {
		t.Fatalf("stream: %d %v", n, ok)
	}
	for i, sample := range samples {
		if sample[0] != float64(50000+i) {
			t.Fatalf("stale PCM at %d: %v", i, sample)
		}
	}
	if b.Position() != 50032 {
		t.Fatalf("audible position includes pre-read samples: %d", b.Position())
	}
}

func TestHTTPReaderCloseUnblocksReadAndUnknownLengthSeek(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	for _, seek := range []bool{false, true} {
		h, err := OpenHTTP(server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			if seek {
				_, err = h.Seek(0, io.SeekEnd)
			} else {
				_, err = h.Read(make([]byte, 1))
			}
			done <- err
		}()
		time.Sleep(10 * time.Millisecond)
		h.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("closed HTTP source is still waiting for network")
		}
	}
}
