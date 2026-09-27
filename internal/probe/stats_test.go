package probe

import (
	"math"
	"testing"
	"time"
)

func TestIQStatsSine(t *testing.T) {
	const n = 48000
	buf := make([]float32, 2*n)
	for i := 0; i < n; i++ {
		ph := 2 * math.Pi * 1000 * float64(i) / 48000
		buf[2*i] = float32(0.5*math.Cos(ph) + 0.01) // I: -9 dBFS rms, DC +0.01
		buf[2*i+1] = float32(0.25 * math.Sin(ph))   // Q: 6 dB lower
	}
	var s IQStats
	s.AddInterleaved(buf, n)
	if s.Frames() != n {
		t.Fatalf("frames = %d", s.Frames())
	}
	wantRMS := 20 * math.Log10(0.5/math.Sqrt2)
	if got := s.I.RMSdBFS(); math.Abs(got-wantRMS) > 0.01 {
		t.Errorf("I rms = %.3f, want %.3f", got, wantRMS)
	}
	if got := s.I.DC(); math.Abs(got-0.01) > 1e-4 {
		t.Errorf("I dc = %.5f, want 0.01", got)
	}
	if got := s.I.RMSdBFS() - s.Q.RMSdBFS(); math.Abs(got-6.02) > 0.02 {
		t.Errorf("imbalance = %.3f dB, want 6.02", got)
	}
	if got := s.Q.PeakdBFS(); math.Abs(got-20*math.Log10(0.25)) > 0.01 {
		t.Errorf("Q peak = %.3f", got)
	}
}

func TestIQStatsEmptyAndSilent(t *testing.T) {
	var s IQStats
	if !math.IsInf(s.I.RMSdBFS(), -1) || !math.IsInf(s.I.PeakdBFS(), -1) {
		t.Fatal("empty stats should be -Inf")
	}
	s.AddInterleaved(make([]float32, 20), 10)
	if !math.IsInf(s.Q.RMSdBFS(), -1) {
		t.Fatal("silence should be -Inf")
	}
}

func TestRateMeter(t *testing.T) {
	var r RateMeter
	t0 := time.Unix(1000, 0)
	r.Start(t0)
	r.Add(480048) // 10 s at 48000 Hz + 100 ppm
	rate, ppm := r.Rate(t0.Add(10*time.Second), 48000)
	if math.Abs(rate-48004.8) > 1e-6 || math.Abs(ppm-100) > 1e-6 {
		t.Fatalf("rate=%f ppm=%f", rate, ppm)
	}
	if rate, _ := r.Rate(t0, 48000); rate != 0 {
		t.Fatal("zero elapsed should give zero rate")
	}
}
