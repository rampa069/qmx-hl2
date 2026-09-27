// Package engine connects the QMX (audio + CAT) to the Protocol 1 server: it streams QMX IQ to
// the client as EP6 packets paced by the QMX capture clock, applies host register writes, and
// keeps the QMX tuned so that its IQ window covers RX1.
//
// Transmit is not implemented yet: MOX from the host is ignored and the QMX is never keyed.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"sync"
	"time"

	"github.com/rampa/qmx-hl2/internal/audio"
	"github.com/rampa/qmx-hl2/internal/dsp"
	"github.com/rampa/qmx-hl2/internal/hpsdr"
	"github.com/rampa/qmx-hl2/internal/qmx"
)

// Config tunes the engine.
type Config struct {
	SampleRate int     // QMX audio rate (48000)
	Frames     int     // capture buffer size
	RXGainDB   float64 // digital gain applied to QMX IQ before sending
	DCCornerHz float64 // DC-blocker corner; 0 disables it
	// IFOffset is how far the QMX dial sits above the IQ centre (LO) in Digi/USB mode: 12 kHz.
	IFOffset int
	// IQSettle is how long IQ is muted after enabling IQ mode (the ADC path shows a large
	// decaying transient, bench 2026-09-27).
	IQSettle time.Duration
	// SwapIQ exchanges the channels (use if the spectrum shows up mirrored). The bench and
	// third-party code say left = I, but on-air orientation is not yet verified.
	SwapIQ bool
}

// DefaultConfig returns bench-derived defaults.
func DefaultConfig() Config {
	return Config{SampleRate: 48000, Frames: 240, DCCornerHz: 20, IFOffset: 12000, IQSettle: time.Second}
}

// Sender transmits an EP6 packet to the client.
type Sender interface {
	Send(pkt []byte, to netip.AddrPort) error
}

// Engine implements hpsdr.Handler.
type Engine struct {
	cfg  Config
	cat  *qmx.Client
	capt audio.CaptureStream
	out  Sender

	mu        sync.Mutex
	state     hpsdr.RadioState
	streaming bool
	client    netip.AddrPort
	gen       uint64 // bumped when the stream (re)starts or its shape changes
	acks      []hpsdr.CC
	lo        float64   // current QMX IQ centre, Hz (0 = unknown)
	muteUntil time.Time // IQ is replaced with zeros until then
	moxWarned bool

	tune chan struct{}
}

// New creates an engine. The capture stream must be open and delivering stereo IQ.
func New(cfg Config, cat *qmx.Client, capt audio.CaptureStream, out Sender) *Engine {
	return &Engine{
		cfg:   cfg,
		cat:   cat,
		capt:  capt,
		out:   out,
		state: hpsdr.DefaultRadioState(),
		tune:  make(chan struct{}, 1),
	}
}

// Started implements hpsdr.Handler.
func (e *Engine) Started(addr netip.AddrPort, _ hpsdr.StartStop) {
	e.mu.Lock()
	if e.streaming && e.client == addr {
		e.mu.Unlock()
		return // clients repeat Start until EP6 arrives
	}
	e.streaming = true
	e.client = addr
	e.gen++
	e.mu.Unlock()
	e.requestTune()
}

// Stopped implements hpsdr.Handler.
func (e *Engine) Stopped(reason string) {
	e.mu.Lock()
	e.streaming = false
	e.mu.Unlock()
}

