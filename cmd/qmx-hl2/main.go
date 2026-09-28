// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// Command qmx-hl2 presents a QRP Labs QMX/QMX+ as a Hermes-Lite 2 (openHPSDR Protocol 1).
//
// The daemon streams QMX IQ to the client and, with -tx, transmits single-tone modes by
// following the client's TX IQ with the QMX's CAT tone command.
// -list and -probe are bench helpers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rampa069/qmx-hl2/internal/audio"
	"github.com/rampa069/qmx-hl2/internal/config"
	"github.com/rampa069/qmx-hl2/internal/engine"
	"github.com/rampa069/qmx-hl2/internal/hpsdr"
	"github.com/rampa069/qmx-hl2/internal/logging"
	"github.com/rampa069/qmx-hl2/internal/probe"
	"github.com/rampa069/qmx-hl2/internal/qmx"
	"github.com/rampa069/qmx-hl2/internal/serial"
	"github.com/rampa069/qmx-hl2/internal/virtual"
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
		err = runDaemon(ctx, cfg)
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
	if vn, err := qmx.Query(port, "VN;", catTimeout); err == nil {
		fmt.Println("firmware:", vn)
	} else {
		fmt.Println("firmware: unknown:", err)
	}
	origQ9, err := qmx.Query(port, "Q9;", catTimeout)
	if err != nil {
		return fmt.Errorf("read IQ mode: %w", err)
	}
	fmt.Println("IQ mode at start:", origQ9)

	if cfg.ProbeIQMode && origQ9 != "Q91;" {
		if err := qmx.Send(port, "Q91;"); err != nil {
			return err
		}
		defer func() {
			if err := qmx.Send(port, origQ9); err != nil {
				slog.Warn("could not restore IQ mode", "want", origQ9, "err", err)
			} else {
				fmt.Println("IQ mode restored to", origQ9)
			}
		}()
		// Q9 is volatile and some units ignore it, so read it back (doc/qmx/iq-mode.md).
		time.Sleep(150 * time.Millisecond)
		q9, err := qmx.Query(port, "Q9;", catTimeout)
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

	// Optionally play silence to the QMX and measure the playback clock the same way. Blocking
	// writes return at the device's consumption rate once the output buffer is full.
	type playResult struct {
		rate, ppm  float64
		underflows int
		err        error
	}
	playDone := make(chan playResult, 1)
	playCtx, stopPlay := context.WithCancel(ctx)
	defer stopPlay()
	if cfg.ProbePlayback {
		pb, err := be.OpenPlayback(cfg.AudioDevice, cfg.SampleRate, cfg.Frames)
		if err != nil {
			return err
		}
		defer pb.Close()
		go func() {
			var (
				m      probe.RateMeter
				res    playResult
				silent = make([]float32, cfg.Frames*audio.Channels)
				t0     = time.Now()
				on     bool
			)
			for playCtx.Err() == nil {
				err := pb.Write(silent)
				if errors.Is(err, audio.ErrUnderflow) {
					res.underflows++
				} else if err != nil {
					res.err = err
					break
				}
				now := time.Now()
				if !on {
					if now.Sub(t0) >= time.Second {
						m.Start(now)
						on = true
					}
					continue
				}
				m.Add(now, cfg.Frames)
			}
			res.rate, res.ppm = m.Rate(cfg.SampleRate)
			playDone <- res
		}()
	} else {
		playDone <- playResult{}
	}

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
		meter.Add(now, n)
		total.AddInterleaved(buf, n)
		second.AddInterleaved(buf, n)
		if now.Sub(lastReport) >= time.Second {
			fmt.Printf("  %s\n", second.String())
			second = probe.IQStats{}
			lastReport = now
		}
	}
	rate, ppm := meter.Rate(cfg.SampleRate)
	fmt.Println("summary:")
	fmt.Printf("  %s\n", total.String())
	fmt.Printf("  frames %d, measured rate %.2f Hz (%+.1f ppm vs host clock, regression), overflows %d\n",
		total.Frames(), rate, ppm, overflows)
	stopPlay()
	if pr := <-playDone; cfg.ProbePlayback {
		if pr.err != nil {
			return fmt.Errorf("playback: %w", pr.err)
		}
		fmt.Printf("  playback (silence): measured rate %.2f Hz (%+.1f ppm vs host), underflows %d; capture-playback %+.1f ppm\n",
			pr.rate, pr.ppm, pr.underflows, ppm-pr.ppm)
	}
	return nil
}

