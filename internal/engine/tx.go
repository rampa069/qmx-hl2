// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package engine

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"math"
	"os"
	"sync/atomic"
	"time"

	"github.com/rampa069/qmx-hl2/internal/dsp"
	"github.com/rampa069/qmx-hl2/internal/hpsdr"
)

// TXRadio is the part of the QMX CAT client the transmitter needs.
type TXRadio interface {
	SetFreqA(hz uint32) error
	TX() error
	RX() error
	Tone(hz float64) error
	SetMode(m int) error
	SWR(ctx context.Context) (float64, error)
	PowerOut(ctx context.Context) (float64, error)
	// SWRProtection reports whether the QMX's own SWR protection has locked transmit (CAT SR,
	// firmware 1_04_004 or later).
	SWRProtection(ctx context.Context) (bool, error)
	// Transmitting reports the QMX's TX state (CAT TQ).
	Transmitting(ctx context.Context) (bool, error)
	// SetSplit turns split (TX on VFO B) on or off.
	SetSplit(on bool) error
}

// TXConfig controls the transmit path. The QMX transmits a single tone whose frequency is set
// over CAT (TA): the host's TX IQ is reduced to its instantaneous frequency, which covers
// FT8/FT4/WSPR/JS8/RTTY and CW sent as IQ. Voice and multi-tone modes are not supported yet.
type TXConfig struct {
	Enabled bool
	// ToneCentre is where the audio tone is placed at key-down; the dial is set so that the
	// first tone lands here, leaving room for the tone to move either way.
	ToneCentre float64
	ToneMin    float64 // TA range verified on the bench: 100..8000 Hz
	ToneMax    float64
	// GateLevel is the RMS TX IQ level (1.0 = full scale) above which RF is on.
	GateLevel float64
	// Window is the frequency-estimation window in samples at 48 kHz.
	Window int
	// Starve drops TX when no MOX frames arrive for this long (HL2: latency + hang, ~32 ms;
	// USB/network jitter needs more).
	Starve time.Duration
	// MaxTX is the longest continuous transmission; after it TX is inhibited until MOX drops.
	MaxTX time.Duration
	// SWRMax aborts TX when the SWR is above it on two consecutive polls after SWRGrace
	// (the QMX reports a large transient at key-down).
	SWRMax   float64
	SWRGrace time.Duration
	// Mode selects the transmit path: "tone" (CAT TA only), "ssb" (QMX SSB mode fed with USB
	// audio) or "auto" (decide per over from the first 20-150 ms of TX IQ). SSB needs a
	// playback stream to the QMX.
	Mode string
	// SSBGain scales the SSB audio (1.0 = the client's TX IQ amplitude as is).
	SSBGain float64
	// Virtual is set when the radio is the -parrot stand-in: nothing reaches the air.
	Virtual bool
	// WAVDir, if set, receives one WAV file per SSB over with the audio exactly as played
	// to the QMX, so an SSTV picture can be decoded offline.
	WAVDir string
	// SwapIQ exchanges the host's TX I and Q words. HPSDR clients send TX I/Q "reversed
	// relative to receive" (USB protocol doc); with Zeus on 2026-09-27 an FT8 tone at +1500 Hz
	// arrived as -1500 Hz unswapped, so the default is true.
	SwapIQ bool
}

// DefaultTXConfig returns bench-derived defaults with TX disabled.
func DefaultTXConfig() TXConfig {
	return TXConfig{
		ToneCentre: 1500, ToneMin: 100, ToneMax: 8000,
		GateLevel: 0.01, Window: 240,
		Starve: 150 * time.Millisecond, MaxTX: 3 * time.Minute,
		SWRMax: 3.0, SWRGrace: 600 * time.Millisecond,
		SwapIQ: true,
		Mode:   "auto", SSBGain: 1.0,
	}
}

type txFrame struct {
	mox    bool
	cwx    bool // CWX enabled (C&C 0x0f bit 24): the host may key CW with MOX off
	txFreq uint32
	iq     [hpsdr.SamplesPerEP2Frm][2]int16
}

type txState int

const (
	txIdle    txState = iota
	txArming          // MOX seen, waiting for enough IQ to estimate the tone
	txOn              // QMX keyed
	txInhibit         // aborted; wait for MOX to drop
)

