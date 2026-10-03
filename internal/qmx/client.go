// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package qmx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrTXNotAllowed is returned for transmit commands unless AllowTX(true) was called.
var ErrTXNotAllowed = errors.New("qmx: transmit commands are disabled")

// ErrClosed is returned after the client stops.
var ErrClosed = errors.New("qmx: client closed")

// DefaultTimeout bounds a query round trip. The QMX usually answers within a few ms.
const DefaultTimeout = 500 * time.Millisecond

// Mode values for MD (CAT manual 1_04_004).
const (
	ModeLSB  = 1
	ModeUSB  = 2
	ModeCW   = 3
	ModeAM   = 5
	ModeDigi = 6 // "FSK" in Kenwood terms
	ModeCWR  = 7
)

// Client is a concurrent-safe QMX CAT client. One goroutine (Run) owns reading; any goroutine
// may call Set and Query. Replies are matched to queries by their two-letter prefix; messages
// nobody asked for are passed to the Unsolicited callback.
type Client struct {
	rw ReadWriter

	// Unsolicited, if set before Run, receives messages that match no pending query
	// (for example IF; auto-information or echoed set commands). It runs on the reader
	// goroutine and must not block.
	Unsolicited func(msg string)

	writeMu sync.Mutex // serialises writes
	queryMu sync.Mutex // one query in flight: the QMX answers in order, and prefixes may repeat

	mu      sync.Mutex
	pending *pendingQuery
	closed  bool
	allowTX bool
	done    chan struct{}
}

type pendingQuery struct {
	prefix string
	reply  chan string
}

// NewClient wraps a serial port (or any ReadWriter whose Read returns (0, nil) on timeout).
func NewClient(rw ReadWriter) *Client {
	return &Client{rw: rw, done: make(chan struct{})}
}

// AllowTX enables or disables the transmit commands (TX, TQ1, TA). They are refused by
// default so that receive-only code can never key the radio by mistake.
func (c *Client) AllowTX(on bool) {
	c.mu.Lock()
	c.allowTX = on
	c.mu.Unlock()
}

// Run reads and dispatches replies until ctx ends or the port fails.
func (c *Client) Run(ctx context.Context) error {
	defer func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.done)
	}()
	buf := make([]byte, 256)
	var acc []byte
	for ctx.Err() == nil {
		n, err := c.rw.Read(buf)
		if err != nil {
			return fmt.Errorf("qmx read: %w", err)
		}
		acc = append(acc, buf[:n]...)
		for {
			i := indexByte(acc, ';')
			if i < 0 {
				break
			}
			msg := strings.TrimLeft(string(acc[:i+1]), "\r\n ")
			acc = acc[i+1:]
			if msg != ";" {
				c.dispatch(msg)
			}
		}
		if len(acc) > 1024 { // garbage without terminators: drop it
			acc = acc[:0]
		}
	}
	return nil
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func (c *Client) dispatch(msg string) {
	c.mu.Lock()
	p := c.pending
	if p != nil && strings.HasPrefix(msg, p.prefix) {
		c.pending = nil
		c.mu.Unlock()
		p.reply <- msg
		return
	}
	c.mu.Unlock()
	if c.Unsolicited != nil {
		c.Unsolicited(msg)
	} else {
		slog.Debug("qmx unsolicited", "msg", msg)
	}
}

