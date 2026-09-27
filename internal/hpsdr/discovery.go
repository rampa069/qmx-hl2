// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// Package hpsdr implements the radio side of openHPSDR Protocol 1 ("old protocol") as spoken by
// a Hermes-Lite 2. The wire format is documented in doc/hl2/protocol1.md, and what clients
// actually check is in doc/hl2/client-behavior.md.
package hpsdr

import (
	"fmt"
	"net"
)

// Port is the UDP port an HL2 listens on.
const Port = 1024

// Packet types (byte 2 after the EF FE magic).
const (
	typeData      = 0x01
	typeDiscovery = 0x02
	typeStartStop = 0x04
)

// Discovery reply status byte.
const (
	statusIdle    = 0x02
	statusRunning = 0x03
)

// DiscoveryReplyLen is the length of an HL2 discovery reply.
const DiscoveryReplyLen = 60

// BoardIDHermesLite is the board ID every client uses to select HL2 behaviour.
const BoardIDHermesLite = 0x06

// Identity is what the emulated radio reports in its discovery reply.
type Identity struct {
	MAC           net.HardwareAddr // 6 bytes
	GatewareMajor byte             // byte 0x09; piHPSDR needs 10*major+minor >= 400 to treat it as HL2
	GatewareMinor byte             // byte 0x15
	Receivers     byte             // byte 0x13; Thetis always runs 4 DDCs on an HL2
	BoardBuild    byte             // byte 0x14: [7:6] wideband format (01 = 16-bit), [5:0] build
}

// DefaultIdentity mirrors a current HL2 (gateware 74.2, build 5, 4 receivers). The MAC uses the
// HL2 OUI 00:1C:C0:A2 with "QM" as the last two bytes.
func DefaultIdentity() Identity {
	return Identity{
		MAC:           net.HardwareAddr{0x00, 0x1C, 0xC0, 0xA2, 'Q', 'M'},
		GatewareMajor: 74,
		GatewareMinor: 2,
		Receivers:     4,
		BoardBuild:    0x45,
	}
}

// Validate checks the identity can be encoded.
func (id Identity) Validate() error {
	if len(id.MAC) != 6 {
		return fmt.Errorf("MAC must be 6 bytes, got %d", len(id.MAC))
	}
	if id.Receivers < 1 || id.Receivers > 12 {
		return fmt.Errorf("receivers must be 1..12, got %d", id.Receivers)
	}
	return nil
}

// ParseMAC parses a MAC address for use in an Identity.
func ParseMAC(s string) (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(s)
	if err != nil {
		return nil, err
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("MAC %q is not 6 bytes", s)
	}
	return mac, nil
}

// IsDiscoveryRequest reports whether b is a discovery request. Like the HL2 gateware, only the
// first three bytes are checked. Clients send 63 or 60 bytes (and 1032 over TCP).
func IsDiscoveryRequest(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xEF && b[1] == 0xFE && b[2] == typeDiscovery
}

// DiscoveryReply encodes the 60-byte reply. Report running=true only while streaming to a
// client: piHPSDR shows status 0x03 as "In Use" and will not connect.
//
// Telemetry fields (0x1B-0x29) are left zero for now.
func DiscoveryReply(id Identity, running bool) []byte {
	b := make([]byte, DiscoveryReplyLen)
	b[0], b[1] = 0xEF, 0xFE
	b[2] = statusIdle
	if running {
		b[2] = statusRunning
	}
	copy(b[3:9], id.MAC)
	b[0x09] = id.GatewareMajor
	b[0x0A] = BoardIDHermesLite
	b[0x13] = id.Receivers
	b[0x14] = id.BoardBuild
	b[0x15] = id.GatewareMinor
	return b
}

// StartStop is a decoded start/stop command.
type StartStop struct {
	IQ              bool // bit 0: stream EP6 (RX IQ + mic)
	Wideband        bool // bit 1: stream EP4; the QMX cannot provide it, so it is ignored
	WatchdogDisable bool // bit 7 (HL2 only): don't stop when EP2 packets stop arriving
}

// ParseStartStop decodes an `EF FE 04 <cmd>` packet.
func ParseStartStop(b []byte) (StartStop, bool) {
	if len(b) < 4 || b[0] != 0xEF || b[1] != 0xFE || b[2] != typeStartStop {
		return StartStop{}, false
	}
	c := b[3]
	return StartStop{IQ: c&0x01 != 0, Wideband: c&0x02 != 0, WatchdogDisable: c&0x80 != 0}, true
}

// Running reports whether the command starts any stream.
func (s StartStop) Running() bool { return s.IQ || s.Wideband }

// EP2 packet layout.
const (
	DataPacketLen = 1032
	epHostToRadio = 0x02
)

// IsEP2 reports whether b is a full host-to-radio data packet.
func IsEP2(b []byte) bool {
	return len(b) == DataPacketLen && b[0] == 0xEF && b[1] == 0xFE && b[2] == typeData && b[3] == epHostToRadio
}
