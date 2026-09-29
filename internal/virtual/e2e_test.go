// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package virtual

import (
	"context"
	"encoding/binary"
	"math"
	"math/cmplx"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rampa069/qmx-hl2/internal/engine"
	"github.com/rampa069/qmx-hl2/internal/hpsdr"
	"github.com/rampa069/qmx-hl2/internal/qmx"
)

// ep6Sink collects EP6 packets with their arrival time.
type ep6Sink struct {
	mu   sync.Mutex
	pkts [][]byte
	at   []time.Time
	n    atomic.Int64
}

func (s *ep6Sink) Send(p []byte, _ netip.AddrPort) error {
	s.mu.Lock()
	s.pkts = append(s.pkts, append([]byte(nil), p...))
	s.at = append(s.at, time.Now())
	s.mu.Unlock()
	s.n.Add(1)
	return nil
}

// between returns receiver 1's samples (1 RX) from packets that arrived in [from, to).
func (s *ep6Sink) between(from, to time.Time) []complex128 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s24 := func(b []byte) float64 {
		v := int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
		if v&0x800000 != 0 {
			v -= 1 << 24
		}
		return float64(v) / (1 << 23)
	}
	per := hpsdr.SamplesPerFrame(1)
	var x []complex128
	for i, p := range s.pkts {
		if s.at[i].Before(from) || !s.at[i].Before(to) {
			continue
		}
		for f := 0; f < 2; f++ {
			fr := p[8+f*512+8:]
			for k := 0; k < per; k++ {
				x = append(x, complex(s24(fr[k*8:]), s24(fr[k*8+3:])))
			}
		}
	}
	return x
}

// ep2 builds a host packet: two frames with C0 and a 32-bit C1-C4 value, and TX I/Q from iq.
func ep2(c0 [2]byte, data [2]uint32, iq func() (float64, float64)) []byte {
	p := make([]byte, hpsdr.DataPacketLen)
	p[0], p[1], p[2], p[3] = 0xEF, 0xFE, 0x01, 0x02
	for f := 0; f < 2; f++ {
		fr := p[8+f*512:]
		fr[0], fr[1], fr[2], fr[3] = 0x7F, 0x7F, 0x7F, c0[f]
		binary.BigEndian.PutUint32(fr[4:8], data[f])
		for i := 0; iq != nil && i < 63; i++ {
			I, Q := iq()
			s := fr[8+i*8:]
			binary.BigEndian.PutUint16(s[4:], uint16(int16(I*32767)))
			binary.BigEndian.PutUint16(s[6:], uint16(int16(Q*32767)))
		}
	}
	return p
}

func bin(x []complex128, f float64) float64 {
	var acc complex128
	for i, v := range x {
		acc += v * cmplx.Exp(complex(0, -2*math.Pi*f*float64(i)/48000))
	}
	return cmplx.Abs(acc) / float64(len(x))
}

// parrotRun runs the engine on a virtual QMX, sends 1 s of MOX with the TX I/Q from iq (a
// client pacing one EP2 per EP6, 48 kHz, 1 RX, RX1 = TX = 14.074 MHz), and returns the RX I/Q
// seen by the client during the replay.
func parrotRun(t *testing.T, iq func() (float64, float64)) []complex128 {
	t.Helper()
	const delay = 2 * time.Second
	r := New(48000, 240, delay)
	cat := qmx.NewClient(r.CAT())
	catCtx, catCancel := context.WithCancel(context.Background())
	defer catCancel()
	go cat.Run(catCtx)

	cfg := engine.DefaultConfig()
	cfg.IQSettle = 0
	cfg.TX.Enabled = true
	cfg.TX.SwapIQ = false // iq gives textbook I + jQ
	out := &ep6Sink{}
	e := engine.New(cfg, cat, r.Capture(), out)
	e.SetPlayback(r.Playback())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.Run(ctx) }()
	defer func() { cancel(); <-done }()

	const f0 = 14074000
	e.EP2(ep2([2]byte{0x02 << 1, 0x00}, [2]uint32{f0, 0x04}, nil)) // RX1; 48 kHz, 1 RX
	e.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	time.Sleep(time.Second) // tune and settle

	// A client pacing TX off EP6: one EP2 (126 samples) per EP6 at 48 kHz, 1 RX.
	send := func(mox bool, d time.Duration, iq func() (float64, float64)) {
		c0 := byte(0x01 << 1) // TX frequency
		if mox {
			c0 |= 1
		}
		sent := out.n.Load()
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Millisecond) {
			for ; sent < out.n.Load(); sent++ {
				e.EP2(ep2([2]byte{c0, c0}, [2]uint32{f0, f0}, iq))
			}
		}
	}
	mox := time.Now()
	send(true, time.Second, iq)
	send(false, 200*time.Millisecond, func() (float64, float64) { return 0, 0 })
	// The replay starts about delay after key-down (MOX + <150 ms) and lasts < 1 s.
	time.Sleep(time.Until(mox.Add(delay + 400*time.Millisecond)))
	from := time.Now()
	time.Sleep(500 * time.Millisecond)
	return out.between(from, time.Now())
}

