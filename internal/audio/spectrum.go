package audio

import (
	"math"
	"math/cmplx"
	"sync"
	"time"
)

// The spectrum of what is being heard. The speaker callback keeps the last samples it produced, after the
// equalizer and before volume and fades: what a visualization shows does not shrink with the volume. The
// samples handed to the speaker are heard about one buffer later, so the window analysed ends that far back.
const (
	scopeSize  = 1 << 14
	windowSize = 2048
)

var scopeDelay = SampleRate.N(200 * time.Millisecond)

type scope struct {
	samples [scopeSize]float32
	written uint64
}

// record keeps a mono copy of samples just produced; called with the engine locked.
func (s *scope) record(samples [][2]float64) {
	for _, sample := range samples {
		s.samples[s.written%scopeSize] = float32((sample[0] + sample[1]) / 2)
		s.written++
	}
}

var spectrumTables = sync.OnceValues(func() ([]float64, []complex128) {
	window := make([]float64, windowSize)
	for i := range window {
		window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(windowSize-1))
	}
	twiddles := make([]complex128, windowSize/2)
	for i := range twiddles {
		twiddles[i] = cmplx.Exp(complex(0, -2*math.Pi*float64(i)/float64(windowSize)))
	}
	return window, twiddles
})

// fft transforms in place; len(x) is windowSize.
func fft(x []complex128) {
	_, twiddles := spectrumTables()
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		step := n / size
		for start := 0; start < n; start += size {
			for k := 0; k < size/2; k++ {
				t := twiddles[k*step] * x[start+k+size/2]
				x[start+k+size/2] = x[start+k] - t
				x[start+k] += t
			}
		}
	}
}

// Spectrum returns the level of bands frequency bands, spaced evenly in pitch from 40 Hz to 16 kHz, each
// from 0 (-70 dB or quieter) to 1 (a full-scale tone). Silence, a pause and an empty player give zeros.
func (e *Engine) Spectrum(bands int) []float64 {
	bands = max(1, min(256, bands))
	window, _ := spectrumTables()
	x := make([]complex128, windowSize)
	e.mu.Lock()
	if end := e.scope.written; end >= uint64(scopeDelay+windowSize) {
		start := end - uint64(scopeDelay) - windowSize
		for i := range x {
			x[i] = complex(float64(e.scope.samples[(start+uint64(i))%scopeSize])*window[i], 0)
		}
	}
	e.mu.Unlock()
	fft(x)
	const low, high = 40.0, 16000.0
	binWidth := float64(SampleRate) / windowSize
	// A full-scale sine under a Hann window peaks at a quarter of the window length.
	reference := windowSize / 4.0
	out := make([]float64, bands)
	for band := range out {
		from := low * math.Pow(high/low, float64(band)/float64(bands))
		to := low * math.Pow(high/low, float64(band+1)/float64(bands))
		first := int(math.Round(from / binWidth))
		last := max(first, int(math.Round(to/binWidth))-1)
		peak := 0.0
		for bin := first; bin <= last && bin < windowSize/2; bin++ {
			peak = max(peak, cmplx.Abs(x[bin]))
		}
		level := 20 * math.Log10(peak/reference+1e-9)
		out[band] = max(0, min(1, (level+70)/70))
	}
	return out
}
