// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package dsp

import "math"

// IQBalancer blindly corrects receive I/Q gain and phase mismatch, which otherwise shows up as
// mirror images of strong signals. It assumes the band is statistically circular on average
// (E[I^2] = E[Q^2], E[IQ] = 0), which holds for band noise plus many signals. Statistics are
// taken from a high-passed copy so the QMX's strong, non-circular energy near DC does not bias
// them. The correction orthogonalises Q against I and rescales it (Gram-Schmidt):
//
//	Q' = (Q - (E[IQ]/E[I^2]) I) * sqrt(E[I^2] / E[Q'^2])
type IQBalancer struct {
	alpha            float64 // averaging weight per sample
	hp               *DCBlocker
	pII, pQQ, pIQ    float64
	warm             int
	phaseCoef, scale float64
	updEvery, n      int
}

// NewIQBalancer averages over roughly tau seconds at sampleRate.
func NewIQBalancer(sampleRate, tau float64) *IQBalancer {
	return &IQBalancer{
		alpha:    1 / (tau * sampleRate),
		hp:       NewDCBlocker(1000, sampleRate),
		scale:    1,
		updEvery: 256,
	}
}

// Process corrects one sample.
func (b *IQBalancer) Process(x complex128) complex128 {
	h := b.hp.Process(x)
	i, q := real(h), imag(h)
	a := b.alpha
	b.pII += a * (i*i - b.pII)
	b.pQQ += a * (q*q - b.pQQ)
	b.pIQ += a * (i*q - b.pIQ)
	b.n++
	if b.n >= b.updEvery {
		b.n = 0
		b.warm++
		if b.pII > 0 {
			c := b.pIQ / b.pII
			// E[Q'^2] = E[Q^2] - c^2 E[I^2]
			qq := b.pQQ - c*c*b.pII
			if qq > 0 {
				// Clamp to what a real QSD mismatch looks like (a few degrees, a few dB), so
				// strong non-circular interference cannot drive the correction off.
				b.phaseCoef = math.Max(-0.1, math.Min(0.1, c))
				b.scale = math.Max(0.7, math.Min(1.4, math.Sqrt(b.pII/qq)))
			}
		}
	}
	if b.warm < 4 { // wait for the averages to settle a little
		return x
	}
	return complex(real(x), (imag(x)-b.phaseCoef*real(x))*b.scale)
}

// Correction returns the current phase coefficient and Q scale (for logging).
func (b *IQBalancer) Correction() (phaseCoef, scale float64) { return b.phaseCoef, b.scale }
