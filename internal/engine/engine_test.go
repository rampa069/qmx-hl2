package engine

import (
	"context"
	"encoding/binary"
	"math"
	"math/cmplx"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rampa069/qmx-hl2/internal/hpsdr"
	"github.com/rampa069/qmx-hl2/internal/qmx"
)

// fakeCAT emulates the few QMX CAT commands the engine uses.
type fakeCAT struct {
	mu      sync.Mutex
	state   map[string]string
	pending string
	log     []string
}

func newFakeCAT() *fakeCAT {
	return &fakeCAT{state: map[string]string{"FA": "FA00024915000;", "MD": "MD3;", "Q9": "Q90;"}}
}

func (f *fakeCAT) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range strings.SplitAfter(string(p), ";") {
		if len(cmd) < 3 {
			continue
		}
		f.log = append(f.log, cmd)
		key := cmd[:2]
		if cmd == key+";" {
			f.pending += f.state[key]
		} else {
			f.state[key] = cmd
		}
	}
	return len(p), nil
}

func (f *fakeCAT) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == "" {
		f.mu.Unlock()
		time.Sleep(time.Millisecond)
		f.mu.Lock()
		return 0, nil
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func (f *fakeCAT) get(k string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[k]
}

// fakeCapture delivers a complex tone at toneHz, paced in real time.
type fakeCapture struct {
	frames int
	toneHz float64
	n      int
	speed  float64 // >1 delivers frames faster than real time (0 = 1)
}

func (c *fakeCapture) FramesPerBuffer() int { return c.frames }
func (c *fakeCapture) Close() error         { return nil }
func (c *fakeCapture) Read(dst []float32) (int, error) {
	sp := c.speed
	if sp == 0 {
		sp = 1
	}
	time.Sleep(time.Duration(float64(c.frames) * float64(time.Second) / 48000 / sp))
	for i := 0; i < c.frames; i++ {
		ph := 2 * math.Pi * c.toneHz * float64(c.n) / 48000
		dst[2*i] = float32(0.5 * math.Cos(ph))
		dst[2*i+1] = float32(0.5 * math.Sin(ph))
		c.n++
	}
	return c.frames, nil
}

type capSender struct {
	mu   sync.Mutex
	pkts [][]byte
	to   netip.AddrPort
}

func (s *capSender) Send(p []byte, to netip.AddrPort) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pkts = append(s.pkts, append([]byte(nil), p...))
	s.to = to
	return nil
}

func (s *capSender) take() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pkts
	s.pkts = nil
	return p
}

func ep2(c0 [2]byte, data [2]uint32) []byte {
	p := make([]byte, hpsdr.DataPacketLen)
	p[0], p[1], p[2], p[3] = 0xEF, 0xFE, 0x01, 0x02
	for f := 0; f < 2; f++ {
		fr := p[8+f*512:]
		fr[0], fr[1], fr[2], fr[3] = 0x7F, 0x7F, 0x7F, c0[f]
		binary.BigEndian.PutUint32(fr[4:8], data[f])
	}
	return p
}

// decode extracts receiver r's samples from EP6 packets with nrx receivers.
func decode(pkts [][]byte, nrx, r int) []complex128 {
	per := hpsdr.SamplesPerFrame(nrx)
	var out []complex128
	s24 := func(b []byte) float64 {
		v := int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
		if v&0x800000 != 0 {
			v -= 1 << 24
		}
		return float64(v) / (1 << 23)
	}
	for _, p := range pkts {
		for f := 0; f < 2; f++ {
			fr := p[8+f*512+8:]
			for k := 0; k < per; k++ {
				s := fr[k*(6*nrx+2)+6*r:]
				out = append(out, complex(s24(s), s24(s[3:])))
			}
		}
	}
	return out
}

