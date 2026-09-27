package hpsdr

import (
	"encoding/binary"
	"testing"
)

// buildEP2 makes a host packet with the given C0/data per frame and TX IQ sample values.
func buildEP2(c0 [2]byte, data [2]uint32, i0, q0 int16) []byte {
	p := make([]byte, DataPacketLen)
	p[0], p[1], p[2], p[3] = 0xEF, 0xFE, 0x01, 0x02
	for f := 0; f < 2; f++ {
		fr := p[8+f*512:]
		fr[0], fr[1], fr[2], fr[3] = 0x7F, 0x7F, 0x7F, c0[f]
		binary.BigEndian.PutUint32(fr[4:8], data[f])
		for i := 0; i < 63; i++ {
			s := fr[8+i*8:]
			binary.BigEndian.PutUint16(s[4:], uint16(i0+int16(i)))
			binary.BigEndian.PutUint16(s[6:], uint16(q0-int16(i)))
		}
	}
	return p
}

func TestParseEP2(t *testing.T) {
	// Frame 0: config (addr 0, MOX) with 192k, 2 receivers, duplex. Frame 1: RX1 freq with RQST.
	cfg := uint32(0x02)<<24 | uint32(1<<3|0x04)
	p := buildEP2([2]byte{0x00 | 0x01, 0x80 | 0x02<<1}, [2]uint32{cfg, 14074000}, -100, 200)
	var fr [2]EP2Frame
	if !ParseEP2(p, &fr) {
		t.Fatal("parse failed")
	}
	if fr[0].CC != (CC{Addr: 0, MOX: true, Data: cfg}) {
		t.Errorf("frame0 CC = %+v", fr[0].CC)
	}
	if fr[1].CC != (CC{Addr: 2, RQST: true, Data: 14074000}) {
		t.Errorf("frame1 CC = %+v", fr[1].CC)
	}
	if fr[1].TXIQ[5] != [2]int16{-95, 195} {
		t.Errorf("IQ = %v", fr[1].TXIQ[5])
	}

	s := DefaultRadioState()
	if !s.Apply(fr[0].CC) || s.SampleRate != 192000 || s.Receivers != 2 || !s.Duplex || !s.MOX {
		t.Errorf("state after config: %+v", s)
	}
	if !s.Apply(fr[1].CC) || s.RXFreq[0] != 14074000 || s.MOX {
		t.Errorf("state after rx1: %+v", s)
	}
	if s.Apply(fr[1].CC) {
		t.Error("re-applying the same value should report no change")
	}

	bad := append([]byte(nil), p...)
	bad[8+512+2] = 0x00
	if ParseEP2(bad, &fr) {
		t.Error("bad sync accepted")
	}
	if ParseEP2(p[:1000], &fr) {
		t.Error("short packet accepted")
	}
}

func TestRadioStateRegisters(t *testing.T) {
	s := DefaultRadioState()
	s.Apply(CC{Addr: 0x01, Data: 7074000})
	s.Apply(CC{Addr: 0x08, Data: 7})
	s.Apply(CC{Addr: 0x16, Data: 12})
	s.Apply(CC{Addr: 0x09, Data: 0xF0<<24 | 1<<19})
	s.Apply(CC{Addr: 0x0a, Data: 0x40 | 32})
	s.Apply(CC{Addr: 0x17, Data: 5<<8 | 40})
	s.Apply(CC{Addr: 0x0f, Data: 1 << 24})
	if s.TXFreq != 7074000 || s.RXFreq[6] != 7 || s.RXFreq[11] != 12 {
		t.Errorf("freqs %+v", s)
	}
	if s.Drive != 0xF0 || !s.PAEnable || s.LNAdB != 20 || s.PTTHangMs != 5 || s.TXLatencyMs != 40 || !s.CWX {
		t.Errorf("state %+v", s)
	}
	s.Apply(CC{Addr: 0, Data: 0}) // duplex off
	if s.RX1Freq() != 7074000 {
		t.Errorf("RX1Freq without duplex = %d", s.RX1Freq())
	}
}

func TestEP6BuilderOneReceiver(t *testing.T) {
	var pkts [][]byte
	b := NewEP6Builder(1, func(p []byte) { pkts = append(pkts, append([]byte(nil), p...)) })
	b.SetTelemetry(Telemetry{Overload: true, TXFIFO: 0x0A})
	for i := 0; i < 126*2; i++ {
		b.AddRound([]IQ24{{I: int32(i), Q: -int32(i)}})
	}
	if len(pkts) != 2 {
		t.Fatalf("got %d packets, want 2", len(pkts))
	}
	p := pkts[1]
	if p[0] != 0xEF || p[1] != 0xFE || p[2] != 0x01 || p[3] != 0x06 || binary.BigEndian.Uint32(p[4:8]) != 1 {
		t.Fatalf("header % x", p[:8])
	}
	// Packet 0 carries status addresses 0 and 1, packet 1 carries 2 and 3.
	f0 := pkts[0][8:]
	if f0[3] != 0x00 || f0[4] != 0x1F || f0[6] != 0x0A || f0[7] != 74 {
		t.Errorf("status addr0 % x", f0[:8])
	}
	if pkts[0][8+512+3] != 0x08 || p[8+3] != 0x10 || p[8+512+3] != 0x18 {
		t.Error("status rotation wrong")
	}
	// Second packet, frame 1, sample 62 is round 126+63+62 = 251.
	s := p[8+512+8+62*8:]
	i := int32(s[0])<<16 | int32(s[1])<<8 | int32(s[2])
	q := int32(int8(s[3]))<<16 | int32(s[4])<<8 | int32(s[5])
	if i != 251 || q != -251 || s[6] != 0 || s[7] != 0 {
		t.Errorf("sample = %d,%d mic % x", i, q, s[6:8])
	}
}

func TestEP6BuilderFourReceiversPaddingAndAck(t *testing.T) {
	var pkts [][]byte
	b := NewEP6Builder(4, func(p []byte) { pkts = append(pkts, append([]byte(nil), p...)) })
	if SamplesPerFrame(4) != 19 {
		t.Fatal("19 rounds per frame expected for 4 RX")
	}
	b.QueueAck(CC{Addr: 0x3a, Data: 0x01020304})
	round := []IQ24{{1, 2}, {3, 4}, {5, 6}, {-1, -2}}
	for i := 0; i < 38; i++ {
		b.AddRound(round)
	}
	if len(pkts) != 1 {
		t.Fatalf("got %d packets", len(pkts))
	}
	f0 := pkts[0][8:]
	if f0[3] != 0x80|0x3a<<1 || binary.BigEndian.Uint32(f0[4:8]) != 0x01020304 {
		t.Errorf("ack frame % x", f0[:8])
	}
	// The ACK took frame 0's slot, so frame 1 starts the rotation at address 0.
	if pkts[0][8+512+3] != 0x00 {
		t.Errorf("frame1 C0 = %02x", pkts[0][8+512+3])
	}
	// Receiver 4 of round 0 is -1,-2; the 10 pad bytes after 19 rounds are zero.
	r := f0[8+18:]
	if r[0] != 0xFF || r[2] != 0xFF || r[5] != 0xFE {
		t.Errorf("rx4 % x", r[:6])
	}
	for _, v := range f0[8+19*26 : 512] {
		if v != 0 {
			t.Fatal("padding not zero")
		}
	}
}
