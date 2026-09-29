// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/rampa069/qmx-hl2/internal/audio"
	"github.com/rampa069/qmx-hl2/internal/engine"
	"github.com/rampa069/qmx-hl2/internal/hpsdr"
	"github.com/rampa069/qmx-hl2/internal/qmx"
)

// handlerProxy is the HPSDR server's handler for the whole life of the daemon. It forwards to
// the engine of the current QMX connection, and remembers the client's Start so that an
// engine created after a USB reconnect resumes streaming without the client noticing more
// than a gap. Host packets that arrive while there is no engine are dropped: clients send
// their registers in rotation, so the next engine catches up within a few packets.
type handlerProxy struct {
	mu       sync.Mutex
	eng      *engine.Engine
	running  bool
	addr     netip.AddrPort
	startCmd hpsdr.StartStop
}

func (p *handlerProxy) attach(e *engine.Engine) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eng = e
	if p.running {
		e.Started(p.addr, p.startCmd)
	}
}

func (p *handlerProxy) detach() {
	p.mu.Lock()
	p.eng = nil
	p.mu.Unlock()
}

func (p *handlerProxy) Started(addr netip.AddrPort, cmd hpsdr.StartStop) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running, p.addr, p.startCmd = true, addr, cmd
	if p.eng != nil {
		p.eng.Started(addr, cmd)
	}
}

func (p *handlerProxy) Stopped(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	if p.eng != nil {
		p.eng.Stopped(reason)
	}
}

func (p *handlerProxy) EP2(pkt []byte) {
	p.mu.Lock()
	e := p.eng
	p.mu.Unlock()
	if e != nil {
		e.EP2(pkt)
	}
}

// qmxSession is one connection to the QMX: its CAT port, audio streams and engine.
type qmxSession struct {
	dev     string
	port    qmx.ReadWriter
	capt    audio.CaptureStream
	play    func() (audio.PlaybackStream, error)
	closers []func()
}

func (s *qmxSession) close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	s.closers = nil
}

// errQMXLost reports that the QMX went away (USB unplugged, firmware update, power off).
var errQMXLost = errors.New("lost the QMX")

// run drives one session until the QMX goes away or ctx ends. It attaches the session's engine
// to the proxy and detaches it on return. saved is the original QMX state: nil on the first
// connection (then it is filled in), reused on later ones.
func (s *qmxSession) run(ctx context.Context, ecfg engine.Config, proxy *handlerProxy, send engine.Sender,
	saved *map[string]string, tx bool, txMode string) error {
	cat := qmx.NewClient(s.port)
	catCtx, catCancel := context.WithCancel(context.Background())
	catDone := make(chan error, 1)
	go func() { catDone <- cat.Run(catCtx) }()
	defer func() { catCancel(); <-catDone }()

	eng := engine.New(ecfg, cat, s.capt, send)
	if *saved != nil {
		eng.SetSavedState(*saved)
	}
	if tx && txMode != "tone" {
		pb, err := s.play()
		if err != nil {
			return fmt.Errorf("SSB transmit needs QMX audio playback: %w", err)
		}
		s.closers = append(s.closers, func() { _ = pb.Close() })
		eng.SetPlayback(pb)
	}

	// Stop the engine if the serial link dies (the capture failing stops it by itself).
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	serialLost := make(chan error, 1)
	go func() {
		select {
		case err := <-catDone:
			serialLost <- err
			catDone <- err // for the deferred wait
			engCancel()
		case <-engCtx.Done():
		}
	}()

	proxy.attach(eng)
	defer proxy.detach()
	err := eng.Run(engCtx)
	if *saved == nil {
		*saved = eng.SavedState()
	}
	select {
	case serr := <-serialLost:
		return fmt.Errorf("%w: serial: %v", errQMXLost, serr)
	default:
	}
	if err != nil && ctx.Err() == nil {
		slog.Debug("engine stopped", "err", err)
		return fmt.Errorf("%w: %v", errQMXLost, err)
	}
	return err
}
