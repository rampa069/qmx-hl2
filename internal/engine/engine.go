// Package engine connects the QMX (audio + CAT) to the Protocol 1 server: it streams QMX IQ to
// the client as EP6 packets paced by the QMX capture clock, applies host register writes, and
// keeps the QMX tuned so that its IQ window covers RX1.
//
// Transmit is optional (Config.TX.Enabled). When enabled, MOX-tagged TX IQ from the host is
// reduced to a single tone whose frequency is sent to the QMX with CAT TA (see tx.go). When
// disabled, MOX is ignored and the QMX is never keyed.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
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
	// SwapIQ exchanges the QMX channels at the input. Bench and third-party code say
	// left = I, and FT8 cross-checks confirm it, so this stays false.
	SwapIQ bool
	// MirrorOutput sends the conjugate spectrum to the client. HPSDR clients (Zeus, and per
	// the user any client) expect EP6 IQ mirrored relative to the textbook I + jQ convention,
	// just as their TX IQ arrives mirrored: with a real HL2 they are right-way-up, while the
	// unmirrored emulator output showed FT8 reflected around the RX1 centre (2026-09-27).
	MirrorOutput bool
	// IQBalance enables blind I/Q gain/phase correction (better image rejection).
	IQBalance bool
	// NoiseFill adds low-level noise (10 dB below the QMX's own floor) across the whole
	// output band at 96/192/384 kHz, so the part the QMX cannot cover looks like a quiet band
	// instead of a black hole.
	NoiseFill bool
	// LOOffset places the QMX's IQ centre this many Hz from RX1 (e.g. -4000), moving the
	// QMX's strong low-frequency noise hump away from the middle of the display.
	LOOffset int

	TX TXConfig
}

// DefaultConfig returns bench-derived defaults.
func DefaultConfig() Config {
	return Config{SampleRate: 48000, Frames: 240, DCCornerHz: 20, IFOffset: 12000, IQSettle: time.Second, MirrorOutput: true,
		IQBalance: true, NoiseFill: true, TX: DefaultTXConfig()}
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

	tx        *transmitter // nil when TX is disabled
	txActive  bool         // QMX keyed: RX IQ is junk and retuning waits
	txEndedAt time.Time
	forceTune bool
	txWatts   float64 // last QMX power/SWR reading while keyed, for EP6 telemetry
	txSWR     float64

	tune     chan struct{}
	playback audio.PlaybackStream

	ep2Count, ep6Count atomic.Int64 // packet counters for the rate log
}

// New creates an engine. The capture stream must be open and delivering stereo IQ.
func New(cfg Config, cat *qmx.Client, capt audio.CaptureStream, out Sender) *Engine {
	e := &Engine{
		cfg:   cfg,
		cat:   cat,
		capt:  capt,
		out:   out,
		state: hpsdr.DefaultRadioState(),
		tune:  make(chan struct{}, 1),
	}
	if cfg.TX.Enabled {
		e.tx = newTransmitter(cfg.TX, cat, e.setTXActive)
		e.tx.meter = func(w, swr float64) {
			e.mu.Lock()
			e.txWatts, e.txSWR = w, swr
			e.mu.Unlock()
		}
	}
	return e
}

// SetPlayback gives the engine a stream to the QMX's USB audio input, enabling the SSB
// transmit path. Call before Run.
func (e *Engine) SetPlayback(pb audio.PlaybackStream) {
	e.playback = pb
	if e.tx != nil && pb != nil {
		e.tx.ssb = newSSBAudio(e.cfg.SampleRate)
	}
}

