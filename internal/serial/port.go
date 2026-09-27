// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// Package serial owns the USB CDC serial link to the QMX. CAT framing and command logic live in
// a higher layer; this package only opens, closes, reads and writes the port.
package serial

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	goserial "go.bug.st/serial"
)

// DefaultBaud is what the QMX documentation uses. The QMX port is USB CDC, so the value is
// nominal, but some OS drivers still want one set.
const DefaultBaud = 115200

// Port is a serial port with serialized writes. One goroutine may Read while others Write.
type Port struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	port    goserial.Port
	dev     string
	baud    int
}

// NewPort returns an unopened Port.
func NewPort(baud int) *Port { return &Port{baud: baud} }

// Open opens (or reopens) device.
func (p *Port) Open(device string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.port != nil {
		_ = p.port.Close()
		p.port = nil
	}
	sp, err := goserial.Open(device, &goserial.Mode{
		BaudRate: p.baud,
		DataBits: 8,
		Parity:   goserial.NoParity,
		StopBits: goserial.OneStopBit,
	})
	if err != nil {
		return fmt.Errorf("open %s: %w", device, err)
	}
	// A short timeout keeps reader goroutines responsive to shutdown.
	if err := sp.SetReadTimeout(100 * time.Millisecond); err != nil {
		_ = sp.Close()
		return fmt.Errorf("set read timeout on %s: %w", device, err)
	}
	p.port = sp
	p.dev = device
	slog.Info("serial port opened", "device", device, "baud", p.baud)
	return nil
}

// Close closes the port. It is safe to call on a closed Port.
func (p *Port) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.port == nil {
		return nil
	}
	err := p.port.Close()
	p.port = nil
	slog.Info("serial port closed", "device", p.dev)
	return err
}

// IsOpen reports whether the port is open.
func (p *Port) IsOpen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port != nil
}

// Device returns the path of the last opened device.
func (p *Port) Device() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dev
}

func (p *Port) current() goserial.Port {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

// Write sends data, serialized against other writers.
func (p *Port) Write(data []byte) (int, error) {
	sp := p.current()
	if sp == nil {
		return 0, fmt.Errorf("serial port not open")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return sp.Write(data)
}

// Read reads up to len(buf) bytes. It returns (0, nil) when the read timeout expires.
func (p *Port) Read(buf []byte) (int, error) {
	sp := p.current()
	if sp == nil {
		return 0, fmt.Errorf("serial port not open")
	}
	return sp.Read(buf)
}
