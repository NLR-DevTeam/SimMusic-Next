package audio

import (
	"errors"
	"math"
	"sync"
)

// EQBands 是 10 段均衡器的中心频率。
var EQBands = []float64{70, 180, 320, 600, 1000, 3000, 6000, 12000, 14000, 16000}

// EQFilter retains the 2024 manual JSON format, including custom frequencies/Q.
type EQFilter struct {
	F float64 `json:"F"`
	G float64 `json:"G"`
	Q float64 `json:"Q"`
}

func ValidateEQFilters(filters []EQFilter) error {
	if len(filters) > 64 {
		return errors.New("均衡器最多支持 64 个滤波器")
	}
	for _, f := range filters {
		if math.IsNaN(f.F+f.G+f.Q) || math.IsInf(f.F+f.G+f.Q, 0) || f.F < 10 || f.F >= float64(SampleRate)/2 || f.G < -20 || f.G > 20 || f.Q < .0001 || f.Q > 1000 {
			return errors.New("频率须在 [10, 24000) Hz，增益须在 [-20, 20] dB，Q 须在 [0.0001, 1000]")
		}
	}
	return nil
}

func BasicEQFilters(gains []float64) []EQFilter {
	filters := make([]EQFilter, len(EQBands))
	for i, f := range EQBands {
		filters[i] = EQFilter{F: f, Q: 1}
		if i < len(gains) {
			filters[i].G = gains[i]
		}
	}
	return filters
}

// EQPresets 预设（单位 dB）。
var EQPresets = []struct {
	Name  string
	Gains []float64
}{
	{"平坦", []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	{"流行", []float64{-1, 1, 3, 4, 3, 0, -1, -1, 0, 1}},
	{"摇滚", []float64{5, 4, 2, -1, -2, -1, 2, 4, 5, 5}},
	{"古典", []float64{4, 3, 2, 1, -1, -1, 0, 2, 3, 4}},
	{"低音增强", []float64{7, 6, 5, 3, 1, 0, 0, 0, 0, 0}},
	{"人声", []float64{-2, -2, -1, 1, 3, 4, 3, 1, 0, -1}},
	{"电子", []float64{5, 4, 1, 0, -2, 2, 1, 2, 4, 5}},
}

type biquad struct {
	b0, b1, b2, a1, a2 float64
	z1, z2             [2]float64
}

func (q *biquad) peaking(fs, f0, gainDB, Q float64) {
	A := math.Pow(10, gainDB/40)
	w0 := 2 * math.Pi * f0 / fs
	alpha := math.Sin(w0) / (2 * Q)
	cw := math.Cos(w0)
	a0 := 1 + alpha/A
	q.b0 = (1 + alpha*A) / a0
	q.b1 = -2 * cw / a0
	q.b2 = (1 - alpha*A) / a0
	q.a1 = -2 * cw / a0
	q.a2 = (1 - alpha/A) / a0
}

// Equalizer 是 10 段峰值滤波均衡器。
type Equalizer struct {
	mu      sync.Mutex
	fs      float64
	enabled bool
	gains   []float64
	profile []EQFilter
	filters []biquad
	preamp  float64
}

func NewEqualizer(fs float64) *Equalizer {
	e := &Equalizer{fs: fs, preamp: 1}
	e.Set(false, make([]float64, len(EQBands)))
	return e
}

func (e *Equalizer) rebuild() {
	maxG := 0.0
	for i, f := range e.profile {
		e.filters[i].peaking(e.fs, f.F, f.G, f.Q)
		maxG = math.Max(maxG, e.gains[i])
	}
	// 简单的预衰减，避免提升后削波
	e.preamp = math.Pow(10, -maxG*0.6/20)
}

func (e *Equalizer) Set(enabled bool, gains []float64) {
	_ = e.SetFilters(enabled, BasicEQFilters(gains))
}

func (e *Equalizer) SetFilters(enabled bool, filters []EQFilter) error {
	if err := ValidateEQFilters(filters); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = enabled
	e.profile = append([]EQFilter(nil), filters...)
	if len(e.filters) != len(filters) {
		e.filters = make([]biquad, len(filters))
	}
	e.gains = make([]float64, len(filters))
	for i, f := range filters {
		e.gains[i] = f.G
	}
	e.rebuild()
	return nil
}

func (e *Equalizer) Gains() (bool, []float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enabled, append([]float64(nil), e.gains...)
}

func (e *Equalizer) Process(s [][2]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.enabled {
		return
	}
	for i := range e.filters {
		if e.gains[i] == 0 {
			continue
		}
		f := &e.filters[i]
		for n := range s {
			for c := 0; c < 2; c++ {
				x := s[n][c]
				y := f.b0*x + f.z1[c]
				f.z1[c] = f.b1*x - f.a1*y + f.z2[c]
				f.z2[c] = f.b2*x - f.a2*y
				s[n][c] = y
			}
		}
	}
	if e.preamp != 1 {
		for n := range s {
			s[n][0] *= e.preamp
			s[n][1] *= e.preamp
		}
	}
}
