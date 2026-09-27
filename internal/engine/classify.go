// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package engine

import "math"

// txKind is what the client is sending.
type txKind int

const (
	txUndecided txKind = iota
	txTone             // single tone: FT8/FT4/WSPR/JS8/RTTY/CW -> CAT TA
	txVoice            // anything else: SSB voice (or multi-tone digital) -> QMX SSB mode
)

// classifier decides from the start of an over whether the TX IQ is a single tone. It looks at
// 2.5 ms blocks with signal and uses two tests:
//   - frequency: the lag-1 phase step gives each block's mean frequency; a tone (FT8, FSK, CW)
//     keeps it within a few Hz, voice and PSK phase reversals move it by hundreds of Hz;
//   - coherence at a 2 ms lag: ~1 for a single tone even while its amplitude ramps (CW), but
//     low for several simultaneous components (two-tone tests, voice harmonics), whose mean
//     frequency can otherwise look steady.
//
// The sign of the averaged phase step also gives the sideband for voice.
type classifier struct {
	gate       float64
	block      int
	n          int
	acc        complex128
	energy     float64
	prev       complex128
	primed     bool
	blocks     int
	voiced     int
	fmin, fmax float64
	lagSum     complex128

	delay        [cohLag]complex128
	dpos, dfill  int
	cohAcc       complex128
	cohE1, cohE2 float64
	min1, min2   float64 // smallest |x|^2 in the block and in its delayed copy
	minCoh       float64
	nullBlocks   int // voiced blocks whose envelope dips near zero
}

const cohLag = 96 // 2 ms

const classifyBlock = 120 // 2.5 ms at 48 kHz

func newClassifier(gate float64) *classifier {
	c := &classifier{gate: gate, block: classifyBlock}
	c.reset()
	return c
}

func (c *classifier) reset() {
	*c = classifier{gate: c.gate, block: c.block, fmin: math.Inf(1), fmax: math.Inf(-1), minCoh: 1,
		min1: math.Inf(1), min2: math.Inf(1)}
}

func (c *classifier) add(x complex128) {
	if c.primed {
		c.acc += x * complex(real(c.prev), -imag(c.prev))
	}
	c.prev, c.primed = x, true
	if c.dfill == cohLag {
		d := c.delay[c.dpos]
		c.cohAcc += x * complex(real(d), -imag(d))
		p1 := real(x)*real(x) + imag(x)*imag(x)
		p2 := real(d)*real(d) + imag(d)*imag(d)
		c.cohE1 += p1
		c.cohE2 += p2
		c.min1 = math.Min(c.min1, p1)
		c.min2 = math.Min(c.min2, p2)
	} else {
		c.dfill++
	}
	c.delay[c.dpos] = x
	c.dpos = (c.dpos + 1) % cohLag
	c.energy += real(x)*real(x) + imag(x)*imag(x)
	c.n++
	if c.n < c.block {
		return
	}
	// Count time from the first block with signal: clients key MOX well before the audio
	// (WSJT-X via Zeus: about 600 ms of silence), which must not use up the decision window.
	if c.voiced > 0 || math.Sqrt(c.energy/float64(c.n)) >= c.gate {
		c.blocks++
	}
	if math.Sqrt(c.energy/float64(c.n)) >= c.gate {
		c.voiced++
		f := math.Atan2(imag(c.acc), real(c.acc)) * 48000 / (2 * math.Pi)
		c.fmin = math.Min(c.fmin, f)
		c.fmax = math.Max(c.fmax, f)
		c.lagSum += c.acc
		// Judge coherence only on steady stretches: both the block and its 2 ms-old
		// counterpart carry signal at similar levels (not a CW key-down or key-up ramp).
		// Also skip blocks with dropouts: Zeus's CW IQ has a ~0.5 ms gap with a phase jump
		// every 10 ms (seen 2026-09-27), which would otherwise look like several components.
		floor := c.gate * c.gate * float64(c.n)
		steady := c.min1 > 0.1*c.cohE1/float64(c.n) && c.min2 > 0.1*c.cohE2/float64(c.n)
		if c.cohE1 > floor && c.min1 <= 0.1*c.cohE1/float64(c.n) {
			// Several components beat against each other and null the envelope in nearly
			// every block; a glitchy single tone does so only occasionally.
			c.nullBlocks++
		}
		if steady && c.cohE1 > floor && c.cohE2 > floor && math.Max(c.cohE1, c.cohE2) < 1.25*math.Min(c.cohE1, c.cohE2) {
			coh := math.Hypot(real(c.cohAcc), imag(c.cohAcc)) / math.Sqrt(c.cohE1*c.cohE2)
			c.minCoh = math.Min(c.minCoh, coh)
		}
	}
	c.n, c.acc, c.energy = 0, 0, 0
	c.cohAcc, c.cohE1, c.cohE2 = 0, 0, 0
	c.min1, c.min2 = math.Inf(1), math.Inf(1)
}

// decide returns the classification so far.
func (c *classifier) decide() txKind {
	spread := c.fmax - c.fmin
	beating := c.nullBlocks*2 > c.voiced
	toneLike := spread < 60 && c.minCoh > 0.9 && !beating
	switch {
	case c.voiced >= 16 && toneLike: // 40 ms: longer than a PSK31 symbol, so a reversal shows up
		return txTone
	case c.voiced >= 6 && (spread >= 200 || c.minCoh < 0.7 || beating):
		return txVoice
	case c.blocks >= 60 && c.voiced >= 4: // 150 ms after the signal started: settle
		if toneLike {
			return txTone
		}
		return txVoice
	}
	return txUndecided
}

// upperSideband reports whether the signal's energy is mostly above the carrier.
func (c *classifier) upperSideband() bool { return math.Atan2(imag(c.lagSum), real(c.lagSum)) >= 0 }