// An FT8-like tone goes out through CAT TA and comes back at its RF frequency: +1500 Hz above
// RX1, which the client decodes (HPSDR mirrored convention) at -1500 Hz.
func TestParrotToneEndToEnd(t *testing.T) {
	var ph float64
	x := parrotRun(t, func() (float64, float64) {
		ph += 2 * math.Pi * 1500 / 48000
		return 0.5 * math.Cos(ph), 0.5 * math.Sin(ph)
	})
	on, mirror, off := bin(x, -1500), bin(x, 1500), bin(x, -1700)
	if on < 20*mirror || on < 20*off {
		t.Fatalf("replayed tone: %.3g at -1500 Hz, %.3g mirrored, %.3g off", on, mirror, off)
	}
}

// Two tones (voice-like) go out through the QMX SSB path in USB and come back on the right
// side of RX1.
func TestParrotSSBEndToEnd(t *testing.T) {
	var p1, p2 float64
	x := parrotRun(t, func() (float64, float64) {
		p1 += 2 * math.Pi * 700 / 48000
		p2 += 2 * math.Pi * 1900 / 48000
		return 0.3*math.Cos(p1) + 0.3*math.Cos(p2), 0.3*math.Sin(p1) + 0.3*math.Sin(p2)
	})
	for _, f := range []float64{700, 1900} {
		on, mirror := bin(x, -f), bin(x, f)
		if on < 20*mirror || on < 1e-4 {
			t.Errorf("%.0f Hz tone: %.3g on the right side, %.3g mirrored", f, on, mirror)
		}
	}
}

// CWX end to end: the client enables CWX (C&C 0x0f bit 24) and keys with MOX off through
// bit 0 of the TX I words. The QMX (virtual) must transmit a carrier on the TX frequency,
// here 1 kHz above RX1, which the client decodes at -1 kHz.
func TestParrotCWXEndToEnd(t *testing.T) {
	const delay = time.Second
	r := New(48000, 240, delay)
	cat := qmx.NewClient(r.CAT())
	catCtx, catCancel := context.WithCancel(context.Background())
	defer catCancel()
	go cat.Run(catCtx)
	cfg := engine.DefaultConfig()
	cfg.IQSettle = 0
	cfg.TX.Enabled = true
	out := &ep6Sink{}
	e := engine.New(cfg, cat, r.Capture(), out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.Run(ctx) }()
	defer func() { cancel(); <-done }()

	const f0 = 7030000
	e.EP2(ep2([2]byte{0x02 << 1, 0x00}, [2]uint32{f0, 0x04}, nil))                // RX1; 48 kHz, 1 RX
	e.EP2(ep2([2]byte{0x01 << 1, 0x0f << 1}, [2]uint32{f0 + 1000, 1 << 24}, nil)) // TX freq; CWX on
	e.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	time.Sleep(time.Second)

	keyed := func() (float64, float64) { return 1.5 / 32767, 1.5 / 32767 } // bit 0 set in I and Q
	idle := func() (float64, float64) { return 0, 0 }
	send := func(d time.Duration, iq func() (float64, float64)) {
		sent := out.n.Load()
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Millisecond) {
			for ; sent < out.n.Load(); sent++ {
				e.EP2(ep2([2]byte{0x01 << 1, 0x0f << 1}, [2]uint32{f0 + 1000, 1 << 24}, iq)) // MOX off
			}
		}
	}
	start := time.Now()
	send(800*time.Millisecond, keyed)
	send(900*time.Millisecond, idle) // past the 500 ms hang: the over ends
	time.Sleep(time.Until(start.Add(delay + 700*time.Millisecond)))
	from := time.Now()
	time.Sleep(400 * time.Millisecond)
	x := out.between(from, time.Now())
	on, mirror := bin(x, -1000), bin(x, 1000)
	if on < 1e-4 || on < 20*mirror {
		t.Fatalf("CWX carrier: %.3g at -1 kHz, %.3g mirrored", on, mirror)
	}
}