func (c *Client) write(cmd string) error {
	c.mu.Lock()
	closed, allow := c.closed, c.allowTX
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if !allow && isTXCommand(cmd) {
		return ErrTXNotAllowed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.rw.Write([]byte(cmd))
	return err
}

func isTXCommand(cmd string) bool {
	return strings.HasPrefix(cmd, "TX") || strings.HasPrefix(cmd, "TQ1") ||
		(strings.HasPrefix(cmd, "TA") && cmd != "TA0;" && cmd != "TA;")
}

func terminate(cmd string) string {
	if !strings.HasSuffix(cmd, ";") {
		cmd += ";"
	}
	return cmd
}

// Set sends a command that has no reply, such as "FA00014074000;".
func (c *Client) Set(cmd string) error { return c.write(terminate(cmd)) }

// Query sends a read command such as "FA;" and returns the reply ("FA00014074000;").
func (c *Client) Query(ctx context.Context, cmd string) (string, error) {
	cmd = terminate(cmd)
	if len(cmd) < 3 {
		return "", fmt.Errorf("qmx: command %q too short", cmd)
	}
	c.queryMu.Lock()
	defer c.queryMu.Unlock()

	p := &pendingQuery{prefix: cmd[:2], reply: make(chan string, 1)}
	c.mu.Lock()
	c.pending = p
	c.mu.Unlock()
	clear := func() {
		c.mu.Lock()
		if c.pending == p {
			c.pending = nil
		}
		c.mu.Unlock()
	}
	if err := c.write(cmd); err != nil {
		clear()
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	select {
	case r := <-p.reply:
		return r, nil
	case <-ctx.Done():
		clear()
		return "", fmt.Errorf("qmx: no reply to %q: %w", cmd, ctx.Err())
	case <-c.done:
		return "", ErrClosed
	}
}

// Value returns the payload of a reply after its prefix: "FA00014074000;" -> "00014074000".
func Value(reply string) string {
	if len(reply) < 3 {
		return ""
	}
	return strings.TrimSuffix(reply[2:], ";")
}

// QueryInt queries cmd and parses the payload as an integer.
func (c *Client) QueryInt(ctx context.Context, cmd string) (int64, error) {
	r, err := c.Query(ctx, cmd)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(Value(r), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("qmx: bad reply %q to %q", r, cmd)
	}
	return v, nil
}

// --- typed helpers ---

// SetFreqA tunes VFO A (the dial) in Hz.
func (c *Client) SetFreqA(hz uint32) error { return c.Set(fmt.Sprintf("FA%011d;", hz)) }

// FreqA reads VFO A in Hz.
func (c *Client) FreqA(ctx context.Context) (uint32, error) {
	v, err := c.QueryInt(ctx, "FA;")
	return uint32(v), err
}

// SetFreqB sets VFO B in Hz (the TX VFO when split is on).
func (c *Client) SetFreqB(hz uint32) error { return c.Set(fmt.Sprintf("FB%011d;", hz)) }

// SetSplit turns split on (RX on VFO A, TX on VFO B) or off.
func (c *Client) SetSplit(on bool) error {
	if on {
		return c.Set("SP1;")
	}
	return c.Set("SP0;")
}

// RXVFO reports which VFO the QMX receives on: 0 for VFO A, 1 for VFO B (some firmware also
// answers 2 for split).
func (c *Client) RXVFO(ctx context.Context) (int, error) {
	v, err := c.QueryInt(ctx, "FR;")
	return int(v), err
}

// SetVFOA makes the QMX receive and transmit on VFO A (FR0;), the VFO that FA tunes.
func (c *Client) SetVFOA() error { return c.Set("FR0;") }

// SetKeyerSpeed sets the keyer speed in WPM (session only, not saved to EEPROM).
func (c *Client) SetKeyerSpeed(wpm int) error { return c.Set(fmt.Sprintf("KS%02d;", wpm)) }

// CWOffset reads the CW offset (sidetone pitch) in Hz from the menu: in CW mode the receive LO
// sits this much further below the dial (LO = dial - 12000 - offset). Firmware 1_02_006+.
func (c *Client) CWOffset(ctx context.Context) (int, error) {
	r, err := c.Query(ctx, "MMCW|CW offset;")
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r, "MM"), ";"))
	if err != nil || v <= 0 || v > 2000 {
		return 0, fmt.Errorf("qmx: bad CW offset reply %q", r)
	}
	return v, nil
}

// SetMode sets the operating mode (ModeDigi etc.).
func (c *Client) SetMode(m int) error { return c.Set(fmt.Sprintf("MD%d;", m)) }

// Mode reads the operating mode.
func (c *Client) Mode(ctx context.Context) (int, error) {
	v, err := c.QueryInt(ctx, "MD;")
	return int(v), err
}

// IQMode reports whether IQ mode (Q9) is on.
func (c *Client) IQMode(ctx context.Context) (bool, error) {
	v, err := c.QueryInt(ctx, "Q9;")
	return v == 1, err
}

// SetIQMode turns IQ mode on or off (session only; not saved to EEPROM).
func (c *Client) SetIQMode(on bool) error {
	if on {
		return c.Set("Q91;")
	}
	return c.Set("Q90;")
}

// Version returns the firmware version string, e.g. "1_03_002QMX".
func (c *Client) Version(ctx context.Context) (string, error) {
	r, err := c.Query(ctx, "VN;")
	return Value(r), err
}

// PowerOut returns the measured output power in watts (valid while transmitting).
func (c *Client) PowerOut(ctx context.Context) (float64, error) {
	v, err := c.QueryInt(ctx, "PC;")
	return float64(v) / 10, err
}

// SWR returns the measured SWR (valid while transmitting; 0 when the radio gives no value).
func (c *Client) SWR(ctx context.Context) (float64, error) {
	r, err := c.Query(ctx, "SW;")
	if err != nil {
		return 0, err
	}
	if Value(r) == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(Value(r))
	if err != nil {
		return 0, fmt.Errorf("qmx: bad SWR reply %q", r)
	}
	return float64(v) / 100, nil
}

// SWRProtection reports whether the QMX's SWR protection has locked transmit (CAT SR, firmware
// 1_04_004 or later): SR1 means locked. Older firmware does not answer, so this times out.
func (c *Client) SWRProtection(ctx context.Context) (bool, error) {
	v, err := c.QueryInt(ctx, "SR;")
	return v == 1, err
}

// Transmitting reports the TX state (TQ).
func (c *Client) Transmitting(ctx context.Context) (bool, error) {
	v, err := c.QueryInt(ctx, "TQ;")
	return v == 1, err
}

// TX keys the transmitter. Requires AllowTX(true).
func (c *Client) TX() error { return c.Set("TX;") }

// RX returns to receive. Always allowed.
func (c *Client) RX() error { return c.Set("RX;") }

// Tone sets the Digi transmit tone in Hz (CAT TA, firmware 1_02_004 or later). A tone below
// 10 Hz keys up with a shaped envelope. Requires AllowTX(true) unless hz < 10.
func (c *Client) Tone(hz float64) error {
	if hz < 10 {
		return c.Set("TA0;")
	}
	return c.Set(fmt.Sprintf("TA%.2f;", hz))
}

// SetCATTimeout arms the QMX's own watchdog: with CAT timeout enabled, the radio returns to
// receive if no CAT command arrives for secs seconds while transmitting.
func (c *Client) SetCATTimeout(enable bool, secs int) error {
	if err := c.Set(fmt.Sprintf("QC%d;", secs)); err != nil {
		return err
	}
	if enable {
		return c.Set("QB1;")
	}
	return c.Set("QB0;")
}