// EP2 implements hpsdr.Handler.
func (e *Engine) EP2(pkt []byte) {
	var frames [2]hpsdr.EP2Frame
	if !hpsdr.ParseEP2(pkt, &frames) {
		return
	}
	retune := false
	e.mu.Lock()
	for _, f := range frames {
		oldRate, oldN, oldRX1 := e.state.SampleRate, e.state.Receivers, e.state.RX1Freq()
		if e.state.Apply(f.CC) {
			if e.state.SampleRate != oldRate || e.state.Receivers != oldN {
				e.gen++
				slog.Info("stream shape", "rate", e.state.SampleRate, "receivers", e.state.Receivers)
			}
			if e.state.RX1Freq() != oldRX1 {
				retune = true
			}
			slog.Debug("host register", "addr", fmt.Sprintf("0x%02x", f.CC.Addr), "data", fmt.Sprintf("0x%08x", f.CC.Data),
				"tx", e.state.TXFreq, "rx1", e.state.RXFreq[0], "duplex", e.state.Duplex, "rate", e.state.SampleRate, "nrx", e.state.Receivers)
		}
		if f.CC.RQST {
			e.acks = append(e.acks, f.CC)
		}
		if f.CC.MOX && !e.moxWarned {
			e.moxWarned = true
			slog.Warn("host requested TX (MOX); transmit is not implemented, ignoring")
		}
	}
	e.mu.Unlock()
	if retune {
		e.requestTune()
	}
}

func (e *Engine) requestTune() {
	select {
	case e.tune <- struct{}{}:
	default:
	}
}

// Run enables IQ mode, then runs the CAT and capture loops until ctx ends. It restores the
// QMX's IQ mode, operating mode and frequency on return, so the CAT client's Run must keep
// going until this returns.
func (e *Engine) Run(ctx context.Context) error {
	restore, err := e.setupRadio(ctx)
	if err != nil {
		return err
	}
	defer restore()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); e.catLoop(ctx) }()
	err = e.captureLoop(ctx)
	cancel()
	wg.Wait()
	return err
}

