// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package engine

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/rampa069/qmx-hl2/internal/hpsdr"
)

// amSignal is an AM carrier at 0 Hz with speech-like modulation (two tones under a 4 Hz
// syllable envelope) starting after silentFor samples of bare carrier.
func amSignal(silentFor int) func(i int) complex128 {
	return func(i int) complex128 {
		t := float64(i) / 48000
		m := 0.0
		if i >= silentFor {
			syl := 0.5 + 0.5*math.Sin(2*math.Pi*4*t)
			m = 0.8 * syl * (0.6*math.Sin(2*math.Pi*300*t) + 0.4*math.Sin(2*math.Pi*1100*t))
		}
		return complex(0.4*(1+m), 0)
	}
}

func TestAMDetector(t *testing.T) {
	cw := func(i int) complex128 { // 25 WPM dits with 5 ms edges, carrier at 0 Hz
		p := i % 4608
		switch {
		case p < 240:
			return complex(0.5-0.5*math.Cos(math.Pi*float64(p)/240), 0)
		case p < 2064:
			return 1
		case p < 2304:
			return complex(0.5+0.5*math.Cos(math.Pi*float64(p-2064)/240), 0)
		}
		return 0
	}
	for name, tc := range map[string]struct {
		g    func(int) complex128
		want bool
	}{
		"tune":                 {func(int) complex128 { return 0.99 }, false},
		"cw":                   {cw, false},
		"am":                   {amSignal(0), true},
		"am after 1 s carrier": {amSignal(48000), true},
	} {
		var d amDetector
		got := false
		for i := 0; i < 3*48000; i++ {
			d.add(tc.g(i))
			got = got || d.isAM()
		}
		if got != tc.want {
			t.Errorf("%s: AM = %v, want %v", name, got, tc.want)
		}
	}
}

// sendIQ feeds d of MOX frames with the TX I/Q from g(sample index).
func (h *harness) sendIQ(txFreq uint32, g func(int) complex128, d time.Duration) {
	frames := int(d.Seconds() * 48000 / hpsdr.SamplesPerEP2Frm)
	for i := 0; i < frames; i++ {
		var f txFrame
		f.mox, f.txFreq = true, txFreq
		for k := range f.iq {
			x := g(int(h.phase))
			h.phase++
			f.iq[k] = [2]int16{int16(real(x) * 32767), int16(imag(x) * 32767)}
		}
		h.tx.frame(context.Background(), f)
		h.clock = h.clock.Add(time.Second * hpsdr.SamplesPerEP2Frm / 48000)
		h.tx.tick(context.Background())
	}
}

// An AM over starts as a bare carrier, goes out as a tone like a TUNE, and moves to USB once
// the modulation shows: the QMX cannot transmit AM.
func TestTXAMSwitchesToUSB(t *testing.T) {
	h := newHarness(t)
	h.tx.ssb = newSSBAudio(48000)
	h.sendIQ(7200000, amSignal(24000), 1500*time.Millisecond)
	if !h.tx.voice {
		t.Fatalf("AM over still a tone; radio log %v", h.radio.log)
	}
	if n := len(h.active); n != 2 || !h.active[0] || !h.active[1] {
		t.Errorf("active callbacks %v", h.active)
	}
	sawUSB := false
	for _, c := range h.radio.log {
		sawUSB = sawUSB || c == "MD2"
	}
	if !sawUSB {
		t.Errorf("no switch to USB: %v", h.radio.log)
	}
}

// A TUNE carrier stays a tone (through SSB, 0 Hz would be filtered to nothing).
func TestTXTuneStaysTone(t *testing.T) {
	h := newHarness(t)
	h.tx.ssb = newSSBAudio(48000)
	h.sendIQ(7200000, func(int) complex128 { return 0.99 }, 2*time.Second)
	if h.tx.voice || h.radio.count("TA") == 0 {
		t.Fatalf("TUNE: voice=%v, TA commands %d", h.tx.voice, h.radio.count("TA"))
	}
}