// transmitter turns MOX-tagged host frames into QMX CAT TX/TA/RX commands.
type transmitter struct {
	cfg    TXConfig
	radio  TXRadio
	frames chan txFrame
	now    func() time.Time
	// active is called with true when the QMX is keyed and false when it returns to RX.
	active func(on bool)
	// meter, if set, receives the QMX's measured power (W) and SWR during TX.
	meter func(watts, swr float64)
	// paddleMode, if set, reports that the QMX is in CW mode with split for its own paddles;
	// the daemon's own transmissions then switch it to Digi (or SSB) without split first, and
	// the engine sets CW up again afterwards.
	paddleMode func() bool

	state      txState
	tr         *dsp.ToneTracker
	lastMox    time.Time
	onSince    time.Time
	dial       uint32
	toneOn     bool
	lastTone   float64
	lastSent   time.Time
	highSWR    int
	zeroWarned bool // the 0 W warning was given this over
	zeroCount  int  // consecutive keyed meter readings at 0 W
	// After key-up the stop is confirmed with CAT TQ and re-sent until the QMX reports RX: a
	// lost TA0/RX (a USB serial hiccup) would otherwise leave it keyed, and the engine's
	// retuning would keep feeding the QMX's CAT watchdog (seen in another QMX project,
	// groups.io QRPLabs topic 119565643). Until then the engine still sees TX as active.
	// CWX: the host keys CW with MOX off through bits in the TX I words (the HL2 gateware
	// shapes the carrier itself); here each key edge becomes TX/TA or TA0.
	cwxOn   bool
	cwxLast time.Time // last frame with the key or the CWX PTT set

	stopping     bool
	stopTone     bool // the over was a tone: re-send TA0 as well as RX
	stopAttempts int
	stopNext     time.Time
	stopWarned   time.Time
	armedFreq    uint32

	lastPower, lastSWRVal float64
	// per-transmission statistics, logged at key-up
	statKeyDowns, statTones int
	statMaxLevel            float64
	statRawWatts            float64
	keyed                   atomic.Bool // read by meterLoop

	cls     *classifier
	pending []float32 // SSB audio captured while classifying (the last maxPending samples)
	// armSignal counts TX samples since the first one with signal in this arming. The
	// classification deadline runs from there, not from MOX: WSJT-X via Zeus keys MOX about
	// 600 ms before its tones, and a deadline counted from MOX sent every such FT8 over to
	// SSB (2026-09-27/28).
	armSignal int
	am        amDetector // AM over going out as a tone: switch it to SSB
	// A tone over whose frequency moves away from where it started is FSK with a wide shift
	// (RTTY) or MFSK, which CAT TA cannot follow cleanly: switch it to SSB.
	baseFreq  float64
	baseSet   bool
	excursion int       // consecutive samples with signal away from baseFreq
	dcX, dcY  float64   // DC blocker on the SSB audio (an AM carrier is DC in it)
	voice     bool      // current over uses the SSB path
	ssb       *ssbAudio // nil when no playback stream (tone only)
	scratch   []float32
	dump      io.Writer // debug: raw MOX-frame TX IQ (int16 LE pairs), if set
}

func newTransmitter(cfg TXConfig, radio TXRadio, active func(bool)) *transmitter {
	var dump io.Writer
	// Debug aid: QMXHL2_TXDUMP=/path records the host's TX IQ while MOX is set.
	if path := os.Getenv("QMXHL2_TXDUMP"); path != "" {
		if f, err := os.Create(path); err == nil {
			dump = f
			slog.Warn("dumping TX IQ", "path", path)
		}
	}
	return &transmitter{
		dump:   dump,
		cfg:    cfg,
		radio:  radio,
		frames: make(chan txFrame, 256),
		now:    time.Now,
		active: active,
		tr:     dsp.NewToneTracker(hpsdr.TXSampleRate, cfg.Window),
		cls:    newClassifier(cfg.GateLevel),
	}
}

// submit queues a frame without blocking the network goroutine.
func (t *transmitter) submit(f txFrame) {
	select {
	case t.frames <- f:
	default:
		slog.Warn("TX frame queue full; dropping frame")
	}
}

