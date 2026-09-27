package hpsdr

import (
	"math"
	"testing"
)

// The client-side decoders, as in Zeus/Thetis.
func wattsFromRaw(raw uint16) float64 {
	v := (float64(raw) - 6) / 4095 * 3.3
	return v * v / 1.5
}

func celsiusFromRaw(raw uint16) float64 { return (3.26*float64(raw)/4096 - 0.5) / 0.01 }

func TestTelemetryRoundTrip(t *testing.T) {
	for _, w := range []float64{0.1, 1, 3.8, 5} {
		if got := wattsFromRaw(PowerRaw(w)); math.Abs(got-w)/w > 0.01 {
			t.Errorf("%g W -> %g W", w, got)
		}
	}
	if PowerRaw(0) != 0 || PowerRaw(100) != 4095 {
		t.Error("power clamp")
	}
	// SWR 1.5 reflects 4% of the power.
	if got := wattsFromRaw(ReversePowerRaw(4, 1.5)); math.Abs(got-0.16) > 0.01 {
		t.Errorf("reverse = %g W", got)
	}
	if ReversePowerRaw(4, 1.0) != 0 {
		t.Error("perfect match should give 0")
	}
	if got := celsiusFromRaw(TempRaw(30)); math.Abs(got-30) > 0.1 {
		t.Errorf("temp %g", got)
	}
}
