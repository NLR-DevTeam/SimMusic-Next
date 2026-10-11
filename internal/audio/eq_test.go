package audio

import (
	"math"
	"testing"
)

func TestManualEqualizerFilters(t *testing.T) {
	e := NewEqualizer(float64(SampleRate))
	if err := e.SetFilters(true, []EQFilter{{F: 1000, G: 6, Q: 2}, {F: 4000, G: -3, Q: .5}}); err != nil {
		t.Fatal(err)
	}
	if len(e.filters) != 2 || e.profile[0].Q != 2 {
		t.Fatal("custom bands and Q were not used")
	}
	samples := make([][2]float64, 4800)
	for i := range samples {
		value := .05 * math.Sin(2*math.Pi*1000*float64(i)/float64(SampleRate))
		samples[i] = [2]float64{value, value}
	}
	before := samples[1000][0]
	e.Process(samples)
	if math.Abs(samples[1000][0]-before) < 1e-5 {
		t.Fatal("manual filters did not affect audio")
	}
	for _, sample := range samples {
		if math.IsNaN(sample[0]) || math.IsInf(sample[0], 0) {
			t.Fatal("unstable audio")
		}
	}
	for _, bad := range []EQFilter{{F: 0, G: 1, Q: 1}, {F: 1000, G: 21, Q: 1}, {F: 1000, G: 1, Q: 0}, {F: 24000, G: 0, Q: 1}} {
		if e.SetFilters(true, []EQFilter{bad}) == nil {
			t.Fatalf("invalid filter accepted: %+v", bad)
		}
	}
	e.Set(true, []float64{1, 2, 3})
	if len(e.filters) != 10 || e.profile[0].F != 70 || e.profile[0].Q != 1 || e.gains[9] != 0 {
		t.Fatal("slider mode did not restore the ten-band profile")
	}
}
