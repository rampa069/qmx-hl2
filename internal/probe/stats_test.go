// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

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

func TestRateMeterRegressionIgnoresJitter(t *testing.T) {
	var r RateMeter
	t0 := time.Unix(1000, 0)
	r.Start(t0)
	// 240-frame buffers from a device running 100 ppm fast, delivered with up to +/-3 ms of
	// alternating arrival jitter. frames/elapsed would be thrown off by the last arrival;
	// the regression should not be.
	const rate = 48000 * (1 + 100e-6)
	for i := 1; i <= 12000; i++ { // 60 s
		ideal := float64(i*240) / rate
		jit := 0.003
		if i%2 == 0 {
			jit = -jit
		}
		r.Add(t0.Add(time.Duration((ideal+jit)*1e9)), 240)
	}
	got, ppm := r.Rate(48000)
	if math.Abs(ppm-100) > 1 {
		t.Fatalf("rate=%f ppm=%f, want about 100 ppm", got, ppm)
	}
	if r.Frames() != 12000*240 {
		t.Fatalf("frames=%d", r.Frames())
	}
}

func TestRateMeterNeedsTwoPoints(t *testing.T) {
	var r RateMeter
	t0 := time.Unix(1000, 0)
	r.Start(t0)
	if rate, _ := r.Rate(48000); rate != 0 {
		t.Fatal("no points should give zero rate")
	}
	r.Add(t0.Add(time.Second), 48000)
	if rate, _ := r.Rate(48000); rate != 0 {
		t.Fatal("one point should give zero rate")
	}
}
