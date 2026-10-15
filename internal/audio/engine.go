// Package audio 实现播放引擎：解码、重采样、均衡器、淡入淡出与音量控制。
package audio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"
)

const SampleRate = beep.SampleRate(48000)

// Source 描述要播放的音频来源。
type Source struct {
	Path    string
	URL     string
	Headers map[string]string
	Format  string
}

type Engine struct {
	mu sync.Mutex

	dec    beep.StreamSeekCloser
	format beep.Format
	res    *beep.Resampler
	closer io.Closer
	gen    int

	playing  bool    // 用户意图：播放 / 暂停
	gain     float64 // 当前淡入淡出增益 0..1
	fadeStep float64 // 每个采样的增益变化量
	volume   float64 // 0..1
	fadeOn   bool
	speed    float64

	eq    *Equalizer
	scope scope

	// OnEnded 在当前曲目自然播放结束时调用（在独立 goroutine 中）。
	OnEnded func()
}

func New() (*Engine, error) {
	e := &Engine{volume: 0.8, gain: 1, speed: 1, eq: NewEqualizer(float64(SampleRate))}
	if err := speaker.Init(SampleRate, SampleRate.N(200*time.Millisecond)); err != nil {
		return nil, err
	}
	speaker.Play(e)
	log.Print("audio: speaker buffer 200ms; bounded decode-ahead buffer 2s")
	go e.monitor()
	return e, nil
}

// Report real PCM starvation away from the callback, without doing logging I/O
// on the speaker thread. A stalled source now leaves useful diagnostic data.
func (e *Engine) monitor() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previous *bufferedDecoder
	var reported uint64
	for range ticker.C {
		e.mu.Lock()
		decoder, _ := e.dec.(*bufferedDecoder)
		e.mu.Unlock()
		if decoder != previous {
			previous, reported = decoder, 0
		}
		if decoder == nil {
			continue
		}
		count := decoder.underruns.Load()
		if count != reported {
			log.Printf("audio: PCM source buffer underrun (%d source frames)", count-reported)
			reported = count
		}
	}
}

func (e *Engine) EQ() *Equalizer { return e.eq }

// Load 打开并切换到新的音源，autoplay 决定是否立即播放。
func (e *Engine) Load(src Source, autoplay bool) error {
	return e.LoadWhen(src, func() bool { return autoplay })
}

// LoadWhen 与 Load 相同，但在音源打开之后才询问是否播放：打开期间用户可能已按下播放或暂停。
func (e *Engine) LoadWhen(src Source, play func() bool) error {
	dec, format, closer, err := open(src)
	if err != nil {
		return err
	}
	dec = newBufferedDecoder(dec, closer, format.SampleRate)
	closer = nil
	autoplay := play()
	e.mu.Lock()
	old, oldCloser := e.dec, e.closer
	e.dec, e.format, e.closer = dec, format, closer
	e.res = beep.ResampleRatio(3, e.ratio(), dec)
	e.gen++
	e.playing = autoplay
	// A track opened paused must be silent and still: with any gain left over,
	// the stream would keep consuming it while the player shows it as paused.
	e.gain, e.fadeStep = 0, 0
	if autoplay {
		e.gain = 1
	}
	e.mu.Unlock()
	if old != nil {
		go func() {
			old.Close()
			if oldCloser != nil {
				oldCloser.Close()
			}
		}()
	}
	return nil
}

// Unload 停止并释放当前音源。
func (e *Engine) Unload() {
	e.mu.Lock()
	old, oldCloser := e.dec, e.closer
	e.dec, e.closer, e.res = nil, nil, nil
	e.playing = false
	e.gen++
	e.mu.Unlock()
	if old != nil {
		old.Close()
		if oldCloser != nil {
			oldCloser.Close()
		}
	}
}

func (e *Engine) fade(target float64) {
	const fadeDur = 0.35 // 秒
	if !e.fadeOn {
		e.gain = target
		e.fadeStep = 0
		return
	}
	e.fadeStep = 1 / (fadeDur * float64(SampleRate))
	if target < e.gain {
		e.fadeStep = -e.fadeStep
	}
}

func (e *Engine) SetPlaying(p bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p == e.playing {
		return
	}
	e.playing = p
	if p {
		if e.fadeOn {
			e.gain = 0
		}
		e.fade(1)
	} else {
		e.fade(0)
	}
}

