package audio

import (
	"math"
	"testing"
)

func TestSpectrumFindsATone(t *testing.T) {
	e := &Engine{}
	if levels := e.Spectrum(32); len(levels) != 32 || levels[10] != 0 {
		t.Fatalf("an empty player is silent: %v", levels)
	}
	tone := make([][2]float64, scopeSize)
	for i := range tone {
		v := .5 * math.Sin(2*math.Pi*1000*float64(i)/float64(SampleRate))
		tone[i] = [2]float64{v, v}
	}
	e.scope.record(tone)
	levels := e.Spectrum(32)
	loudest := 0
	for band, level := range levels {
		if level > levels[loudest] {
			loudest = band
		}
	}
	// 1 kHz in 32 pitch-spaced bands from 40 Hz to 16 kHz: band 17 (ln 25 / ln 400 of the way).
	if loudest != 17 || levels[loudest] < .85 || levels[2] > .3 || levels[30] > .3 {
		t.Fatalf("a 1 kHz tone at half scale: %v", levels)
	}
}
