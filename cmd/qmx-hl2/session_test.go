// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package main

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rampa069/qmx-hl2/internal/audio"
	"github.com/rampa069/qmx-hl2/internal/engine"
	"github.com/rampa069/qmx-hl2/internal/hpsdr"
	"github.com/rampa069/qmx-hl2/internal/virtual"
)

// dyingCapture fails like an unplugged USB sound card after n reads.
type dyingCapture struct {
	audio.CaptureStream
	n int
}

func (c *dyingCapture) Read(dst []float32) (int, error) {
	if c.n--; c.n < 0 {
		return 0, errors.New("capture: I/O error")
	}
	return c.CaptureStream.Read(dst)
}

type countSender struct{ n atomic.Int64 }

func (s *countSender) Send([]byte, netip.AddrPort) error { s.n.Add(1); return nil }

func virtualSession(r *virtual.Radio, capt audio.CaptureStream) *qmxSession {
	return &qmxSession{dev: "virtual", port: r.CAT(), capt: capt,
		play: func() (audio.PlaybackStream, error) { return r.Playback(), nil }}
}

// A USB disconnect ends the session with errQMXLost; the next session's engine resumes
// streaming to the client (no new Start needed) and keeps the original QMX state.
func TestSessionReconnect(t *testing.T) {
	ecfg := engine.DefaultConfig()
	ecfg.IQSettle = 0
	proxy := &handlerProxy{}
	send := &countSender{}
	proxy.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := virtual.New(48000, 240, time.Second)
	var saved map[string]string
	s1 := virtualSession(r, &dyingCapture{CaptureStream: r.Capture(), n: 200}) // ~1 s
	err := s1.run(ctx, ecfg, proxy, send, &saved, false, "auto")
	s1.close()
	if !errors.Is(err, errQMXLost) {
		t.Fatalf("first session ended with %v, want errQMXLost", err)
	}
	if saved == nil || saved["MD"] == "" {
		t.Fatalf("original QMX state not kept: %v", saved)
	}
	first := send.n.Load()
	if first == 0 {
		t.Fatal("no EP6 in the first session")
	}

	// The second session: the radio "came back". The QMX now holds our settings (Digi, IQ
	// on); the original state from the first session must be the one kept.
	cat := r.CAT()
	cat.Write([]byte("MD2;")) // left in a mode that is not the user's original (Digi)
	s2 := virtualSession(r, r.Capture())
	done := make(chan error, 1)
	ctx2, cancel2 := context.WithCancel(ctx)
	go func() { done <- s2.run(ctx2, ecfg, proxy, send, &saved, false, "auto") }()
	time.Sleep(time.Second)
	cancel2()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("second session: %v", err)
	}
	s2.close()
	if send.n.Load() <= first {
		t.Fatal("EP6 did not resume after the reconnect")
	}
	// On exit the second engine restores the original state, not what it found (MD2).
	time.Sleep(50 * time.Millisecond)
	cat.Write([]byte("MD;"))
	buf := make([]byte, 16)
	n, _ := cat.Read(buf)
	if got := string(buf[:n]); got != saved["MD"] {
		t.Fatalf("mode after the second session %q, want the original %q", got, saved["MD"])
	}
}

// Packets that arrive while no engine is attached are dropped; Start is replayed on attach.
func TestHandlerProxyReplaysStart(t *testing.T) {
	p := &handlerProxy{}
	p.EP2(make([]byte, hpsdr.DataPacketLen)) // no engine: must not panic
	p.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	if !p.running {
		t.Fatal("Start not remembered")
	}
	p.Stopped("stop")
	if p.running {
		t.Fatal("Stop not remembered")
	}
}
