// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package config

import (
	"io"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.AudioDevice != "QMX" || c.SampleRate != 48000 || c.Frames != 240 || c.Probe != 0 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestParseProbe(t *testing.T) {
	c, err := Parse([]string{"-probe", "10s", "-iq", "-serial", "/dev/ttyACM0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Probe != 10*time.Second || !c.ProbeIQMode || c.SerialPort != "/dev/ttyACM0" {
		t.Fatalf("got %+v", c)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"-frames", "0"},
		{"-rate", "-1"},
		{"-probe", "-1s"},
		{"extra"},
		{"-nosuchflag"},
		{"-maxtx", "0s"},
		{"-looffset", "30000"},
		{"-txmode", "fm"},
	} {
		if _, err := Parse(args, io.Discard); err == nil {
			t.Errorf("Parse(%v) succeeded, want error", args)
		}
	}
}