func (e *Engine) setupRadio(ctx context.Context) (restore func(), err error) {
	orig := map[string]string{}
	for _, c := range []string{"FA", "MD", "Q9"} {
		r, err := e.cat.Query(ctx, c+";")
		if err != nil {
			return nil, fmt.Errorf("read QMX %s: %w", c, err)
		}
		orig[c] = r
	}
	slog.Info("QMX state saved", "fa", orig["FA"], "md", orig["MD"], "q9", orig["Q9"])
	// Digi mode puts the LO exactly IFOffset below the dial (CW mode adds its own offset).
	if err := e.cat.SetMode(qmx.ModeDigi); err != nil {
		return nil, err
	}
	if err := e.ensureIQMode(ctx); err != nil {
		return nil, err
	}
	return func() {
		for _, c := range []string{"MD", "FA", "Q9"} {
			if err := e.cat.Set(orig[c]); err != nil {
				slog.Warn("restore failed", "cmd", orig[c], "err", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		slog.Info("QMX state restored")
	}, nil
}

// ensureIQMode sets Q91 and verifies it; Q9 is volatile on the QMX (doc/qmx/iq-mode.md).
func (e *Engine) ensureIQMode(ctx context.Context) error {
	on, err := e.cat.IQMode(ctx)
	if err == nil && on {
		return nil
	}
	if err := e.cat.SetIQMode(true); err != nil {
		return err
	}
	e.mu.Lock()
	e.muteUntil = time.Now().Add(e.cfg.IQSettle)
	e.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	on, err = e.cat.IQMode(ctx)
	if err != nil || !on {
		return fmt.Errorf("QMX did not enter IQ mode (on=%v, err %v)", on, err)
	}
	slog.Info("QMX IQ mode enabled")
	return nil
}

// catLoop owns the serial port after setup: it retunes on request and re-asserts IQ mode.
func (e *Engine) catLoop(ctx context.Context) {
	check := time.NewTicker(5 * time.Second)
	defer check.Stop()
	var dial uint32
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			if err := e.ensureIQMode(ctx); err != nil {
				slog.Warn("IQ mode check failed", "err", err)
			}
		case <-e.tune:
			e.mu.Lock()
			rx1 := e.state.RX1Freq()
			e.mu.Unlock()
			if rx1 == 0 {
				continue
			}
			want := rx1 + uint32(e.cfg.IFOffset)
			if want == dial {
				continue
			}
			if err := e.cat.SetFreqA(want); err != nil {
				slog.Warn("tune failed", "err", err)
				continue
			}
			dial = want
			e.mu.Lock()
			e.lo = float64(rx1)
			e.mu.Unlock()
			slog.Debug("tuned", "rx1", rx1, "dial", want)
			// Coalesce bursts of tuning (a client dragging the VFO) to about 20 retunes/s.
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// captureLoop reads QMX IQ, converts it to the host's rate and receivers, and sends EP6.
func (e *Engine) captureLoop(ctx context.Context) error {
	var (
		buf     = make([]float32, e.capt.FramesPerBuffer()*audio.Channels)
		gain    = math.Pow(10, e.cfg.RXGainDB/20)
		dc      = dsp.NewDCBlocker(e.cfg.DCCornerHz, float64(e.cfg.SampleRate))
		useDC   = e.cfg.DCCornerHz > 0
		myGen   = ^uint64(0)
		interp  *dsp.Interpolator
		ncos    []*dsp.NCO
		builder *hpsdr.EP6Builder
		client  netip.AddrPort
		up      = make([]complex128, 8)
		round   = make([]hpsdr.IQ24, hpsdr.MaxReceivers)
		sendErr int
	)
	send := func(pkt []byte) {
		if err := e.out.Send(pkt, client); err != nil {
			sendErr++
			if sendErr == 1 || sendErr%1000 == 0 {
				slog.Warn("EP6 send failed", "err", err, "count", sendErr)
			}
		}
	}
	for ctx.Err() == nil {
		n, err := e.capt.Read(buf)
		if err != nil && !errors.Is(err, audio.ErrOverflow) {
			return fmt.Errorf("capture: %w", err)
		}
		if errors.Is(err, audio.ErrOverflow) {
			slog.Warn("capture overflow")
		}

		e.mu.Lock()
		streaming, gen := e.streaming, e.gen
		st := e.state
		lo := e.lo
		acks := e.acks
		e.acks = nil
		mute := time.Now().Before(e.muteUntil)
		client = e.client
		e.mu.Unlock()

		if !streaming {
			continue
		}
		if gen != myGen {
			myGen = gen
			L := st.SampleRate / e.cfg.SampleRate
			interp = dsp.NewInterpolator(L)
			ncos = make([]*dsp.NCO, st.Receivers)
			for r := range ncos {
				ncos[r] = dsp.NewNCO(float64(st.SampleRate))
			}
			builder = hpsdr.NewEP6Builder(st.Receivers, send)
			slog.Info("EP6 stream configured", "client", client.String(), "rate", st.SampleRate, "receivers", st.Receivers)
		}
		for _, a := range acks {
			builder.QueueAck(a)
		}
		// Shift each receiver's frequency to DC. Before the first tune the LO is unknown and
		// the IQ is passed through unshifted.
		for r, nco := range ncos {
			shift := 0.0
			if lo != 0 {
				shift = lo - float64(st.RXFreq[r])
				if r == 0 {
					shift = lo - float64(st.RX1Freq())
				}
			}
			if nco.Freq() != shift {
				nco.SetFreq(shift)
			}
		}

		overload := false
		L := interp.L
		for i := 0; i < n; i++ {
			li, ri := float64(buf[2*i]), float64(buf[2*i+1])
			if e.cfg.SwapIQ {
				li, ri = ri, li
			}
			x := complex(li*gain, ri*gain)
			if math.Abs(real(x)) > 0.99 || math.Abs(imag(x)) > 0.99 {
				overload = true
			}
			if useDC {
				x = dc.Process(x)
			}
			if mute {
				x = 0
			}
			interp.Process(x, up)
			for k := 0; k < L; k++ {
				for r, nco := range ncos {
					y := nco.Mix(up[k])
					round[r] = hpsdr.IQ24{I: to24(real(y)), Q: to24(imag(y))}
				}
				builder.AddRound(round)
			}
		}
		builder.SetTelemetry(hpsdr.Telemetry{Overload: overload})
	}
	return nil
}

func to24(v float64) int32 {
	const full = 1<<23 - 1
	s := v * full
	if s > full {
		return full
	}
	if s < -full-1 {
		return -full - 1
	}
	return int32(math.Round(s))
}
