package serial

import (
	"fmt"
	"log/slog"
	"strings"

	goserial "go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

// QMX USB identity. The VID is STMicroelectronics'. Source: doc/qmx/qmx-overview.md (field
// report from SteffenLav/qmx-panadapter). The product string is from the QMX manual.
const (
	QMXVID          = "0483"
	QMXPID          = "a34c"
	QMXProductMatch = "QMX"
)

// PortInfo describes one serial port found on the system.
type PortInfo struct {
	Device  string
	Product string
	VID     string
	PID     string
	Serial  string
	IsUSB   bool
}

func (p PortInfo) String() string {
	if !p.IsUSB {
		return p.Device
	}
	return fmt.Sprintf("%s [%s] VID:%s PID:%s SN:%s", p.Device, p.Product, p.VID, p.PID, p.Serial)
}

// ListPorts returns every serial port the OS reports.
func ListPorts() ([]PortInfo, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, fmt.Errorf("enumerate ports: %w", err)
	}
	out := make([]PortInfo, 0, len(ports))
	for _, p := range ports {
		out = append(out, PortInfo{
			Device:  p.Name,
			Product: p.Product,
			VID:     strings.ToLower(p.VID),
			PID:     strings.ToLower(p.PID),
			Serial:  p.SerialNumber,
			IsUSB:   p.IsUSB,
		})
	}
	return out, nil
}

// FindQMX returns the QMX's serial device. A non-empty hint must name an existing port and is
// returned as is. Otherwise it matches by VID:PID, then by product string.
//
// On macOS a CDC device shows up as both /dev/cu.* and /dev/tty.*. The cu.* node is preferred
// because opening tty.* can block waiting for carrier detect.
func FindQMX(hint string) (string, error) {
	if hint != "" {
		names, err := goserial.GetPortsList()
		if err != nil {
			return "", fmt.Errorf("list ports: %w", err)
		}
		for _, n := range names {
			if n == hint {
				return hint, nil
			}
		}
		return "", fmt.Errorf("serial port %q not found", hint)
	}
	ports, err := ListPorts()
	if err != nil {
		return "", err
	}
	if dev, ok := pickQMX(ports); ok {
		slog.Info("found QMX serial port", "device", dev)
		return dev, nil
	}
	return "", fmt.Errorf("no QMX serial port found (VID:PID %s:%s or product %q); is the radio connected?",
		QMXVID, QMXPID, QMXProductMatch)
}

// pickQMX holds the matching rules, split out so they can be tested without hardware.
func pickQMX(ports []PortInfo) (string, bool) {
	match := func(pred func(PortInfo) bool) (string, bool) {
		var found string
		for _, p := range ports {
			if !pred(p) {
				continue
			}
			if strings.HasPrefix(p.Device, "/dev/cu.") {
				return p.Device, true
			}
			if found == "" {
				found = p.Device
			}
		}
		return found, found != ""
	}
	if dev, ok := match(func(p PortInfo) bool { return p.IsUSB && p.VID == QMXVID && p.PID == QMXPID }); ok {
		return dev, true
	}
	return match(func(p PortInfo) bool { return strings.Contains(strings.ToUpper(p.Product), QMXProductMatch) })
}
