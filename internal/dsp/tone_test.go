package dsp

import (
	"math"
	"math/cmplx"
	"testing"
)

func TestToneTrackerFrequencies(t *testing.T) {
	for _, f := range []float64{1500, -1200, 0.5, 3000, -23000} {
		tr := NewToneTracker(48000, 240)
		for i := 0; i < 1000; i++ {
			tr.Add(complex(0.3, 0) * cmplx.Exp(complex(0, 2*math.Pi*f*float64(i)/48000)))
		}
		if !tr.Ready() {
			t.Fatal("not ready")
		}
		if got := tr.Freq(); math.Abs(got-f) > 0.01 {
			t.Errorf("f=%g: got %g", f, got)
		}
		if l := tr.Level(); math.Abs(l-0.3) > 1e-6 {
			t.Errorf("level %g", l)
		}
	}
}

func TestToneTrackerFollowsFT8Step(t *testing.T) {
	tr := NewToneTracker(48000, 240)
	ph := 0.0
	for i := 0; i < 48000; i++ {
		f := 1500.0
		if i >= 24000 {
			f = 1500 + 6.25*3
		}
		ph += 2 * math.Pi * f / 48000
		tr.Add(cmplx.Exp(complex(0, ph)))
		if i == 23999 && math.Abs(tr.Freq()-1500) > 0.01 {
			t.Fatalf("before step %g", tr.Freq())
		}
	}
	if math.Abs(tr.Freq()-1518.75) > 0.01 {
		t.Fatalf("after step %g", tr.Freq())
	}
}
