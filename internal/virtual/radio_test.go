// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package virtual

import (
	"context"
	"math"
	"math/cmplx"
	"testing"
	"time"

	"github.com/rampa069/qmx-hl2/internal/qmx"
)

func newTestRadio(t *testing.T, delay time.Duration) (*Radio, *qmx.Client) {
	t.Helper()
	r := New(48000, 240, delay)
	c := qmx.NewClient(r.CAT())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	c.AllowTX(true)
	return r, c
}

// power returns the mean power of x at f Hz (single-bin DFT).
func power(x []complex128, f float64) float64 {
	var acc complex128
	for i, v := range x {
		acc += v * cmplx.Exp(complex(0, -2*math.Pi*f*float64(i)/48000))
	}
	a := cmplx.Abs(acc) / float64(len(x))
	return a * a
}

// captureFor reads d of I/Q from the virtual radio.
func captureFor(r *Radio, d time.Duration) []complex128 {
	c := r.Capture()
	buf := make([]float32, 2*240)
	var x []complex128
	for n := 0; n < int(d.Seconds()*48000); n += 240 {
		c.Read(buf)
		for i := 0; i < 240; i++ {
			x = append(x, complex(float64(buf[2*i]), float64(buf[2*i+1])))
		}
	}
	return x
}

func TestCATState(t *testing.T) {
	_, c := newTestRadio(t, time.Second)
	ctx := context.Background()
	if err := c.SetFreqA(7074000); err != nil {
		t.Fatal(err)
	}
	if f, err := c.FreqA(ctx); err != nil || f != 7074000 {
		t.Fatalf("FA = %d, %v", f, err)
	}
	c.SetMode(qmx.ModeUSB)
	if m, err := c.Mode(ctx); err != nil || m != qmx.ModeUSB {
		t.Fatalf("MD = %d, %v", m, err)
	}
	if r, err := c.Query(ctx, "SS;"); err != nil || r != "SS0;" {
		t.Fatalf("SS = %q, %v", r, err)
	}
	c.TX()
	if tx, _ := c.Transmitting(ctx); !tx {
		t.Fatal("not transmitting after TX")
	}
	if w, _ := c.PowerOut(ctx); w != txWatts {
		t.Fatalf("power %.1f W", w)
	}
	c.RX()
	if tx, _ := c.Transmitting(ctx); tx {
		t.Fatal("still transmitting after RX")
	}
}

// A Digi tone at dial + TA comes back in the I/Q at its RF offset from the LO (dial - 12 kHz).
func TestToneIsReplayed(t *testing.T) {
	r, c := newTestRadio(t, 200*time.Millisecond)
	c.SetFreqA(14074000)
	c.SetMode(qmx.ModeDigi)
	c.TX()
	c.Tone(1500)
	time.Sleep(500 * time.Millisecond)
	c.RX()
	// The recording ends 0.7 s after key-up, then replays 0.3 s later for 0.5 s.
	x := captureFor(r, 2*time.Second)
	want := 12000.0 + 1500
	on, off := power(x, want), power(x, -want)
	if on < 1e-6 || on < 1000*off {
		t.Fatalf("replay at %+.0f Hz: power %.2g (mirror %.2g)", want, on, off)
	}
}

// USB and LSB audio come back on the right side of the dial.
func TestSSBIsReplayed(t *testing.T) {
	for _, tc := range []struct {
		mode int
		want float64 // I/Q frequency of a 1 kHz audio tone
	}{{qmx.ModeUSB, 12000 + 1000}, {qmx.ModeLSB, 12000 - 1000}} {
		r, c := newTestRadio(t, 200*time.Millisecond)
		c.SetFreqA(14230000)
		c.SetMode(tc.mode)
		c.TX()
		time.Sleep(20 * time.Millisecond) // let the CAT client deliver TX
		pb := r.Playback()
		buf := make([]float32, 2*240)
		for n := 0; n < 100; n++ { // 0.5 s
			for i := 0; i < 240; i++ {
				v := float32(0.5 * math.Sin(2*math.Pi*1000*float64(n*240+i)/48000))
				buf[2*i], buf[2*i+1] = v, v
			}
			pb.Write(buf)
		}
		c.RX()
		x := captureFor(r, 2*time.Second)
		on, mirror := power(x, tc.want), power(x, 2*12000-tc.want)
		if on < 1e-6 || on < 100*mirror {
			t.Errorf("mode %d: power at %.0f Hz %.2g, at the other sideband %.2g", tc.mode, tc.want, on, mirror)
		}
	}
}

func TestAnalytic(t *testing.T) {
	x := make([]float32, 4800)
	for i := range x {
		x[i] = float32(math.Cos(2 * math.Pi * 1000 * float64(i) / 48000))
	}
	z := analytic(x, false)
	var pos, neg complex128
	for i, v := range z {
		pos += complex128(v) * cmplx.Exp(complex(0, -2*math.Pi*1000*float64(i)/48000))
		neg += complex128(v) * cmplx.Exp(complex(0, 2*math.Pi*1000*float64(i)/48000))
	}
	if cmplx.Abs(pos) < 100*cmplx.Abs(neg) {
		t.Fatalf("sideband rejection too low: %.3g vs %.3g", cmplx.Abs(pos), cmplx.Abs(neg))
	}
}

// A replay that falls due while the client transmits waits for key-up instead of being lost
// under the daemon's receive mute.
func TestReplayWaitsWhileKeyed(t *testing.T) {
	r, c := newTestRadio(t, 100*time.Millisecond)
	c.SetFreqA(14074000)
	c.SetMode(qmx.ModeDigi)
	c.TX()
	c.Tone(1500)
	time.Sleep(300 * time.Millisecond)
	c.RX()
	time.Sleep(time.Second) // recording closes 0.7 s after key-up; replay due 0.3 s later
	c.TX()                  // the client transmits again before the replay is heard
	time.Sleep(50 * time.Millisecond)
	captureFor(r, 500*time.Millisecond)
	r.mu.Lock()
	pos := r.queue[0].pos
	r.mu.Unlock()
	if pos != 0 {
		t.Fatalf("replay advanced %d samples while keyed", pos)
	}
	c.RX()
	time.Sleep(50 * time.Millisecond)
	x := captureFor(r, 500*time.Millisecond)
	if p := power(x, 12000+1500); p < 1e-6 {
		t.Fatalf("replay not heard after key-up: %.2g", p)
	}
}