func (t *transmitter) run(ctx context.Context) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	defer func() {
		t.keyUp("shutdown")
		// No ticks follow; the engine's restore sends RX on its way out anyway.
		if t.stopping {
			t.stopping = false
			if t.active != nil {
				t.active(false)
			}
		}
	}()
	// Meter queries can take up to the CAT timeout; run them off the frame path so tone
	// updates never stall behind them.
	readings := make(chan meterReading, 1)
	go t.meterLoop(ctx, readings)
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-t.frames:
			t.frame(ctx, f)
		case <-tick.C:
			t.tick(ctx)
		case r := <-readings:
			t.onMeter(r)
		}
	}
}

type meterReading struct {
	watts, swr float64
	ok         bool
	protected  bool // the QMX reports SR1: its SWR protection has locked transmit
}

// meterLoop polls power and SWR every 300 ms while the QMX is keyed.
func (t *transmitter) meterLoop(ctx context.Context, out chan<- meterReading) {
	srSupported := true
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if !t.keyed.Load() {
			continue
		}
		swr, err1 := t.radio.SWR(ctx)
		watts, err2 := t.radio.PowerOut(ctx)
		r := meterReading{watts: watts, swr: swr, ok: err1 == nil && err2 == nil}
		// Firmware before 1_04_004 has no SR: stop asking after the first failure, since each
		// unanswered query costs the CAT timeout.
		if srSupported {
			locked, err := t.radio.SWRProtection(ctx)
			if err != nil {
				srSupported = false
				slog.Info("QMX firmware has no CAT SR; its SWR protection is not monitored", "err", err)
			}
			r.protected = locked
		}
		select {
		case out <- r:
		default:
		}
	}
}

// onMeter updates the client-facing meters and applies the SWR guard.
func (t *transmitter) onMeter(r meterReading) {
	if t.state != txOn {
		return
	}
	watts, swr := r.watts, r.swr
	if watts > t.statRawWatts {
		t.statRawWatts = watts
	}
	if !t.toneOn {
		watts, swr = 0, 0
	}
	t.lastPower, t.lastSWRVal = watts, swr
	if t.meter != nil {
		t.meter(watts, swr)
	}
	slog.Debug("TX meter", "power_w", watts, "swr", swr, "tone", t.lastTone)
	if r.protected {
		// The QMX samples SWR every 1 ms and locks transmit above its threshold. On firmware
		// 1_04_010 to 1_04_015 its key-down transient alone tripped it on a good antenna with
		// the threshold at 3 and 5, but not at 7 or 9 (QMX+, 2026-09-29).
		slog.Error("the QMX's own SWR protection has locked transmit (CAT SR1): clear it on the radio " +
			"(enter and leave the menu). On firmware 1_04_010-1_04_015 its key-down transient can trip it even with a good " +
			"antenna (seen with thresholds 3 and 5); if the antenna is fine, raising the QMX's SWR threshold to about 7 avoids it")
		t.abort("QMX SWR protection")
		return
	}
	// A tone over at 0 W past the key-down transient: the QMX is not transmitting (older
	// firmware without SR, or another lock-out). Warn once per over.
	// A single 0 W reading is normal in CW (it can fall between elements), so it must repeat.
	if r.ok && t.toneOn && !t.voice && r.watts == 0 && t.now().Sub(t.onSince) >= t.cfg.SWRGrace+time.Second {
		t.zeroCount++
	} else if r.watts > 0 {
		t.zeroCount = 0
	}
	if t.zeroCount >= 4 && !t.zeroWarned {
		t.zeroWarned = true
		slog.Warn("the QMX reports 0 W while keyed: check its display for a lock-out (S, P, B)")
	}
	// With no RF (r.watts == 0) the QMX still reports an SWR computed from noise in its
	// forward and reverse readings (seen on groups.io, 2026-09): it means nothing.
	if !r.ok || swr == 0 || r.watts == 0 || !t.toneOn || t.now().Sub(t.onSince) < t.cfg.SWRGrace {
		return
	}
	if swr > t.cfg.SWRMax {
		t.highSWR++
		slog.Warn("high SWR", "swr", swr, "count", t.highSWR)
		if t.highSWR >= 2 {
			t.abort("high SWR")
		}
	} else {
		t.highSWR = 0
	}
}

