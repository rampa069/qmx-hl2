// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package engine

import (
	"math"
	"math/cmplx"
	"testing"
)

// feed runs gen(i) (sample index -> complex) through a classifier until it decides or 0.5 s.
func classify(gen func(i int) complex128) (txKind, bool, int) {
	c := newClassifier(0.01)
	for i := 0; i < 24000; i++ {
		c.add(gen(i))
		if k := c.decide(); k != txUndecided {
			return k, c.upperSideband(), i
		}
	}
	return txUndecided, false, 24000
}

func tone(f float64) func(int) complex128 {
	return func(i int) complex128 { return cmplx.Exp(complex(0, 2*math.Pi*f*float64(i)/48000)) }
}

func TestClassifyTones(t *testing.T) {
	ft8 := func(i int) complex128 { // 8-FSK, 160 ms symbols, continuous phase
		ph := 0.0
		for k := 0; k <= i; k++ {
			ph += 2 * math.Pi * (1500 + 6.25*float64([]int{3, 1, 4, 0, 6, 5, 2, 7}[(k/7680)%8])) / 48000
		}
		return cmplx.Exp(complex(0, ph))
	}
	cw := func(i int) complex128 { // 25 WPM dits (48 ms) with 5 ms raised-cosine edges
		p := i % 4608
		a := 0.0
		switch {
		case p < 240:
			a = 0.5 - 0.5*math.Cos(math.Pi*float64(p)/240)
		case p < 2064:
			a = 1
		case p < 2304:
			a = 0.5 + 0.5*math.Cos(math.Pi*float64(p-2064)/240)
		}
		return complex(a, 0) * tone(600)(i)
	}
	lsbTone := tone(-1200)
	for name, g := range map[string]func(int) complex128{"ft8": ft8, "cw": cw, "lsb": lsbTone} {
		if name == "ft8" {
			// precompute phases for speed
			ph := make([]complex128, 24000)
			acc := 0.0
			for i := range ph {
				acc += 2 * math.Pi * (1500 + 6.25*float64([]int{3, 1, 4, 0, 6, 5, 2, 7}[(i/7680)%8])) / 48000
				ph[i] = cmplx.Exp(complex(0, acc))
			}
			g = func(i int) complex128 { return ph[i] }
		}
		k, _, at := classify(g)
		if k != txTone {
			t.Errorf("%s: got %v after %d samples, want tone", name, k, at)
		}
		if at > 48000*160/1000 {
			t.Errorf("%s: decided late (%d samples)", name, at)
		}
	}
}

func TestClassifyVoiceLike(t *testing.T) {
	twoTone := func(i int) complex128 { return 0.5 * (tone(700)(i) + tone(1900)(i)) }
	// Harmonic "vowel" with a gliding pitch 120 -> 220 Hz over 0.2 s, USB.
	voice := func(sign float64) func(int) complex128 {
		return func(i int) complex128 {
			tt := float64(i) / 48000
			f0 := 120 + 500*tt
			ph := 2 * math.Pi * (120*tt + 250*tt*tt)
			var x complex128
			for h := 1; h <= 15; h++ {
				amp := math.Exp(-math.Pow(float64(h)*f0-700, 2) / (2 * 400 * 400))
				x += complex(amp/4, 0) * cmplx.Exp(complex(0, sign*float64(h)*ph))
			}
			return x
		}
	}
	psk := func(i int) complex128 { // BPSK31 idle: a phase reversal every symbol
		sym := 1536
		p := i % sym
		a := math.Cos(math.Pi * float64(p) / float64(sym)) // raised-cosine envelope through zero
		return complex(a, 0) * tone(1000)(i)
	}
	cases := []struct {
		name string
		g    func(int) complex128
		usb  bool
	}{{"two-tone", twoTone, true}, {"voice USB", voice(1), true}, {"voice LSB", voice(-1), false}, {"psk31", psk, true}}
	for _, c := range cases {
		k, usb, at := classify(c.g)
		if k != txVoice {
			t.Errorf("%s: got %v after %d samples, want voice", c.name, k, at)
			continue
		}
		if usb != c.usb {
			t.Errorf("%s: USB=%v, want %v", c.name, usb, c.usb)
		}
	}
}

func TestClassifyToneAfterLongSilence(t *testing.T) {
	// Zeus + WSJT-X: MOX with 600 ms of silence, then an FT8 tone with a ~10 ms ramp.
	g := func(i int) complex128 {
		const start = 28800
		if i < start {
			return 0
		}
		a := math.Min(1, float64(i-start)/480)
		return complex(a, 0) * tone(1518.75)(i)
	}
	c := newClassifier(0.01)
	for i := 0; i < 48000; i++ {
		c.add(g(i))
		if k := c.decide(); k != txUndecided {
			if k != txTone {
				t.Fatalf("got %v at sample %d, want tone", k, i)
			}
			return
		}
	}
	t.Fatal("undecided")
}
