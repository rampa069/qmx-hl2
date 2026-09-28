// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package engine

import "math"

// amDetector tells AM from the other carriers at 0 Hz baseband. The QMX cannot transmit AM
// (MD5 is receive only), and an AM over starts as a plain carrier, which the classifier sends
// out as a tone like a TUNE. Once the operator speaks, the envelope moves at audio rate, so
// nearly every 2.5 ms block with carrier has its minimum well below its maximum. A TUNE
// carrier is constant, and CW only varies in the blocks of its 5 ms edges.
type amDetector struct {
	n        int
	min, max float64 // |x| in the current block
	peak     float64 // largest block maximum so far
	window   [amWindow]int8
	pos      int
	carrier  int // blocks with carrier in the window
	mod      int // of those, blocks whose envelope moved
}

const (
	amBlock  = 120 // 2.5 ms at 48 kHz
	amWindow = 80  // 200 ms of blocks
)

func (d *amDetector) reset() { *d = amDetector{} }

func (d *amDetector) add(x complex128) {
	a := math.Hypot(real(x), imag(x))
	if d.n == 0 {
		d.min, d.max = a, a
	} else {
		d.min, d.max = math.Min(d.min, a), math.Max(d.max, a)
	}
	d.n++
	if d.n < amBlock {
		return
	}
	d.n = 0
	d.peak = math.Max(d.peak, d.max)
	var state int8 // 0 no carrier, 1 steady carrier, 2 moving envelope
	if d.max > 0.3*d.peak && d.max > 0.01 {
		state = 1
		if d.min < 0.9*d.max {
			state = 2
		}
	}
	switch d.window[d.pos] { // the block leaving the window
	case 2:
		d.mod--
		d.carrier--
	case 1:
		d.carrier--
	}
	switch state {
	case 2:
		d.mod++
		d.carrier++
	case 1:
		d.carrier++
	}
	d.window[d.pos] = state
	d.pos = (d.pos + 1) % amWindow
}

// isAM reports whether, over the last 200 ms, most of the blocks carried a carrier whose
// envelope was moving.
func (d *amDetector) isAM() bool { return d.carrier >= amWindow/2 && d.mod*2 > d.carrier }