func (t *transmitter) frame(ctx context.Context, f txFrame) {
	now := t.now()
	if !f.mox && (t.cwxOn || f.cwx && cwxBits(f) != 0) {
		t.cwxFrame(f, now)
		return
	}
	if t.cwxOn { // MOX during a CWX over: end it; the next frames start a normal over
		t.cwxOn = false
		t.keyUp("MOX during CWX")
		return
	}
	if !f.mox {
		if t.state != txIdle {
			t.keyUp("MOX released")
		}
		return
	}
	if t.stopping {
		return // the previous over's stop is not confirmed yet
	}
	t.lastMox = now
	if t.dump != nil {
		for _, v := range f.iq {
			_ = binary.Write(t.dump, binary.LittleEndian, v)
		}
	}
	switch t.state {
	case txInhibit:
		return
	case txIdle:
		t.state = txArming
		t.tr.Reset()
		t.cls.reset()
		t.pending = t.pending[:0]
		t.armSignal = 0
		t.am.reset()
		t.baseSet, t.excursion = false, 0
		t.dcX, t.dcY = 0, 0
		t.voice = false
		t.statMaxLevel = 0
		t.armedFreq = f.txFreq
		slog.Info("TX requested by host", "tx_freq", f.txFreq)
	}
	t.scratch = t.scratch[:0]
	for _, s := range f.iq {
		i, q := float64(s[0])/32768, float64(s[1])/32768
		if t.cfg.SwapIQ {
			i, q = q, i
		}
		x := complex(i, q)
		t.tr.Add(x)
		if t.state == txArming {
			t.cls.add(x)
		}
		if t.state == txOn && !t.voice {
			t.am.add(x)
		}
		// The real part of the analytic signal is the audio; the QMX's SSB modulator picks
		// the sideband, so the same audio serves USB and LSB.
		a := i * t.cfg.SSBGain
		y := a - t.dcX + dcPole*t.dcY
		t.dcX, t.dcY = a, y
		t.scratch = append(t.scratch, float32(clamp1(y)))
	}
	// Also while a tone over is on: an AM over may switch to SSB and needs recent audio.
	if (t.state == txArming || t.state == txOn && !t.voice) && t.ssb != nil {
		t.pending = append(t.pending, t.scratch...)
		if over := len(t.pending) - maxPending; over > 0 { // only the latest audio is used
			t.pending = append(t.pending[:0], t.pending[over:]...)
		}
	}
	if t.tr.Ready() {
		if l := t.tr.Level(); l > t.statMaxLevel {
			t.statMaxLevel = l
		}
	}
	if t.state == txOn && t.voice {
		t.ssb.Push(t.scratch)
		return
	}
	if !t.tr.Ready() {
		return
	}
	signal := t.tr.Level() >= t.cfg.GateLevel
	if t.state == txArming && (signal || t.armSignal > 0) {
		t.armSignal += len(f.iq)
	}
	freq := t.tr.Freq()
	switch t.state {
	case txArming:
		if !signal {
			return // MOX without audio yet (e.g. WSJT-X keys PTT before the tones)
		}
		kind := txTone
		switch t.cfg.Mode {
		case "ssb":
			kind = txVoice
		case "auto":
			if t.ssb != nil {
				kind = t.cls.decide()
			}
		}
		switch kind {
		case txUndecided:
			if t.armSignal > hpsdr.TXSampleRate/4 { // should not happen: decide() settles by 150 ms
				kind = txVoice
			} else {
				return
			}
		}
		if kind == txVoice && t.ssb != nil {
			t.keyDownSSB(f.txFreq, now, t.cls.upperSideband())
			return
		}
		t.keyDown(f.txFreq, freq, now)
	case txOn:
		if t.ssb != nil && !t.voice {
			if signal && !t.baseSet {
				t.baseFreq, t.baseSet = freq, true
			}
			// Judge AM by where the over's carrier started, not by the momentary estimate:
			// between CW elements that estimate is meaningless and can sit near 0 Hz, and
			// Zeus's CW (at -600 Hz) was taken for AM (2026-09-29).
			if t.baseSet && math.Abs(t.baseFreq) < amMaxCarrierHz && t.am.isAM() {
				slog.Warn("AM detected: the QMX cannot transmit AM; sending it as USB")
				t.switchToSSB(f.txFreq, now, true)
				return
			}
			if signal {
				if math.Abs(freq-t.baseFreq) > fskShiftHz {
					t.excursion += len(f.iq)
				} else {
					t.excursion = 0
				}
				if t.excursion >= fskHoldSamples {
					slog.Info("wide FSK or MFSK (e.g. RTTY): moving the over to SSB", "from_hz", math.Round(t.baseFreq), "to_hz", math.Round(freq))
					t.switchToSSB(f.txFreq, now, t.baseFreq >= 0)
					return
				}
			}
		}
		t.follow(f.txFreq, freq, signal, now)
	}
}

