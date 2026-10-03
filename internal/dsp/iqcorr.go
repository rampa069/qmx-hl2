// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package dsp

import "math"

// IQCorrelation measures the normalised correlation of I and Q, |E[IQ]|/sqrt(E[I^2] E[Q^2]),
// over blocks of samples. Real I/Q stays near 0 (a few degrees of quadrature error give about
// 0.05); the same audio on both channels, as the QMX sends when IQ mode is lost, gives about 1.
// Like IQBalancer it works on a high-passed copy, so energy near DC does not count.
type IQCorrelation struct {
	hp         *DCBlocker
	ii, qq, iq float64
	n, block   int
}

// NewIQCorrelation measures over blocks of block samples at sampleRate.
func NewIQCorrelation(sampleRate float64, block int) *IQCorrelation {
	return &IQCorrelation{hp: NewDCBlocker(1000, sampleRate), block: block}
}

// Add adds one sample. At the end of each block it returns the block's correlation and true;
// a silent block (all zeros) reads 0.
func (c *IQCorrelation) Add(x complex128) (float64, bool) {
	h := c.hp.Process(x)
	i, q := real(h), imag(h)
	c.ii += i * i
	c.qq += q * q
	c.iq += i * q
	c.n++
	if c.n < c.block {
		return 0, false
	}
	var r float64
	if p := c.ii * c.qq; p > 0 {
		r = math.Abs(c.iq) / math.Sqrt(p)
	}
	c.ii, c.qq, c.iq, c.n = 0, 0, 0, 0
	return r, true
}
