// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package hpsdr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Handler receives protocol events. Calls come from the server's receive goroutine, one at a
// time, so implementations must not block for long.
type Handler interface {
	// Started is called when a client starts streaming. EP6 must go to addr (the source of the
	// Start packet), as the HL2 does.
	Started(addr netip.AddrPort, cmd StartStop)
	// Stopped is called on an explicit Stop, on a watchdog timeout, or when the server shuts down.
	Stopped(reason string)
	// EP2 is called for each host-to-radio data packet from the active client, and also before
	// Start (clients prime C&C that way). pkt is only valid during the call.
	EP2(pkt []byte)
}

// Server answers discovery and start/stop on UDP port 1024 and forwards EP2 packets.
type Server struct {
	id       Identity
	handler  Handler
	watchdog time.Duration

	conn *net.UDPConn

	mu         sync.Mutex
	running    bool
	client     netip.AddrPort
	wdDisabled bool
	lastEP2    time.Time
}

// NewServer creates a server. watchdog is how long streaming continues without EP2 packets
// before the server stops on its own (the HL2 gateware uses about 11-13 s). Zero disables it.
func NewServer(id Identity, h Handler, watchdog time.Duration) (*Server, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return &Server{id: id, handler: h, watchdog: watchdog}, nil
}

// Listen binds the UDP socket. addr is usually ":1024".
func (s *Server) Listen(addr string) error {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	c, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.conn = c
	slog.Info("HPSDR server listening", "addr", c.LocalAddr().String(), "mac", s.id.MAC.String())
	return nil
}

// LocalAddr returns the bound address. Listen must have been called.
func (s *Server) LocalAddr() netip.AddrPort {
	return s.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// Conn returns the socket, for sending EP6 from the same port 1024 as a real HL2.
func (s *Server) Conn() *net.UDPConn { return s.conn }

// Send writes a packet to addr from the server's port 1024 socket, as a real HL2 does.
func (s *Server) Send(pkt []byte, addr netip.AddrPort) error {
	_, err := s.conn.WriteToUDPAddrPort(pkt, addr)
	return err
}

// Client returns the streaming client, if any.
func (s *Server) Client() (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client, s.running
}

// Serve handles packets until ctx is cancelled. It closes the socket on return.
func (s *Server) Serve(ctx context.Context) error {
	if s.conn == nil {
		return errors.New("Serve called before Listen")
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = s.conn.Close()
	}()
	if s.watchdog > 0 {
		go s.watchdogLoop(ctx)
	}

	buf := make([]byte, 2048)
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				s.stop("shutdown")
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		s.handle(buf[:n], from)
	}
}

func (s *Server) handle(b []byte, from netip.AddrPort) {
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	switch {
	case IsDiscoveryRequest(b):
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if _, err := s.conn.WriteToUDPAddrPort(DiscoveryReply(s.id, running), from); err != nil {
			slog.Warn("discovery reply failed", "to", from.String(), "err", err)
			return
		}
		slog.Info("discovery answered", "from", from.String(), "running", running)

	case len(b) >= 4 && b[2] == typeStartStop:
		cmd, ok := ParseStartStop(b)
		if !ok {
			return
		}
		if cmd.IQ {
			s.start(from, cmd)
		} else {
			s.mu.Lock()
			active := s.running && s.client == from
			s.mu.Unlock()
			if active {
				s.stop("stop command")
			} else {
				slog.Debug("stop ignored from non-active client", "from", from.String())
			}
		}

	case IsEP2(b):
		s.mu.Lock()
		// While streaming, only the active client may drive the radio.
		accept := !s.running || s.client == from
		if accept {
			s.lastEP2 = time.Now()
		}
		s.mu.Unlock()
		if accept {
			s.handler.EP2(b)
		}

	default:
		slog.Debug("ignored packet", "from", from.String(), "len", len(b))
	}
}

func (s *Server) start(from netip.AddrPort, cmd StartStop) {
	s.mu.Lock()
	if s.running && s.client != from {
		// A real HL2 freezes the destination while running. Take over anyway: the old client
		// most likely died without sending Stop, and refusing would lock the radio up
		// until the watchdog fires.
		slog.Warn("start from new client while streaming; switching", "old", s.client.String(), "new", from.String())
	}
	s.running = true
	s.client = from
	s.wdDisabled = cmd.WatchdogDisable
	s.lastEP2 = time.Now()
	s.mu.Unlock()
	slog.Info("stream started", "client", from.String(), "wideband", cmd.Wideband, "watchdog_disabled", cmd.WatchdogDisable)
	s.handler.Started(from, cmd)
}

func (s *Server) stop(reason string) {
	s.mu.Lock()
	was := s.running
	s.running = false
	s.mu.Unlock()
	if was {
		slog.Info("stream stopped", "reason", reason)
		s.handler.Stopped(reason)
	}
}

func (s *Server) watchdogLoop(ctx context.Context) {
	t := time.NewTicker(s.watchdog / 10)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			expired := s.running && !s.wdDisabled && now.Sub(s.lastEP2) > s.watchdog
			s.mu.Unlock()
			if expired {
				s.stop("watchdog: no EP2 packets")
			}
		}
	}
}
