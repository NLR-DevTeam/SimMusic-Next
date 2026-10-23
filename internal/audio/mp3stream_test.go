package audio

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gopxl/beep/v2/mp3"
)

// An MP3 over HTTP is decoded from its first frames, with the length and samples go-mp3 gives the whole file.
func TestMP3StreamMatchesWholeFileDecoder(t *testing.T) {
	const name = "../../dev_reference/风吹一夏.mp3"
	if _, err := os.Stat(name); err != nil {
		t.Skip("reference MP3 not present")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, name) }))
	defer server.Close()
	reader, err := OpenHTTP(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stream, format, err := decodeMP3Stream(reader, reader.size())
	if err != nil {
		t.Fatal(err)
	}
	file, _ := os.Open(name)
	whole, wholeFormat, err := mp3.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	defer whole.Close()
	if format.SampleRate != wholeFormat.SampleRate {
		t.Fatalf("sample rate %v, want %v", format.SampleRate, wholeFormat.SampleRate)
	}
	if diff := math.Abs(float64(stream.Len()-whole.Len())) / float64(whole.Len()); diff > .01 {
		t.Fatalf("length %d, want about %d", stream.Len(), whole.Len())
	}
	// The same audio from the start (the Xing frame of silence aside, which go-mp3 decodes as audio).
	got, want := make([][2]float64, 4096), make([][2]float64, 4096+1152)
	stream.Stream(got)
	whole.Stream(want)
	shift := 0
	if want[0] == ([2]float64{}) && got[2000] != want[2000] {
		shift = 1152
	}
	for i := 1000; i < 3000; i++ {
		if math.Abs(got[i][0]-want[i+shift][0]) > 1e-9 {
			t.Fatalf("sample %d differs: %v, want %v", i, got[i], want[i+shift])
		}
	}
	// A seek lands near the position and plays on.
	middle := stream.Len() / 2
	if err := stream.Seek(middle); err != nil || stream.Position() != middle {
		t.Fatalf("seek: %v at %d", err, stream.Position())
	}
	loud := 0.0
	for range 10 {
		n, ok := stream.Stream(got)
		if !ok || n == 0 {
			t.Fatal("nothing after the seek")
		}
		for _, sample := range got[:n] {
			loud = math.Max(loud, math.Abs(sample[0]))
		}
	}
	if loud < .01 {
		t.Fatal("only silence after the seek")
	}
}
