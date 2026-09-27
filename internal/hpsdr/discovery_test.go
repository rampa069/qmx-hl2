// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package hpsdr

import (
	"bytes"
	"testing"
)

func TestDiscoveryReplyLayout(t *testing.T) {
	id := DefaultIdentity()
	b := DiscoveryReply(id, false)
	if len(b) != 60 {
		t.Fatalf("len = %d", len(b))
	}
	want := map[int]byte{
		0x00: 0xEF, 0x01: 0xFE, 0x02: 0x02,
		0x09: 74, 0x0A: 0x06, 0x13: 4, 0x14: 0x45, 0x15: 2,
	}
	for off, v := range want {
		if b[off] != v {
			t.Errorf("byte 0x%02x = 0x%02x, want 0x%02x", off, b[off], v)
		}
	}
	if !bytes.Equal(b[3:9], id.MAC) {
		t.Errorf("MAC = % x", b[3:9])
	}
	// piHPSDR selects HL2 (not HL1) when 10*major + minor >= 400.
	if v := 10*int(b[0x09]) + int(b[0x15]); v < 400 {
		t.Errorf("piHPSDR version %d < 400", v)
	}
	// Everything else stays zero.
	for off := 0x16; off < 60; off++ {
		if b[off] != 0 {
			t.Errorf("byte 0x%02x = 0x%02x, want 0", off, b[off])
		}
	}
	if DiscoveryReply(id, true)[2] != 0x03 {
		t.Error("running status should be 0x03")
	}
}

func TestIsDiscoveryRequest(t *testing.T) {
	req := make([]byte, 63)
	req[0], req[1], req[2] = 0xEF, 0xFE, 0x02
	if !IsDiscoveryRequest(req) || !IsDiscoveryRequest(req[:60]) || !IsDiscoveryRequest(req[:3]) {
		t.Error("valid requests rejected")
	}
	if IsDiscoveryRequest(req[:2]) || IsDiscoveryRequest([]byte{0xEF, 0xFE, 0x04, 0x01}) {
		t.Error("invalid request accepted")
	}
}

func TestParseStartStop(t *testing.T) {
	cases := []struct {
		cmd  byte
		want StartStop
	}{
		{0x00, StartStop{}},
		{0x01, StartStop{IQ: true}},
		{0x03, StartStop{IQ: true, Wideband: true}},
		{0x81, StartStop{IQ: true, WatchdogDisable: true}},
	}
	for _, c := range cases {
		pkt := make([]byte, 64)
		pkt[0], pkt[1], pkt[2], pkt[3] = 0xEF, 0xFE, 0x04, c.cmd
		got, ok := ParseStartStop(pkt)
		if !ok || got != c.want {
			t.Errorf("cmd 0x%02x: got %+v ok=%v", c.cmd, got, ok)
		}
	}
	if _, ok := ParseStartStop([]byte{0xEF, 0xFE, 0x02, 0x01}); ok {
		t.Error("discovery parsed as start/stop")
	}
}

func TestIdentityValidate(t *testing.T) {
	id := DefaultIdentity()
	if err := id.Validate(); err != nil {
		t.Fatal(err)
	}
	id.MAC = id.MAC[:5]
	if id.Validate() == nil {
		t.Error("short MAC accepted")
	}
	id = DefaultIdentity()
	id.Receivers = 0
	if id.Validate() == nil {
		t.Error("0 receivers accepted")
	}
	if _, err := ParseMAC("00:1c:c0:a2:51:4d"); err != nil {
		t.Error(err)
	}
	if _, err := ParseMAC("00:1c:c0:a2:51:4d:00:00"); err == nil {
		t.Error("8-byte MAC accepted")
	}
}