func (e *Engine) setTXActive(on bool) {
	e.mu.Lock()
	e.txActive = on
	if !on {
		e.txWatts, e.txSWR = 0, 0
		e.txEndedAt = time.Now()
		e.forceTune = true // the transmitter moved the dial
	}
	e.mu.Unlock()
	if !on {
		e.requestTune()
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
	e.ep2Count.Add(1)
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
		if e.tx != nil {
			e.tx.submit(txFrame{mox: f.CC.MOX, txFreq: e.state.TXFreq, iq: f.TXIQ})
		} else if f.CC.MOX && !e.moxWarned {
			e.moxWarned = true
			slog.Warn("host requested TX (MOX); transmit is disabled (start with -tx), ignoring")
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
	if e.tx != nil {
		// The transmitter keys up on shutdown before restore runs.
		wg.Add(1)
		go func() { defer wg.Done(); e.tx.run(ctx) }()
		if e.tx.ssb != nil {
			wg.Add(1)
			go func() { defer wg.Done(); e.tx.ssb.run(ctx, e.playback, e.cfg.Frames) }()
		}
	}
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
	keys := []string{"MD", "FA", "Q9"}
	if e.tx != nil {
		for _, c := range []string{"QB", "QC"} {
			r, err := e.cat.Query(ctx, c+";")
			if err != nil {
				return nil, fmt.Errorf("read QMX %s: %w", c, err)
			}
			orig[c] = r
		}
		keys = append(keys, "QC", "QB")
		if e.tx.ssb != nil {
			r, err := e.cat.Query(ctx, "SS;")
			if err != nil {
				return nil, fmt.Errorf("read QMX SS: %w", err)
			}
			orig["SS"] = r
			keys = append(keys, "SS")
			if err := e.cat.Set("SS0;"); err != nil { // SSB audio from USB
				return nil, err
			}
		}
		// If the daemon dies mid-transmission, the QMX drops TX after 3 s without CAT.
		if err := e.cat.SetCATTimeout(true, 3); err != nil {
			return nil, err
		}
		e.cat.AllowTX(true)
		slog.Warn("TX ENABLED: the QMX will transmit when the client keys MOX")
	}
	// Digi mode puts the LO exactly IFOffset below the dial (CW mode adds its own offset).
	if err := e.cat.SetMode(qmx.ModeDigi); err != nil {
		return nil, err
	}
	if err := e.ensureIQMode(ctx); err != nil {
		return nil, err
	}
	return func() {
		_ = e.cat.RX()
		e.cat.AllowTX(false)
		for _, c := range keys {
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
			e.mu.Lock()
			busy := e.txActive
			e.mu.Unlock()
			if busy {
				continue
			}
			if err := e.ensureIQMode(ctx); err != nil {
				slog.Warn("IQ mode check failed", "err", err)
			}
		case <-e.tune:
			e.mu.Lock()
			rx1 := e.state.RX1Freq()
			busy := e.txActive
			verify := false
			if e.forceTune && !busy {
				dial = 0
				e.forceTune = false
				verify = true
			}
			e.mu.Unlock()
			if rx1 == 0 || busy {
				continue // after TX, setTXActive(false) requests a retune
			}
			centre := uint32(int64(rx1) + int64(e.cfg.LOOffset))
			want := centre + uint32(e.cfg.IFOffset)
			if want == dial {
				continue
			}
			if err := e.cat.SetFreqA(want); err != nil {
				slog.Warn("tune failed", "err", err)
				continue
			}
			dial = want
			e.mu.Lock()
			e.lo = float64(centre)
			e.mu.Unlock()
			slog.Debug("tuned", "rx1", rx1, "dial", want)
			if verify {
				// After TX the transmitter changed the dial (and for SSB the mode); make sure
				// the QMX really is back in Digi mode on the RX dial.
				e.verifyRX(ctx, want)
			}
			// Coalesce bursts of tuning (a client dragging the VFO) to about 20 retunes/s.
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// verifyRX reads back the QMX mode and dial after a transmission and corrects them.
func (e *Engine) verifyRX(ctx context.Context, want uint32) {
	time.Sleep(100 * time.Millisecond)
	md, err1 := e.cat.Mode(ctx)
	fa, err2 := e.cat.FreqA(ctx)
	if err1 != nil || err2 != nil {
		slog.Warn("RX verify: CAT read failed", "mode_err", err1, "freq_err", err2)
		return
	}
	if md == qmx.ModeDigi && fa == want {
		slog.Debug("RX verify ok", "mode", md, "dial", fa)
		return
	}
	slog.Warn("QMX not back on the RX setting after TX; correcting", "mode", md, "dial", fa, "want_dial", want)
	_ = e.cat.SetMode(qmx.ModeDigi)
	time.Sleep(50 * time.Millisecond)
	_ = e.cat.SetFreqA(want)
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
		xs      = make([]complex128, e.capt.FramesPerBuffer())
		iqbal   = dsp.NewIQBalancer(float64(e.cfg.SampleRate), 2)
		floor   = newFloorTracker(400) // ~2 s of 5 ms buffers
		rng     = rand.New(rand.NewPCG(1, 2))
		lastBal time.Time
	)
	var (
		rateT0           = time.Now()
		rateEP2, rateEP6 int64
		rateFrames       int64
	)
	send := func(pkt []byte) {
		e.ep6Count.Add(1)
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

		// Condition the QMX IQ even when nobody is listening, so the DC blocker, I/Q
		// balance and noise-floor estimate are settled when a client starts.
		overload := false
		var pow float64
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
			if e.cfg.IQBalance {
				x = iqbal.Process(x)
			}
			pow += real(x)*real(x) + imag(x)*imag(x)
			xs[i] = x
		}
		if n > 0 {
			floor.add(pow / float64(n))
		}
		rateFrames += int64(n)
		if el := time.Since(rateT0).Seconds(); el >= 30 {
			e2, e6 := e.ep2Count.Load(), e.ep6Count.Load()
			slog.Debug("packet rates", "qmx_frames_s", math.Round(float64(rateFrames)/el),
				"ep6_s", math.Round(float64(e6-rateEP6)/el*10)/10, "ep2_s", math.Round(float64(e2-rateEP2)/el*10)/10)
			rateT0, rateEP2, rateEP6, rateFrames = time.Now(), e2, e6, 0
		}
		if e.cfg.IQBalance && time.Since(lastBal) > time.Minute {
			lastBal = time.Now()
			pc, sc := iqbal.Correction()
			slog.Debug("I/Q balance", "phase_deg", math.Asin(math.Max(-1, math.Min(1, pc)))*180/math.Pi, "q_gain_db", 20*math.Log10(sc))
		}

		e.mu.Lock()
		streaming, gen := e.streaming, e.gen
		st := e.state
		lo := e.lo
		acks := e.acks
		e.acks = nil
		now := time.Now()
		mute := now.Before(e.muteUntil) || e.txActive || now.Sub(e.txEndedAt) < 100*time.Millisecond
		transmitting := e.txActive
		watts, swr := e.txWatts, e.txSWR
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

		L := interp.L
		// Fill noise: 10 dB below the QMX floor density, spread over the whole output band.
		// Per component: sigma^2 = floor * L * 0.1 / 2.
		sigma := 0.0
		if e.cfg.NoiseFill && L > 1 && !mute {
			sigma = math.Sqrt(floor.min() * float64(L) * 0.1 / 2)
		}
		for i := 0; i < n; i++ {
			x := xs[i]
			if mute {
				x = 0
			}
			interp.Process(x, up)
			for k := 0; k < L; k++ {
				if sigma > 0 {
					up[k] += complex(sigma*rng.NormFloat64(), sigma*rng.NormFloat64())
				}
				for r, nco := range ncos {
					y := nco.Mix(up[k])
					q := imag(y)
					if e.cfg.MirrorOutput {
						q = -q
					}
					round[r] = hpsdr.IQ24{I: to24(real(y)), Q: to24(q)}
				}
				builder.AddRound(round)
			}
		}
		tel := hpsdr.Telemetry{Overload: overload, Temp: hpsdr.TempRaw(30)}
		if transmitting {
			tel.TXFIFO = 16 // a plausible ~10 ms of TX buffer, as a real HL2 would report
			tel.FwdPower = hpsdr.PowerRaw(watts)
			tel.RevPower = hpsdr.ReversePowerRaw(watts, swr)
		}
		builder.SetTelemetry(tel)
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

// floorTracker estimates the band noise floor as the minimum of recent block powers.
type floorTracker struct {
	ring []float64
	pos  int
	full bool
}

func newFloorTracker(n int) *floorTracker { return &floorTracker{ring: make([]float64, n)} }

func (f *floorTracker) add(p float64) {
	f.ring[f.pos] = p
	f.pos++
	if f.pos == len(f.ring) {
		f.pos, f.full = 0, true
	}
}

func (f *floorTracker) min() float64 {
	n := f.pos
	if f.full {
		n = len(f.ring)
	}
	if n == 0 {
		return 0
	}
	m := f.ring[0]
	for _, v := range f.ring[1:n] {
		m = math.Min(m, v)
	}
	return m
}