func (e *Engine) Playing() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.playing && e.dec != nil
}

func (e *Engine) Loaded() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dec != nil
}

func (e *Engine) SetVolume(v float64) {
	e.mu.Lock()
	e.volume = math.Max(0, math.Min(1, v))
	e.mu.Unlock()
}

func (e *Engine) Volume() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.volume
}

// Position 返回当前播放位置与总时长（秒）。
func (e *Engine) Position() (pos, dur float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dec == nil {
		return 0, 0
	}
	sr := float64(e.format.SampleRate)
	return float64(e.dec.Position()) / sr, float64(e.dec.Len()) / sr
}

func (e *Engine) Seek(sec float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dec == nil {
		return errors.New("no track")
	}
	p := e.format.SampleRate.N(time.Duration(sec * float64(time.Second)))
	p = max(0, min(p, e.dec.Len()-1))
	if err := e.dec.Seek(p); err != nil {
		return err
	}
	e.res = beep.ResampleRatio(3, e.ratio(), e.dec)
	return nil
}

// Stream 实现 beep.Streamer，由 speaker 回调。
func (e *Engine) Stream(samples [][2]float64) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.res == nil || (!e.playing && e.gain <= 0) {
		clear(samples)
		e.scope.record(samples)
		return len(samples), true
	}
	n, ok := e.res.Stream(samples)
	if n < len(samples) {
		clear(samples[n:])
		if !ok || n == 0 {
			// 曲目结束
			gen := e.gen
			e.res = nil
			cb := e.OnEnded
			if cb != nil {
				go func() {
					if err := e.decError(gen); err != nil {
						log.Printf("audio: decoder stopped: %v", err)
					}
					e.mu.Lock()
					same := gen == e.gen
					e.mu.Unlock()
					if same {
						cb()
					}
				}()
			}
		}
	}
	e.eq.Process(samples[:n])
	e.scope.record(samples)
	vol := e.volume * e.volume // 感知音量曲线
	for i := range samples[:n] {
		if e.fadeStep != 0 {
			e.gain += e.fadeStep
			if e.gain >= 1 {
				e.gain, e.fadeStep = 1, 0
			} else if e.gain <= 0 {
				e.gain, e.fadeStep = 0, 0
			}
		}
		g := e.gain * vol
		for c := 0; c < 2; c++ {
			v := samples[i][c] * g
			if v > 1 {
				v = 1
			} else if v < -1 {
				v = -1
			}
			samples[i][c] = v
		}
	}
	return len(samples), true
}

func (e *Engine) Err() error { return nil }

func (e *Engine) decError(gen int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen == e.gen && e.dec != nil {
		return e.dec.Err()
	}
	return nil
}

// ---------- 解码 ----------

type readSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

func open(src Source) (beep.StreamSeekCloser, beep.Format, io.Closer, error) {
	var rsc readSeekCloser
	name := src.Path
	if src.Path != "" {
		f, err := os.Open(src.Path)
		if err != nil {
			return nil, beep.Format{}, nil, err
		}
		rsc = f
	} else if src.URL != "" {
		pr, err := OpenHTTP(src.URL, src.Headers)
		if err != nil {
			return nil, beep.Format{}, nil, err
		}
		rsc = pr
		name = src.URL
	} else {
		return nil, beep.Format{}, nil, errors.New("empty source")
	}
	kind := strings.ToLower(src.Format)
	if kind == "" {
		kind = sniff(rsc)
	}
	if kind == "" {
		kind = strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.Split(name, "?")[0])), ".")
	}
	var (
		dec beep.StreamSeekCloser
		fm  beep.Format
		err error
	)
	switch kind {
	case "mp3":
		dec, fm, err = mp3.Decode(rsc)
	case "flac":
		dec, fm, err = flac.Decode(bufReadSeeker(rsc))
	case "ogg":
		dec, fm, err = vorbis.Decode(rsc)
	case "wav":
		dec, fm, err = wav.Decode(rsc)
	default:
		err = fmt.Errorf("不支持的音频格式: %q", kind)
	}
	if err != nil {
		rsc.Close()
		return nil, beep.Format{}, nil, err
	}
	return dec, fm, rsc, nil
}

