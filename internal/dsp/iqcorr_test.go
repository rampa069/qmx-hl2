// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package dsp

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestIQCorrelation(t *testing.T) {
	const rate = 48000.0
	rng := rand.New(rand.NewPCG(1, 2))
	cases := []struct {
		name     string
		sample   func(n int) complex128
		min, max float64
	}{
		{"noise", func(int) complex128 { return complex(rng.NormFloat64(), rng.NormFloat64()) }, 0, 0.1},
		{"tone with 3 degrees of quadrature error", func(n int) complex128 {
			p := 2 * math.Pi * 3000 * float64(n) / rate
			return complex(math.Cos(p), math.Sin(p+3*math.Pi/180))
		}, 0, 0.1},
		{"same audio on both channels", func(n int) complex128 {
			a := math.Sin(2*math.Pi*700*float64(n)/rate) + 0.1*rng.NormFloat64()
			return complex(a, a)
		}, 0.95, 1},
		{"silence", func(int) complex128 { return 0 }, 0, 0},
	}
	for _, c := range cases {
		m := NewIQCorrelation(rate, 4800)
		var last float64
		blocks := 0
		for n := range 48000 {
			if r, ok := m.Add(c.sample(n)); ok {
				last = r
				blocks++
			}
		}
		if blocks != 10 {
			t.Errorf("%s: %d blocks, want 10", c.name, blocks)
		}
		if last < c.min || last > c.max {
			t.Errorf("%s: correlation %.3f, want %.2f..%.2f", c.name, last, c.min, c.max)
		}
	}
}
