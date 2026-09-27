package hpsdr

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

type event struct {
	kind   string
	addr   netip.AddrPort
	reason string
}

type recHandler struct {
	events chan event
}

func newRec() *recHandler { return &recHandler{events: make(chan event, 16)} }

func (h *recHandler) Started(a netip.AddrPort, _ StartStop) {
	h.events <- event{kind: "start", addr: a}
}
func (h *recHandler) Stopped(r string) { h.events <- event{kind: "stop", reason: r} }
func (h *recHandler) EP2(pkt []byte)   { h.events <- event{kind: "ep2"} }

func (h *recHandler) next(t *testing.T) event {
	t.Helper()
	select {
	case e := <-h.events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handler event")
		return event{}
	}
}

func (h *recHandler) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case e := <-h.events:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(d):
	}
}

func startServer(t *testing.T, wd time.Duration) (*Server, *recHandler) {
	t.Helper()
	h := newRec()
	s, err := NewServer(DefaultIdentity(), h, wd)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, h
}

func dial(t *testing.T, s *Server) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(s.LocalAddr()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func send(t *testing.T, c *net.UDPConn, typ byte, arg byte, n int) {
	t.Helper()
	b := make([]byte, n)
	b[0], b[1], b[2], b[3] = 0xEF, 0xFE, typ, arg
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

func discover(t *testing.T, c *net.UDPConn) []byte {
	t.Helper()
	send(t, c, 0x02, 0, 63)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

func TestServerDiscoveryStartEP2Stop(t *testing.T) {
	s, h := startServer(t, 0)
	c := dial(t, s)

	r := discover(t, c)
	if len(r) != 60 || r[2] != 0x02 || r[0x0A] != 0x06 {
		t.Fatalf("bad idle reply % x", r[:16])
	}

	// Priming EP2 before Start is forwarded.
	send(t, c, 0x01, 0x02, DataPacketLen)
	if e := h.next(t); e.kind != "ep2" {
		t.Fatalf("got %+v", e)
	}

	send(t, c, 0x04, 0x01, 64)
	e := h.next(t)
	local := c.LocalAddr().(*net.UDPAddr).AddrPort()
	if e.kind != "start" || e.addr != local {
		t.Fatalf("got %+v, want start from %v", e, local)
	}
	if got, ok := s.Client(); !ok || got != local {
		t.Fatalf("Client() = %v %v", got, ok)
	}
	if r := discover(t, c); r[2] != 0x03 {
		t.Fatalf("running reply status 0x%02x", r[2])
	}

	// EP2 from another host while streaming is dropped; so is its Stop.
	other := dial(t, s)
	send(t, other, 0x01, 0x02, DataPacketLen)
	send(t, other, 0x04, 0x00, 64)
	h.none(t, 100*time.Millisecond)

	// Wrong-length data packets are ignored.
	send(t, c, 0x01, 0x02, 100)
	h.none(t, 50*time.Millisecond)

	send(t, c, 0x01, 0x02, DataPacketLen)
	if e := h.next(t); e.kind != "ep2" {
		t.Fatalf("got %+v", e)
	}

	send(t, c, 0x04, 0x00, 64)
	if e := h.next(t); e.kind != "stop" || e.reason != "stop command" {
		t.Fatalf("got %+v", e)
	}
	if _, ok := s.Client(); ok {
		t.Fatal("still running after stop")
	}
}

func TestServerWatchdog(t *testing.T) {
	s, h := startServer(t, 200*time.Millisecond)
	c := dial(t, s)
	send(t, c, 0x04, 0x01, 64)
	if e := h.next(t); e.kind != "start" {
		t.Fatalf("got %+v", e)
	}
	// Keep it alive for a while, then go silent.
	for i := 0; i < 5; i++ {
		send(t, c, 0x01, 0x02, DataPacketLen)
		if e := h.next(t); e.kind != "ep2" {
			t.Fatalf("got %+v", e)
		}
		time.Sleep(80 * time.Millisecond)
	}
	if e := h.next(t); e.kind != "stop" || e.reason != "watchdog: no EP2 packets" {
		t.Fatalf("got %+v", e)
	}
}

func TestServerWatchdogDisabledByStartBit(t *testing.T) {
	s, h := startServer(t, 100*time.Millisecond)
	c := dial(t, s)
	send(t, c, 0x04, 0x81, 64)
	if e := h.next(t); e.kind != "start" {
		t.Fatalf("got %+v", e)
	}
	h.none(t, 350*time.Millisecond)
}

func TestServerShutdownStops(t *testing.T) {
	h := newRec()
	s, _ := NewServer(DefaultIdentity(), h, 0)
	if err := s.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- s.Serve(ctx) }()
	c := dial(t, s)
	send(t, c, 0x04, 0x01, 64)
	if e := h.next(t); e.kind != "start" {
		t.Fatalf("got %+v", e)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e := h.next(t); e.kind != "stop" || e.reason != "shutdown" {
		t.Fatalf("got %+v", e)
	}
}
