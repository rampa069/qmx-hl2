// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package dsp

import "math"

// ToneTracker estimates the instantaneous frequency and level of a complex baseband signal
// that is (mostly) a single tone, as FT8/WSPR/FSK/CW transmit IQ is. It averages the lag-1
// autocorrelation x[n]·conj(x[n-1]) over a sliding window; the angle of that sum is the mean
// phase step, which is robust to noise and to the host's amplitude shaping.
type ToneTracker struct {
	rate   float64
	win    int
	prods  []complex128 // ring of per-sample lag-1 products
	pows   []float64    // ring of per-sample powers
	pos    int
	filled int
	sumP   complex128
	sumE   float64
	prev   complex128
	primed bool
}

// NewToneTracker averages over window samples at sampleRate (e.g. 240 = 5 ms at 48 kHz).
func NewToneTracker(sampleRate float64, window int) *ToneTracker {
	return &ToneTracker{rate: sampleRate, win: window, prods: make([]complex128, window), pows: make([]float64, window)}
}

// Reset forgets all history.
func (t *ToneTracker) Reset() {
	clear(t.prods)
	clear(t.pows)
	t.pos, t.filled = 0, 0
	t.sumP, t.sumE = 0, 0
	t.primed = false
}

// Add feeds one sample.
func (t *ToneTracker) Add(x complex128) {
	if !t.primed {
		t.prev, t.primed = x, true
		return
	}
	p := x * complex(real(t.prev), -imag(t.prev))
	e := real(x)*real(x) + imag(x)*imag(x)
	t.prev = x
	t.sumP += p - t.prods[t.pos]
	t.sumE += e - t.pows[t.pos]
	t.prods[t.pos], t.pows[t.pos] = p, e
	t.pos++
	if t.pos == t.win {
		t.pos = 0
		// Recompute the running sums now and then so float error cannot accumulate.
		t.sumP, t.sumE = 0, 0
		for i := range t.prods {
			t.sumP += t.prods[i]
			t.sumE += t.pows[i]
		}
	}
	if t.filled < t.win {
		t.filled++
	}
}

// Ready reports whether a full window has been seen.
func (t *ToneTracker) Ready() bool { return t.filled == t.win }

// Freq returns the estimated frequency in Hz (negative below the carrier).
func (t *ToneTracker) Freq() float64 {
	return math.Atan2(imag(t.sumP), real(t.sumP)) * t.rate / (2 * math.Pi)
}

// Level returns the RMS amplitude over the window (1.0 = full scale).
func (t *ToneTracker) Level() float64 {
	if t.filled == 0 {
		return 0
	}
	return math.Sqrt(math.Max(t.sumE, 0) / float64(t.filled))
}
