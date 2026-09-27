// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package qmx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// radioSim answers CAT commands like a QMX, a few bytes at a time.
type radioSim struct {
	mu      sync.Mutex
	state   map[string]string
	out     string
	written []string
	silent  map[string]bool // prefixes that never answer
	noise   string          // injected before every reply
}

func newRadioSim() *radioSim {
	return &radioSim{
		state: map[string]string{
			"FA": "FA00014074000;", "MD": "MD6;", "Q9": "Q90;", "VN": "VN1_03_002QMX;",
			"PC": "PC38;", "SW": "SW124;", "TQ": "TQ0;",
		},
		silent: map[string]bool{},
	}
}

func (r *radioSim) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cmd := range strings.SplitAfter(string(p), ";") {
		if len(cmd) < 3 {
			continue
		}
		r.written = append(r.written, cmd)
		k := cmd[:2]
		switch {
		case cmd == "TX;":
			r.state["TQ"] = "TQ1;"
		case cmd == "RX;":
			r.state["TQ"] = "TQ0;"
		case cmd == k+";":
			if !r.silent[k] {
				r.out += r.noise + r.state[k]
			}
		default:
			r.state[k] = cmd
		}
	}
	return len(p), nil
}

func (r *radioSim) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.out == "" {
		r.mu.Unlock()
		time.Sleep(time.Millisecond)
		r.mu.Lock()
		return 0, nil
	}
	n := copy(p[:min(5, len(p))], r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *radioSim) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.written...)
}

func start(t *testing.T, r *radioSim) *Client {
	t.Helper()
	c := NewClient(r)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c
}

func TestClientTypedHelpers(t *testing.T) {
	r := newRadioSim()
	c := start(t, r)
	ctx := context.Background()

	if f, err := c.FreqA(ctx); err != nil || f != 14074000 {
		t.Fatalf("FreqA = %d, %v", f, err)
	}
	if err := c.SetFreqA(18113400); err != nil {
		t.Fatal(err)
	}
	if f, _ := c.FreqA(ctx); f != 18113400 {
		t.Fatalf("FreqA after set = %d", f)
	}
	if m, _ := c.Mode(ctx); m != ModeDigi {
		t.Fatalf("Mode = %d", m)
	}
	if err := c.SetIQMode(true); err != nil {
		t.Fatal(err)
	}
	if on, _ := c.IQMode(ctx); !on {
		t.Fatal("IQ mode not on")
	}
	if v, _ := c.Version(ctx); v != "1_03_002QMX" {
		t.Fatalf("Version = %q", v)
	}
	if p, _ := c.PowerOut(ctx); p != 3.8 {
		t.Fatalf("PowerOut = %v", p)
	}
	if s, _ := c.SWR(ctx); s != 1.24 {
		t.Fatalf("SWR = %v", s)
	}
}

func TestClientSkipsUnsolicited(t *testing.T) {
	r := newRadioSim()
	r.noise = "IF00014074000     +00000000002000000;Q91;"
	var mu sync.Mutex
	var got []string
	c := NewClient(r)
	c.Unsolicited = func(m string) { mu.Lock(); got = append(got, m); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if f, err := c.FreqA(context.Background()); err != nil || f != 14074000 {
		t.Fatalf("FreqA = %d, %v", f, err)
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !strings.HasPrefix(got[0], "IF") || got[1] != "Q91;" {
		t.Fatalf("unsolicited = %q", got)
	}
}

func TestClientTimeout(t *testing.T) {
	r := newRadioSim()
	r.silent["VN"] = true
	c := start(t, r)
	t0 := time.Now()
	_, err := c.Version(context.Background())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(t0); d < DefaultTimeout || d > 2*DefaultTimeout {
		t.Fatalf("timeout took %v", d)
	}
	// A later query still works.
	if _, err := c.FreqA(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientTXGuard(t *testing.T) {
	r := newRadioSim()
	c := start(t, r)
	if err := c.TX(); !errors.Is(err, ErrTXNotAllowed) {
		t.Fatalf("TX err = %v", err)
	}
	if err := c.Tone(1500); !errors.Is(err, ErrTXNotAllowed) {
		t.Fatalf("Tone err = %v", err)
	}
	if err := c.Set("TQ1"); !errors.Is(err, ErrTXNotAllowed) {
		t.Fatalf("TQ1 err = %v", err)
	}
	// Leaving TX is always allowed.
	for _, f := range []func() error{c.RX, func() error { return c.Tone(0) }, func() error { return c.Set("TQ0") }} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range r.sent() {
		if s == "TX;" || s == "TQ1;" || strings.HasPrefix(s, "TA1") {
			t.Fatalf("sent %q while TX disabled", s)
		}
	}
	c.AllowTX(true)
	if err := c.TX(); err != nil {
		t.Fatal(err)
	}
	if err := c.Tone(1500.25); err != nil {
		t.Fatal(err)
	}
	if on, _ := c.Transmitting(context.Background()); !on {
		t.Fatal("not transmitting after TX")
	}
	if s := r.sent(); s[len(s)-2] != "TA1500.25;" {
		t.Fatalf("sent %q", s)
	}
}

func TestClientConcurrentQueries(t *testing.T) {
	r := newRadioSim()
	c := start(t, r)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if f, err := c.FreqA(context.Background()); err != nil || f != 14074000 {
				errs <- fmt.Errorf("FreqA %d %v", f, err)
			}
		}()
		go func() {
			defer wg.Done()
			if m, err := c.Mode(context.Background()); err != nil || m != ModeDigi {
				errs <- fmt.Errorf("Mode %d %v", m, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestClientClosed(t *testing.T) {
	r := newRadioSim()
	c := NewClient(r)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	cancel()
	<-done
	if err := c.SetFreqA(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.FreqA(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}