// leavePaddleMode puts a QMX set up for its paddles (CW mode, split) into mode m without split,
// so that the daemon's own transmission goes out on VFO A.
func (t *transmitter) leavePaddleMode(m int) {
	if t.paddleMode == nil || !t.paddleMode() {
		return
	}
	_ = t.radio.SetSplit(false)
	_ = t.radio.SetMode(m)
}

func (t *transmitter) keyDown(txFreq uint32, bb float64, now time.Time) {
	t.leavePaddleMode(qmxModeDigi) // CAT TA works in Digi mode only
	dial := int64(txFreq) + int64(math.Round(bb-t.cfg.ToneCentre))
	if dial <= 0 {
		slog.Warn("TX frequency out of range", "tx_freq", txFreq, "baseband", bb)
		t.state = txInhibit
		return
	}
	t.dial = uint32(dial)
	if err := t.radio.SetFreqA(t.dial); err != nil {
		slog.Error("TX: set dial failed", "err", err)
		t.state = txInhibit
		return
	}
	if err := t.radio.TX(); err != nil {
		slog.Error("TX: key failed", "err", err)
		t.state = txInhibit
		return
	}
	t.state = txOn
	t.keyed.Store(true)
	t.statKeyDowns, t.statTones, t.statRawWatts = 0, 0, 0
	t.onSince = now
	t.highSWR, t.zeroWarned, t.zeroCount = 0, false, 0
	t.toneOn = false
	if t.active != nil {
		t.active(true)
	}
	slog.Info("TX on", "dial", t.dial, "tone", float64(txFreq)+bb-float64(t.dial))
	t.follow(txFreq, bb, true, now)
}

// keyDownSSB transmits the client's audio through the QMX's SSB modulator.
func (t *transmitter) keyDownSSB(txFreq uint32, now time.Time, usb bool) {
	t.leavePaddleMode(qmxModeDigi)
	mode, name := qmxModeUSB, "USB"
	if !usb {
		mode, name = qmxModeLSB, "LSB"
	}
	t.dial = txFreq
	for _, step := range []func() error{
		func() error { return t.radio.SetMode(mode) },
		func() error { return t.radio.SetFreqA(t.dial) },
		func() error { return t.radio.TX() },
	} {
		if err := step(); err != nil {
			slog.Error("TX (SSB): CAT failed", "err", err)
			_ = t.radio.SetMode(qmxModeDigi)
			t.state = txInhibit
			return
		}
	}
	t.ssb.Reset()
	// Clients key MOX well before the audio (Zeus + an SSTV app: over a second of silence),
	// and all of that was captured while classifying. Start the FIFO at its target with the
	// most recent audio: pushing everything overflowed the FIFO and left 0.5 s of latency
	// that the resampler (max 1000 ppm) could not drain within an over (2026-09-27).
	pre := t.pending
	if max := t.ssb.target; len(pre) > max {
		pre = pre[len(pre)-max:]
	}
	t.ssb.Push(pre)
	t.ssb.Start()
	t.pending = t.pending[:0]
	t.voice = true
	t.toneOn = true // meters show real power
	t.state = txOn
	t.keyed.Store(true)
	t.statKeyDowns, t.statTones, t.statRawWatts = 1, 0, 0
	t.onSince = now
	t.highSWR, t.zeroWarned, t.zeroCount = 0, false, 0
	if t.active != nil {
		t.active(true)
	}
	slog.Info("TX on (SSB)", "dial", t.dial, "sideband", name)
}

func clamp1(v float64) float64 {
	return math.Max(-1, math.Min(1, v))
}