func sniff(r io.ReadSeeker) string {
	var h [12]byte
	n, _ := io.ReadFull(r, h[:])
	r.Seek(0, io.SeekStart)
	b := h[:n]
	switch {
	case n >= 4 && string(b[:4]) == "fLaC":
		return "flac"
	case n >= 4 && string(b[:4]) == "OggS":
		return "ogg"
	case n >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "wav"
	case n >= 3 && string(b[:3]) == "ID3":
		// ID3 头后可能是 FLAC（少见），默认按 MP3 处理
		return "mp3"
	case n >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return "mp3"
	}
	return ""
}

// bufReadSeeker 为 flac 解码器提供带缓冲且可 Seek 的读取器。
type brs struct {
	rs io.ReadSeeker
	br *bufio.Reader
}

func bufReadSeeker(rs io.ReadSeeker) io.ReadSeeker {
	return &brs{rs: rs, br: bufio.NewReaderSize(rs, 64<<10)}
}

func (b *brs) Read(p []byte) (int, error) { return b.br.Read(p) }
func (b *brs) Seek(off int64, whence int) (int64, error) {
	if whence == io.SeekCurrent {
		off -= int64(b.br.Buffered())
	}
	n, err := b.rs.Seek(off, whence)
	b.br.Reset(b.rs)
	return n, err
}

// ---------- 渐进式 HTTP 读取 ----------

// HTTPReader 在后台下载整个文件，读取时只在数据尚未到达时阻塞。
type HTTPReader struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	total  int64
	done   bool
	err    error
	pos    int64
	closed bool
	resp   *http.Response
}

func OpenHTTP(url string, headers map[string]string) (*HTTPReader, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SimMusicNext/0.1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	h := &HTTPReader{total: resp.ContentLength, resp: resp}
	h.cond = sync.NewCond(&h.mu)
	if h.total > 0 {
		h.buf = make([]byte, 0, h.total)
	}
	go h.pump()
	return h, nil
}

func (h *HTTPReader) pump() {
	chunk := make([]byte, 64<<10)
	for {
		n, err := h.resp.Body.Read(chunk)
		h.mu.Lock()
		h.buf = append(h.buf, chunk[:n]...)
		if err != nil || h.closed {
			h.done = true
			if err != io.EOF {
				h.err = err
			}
			h.total = int64(len(h.buf))
			h.cond.Broadcast()
			h.mu.Unlock()
			h.resp.Body.Close()
			return
		}
		h.cond.Broadcast()
		h.mu.Unlock()
	}
}

func (h *HTTPReader) Read(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for int64(len(h.buf)) <= h.pos && !h.done {
		h.cond.Wait()
	}
	if h.closed {
		return 0, os.ErrClosed
	}
	if h.pos >= int64(len(h.buf)) {
		if h.err != nil {
			return 0, h.err
		}
		return 0, io.EOF
	}
	n := copy(p, h.buf[h.pos:])
	h.pos += int64(n)
	return n, nil
}

func (h *HTTPReader) Seek(off int64, whence int) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, os.ErrClosed
	}
	switch whence {
	case io.SeekCurrent:
		off += h.pos
	case io.SeekEnd:
		for h.total <= 0 && !h.done {
			h.cond.Wait()
		}
		if h.closed {
			return 0, os.ErrClosed
		}
		off += h.total
	}
	if off < 0 {
		return 0, errors.New("negative seek")
	}
	h.pos = off
	return off, nil
}

func (h *HTTPReader) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.done = true
	h.cond.Broadcast()
	h.mu.Unlock()
	return h.resp.Body.Close()
}

// ratio 返回当前曲目的重采样比例（含倍速）。调用方需持有锁。
func (e *Engine) ratio() float64 {
	if e.format.SampleRate == 0 {
		return e.speed
	}
	return float64(e.format.SampleRate) / float64(SampleRate) * e.speed
}

// SetFade 设置播放 / 暂停时是否淡入淡出。
func (e *Engine) SetFade(on bool) {
	e.mu.Lock()
	e.fadeOn = on
	e.mu.Unlock()
}

// SetSpeed 设置播放倍速（同时改变音调）。
func (e *Engine) SetSpeed(s float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.speed = math.Max(0.1, math.Min(10, s))
	if e.res != nil {
		e.res.SetRatio(e.ratio())
	}
}

func (e *Engine) Speed() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.speed
}
