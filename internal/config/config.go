// Package config holds command-line configuration and the build version.
package config

import (
	"flag"
	"fmt"
	"io"
	"time"
)

// Version is set at build time with -ldflags "-X .../internal/config.Version=...".
var Version = "dev"

// Config is the daemon's runtime configuration.
type Config struct {
	AudioDevice string // substring of the QMX sound card name, used for capture and playback
	SerialPort  string // explicit serial device; empty means auto-detect
	SampleRate  int    // QMX USB audio rate; fixed at 48000 in hardware
	Frames      int    // frames per capture buffer (240 = 5 ms at 48k)
	LogDir      string
	LogFile     string
	Verbose     bool

	// One-shot modes.
	List        bool          // list audio devices and serial ports, then exit
	Probe       time.Duration // capture this long, report IQ levels and measured rate, then exit
	ProbeIQMode bool          // with Probe: send Q91; first and restore the previous Q9 state after
	ShowVersion bool
}

// Parse parses args (without the program name). Errors and -h output go to out.
func Parse(args []string, out io.Writer) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("qmx-hl2", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&c.AudioDevice, "audio", "QMX", "audio device name or substring for the QMX sound card")
	fs.StringVar(&c.SerialPort, "serial", "", "QMX serial port (default: auto-detect by USB VID:PID)")
	fs.IntVar(&c.SampleRate, "rate", 48000, "QMX audio sample rate")
	fs.IntVar(&c.Frames, "frames", 240, "frames per audio capture buffer")
	fs.StringVar(&c.LogDir, "logdir", "logs", "directory for log files")
	fs.StringVar(&c.LogFile, "logfile", "", "log file path (default: timestamped file in -logdir)")
	fs.BoolVar(&c.Verbose, "v", false, "verbose console logging")
	fs.BoolVar(&c.List, "list", false, "list audio devices and serial ports, then exit")
	fs.DurationVar(&c.Probe, "probe", 0, "capture for this long (e.g. 10s), report IQ levels and measured sample rate, then exit")
	fs.BoolVar(&c.ProbeIQMode, "iq", false, "with -probe: enable QMX IQ mode (Q91;) during the capture")
	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if c.SampleRate <= 0 || c.Frames <= 0 {
		return nil, fmt.Errorf("-rate and -frames must be positive")
	}
	if c.Probe < 0 {
		return nil, fmt.Errorf("-probe must not be negative")
	}
	return c, nil
}
