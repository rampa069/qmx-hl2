// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package hpsdr

import "encoding/binary"

// EP2 geometry: two 512-byte USB frames per packet, 63 TX samples per frame, always 48 kHz.
const (
	usbFrameLen       = 512
	SamplesPerEP2Frm  = 63
	SamplesPerEP2Pkt  = 2 * SamplesPerEP2Frm
	ep2HeaderLen      = 8
	ep2SampleBytes    = 8
	MaxReceivers      = 12
	TXSampleRate      = 48000
	gatewareVersionC4 = 74
)

// CC is one decoded command-and-control word from a host frame.
type CC struct {
	Addr byte   // register address, 0..0x3f
	MOX  bool   // transmit request, sampled per frame
	RQST bool   // HL2: host asks for an ACK
	Data uint32 // C1..C4, big-endian
}

// EP2Frame is one decoded 512-byte host frame.
type EP2Frame struct {
	CC CC
	// TX I/Q at 48 kHz, 16-bit signed, in wire order (first word, second word). The HL2 only
	// uses them when CC.MOX is set in the same frame.
	TXIQ [SamplesPerEP2Frm][2]int16
}

// ParseEP2 decodes a 1032-byte host packet into two frames. It reports false if the packet or
// either frame's sync bytes are malformed.
func ParseEP2(pkt []byte, out *[2]EP2Frame) bool {
	if !IsEP2(pkt) {
		return false
	}
	for f := 0; f < 2; f++ {
		fr := pkt[8+f*usbFrameLen : 8+(f+1)*usbFrameLen]
		// The gateware only checks the third sync byte's upper 6 bits.
		if fr[2]>>2 != 0x1F {
			return false
		}
		c0 := fr[3]
		out[f].CC = CC{
			Addr: (c0 >> 1) & 0x3F,
			MOX:  c0&0x01 != 0,
			RQST: c0&0x80 != 0,
			Data: binary.BigEndian.Uint32(fr[4:8]),
		}
		for i := 0; i < SamplesPerEP2Frm; i++ {
			s := fr[ep2HeaderLen+i*ep2SampleBytes:]
			// L1 L0 R1 R0 I1 I0 Q1 Q0; L/R (speaker audio) is ignored.
			out[f].TXIQ[i][0] = int16(binary.BigEndian.Uint16(s[4:6]))
			out[f].TXIQ[i][1] = int16(binary.BigEndian.Uint16(s[6:8]))
		}
	}
	return true
}

// RadioState is the host-controlled register state the emulator cares about.
type RadioState struct {
	SampleRate int  // 48000, 96000, 192000 or 384000
	Receivers  int  // 1..MaxReceivers
	Duplex     bool // when false, RX1 follows the TX frequency

	TXFreq uint32               // Hz
	RXFreq [MaxReceivers]uint32 // Hz

	Drive    byte // 0..255 (the HL2 only uses the top nibble)
	PAEnable bool
	LNAdB    int // -12..+48 dB in full-range mode

	PTTHangMs   int // default 12
	TXLatencyMs int // default 20
	CWX         bool

	MOX bool // from the most recent frame
}

// DefaultRadioState is the HL2 power-on state.
func DefaultRadioState() RadioState {
	return RadioState{SampleRate: 48000, Receivers: 1, Duplex: true, PTTHangMs: 12, TXLatencyMs: 20}
}

var rateCodes = [4]int{48000, 96000, 192000, 384000}

// Apply updates the state from one C&C word and reports whether a register the emulator acts on
// changed (MOX is tracked but not counted as a change).
func (s *RadioState) Apply(cc CC) bool {
	s.MOX = cc.MOX
	d := cc.Data
	c1, c3, c4 := byte(d>>24), byte(d>>8), byte(d)
	old := *s
	switch {
	case cc.Addr == 0x00:
		s.SampleRate = rateCodes[c1&0x03]
		n := int((c4>>3)&0x0F) + 1
		if n > MaxReceivers {
			n = MaxReceivers
		}
		s.Receivers = n
		s.Duplex = c4&0x04 != 0
	case cc.Addr == 0x01:
		s.TXFreq = d
	case cc.Addr >= 0x02 && cc.Addr <= 0x08:
		s.RXFreq[cc.Addr-0x02] = d
	case cc.Addr >= 0x12 && cc.Addr <= 0x16:
		s.RXFreq[7+cc.Addr-0x12] = d
	case cc.Addr == 0x09:
		s.Drive = c1
		s.PAEnable = d&(1<<19) != 0
	case cc.Addr == 0x0a:
		if c4&0x40 != 0 {
			s.LNAdB = int(c4&0x3F) - 12
		} else if c4&0x20 != 0 {
			s.LNAdB = 19 - int(c4&0x1F)
		} else {
			s.LNAdB = 20
		}
	case cc.Addr == 0x0f:
		s.CWX = d&(1<<24) != 0
	case cc.Addr == 0x17:
		s.PTTHangMs = int(c3 & 0x1F)
		s.TXLatencyMs = int(c4 & 0x7F)
	}
	cur := *s
	cur.MOX = old.MOX
	return cur != old
}

// RX1Freq is the frequency the host displays as the RX1 centre. Without duplex the HL2 tunes
// RX1 to the TX frequency.
func (s *RadioState) RX1Freq() uint32 {
	if !s.Duplex {
		return s.TXFreq
	}
	return s.RXFreq[0]
}
