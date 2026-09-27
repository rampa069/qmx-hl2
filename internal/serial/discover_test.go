package serial

import "testing"

func TestPickQMX(t *testing.T) {
	tests := []struct {
		name  string
		ports []PortInfo
		want  string
		ok    bool
	}{
		{
			name: "vid pid prefers cu on macOS",
			ports: []PortInfo{
				{Device: "/dev/tty.usbmodem1", VID: "0483", PID: "a34c", IsUSB: true},
				{Device: "/dev/cu.usbmodem1", VID: "0483", PID: "a34c", IsUSB: true},
			},
			want: "/dev/cu.usbmodem1", ok: true,
		},
		{
			name:  "linux vid pid",
			ports: []PortInfo{{Device: "/dev/ttyACM0", VID: "0483", PID: "a34c", IsUSB: true}},
			want:  "/dev/ttyACM0", ok: true,
		},
		{
			name: "vid pid beats product string",
			ports: []PortInfo{
				{Device: "/dev/ttyACM1", Product: "QMX clone", IsUSB: true},
				{Device: "/dev/ttyACM0", VID: "0483", PID: "a34c", IsUSB: true},
			},
			want: "/dev/ttyACM0", ok: true,
		},
		{
			name:  "product fallback",
			ports: []PortInfo{{Device: "/dev/ttyACM0", Product: "QRP Labs QMX Transceiver", IsUSB: true}},
			want:  "/dev/ttyACM0", ok: true,
		},
		{
			name:  "other STM device is ignored",
			ports: []PortInfo{{Device: "/dev/ttyACM0", VID: "0483", PID: "5740", Product: "STM32 VCP", IsUSB: true}},
			ok:    false,
		},
		{name: "empty", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickQMX(tc.ports)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("pickQMX = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