func (t *transmitter) follow(txFreq uint32, bb float64, signal bool, now time.Time) {
	if !signal {
		if t.toneOn {
			// Shaped key-up. Bench 2026-09-27: TA0 also returns the QMX to RX (TQ0), so the
			// next element must send TX again.
			_ = t.radio.Tone(0)
			t.toneOn = false
		}
		return
	}
	tone := float64(txFreq) + bb - float64(t.dial)
	if tone < t.cfg.ToneMin || tone > t.cfg.ToneMax {
		slog.Warn("TX tone outside the QMX range; keying up", "tone", tone)
		t.abort("tone out of range")
		return
	}
	if !t.toneOn || math.Abs(tone-t.lastTone) >= 0.05 || now.Sub(t.lastSent) >= 250*time.Millisecond {
		if !t.toneOn {
			t.statKeyDowns++
			if t.statKeyDowns > 1 { // keyDown already sent TX for the first element
				if err := t.radio.TX(); err != nil {
					slog.Error("TX: re-key failed", "err", err)
					t.abort("CAT error")
					return
				}
			}
		}
		t.statTones++
		if err := t.radio.Tone(tone); err != nil {
			slog.Error("TX: tone failed", "err", err)
			t.abort("CAT error")
			return
		}
		t.toneOn = true
		t.lastTone = tone
		t.lastSent = now
	}
}

func (t *transmitter) tick(ctx context.Context) {
	now := t.now()
	if t.stopping {
		t.confirmStop(ctx, now)
		return
	}
	if t.state == txIdle {
		return
	}
	if now.Sub(t.lastMox) > t.cfg.Starve {
		t.keyUp("no MOX frames from host")
		return
	}
	if t.state != txOn {
		return
	}
	if now.Sub(t.onSince) > t.cfg.MaxTX {
		t.abort("maximum TX time")
		return
	}
	// During silent gaps the QMX is already back in RX (TA0 ends TX), so there is nothing to
	// keep alive. In SSB the meter queries (every 300 ms) keep the QMX CAT watchdog fed.
}

// abort keys up and ignores MOX until the host releases it.
func (t *transmitter) abort(reason string) {
	t.keyUp(reason)
	t.state = txInhibit
}

func (t *transmitter) keyUp(reason string) {
	t.cwxOn = false
	was := t.state
	t.stopTone = !t.voice
	t.state = txIdle
	t.keyed.Store(false)
	if was == txOn {
		if t.voice {
			u, in, out, disc, ppm := t.ssb.OverStats()
			slog.Info("SSB audio", "underruns", u, "resample_ppm", math.Round(ppm),
				"in_rate", math.Round(in), "out_rate", math.Round(out), "discarded", disc)
			t.ssb.Stop()
			if err := t.radio.RX(); err != nil {
				slog.Error("TX: RX command failed", "err", err)
			}
			// The QMX ignores MD while it is still leaving TX (seen 2026-09-27: MD2 remained),
			// so give it a moment; the engine also verifies the RX setting afterwards.
			time.Sleep(150 * time.Millisecond)
			_ = t.radio.SetMode(qmxModeDigi) // the RX path expects Digi mode
			t.voice = false
		} else {
			_ = t.radio.Tone(0)
			if err := t.radio.RX(); err != nil {
				slog.Error("TX: RX command failed", "err", err)
			}
		}
		t.toneOn = false
		slog.Info("TX off", "reason", reason, "duration", t.now().Sub(t.onSince).Round(time.Millisecond),
			"key_downs", t.statKeyDowns, "tone_cmds", t.statTones, "max_level", math.Round(t.statMaxLevel*1000)/1000,
			"max_qmx_watts", t.statRawWatts)
		t.stopping, t.stopAttempts, t.stopNext, t.stopWarned = true, 0, t.now(), time.Time{}
	} else if was != txIdle {
		slog.Info("TX request ended", "reason", reason)
	}
}

// CWX bits in the low byte of each TX I word (HL2 gateware dsopenhpsdr1.sv and radio.sv):
// bit 0 keys the carrier, bit 3 is the CWX PTT that holds TX between elements. After the last
// of either, the gateware keeps TX up for its 500 ms hang.
const (
	cwxKey  = 1 << 0
	cwxPTT  = 1 << 3
	cwxHang = 500 * time.Millisecond
)

