package audio

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/gopxl/beep/v2"
)

// Only the producer touches the decoder. The speaker consumes a bounded PCM
// ring and never waits for file/network I/O or a decoder seek.
type bufferedDecoder struct {
	mu                            sync.Mutex
	dec                           beep.StreamSeekCloser
	source                        io.Closer
	buf                           [][2]float64
	head, count, position, length int
	generation                    uint64
	seekPending                   bool
	seekPosition                  int
	ended, closed                 bool
	started                       bool
	underruns                     atomic.Uint64
	err                           error
	wake                          chan struct{}
	done                          chan struct{}
}

func newBufferedDecoder(dec beep.StreamSeekCloser, source io.Closer, rate beep.SampleRate) *bufferedDecoder {
	b := &bufferedDecoder{dec: dec, source: source, buf: make([][2]float64, int(rate)*2), position: dec.Position(), length: dec.Len(), wake: make(chan struct{}, 1), done: make(chan struct{})}
	go b.produce()
	return b
}

func (b *bufferedDecoder) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *bufferedDecoder) produce() {
	defer close(b.done)
	defer b.dec.Close()
	chunk := make([][2]float64, max(1, min(2048, len(b.buf)/4)))
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		generation, seek, position := b.generation, b.seekPending, b.seekPosition
		b.seekPending = false
		space := len(b.buf) - b.count
		ended := b.ended
		b.mu.Unlock()
		if seek {
			err := b.dec.Seek(position)
			b.mu.Lock()
			if generation == b.generation && err != nil {
				b.err, b.ended = err, true
			}
			b.mu.Unlock()
			continue
		}
		if ended || space < len(chunk) {
			<-b.wake
			continue
		}
		n, ok := b.dec.Stream(chunk)
		b.mu.Lock()
		if generation == b.generation && !b.closed {
			tail := (b.head + b.count) % len(b.buf)
			first := copy(b.buf[tail:], chunk[:n])
			copy(b.buf, chunk[first:n])
			b.count += n
			if !ok || n == 0 {
				b.ended, b.err = true, b.dec.Err()
			}
		}
		b.mu.Unlock()
	}
}

func (b *bufferedDecoder) Stream(samples [][2]float64) (int, bool) {
	b.mu.Lock()
	n := min(len(samples), b.count)
	first := copy(samples[:n], b.buf[b.head:])
	copy(samples[first:n], b.buf[:n-first])
	b.head = (b.head + n) % len(b.buf)
	b.count -= n
	b.position += n
	ended := b.ended || b.closed
	if n == len(samples) {
		b.started = true
	}
	if n < len(samples) && !ended && b.started {
		b.underruns.Add(uint64(len(samples) - n))
	}
	b.mu.Unlock()
	b.signal()
	if n < len(samples) && !ended {
		clear(samples[n:])
		return len(samples), true
	}
	return n, n > 0
}

func (b *bufferedDecoder) Err() error    { b.mu.Lock(); defer b.mu.Unlock(); return b.err }
func (b *bufferedDecoder) Len() int      { return b.length }
func (b *bufferedDecoder) Position() int { b.mu.Lock(); defer b.mu.Unlock(); return b.position }

func (b *bufferedDecoder) Seek(position int) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("audio source closed")
	}
	b.generation++
	b.head, b.count, b.position = 0, 0, position
	b.seekPending, b.seekPosition, b.ended, b.err = true, position, false, nil
	b.started = false
	b.mu.Unlock()
	b.signal()
	return nil
}

func (b *bufferedDecoder) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	// Closing the source unblocks an in-flight network or file read. The worker
	// closes the decoder itself, avoiding concurrent decoder operations.
	if b.source != nil {
		b.source.Close()
	}
	b.signal()
	return nil
}
