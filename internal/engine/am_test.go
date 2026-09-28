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

// fsk returns a continuous-phase FSK signal: symbol k (of symLen samples) sits at
// centre + shift*sym(k) Hz.
func fsk(centre, shift float64, symLen int, sym func(k int) float64) func(int) complex128 {
	var ph float64
	last := -1
	return func(i int) complex128 {
		if i != last+1 {
			ph = 0
		}
		last = i
		ph += 2 * math.Pi * (centre + shift*sym(i/symLen)) / 48000
		return complex(0.5*math.Cos(ph), 0.5*math.Sin(ph))
	}
}

// RTTY starts with a steady mark (goes out as a tone), then its shift (170 Hz, or the 85 Hz
// Zeus sent: mark 1500, space 1415) at 45.45 baud
// moves it to SSB on the tone's side of the carrier.
func TestTXRTTYSwitchesToSSB(t *testing.T) {
	bits := []float64{0, 1, 1, 0, 1, 0, 0, 1, 0, 1, 1, 1, 0, 0, 1, 0}
	for _, tc := range []struct{ sign, shift float64 }{{1, 170}, {-1, 170}, {1, -85}} {
		sign := tc.sign
		h := newHarness(t)
		h.tx.ssb = newSSBAudio(48000)
		rtty := fsk(sign*1500, sign*tc.shift, 1056, func(k int) float64 {
			if k < 14 { // ~300 ms of mark idle
				return 0
			}
			return bits[k%len(bits)]
		})
		h.sendIQ(14080000, rtty, 1200*time.Millisecond)
		if !h.tx.voice {
			t.Fatalf("sign %+.0f: RTTY still a tone; radio log %v", sign, h.radio.log)
		}
		want := "MD2"
		if sign < 0 {
			want = "MD1"
		}
		saw := false
		for _, c := range h.radio.log {
			saw = saw || c == want
		}
		if !saw {
			t.Errorf("sign %+.0f: no %s in %v", sign, want, h.radio.log)
		}
	}
}

// FT4 (4-FSK, 20.8 Hz spacing, 48 ms symbols) and CW stay tones.
func TestTXNarrowFSKStaysTone(t *testing.T) {
	ft4 := fsk(1500, 20.833, 2304, func(k int) float64 { return float64([]int{0, 1, 3, 2, 1, 0, 2, 3}[k%8]) })
	cw := func(i int) complex128 { // 25 WPM dits at 600 Hz, 5 ms edges
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
		ph := 2 * math.Pi * 600 * float64(i) / 48000
		return complex(0.5*a*math.Cos(ph), 0.5*a*math.Sin(ph))
	}
	for name, g := range map[string]func(int) complex128{"ft4": ft4, "cw": cw} {
		h := newHarness(t)
		h.tx.ssb = newSSBAudio(48000)
		h.sendIQ(14080000, g, 2*time.Second)
		if h.tx.voice {
			t.Errorf("%s moved to SSB; radio log %v", name, h.radio.log)
		}
	}
}

// Zeus's CW on 40 m (CWL) sits at -600 Hz and has short dropouts with phase jumps; on
// 2026-09-29 one over was taken for AM, because the detector used the momentary frequency,
// which reads 0 Hz between elements. Here the over starts clean, so it goes out as a tone,
// then the dropouts are exaggerated (one per 2.5 ms block; the phase jump stays at one per
// 10 ms, as in the real signal) so that the envelope detector trips. The over must still stay
// a tone, since its carrier started at -600 Hz. (A real capture of Zeus CW had 39% of its
// carrier blocks moving: close to the detector's 50%.)
func TestTXZeusCWStaysTone(t *testing.T) {
	const dot = 48000 * 60 / 1000 // 60 ms at 20 WPM
	pattern := []int{1, 0, 1, 1, 1, 0, 1, 0, 0, 0, 1, 1, 1, 0, 1, 0, 1, 0, 0, 0}
	var ph float64
	cw := func(i int) complex128 {
		el := (i / dot) % len(pattern)
		p := i % dot
		a := 0.0
		if pattern[el] == 1 {
			a = 1
			if pattern[(el+len(pattern)-1)%len(pattern)] == 0 && p < 240 { // 5 ms edges
				a = 0.5 - 0.5*math.Cos(math.Pi*float64(p)/240)
			}
			if pattern[(el+1)%len(pattern)] == 0 && p >= dot-240 {
				a = 0.5 + 0.5*math.Cos(math.Pi*float64(p-(dot-240))/240)
			}
		}
		if i%480 < 24 { // Zeus: a 0.5 ms dropout with a phase jump every 10 ms
			a = 0
			if i%480 == 0 {
				ph += 1.3
			}
		}
		if i > 48000/2 && i%120 < 12 { // exaggerated: also a 0.25 ms dropout in every block
			a = 0
		}
		ph -= 2 * math.Pi * 600 / 48000
		return complex(0.5*a*math.Cos(ph), 0.5*a*math.Sin(ph))
	}
	var d amDetector
	tripped := false
	for i := 0; i < 5*48000; i++ {
		d.add(cw(i))
		tripped = tripped || d.isAM()
	}
	if !tripped {
		t.Fatal("test signal does not trip the envelope detector; the test proves nothing")
	}
	ph = 0
	h := newHarness(t)
	h.tx.ssb = newSSBAudio(48000)
	h.sendIQ(7035700, cw, 5*time.Second)
	if h.tx.voice {
		t.Fatalf("CW moved to SSB; radio log %v", h.radio.log[:min(len(h.radio.log), 12)])
	}
}
