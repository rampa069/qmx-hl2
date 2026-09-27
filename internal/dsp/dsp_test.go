// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package dsp

import (
	"math"
	"math/cmplx"
	"testing"
)

// toneAt measures the amplitude of frequency f in x (complex DFT at a single bin).
func toneAt(x []complex128, f, rate float64) float64 {
	var acc complex128
	for i, v := range x {
		acc += v * cmplx.Exp(complex(0, -2*math.Pi*f*float64(i)/rate))
	}
	return cmplx.Abs(acc) / float64(len(x))
}

func TestDCBlockerRemovesDCKeepsTone(t *testing.T) {
	d := NewDCBlocker(20, 48000)
	var out []complex128
	for i := 0; i < 48000; i++ {
		ph := 2 * math.Pi * 1000 * float64(i) / 48000
		y := d.Process(complex(0.3+0.5*math.Cos(ph), -0.2+0.5*math.Sin(ph)))
		if i >= 24000 {
			out = append(out, y)
		}
	}
	if dc := toneAt(out, 0, 48000); dc > 1e-3 {
		t.Errorf("DC residue %g", dc)
	}
	if a := toneAt(out, 1000, 48000); math.Abs(a-0.5) > 0.01 {
		t.Errorf("1 kHz amplitude %g, want 0.5", a)
	}
}

func TestInterpolatorPassbandAndImages(t *testing.T) {
	for _, L := range []int{2, 4, 8} {
		it := NewInterpolator(L)
		out := make([]complex128, L)
		var y []complex128
		const f = 15000.0 // a signal near the top of the QMX's useful band
		for i := 0; i < 9600; i++ {
			it.Process(cmplx.Exp(complex(0, 2*math.Pi*f*float64(i)/48000)), out)
			if i >= 200 {
				y = append(y, out...)
			}
		}
		rate := 48000.0 * float64(L)
		if a := toneAt(y, f, rate); math.Abs(a-1) > 0.02 {
			t.Errorf("L=%d: passband gain %g", L, a)
		}
		// The first image sits at f - 48 kHz.
		if img := toneAt(y, f-48000, rate); img > 1e-3 {
			t.Errorf("L=%d: image at %g Hz is %g (%.0f dB)", L, f-48000, img, 20*math.Log10(img))
		}
	}
}

func TestInterpolatorPassThrough(t *testing.T) {
	it := NewInterpolator(1)
	out := make([]complex128, 1)
	it.Process(complex(0.25, -0.5), out)
	if out[0] != complex(0.25, -0.5) {
		t.Fatal(out[0])
	}
}

func TestNCOShift(t *testing.T) {
	n := NewNCO(192000)
	n.SetFreq(-10000)
	var y []complex128
	for i := 0; i < 192000; i++ {
		y = append(y, n.Mix(cmplx.Exp(complex(0, 2*math.Pi*25000*float64(i)/192000))))
	}
	if a := toneAt(y, 15000, 192000); math.Abs(a-1) > 1e-3 {
		t.Errorf("shifted tone amplitude %g", a)
	}
	if a := cmplx.Abs(y[len(y)-1]); math.Abs(a-1) > 1e-9 {
		t.Errorf("amplitude drift %g", a)
	}
	n.SetFreq(0)
	if n.Mix(2) != 2 {
		t.Error("zero shift should pass through")
	}
}