// cwxBits ORs the CWX bits of a frame's samples.
func cwxBits(f txFrame) int16 {
	var b int16
	for _, s := range f.iq {
		b |= s[0] & (cwxKey | cwxPTT)
	}
	return b
}

// cwxFrame keys the QMX from a CWX frame: a carrier exactly on the TX frequency, on while the
// key bit is set, like the HL2's own CWX output.
func (t *transmitter) cwxFrame(f txFrame, now time.Time) {
	bits := cwxBits(f)
	key := bits&cwxKey != 0
	if !t.cwxOn {
		if t.stopping || t.state != txIdle || !key {
			return
		}
		t.cwxOn, t.cwxLast, t.lastMox = true, now, now
		t.voice = false
		slog.Info("CWX keying from the host", "tx_freq", f.txFreq)
		t.keyDown(f.txFreq, 0, now) // dial = TX - 1500 Hz, TA 1500: the carrier on the TX frequency
		return
	}
	t.lastMox = now // frames keep coming, so the starvation watchdog stays quiet
	if bits != 0 {
		t.cwxLast = now
	}
	if !f.cwx || now.Sub(t.cwxLast) > cwxHang {
		t.cwxOn = false
		t.keyUp("CWX idle")
		return
	}
	if t.state == txOn {
		t.follow(f.txFreq, 0, key, now)
	}
}

// stopRetry is the interval between attempts to confirm (and re-send) the stop after key-up.
const stopRetry = 200 * time.Millisecond

// confirmStop checks with CAT TQ that the QMX left TX; until it does, it re-sends the stop
// on every attempt. Only then is the engine told that TX has ended.
func (t *transmitter) confirmStop(ctx context.Context, now time.Time) {
	if now.Before(t.stopNext) {
		return
	}
	t.stopNext = now.Add(stopRetry)
	if t.stopAttempts > 0 {
		if t.stopTone {
			_ = t.radio.Tone(0)
		}
		_ = t.radio.RX()
	}
	keyed, err := t.radio.Transmitting(ctx)
	if err == nil && !keyed {
		if t.stopAttempts > 0 {
			slog.Warn("the QMX confirmed receive after the stop was re-sent", "attempts", t.stopAttempts)
		}
		t.stopping = false
		if t.active != nil {
			t.active(false)
		}
		return
	}
	t.stopAttempts++
	if t.stopWarned.IsZero() || now.Sub(t.stopWarned) >= 5*time.Second {
		t.stopWarned = now
		slog.Error("the QMX may still be transmitting: its return to receive is not confirmed; re-sending TA0/RX",
			"tq_keyed", keyed, "err", err, "attempts", t.stopAttempts)
	}
}

// dcPole puts the SSB audio DC blocker's corner at about 20 Hz.
const dcPole = 1 - 2*math.Pi*20/48000

// amMaxCarrierHz: an AM carrier sits at 0 Hz baseband (the TX frequency).
const amMaxCarrierHz = 100

// FT8/JS8 span 44 Hz, FT4 63 Hz and WSPR 6 Hz; RTTY shifts by 170 Hz (Zeus sent 85 Hz on
// 2026-09-29) for bits of 22 ms. A CW edge can glitch the frequency estimate for a few ms, so
// the excursion must last.
const (
	fskShiftHz     = 75
	fskHoldSamples = 48000 * 15 / 1000 // 15 ms
)

// switchToSSB moves an over that went out as a tone to the QMX's SSB mode: AM (the QMX cannot
// transmit it; the voice goes out without the carrier) or wide FSK such as RTTY (CAT TA cannot
// follow its 22 ms bits cleanly). The over keeps its start time, so -maxtx still counts from
// the key-down.
func (t *transmitter) switchToSSB(txFreq uint32, now time.Time, usb bool) {
	_ = t.radio.Tone(0) // returns the QMX to receive
	t.toneOn = false
	onSince := t.onSince
	t.keyDownSSB(txFreq, now, usb)
	t.onSince = onSince
}

// maxPending bounds the audio kept while classifying; keyDownSSB uses the last FIFO target.
const maxPending = hpsdr.TXSampleRate / 2

// QMX MD values (kept local so tests need not import the qmx package).
const (
	qmxModeLSB  = 1
	qmxModeUSB  = 2
	qmxModeDigi = 6
)