func runDaemon(ctx context.Context, cfg *config.Config) error {
	id := hpsdr.DefaultIdentity()
	mac, err := hpsdr.ParseMAC(cfg.MAC)
	if err != nil {
		return fmt.Errorf("-mac: %w", err)
	}
	id.MAC = mac

	// The QMX: the real one on USB, or with -parrot a virtual one that replays each
	// transmission to the client (no serial port, no sound card, nothing on the air).
	var (
		port     qmx.ReadWriter
		capt     audio.CaptureStream
		openPlay func() (audio.PlaybackStream, error)
		dev      string
	)
	if cfg.Parrot > 0 {
		vr := virtual.New(cfg.SampleRate, cfg.Frames, cfg.Parrot)
		port, capt, dev = vr.CAT(), vr.Capture(), "virtual (parrot)"
		openPlay = func() (audio.PlaybackStream, error) { return vr.Playback(), nil }
	} else {
		dev, err = serial.FindQMX(cfg.SerialPort)
		if err != nil {
			return err
		}
		sp := serial.NewPort(serial.DefaultBaud)
		if err := sp.Open(dev); err != nil {
			return err
		}
		defer sp.Close()
		port = sp

		be := audio.NewPABackend()
		if err := be.Init(); err != nil {
			return fmt.Errorf("init audio: %w", err)
		}
		defer be.Terminate()
		c, err := be.OpenCapture(cfg.AudioDevice, cfg.SampleRate, cfg.Frames)
		if err != nil {
			return err
		}
		defer c.Close()
		capt = c
		openPlay = func() (audio.PlaybackStream, error) {
			return be.OpenPlayback(cfg.AudioDevice, cfg.SampleRate, cfg.Frames)
		}
	}

	ecfg := engine.DefaultConfig()
	ecfg.SampleRate = cfg.SampleRate
	ecfg.Frames = cfg.Frames
	ecfg.RXGainDB = cfg.RXGainDB
	if cfg.SwapIQ {
		ecfg.MirrorOutput = !ecfg.MirrorOutput
	}
	ecfg.TX.Enabled = cfg.TX
	ecfg.TX.Mode = cfg.TXMode
	ecfg.TX.MaxTX = cfg.MaxTX
	ecfg.IQBalance = cfg.IQBal
	ecfg.NoiseFill = cfg.NoiseFill
	ecfg.LOOffset = cfg.LOOffset
	ecfg.TX.SSBGain = math.Pow(10, cfg.SSBGain/20)
	ecfg.TX.WAVDir = cfg.TXWAVDir
	ecfg.TX.Virtual = cfg.Parrot > 0
	if cfg.TXSwapIQ {
		ecfg.TX.SwapIQ = !ecfg.TX.SwapIQ
	}

	// The CAT client outlives the engine so the engine can restore the radio on exit.
	cat := qmx.NewClient(port)
	catCtx, catCancel := context.WithCancel(context.Background())
	catDone := make(chan error, 1)
	go func() { catDone <- cat.Run(catCtx) }()
	defer func() { catCancel(); <-catDone }()

	// The engine sends through the server's socket, and the server calls the engine.
	var srv *hpsdr.Server
	eng := engine.New(ecfg, cat, capt, senderFunc(func(p []byte, to netip.AddrPort) error { return srv.Send(p, to) }))
	if cfg.TX && cfg.TXMode != "tone" {
		pb, err := openPlay()
		if err != nil {
			return fmt.Errorf("SSB transmit needs QMX audio playback: %w", err)
		}
		defer pb.Close()
		eng.SetPlayback(pb)
	}
	srv, err = hpsdr.NewServer(id, eng, cfg.Watchdog)
	if err != nil {
		return err
	}
	if err := srv.Listen(cfg.Listen); err != nil {
		return err
	}
	fmt.Printf("emulating Hermes-Lite 2 on udp %s (MAC %s) with QMX on %s; Ctrl-C to stop\n", srv.LocalAddr(), id.MAC, dev)
	if cfg.Parrot > 0 {
		fmt.Printf("PARROT: nothing is transmitted; each transmission is replayed to the client %s after it starts\n", cfg.Parrot)
	} else if cfg.TX {
		fmt.Printf("TX ENABLED (mode %s): the QMX transmits when the client keys MOX\n", cfg.TXMode)
	} else {
		fmt.Println("receive only (start with -tx to allow transmitting)")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ctx) }()
	err = eng.Run(ctx)
	cancel()
	if serr := <-srvErr; err == nil {
		err = serr
	}
	return err
}

type senderFunc func([]byte, netip.AddrPort) error

func (f senderFunc) Send(p []byte, to netip.AddrPort) error { return f(p, to) }
