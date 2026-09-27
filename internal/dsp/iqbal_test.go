// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package dsp

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

func TestIQBalancerRemovesImage(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	b := NewIQBalancer(48000, 0.5)
	const g, phi = 0.8, 0.1 // 2 dB gain error, 5.7 degrees phase error
	var out []complex128
	var in []complex128
	for n := 0; n < 5*48000; n++ {
		// Band noise plus a strong tone at +9 kHz.
		s := complex(r.NormFloat64()*0.01, r.NormFloat64()*0.01) +
			0.3*cmplx.Exp(complex(0, 2*math.Pi*9000*float64(n)/48000))
		// Mismatch: Q gets gain g and leaks phi of I.
		x := complex(real(s), g*(imag(s)*math.Cos(phi)+real(s)*math.Sin(phi)))
		y := b.Process(x)
		if n >= 4*48000 {
			in = append(in, x)
			out = append(out, y)
		}
	}
	rej := func(v []complex128) float64 {
		return 20 * math.Log10(toneAt(v, 9000, 48000)/toneAt(v, -9000, 48000))
	}
	before, after := rej(in), rej(out)
	if before > 25 {
		t.Fatalf("test setup: image rejection before is already %.1f dB", before)
	}
	if after < 45 {
		t.Errorf("image rejection %.1f dB -> %.1f dB, want > 45 dB", before, after)
	}
}
