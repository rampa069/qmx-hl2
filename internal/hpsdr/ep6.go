// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package hpsdr

import "encoding/binary"

// Telemetry is what the radio reports in the EP6 status rotation.
type Telemetry struct {
	PTT         bool   // C0[0]: radio-side PTT/CW (not an echo of MOX)
	Overload    bool   // ADC overload since the last report
	TXFIFO      byte   // [7] recovery flag, [6:0] fill in 32-sample units
	Temp        uint16 // 12-bit raw
	FwdPower    uint16 // 12-bit raw
	RevPower    uint16 // 12-bit raw
	BiasCurrent uint16 // 12-bit raw
}

// IQ24 is one receiver sample, each component a signed 24-bit value.
type IQ24 struct{ I, Q int32 }

// EP6Builder assembles radio-to-host packets. Feed it one round (one sample per active receiver)
// at a time; it calls send with each complete 1032-byte packet. It is not safe for concurrent
// use.
type EP6Builder struct {
	send func(pkt []byte)

	nrx       int
	perFrame  int // rounds per 512-byte frame
	roundLen  int
	pkt       [DataPacketLen]byte
	frame     int // 0 or 1: frame being filled
	round     int // rounds filled in the current frame
	seq       uint32
	statusIdx byte // rotation 0..3

	acks []CC
	tel  Telemetry
}

// NewEP6Builder creates a builder for nrx receivers.
func NewEP6Builder(nrx int, send func(pkt []byte)) *EP6Builder {
	b := &EP6Builder{send: send}
	b.SetReceivers(nrx)
	return b
}

// SamplesPerFrame returns rounds per USB frame for nrx receivers (63 for one receiver).
func SamplesPerFrame(nrx int) int { return 504 / (6*nrx + 2) }

// SetReceivers changes the receiver count. Any partly built packet is discarded.
func (b *EP6Builder) SetReceivers(nrx int) {
	if nrx < 1 {
		nrx = 1
	}
	if nrx > MaxReceivers {
		nrx = MaxReceivers
	}
	b.nrx = nrx
	b.roundLen = 6*nrx + 2
	b.perFrame = SamplesPerFrame(nrx)
	b.frame, b.round = 0, 0
}

// Receivers returns the current receiver count.
func (b *EP6Builder) Receivers() int { return b.nrx }

// Reset clears the sequence number and any partial packet (on Stop/Start).
func (b *EP6Builder) Reset() {
	b.seq = 0
	b.frame, b.round = 0, 0
	b.statusIdx = 0
	b.acks = b.acks[:0]
}

// SetTelemetry sets the status values for subsequent frames.
func (b *EP6Builder) SetTelemetry(t Telemetry) { b.tel = t }

// QueueAck schedules an ACK for a host write that had RQST set.
func (b *EP6Builder) QueueAck(cc CC) {
	if len(b.acks) < 8 {
		b.acks = append(b.acks, cc)
	}
}

// AddRound appends one sample per receiver. rx must have at least Receivers() entries.
func (b *EP6Builder) AddRound(rx []IQ24) {
	if b.round == 0 {
		b.startFrame()
	}
	off := 8 + b.frame*usbFrameLen + 8 + b.round*b.roundLen
	p := b.pkt[off : off+b.roundLen]
	for r := 0; r < b.nrx; r++ {
		put24(p[6*r:], rx[r].I)
		put24(p[6*r+3:], rx[r].Q)
	}
	p[6*b.nrx], p[6*b.nrx+1] = 0, 0 // mic word
	b.round++
	if b.round < b.perFrame {
		return
	}
	b.round = 0
	b.frame++
	if b.frame < 2 {
		return
	}
	b.frame = 0
	b.pkt[0], b.pkt[1], b.pkt[2], b.pkt[3] = 0xEF, 0xFE, typeData, 0x06
	binary.BigEndian.PutUint32(b.pkt[4:8], b.seq)
	b.seq++
	b.send(b.pkt[:])
}

func (b *EP6Builder) startFrame() {
	base := 8 + b.frame*usbFrameLen
	fr := b.pkt[base : base+usbFrameLen]
	clear(fr)
	fr[0], fr[1], fr[2] = 0x7F, 0x7F, 0x7F
	ptt := byte(0)
	if b.tel.PTT {
		ptt = 1
	}
	if len(b.acks) > 0 {
		a := b.acks[0]
		b.acks = b.acks[1:]
		fr[3] = 0x80 | a.Addr<<1 | ptt
		binary.BigEndian.PutUint32(fr[4:8], a.Data)
		return
	}
	fr[3] = b.statusIdx<<3 | ptt
	switch b.statusIdx {
	case 0:
		c1 := byte(0x1E) // bits 4:2 set, bit1 = TX not inhibited
		if b.tel.Overload {
			c1 |= 0x01
		}
		fr[4], fr[5], fr[6], fr[7] = c1, 0, b.tel.TXFIFO, gatewareVersionC4
	case 1:
		put12(fr[4:], b.tel.Temp)
		put12(fr[6:], b.tel.FwdPower)
	case 2:
		put12(fr[4:], b.tel.RevPower)
		put12(fr[6:], b.tel.BiasCurrent)
	}
	b.statusIdx = (b.statusIdx + 1) & 3
}

func put24(p []byte, v int32) {
	p[0], p[1], p[2] = byte(v>>16), byte(v>>8), byte(v)
}

func put12(p []byte, v uint16) {
	p[0], p[1] = byte(v>>8)&0x0F, byte(v)
}