func toneAt(x []complex128, f, rate float64) float64 {
	var acc complex128
	for i, v := range x {
		acc += v * cmplx.Exp(complex(0, -2*math.Pi*f*float64(i)/rate))
	}
	return cmplx.Abs(acc) / float64(len(x))
}

func TestEngineStreamsShiftedReceivers(t *testing.T) {
	cat := newFakeCAT()
	capt := &fakeCapture{frames: 240, toneHz: 3000}
	out := &capSender{}
	cfg := DefaultConfig()
	cfg.IQSettle = 0
	cfg.DCCornerHz = 0
	client := qmx.NewClient(cat)
	catCtx, catCancel := context.WithCancel(context.Background())
	defer catCancel()
	go client.Run(catCtx)
	e := New(cfg, client, capt, out)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.Run(ctx) }()

	// Priming: 192 kHz, 2 receivers, duplex; RX1 14.074 MHz, RX2 14.076 MHz.
	e.EP2(ep2([2]byte{0x00, 0x02 << 1}, [2]uint32{0x02<<24 | 1<<3 | 0x04, 14074000}))
	e.EP2(ep2([2]byte{0x03 << 1, 0x80 | 0x3a<<1}, [2]uint32{14076000, 0x1}))
	peer := netip.MustParseAddrPort("192.0.2.1:50000")
	e.Started(peer, hpsdr.StartStop{IQ: true})

	deadline := time.Now().Add(3 * time.Second)
	for cat.get("FA") != "FA00014086000;" {
		if time.Now().After(deadline) {
			t.Fatalf("QMX not tuned: FA=%s", cat.get("FA"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cat.get("Q9") != "Q91;" || cat.get("MD") != "MD6;" {
		t.Fatalf("radio not set up: %v", cat.state)
	}
	time.Sleep(100 * time.Millisecond)
	out.take() // drop packets from before the tune settled
	time.Sleep(400 * time.Millisecond)
	pkts := out.take()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// 192 kHz, 2 RX: 72 samples per packet -> about 2667 packets/s; allow for scheduling.
	// Wide bounds: under -race the DSP runs slower than real time.
	if len(pkts) < 250 || len(pkts) > 1400 {
		t.Errorf("got %d packets in 0.4 s, want about 1067", len(pkts))
	}
	if out.to != peer {
		t.Errorf("sent to %v", out.to)
	}
	// The QMX tone at IQ +3000 Hz is RF 14.077 MHz: +3000 Hz for RX1 and +1000 Hz for RX2.
	// The output follows the HPSDR convention (mirrored), so decoded as I + jQ the tones
	// sit at -3000 and -1000 Hz.
	rx1, rx2 := decode(pkts, 2, 0), decode(pkts, 2, 1)
	if a := toneAt(rx1, -3000, 192000); a < 0.45 {
		t.Errorf("RX1 tone at -3000: %g", a)
	}
	if a := toneAt(rx2, -1000, 192000); a < 0.45 {
		t.Errorf("RX2 tone at -1000: %g", a)
	}
	if a := toneAt(rx2, -3000, 192000); a > 0.01 {
		t.Errorf("RX2 has the unshifted tone: %g", a)
	}

	// The radio state is restored.
	if cat.get("FA") != "FA00024915000;" || cat.get("MD") != "MD3;" || cat.get("Q9") != "Q90;" {
		t.Errorf("not restored: %v", cat.state)
	}
	// Nothing ever keyed the transmitter.
	for _, c := range cat.log {
		if strings.HasPrefix(c, "TX") || strings.HasPrefix(c, "TQ1") || strings.HasPrefix(c, "TA") {
			t.Errorf("engine sent %q", c)
		}
	}
}

func TestEngineAck(t *testing.T) {
	e := New(DefaultConfig(), qmx.NewClient(newFakeCAT()), &fakeCapture{frames: 240}, &capSender{})
	e.EP2(ep2([2]byte{0x80 | 0x3a<<1, 0}, [2]uint32{0x1, 0}))
	if len(e.acks) != 1 || e.acks[0].Addr != 0x3a {
		t.Fatalf("acks %+v", e.acks)
	}
}

// ep2Tone builds a host packet whose frames carry MOX and a TX IQ tone at bb Hz.
func ep2Tone(mox bool, bb float64, phase *float64) []byte {
	c0 := byte(0x01 << 1) // TX frequency register, data unchanged below
	if mox {
		c0 |= 1
	}
	p := ep2([2]byte{c0, c0}, [2]uint32{14074000, 14074000})
	for f := 0; f < 2; f++ {
		for i := 0; i < 63; i++ {
			*phase += 2 * math.Pi * bb / 48000
			s := p[8+f*512+8+i*8:]
			binary.BigEndian.PutUint16(s[4:], uint16(int16(16000*math.Cos(*phase))))
			binary.BigEndian.PutUint16(s[6:], uint16(int16(16000*math.Sin(*phase))))
		}
	}
	return p
}

func TestEngineTransmitsWithTA(t *testing.T) {
	cat := newFakeCAT()
	cat.state["QB"], cat.state["QC"] = "QB1;", "QC120;"
	cat.state["SW"], cat.state["PC"] = "SW120;", "PC38;"
	cfg := DefaultConfig()
	cfg.IQSettle = 0
	cfg.TX.Enabled = true
	cfg.TX.SwapIQ = false // ep2Tone generates I=cos, Q=sin
	client := qmx.NewClient(cat)
	catCtx, catCancel := context.WithCancel(context.Background())
	defer catCancel()
	go client.Run(catCtx)
	out := &capSender{}
	e := New(cfg, client, &fakeCapture{frames: 240, toneHz: 3000}, out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.Run(ctx) }()

	e.EP2(ep2([2]byte{0x02 << 1, 0x00}, [2]uint32{14074000, 0x04}))
	e.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	waitFor(t, func() bool { return cat.get("FA") == "FA00014086000;" }, "RX tune")

	// 300 ms of MOX with a 1500 Hz USB tone, paced like a real client.
	var ph float64
	for i := 0; i < 115; i++ {
		e.EP2(ep2Tone(true, 1500, &ph))
		time.Sleep(2625 * time.Microsecond)
	}
	waitFor(t, func() bool { return strings.HasPrefix(cat.get("TA"), "TA1500") }, "TA tone")
	if cat.get("FA") != "FA00014074000;" {
		t.Errorf("TX dial = %s, want 14074000", cat.get("FA"))
	}
	// Keep MOX up a little longer so the meters are polled, then look for forward power
	// (EP6 status address 1, C3:C4) in what the client received.
	for i := 0; i < 190; i++ {
		e.EP2(ep2Tone(true, 1500, &ph))
		time.Sleep(2625 * time.Microsecond)
	}
	var fwd uint16
	for _, p := range out.take() {
		for f := 0; f < 2; f++ {
			fr := p[8+f*512:]
			if fr[3]&0xF8 == 0x08 {
				fwd = uint16(fr[6])<<8 | uint16(fr[7])
			}
		}
	}
	if fwd != hpsdr.PowerRaw(3.8) {
		t.Errorf("forward power raw = %d, want %d", fwd, hpsdr.PowerRaw(3.8))
	}
	e.EP2(ep2Tone(false, 0, &ph))
	waitFor(t, func() bool { return cat.get("TA") == "TA0;" }, "key-up")
	waitFor(t, func() bool { return cat.get("FA") == "FA00014086000;" }, "RX retune after TX")

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cat.mu.Lock()
	log := strings.Join(cat.log, "")
	cat.mu.Unlock()
	for _, want := range []string{"QC3;", "QB1;", "TX;", "RX;", "QC120;"} {
		if !strings.Contains(log, want) {
			t.Errorf("CAT log missing %q", want)
		}
	}
	if strings.Index(log, "TX;") > strings.Index(log, "TA1500") {
		t.Error("tone sent before TX")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEngineLOOffsetAndNoiseFill(t *testing.T) {
	cat := newFakeCAT()
	out := &capSender{}
	cfg := DefaultConfig()
	cfg.IQSettle = 0
	cfg.LOOffset = -4000
	client := qmx.NewClient(cat)
	catCtx, catCancel := context.WithCancel(context.Background())
	defer catCancel()
	go client.Run(catCtx)
	e := New(cfg, client, &fakeCapture{frames: 240, toneHz: 3000}, out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.Run(ctx) }()
	e.EP2(ep2([2]byte{0x00, 0x02 << 1}, [2]uint32{0x02<<24 | 0x04, 14074000})) // 192k, 1 RX
	e.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	// LO = RX1 - 4 kHz, so the dial is RX1 + 8 kHz.
	waitFor(t, func() bool { return cat.get("FA") == "FA00014082000;" }, "offset tune")
	time.Sleep(300 * time.Millisecond)
	out.take()
	time.Sleep(400 * time.Millisecond)
	pkts := out.take()
	cancel()
	<-done
	rx := decode(pkts, 1, 0)
	// The QMX tone at IQ +3 kHz is RF = LO + 3 kHz = RX1 - 1 kHz; mirrored on the wire: +1 kHz.
	if a := toneAt(rx, 1000, 192000); a < 0.4 {
		t.Errorf("tone at RX1-1kHz: %g", a)
	}
	// Noise fill: something well outside the QMX's +-24 kHz window.
	var e2 float64
	for _, f := range []float64{-60000, -45000, 50000, 70000} {
		e2 += toneAt(rx, f, 192000)
	}
	if e2 == 0 {
		t.Error("no noise fill outside the QMX band")
	}
}

// With SSB audio playing, the real QMX capture runs ~1.1% fast; EP6 must not follow it while
// transmitting, or the client sends 1.1% too much TX audio.
func TestEngineEP6PacedByClockDuringTX(t *testing.T) {
	cat := newFakeCAT()
	cat.state["QB"], cat.state["QC"] = "QB1;", "QC120;"
	cat.state["SW"], cat.state["PC"] = "SW120;", "PC38;"
	cfg := DefaultConfig()
	cfg.IQSettle = 0
	cfg.TX.Enabled = true
	cfg.TX.SwapIQ = false
	client := qmx.NewClient(cat)
	catCtx, catCancel := context.WithCancel(context.Background())
	defer catCancel()
	go client.Run(catCtx)
	out := &capSender{}
	e := New(cfg, client, &fakeCapture{frames: 240, speed: 1.011}, out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- e.Run(ctx) }()
	e.EP2(ep2([2]byte{0x02 << 1, 0x00}, [2]uint32{14074000, 0x04})) // 48k, 1 RX
	e.Started(netip.MustParseAddrPort("192.0.2.1:50000"), hpsdr.StartStop{IQ: true})
	waitFor(t, func() bool { return cat.get("FA") == "FA00014086000;" }, "RX tune")

	var ph float64
	stop := make(chan struct{})
	go func() { // keep MOX with a tone flowing
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.EP2(ep2Tone(true, 1500, &ph))
			time.Sleep(2625 * time.Microsecond)
		}
	}()
	waitFor(t, func() bool { return strings.HasPrefix(cat.get("TA"), "TA1500") }, "TX on")
	time.Sleep(200 * time.Millisecond)
	out.take()
	t0 := time.Now()
	time.Sleep(2 * time.Second)
	n := len(out.take())
	el := time.Since(t0).Seconds()
	close(stop)
	cancel()
	<-done
	rate := float64(n) / el
	// 48 kHz / 126 = 381 packets/s; following the fast capture would give ~385.
	if rate > 381*1.006 || rate < 381*0.97 {
		t.Errorf("EP6 during TX: %.1f packets/s, want about 381", rate)
	}
}
