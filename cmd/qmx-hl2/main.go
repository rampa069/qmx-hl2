// Command qmx-hl2 presents a QRP Labs QMX/QMX+ as a Hermes-Lite 2 (openHPSDR Protocol 1).
//
// Only the bench helpers exist so far: -list and -probe. The protocol daemon is tracked in
// beads epic QMX-dfb.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rampa/qmx-hl2/internal/audio"
	"github.com/rampa/qmx-hl2/internal/config"
	"github.com/rampa/qmx-hl2/internal/logging"
	"github.com/rampa/qmx-hl2/internal/probe"
	"github.com/rampa/qmx-hl2/internal/serial"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.ShowVersion {
		fmt.Println("qmx-hl2", config.Version)
		return 0
	}
	logPath, err := logging.Setup(cfg.LogDir, cfg.LogFile, cfg.Verbose)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	slog.Info("qmx-hl2 starting", "version", config.Version, "log", logPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case cfg.List:
		err = listDevices()
	case cfg.Probe > 0:
		err = runProbe(ctx, cfg)
	default:
		fmt.Fprintln(os.Stderr, "the HL2 daemon is not implemented yet; use -list or -probe <duration> (see -h)")
		return 2
	}
	if err != nil {
		slog.Error("failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func listDevices() error {
	be := audio.NewPABackend()
	if err := be.Init(); err != nil {
		return fmt.Errorf("init audio: %w", err)
	}
	defer be.Terminate()
	devs, err := be.ListDevices()
	if err != nil {
		return err
	}
	fmt.Println("Audio devices:")
	for _, d := range devs {
		fmt.Printf("  [%2d] in:%d out:%d %6.0f Hz  %s\n", d.Index, d.MaxInput, d.MaxOutput, d.SampleRate, d.Name)
	}
	ports, err := serial.ListPorts()
	if err != nil {
		return err
	}
	fmt.Println("Serial ports:")
	for _, p := range ports {
		fmt.Println("  " + p.String())
	}
	if dev, err := serial.FindQMX(""); err == nil {
		fmt.Println("QMX serial port:", dev)
	} else {
		fmt.Println("QMX serial port: not found")
	}
	return nil
}

// runProbe reads the firmware version and IQ-mode state, optionally enables IQ mode, captures
// for cfg.Probe, and prints IQ levels, DC and the QMX sample rate measured against the host.
func runProbe(ctx context.Context, cfg *config.Config) error {
	dev, err := serial.FindQMX(cfg.SerialPort)
	if err != nil {
		return err
	}
	port := serial.NewPort(serial.DefaultBaud)
	if err := port.Open(dev); err != nil {
		return err
	}
	defer port.Close()

	const catTimeout = 500 * time.Millisecond
	if vn, err := probe.Query(port, "VN;", catTimeout); err == nil {
		fmt.Println("firmware:", vn)
	} else {
		fmt.Println("firmware: unknown:", err)
	}
	origQ9, err := probe.Query(port, "Q9;", catTimeout)
	if err != nil {
		return fmt.Errorf("read IQ mode: %w", err)
	}
	fmt.Println("IQ mode at start:", origQ9)

	if cfg.ProbeIQMode && origQ9 != "Q91;" {
		if err := probe.Send(port, "Q91;"); err != nil {
			return err
		}
		defer func() {
			if err := probe.Send(port, origQ9); err != nil {
				slog.Warn("could not restore IQ mode", "want", origQ9, "err", err)
			} else {
				fmt.Println("IQ mode restored to", origQ9)
			}
		}()
		// Q9 is volatile and some units ignore it, so read it back (doc/qmx/iq-mode.md).
		time.Sleep(150 * time.Millisecond)
		q9, err := probe.Query(port, "Q9;", catTimeout)
		if err != nil || q9 != "Q91;" {
			return fmt.Errorf("IQ mode did not enable (reply %q, err %v)", q9, err)
		}
		fmt.Println("IQ mode enabled")
	}

	be := audio.NewPABackend()
	if err := be.Init(); err != nil {
		return fmt.Errorf("init audio: %w", err)
	}
	defer be.Terminate()
	capt, err := be.OpenCapture(cfg.AudioDevice, cfg.SampleRate, cfg.Frames)
	if err != nil {
		return err
	}
	defer capt.Close()

	buf := make([]float32, cfg.Frames*audio.Channels)
	var (
		total, second probe.IQStats
		meter         probe.RateMeter
		overflows     int
		settle        = 500 * time.Millisecond
		start         = time.Now()
		metering      bool
		lastReport    = start
	)
	fmt.Printf("capturing %v from %q at %d Hz, %d frames/buffer\n", cfg.Probe, cfg.AudioDevice, cfg.SampleRate, cfg.Frames)
	for time.Since(start) < cfg.Probe+settle {
		if ctx.Err() != nil {
			fmt.Println("interrupted")
			break
		}
		n, err := capt.Read(buf)
		if errors.Is(err, audio.ErrOverflow) {
			overflows++
		} else if err != nil {
			return fmt.Errorf("capture: %w", err)
		}
		now := time.Now()
		if !metering {
			// The stream delivers an initial burst while PortAudio fills its buffers.
			if now.Sub(start) >= settle {
				meter.Start(now)
				metering = true
				lastReport = now
			}
			continue
		}
		meter.Add(n)
		total.AddInterleaved(buf, n)
		second.AddInterleaved(buf, n)
		if now.Sub(lastReport) >= time.Second {
			fmt.Printf("  %s\n", second.String())
			second = probe.IQStats{}
			lastReport = now
		}
	}
	rate, ppm := meter.Rate(time.Now(), cfg.SampleRate)
	fmt.Println("summary:")
	fmt.Printf("  %s\n", total.String())
	fmt.Printf("  frames %d, measured rate %.2f Hz (%+.0f ppm vs host clock; run >= 60 s for drift work), overflows %d\n",
		total.Frames(), rate, ppm, overflows)
	return nil
}
