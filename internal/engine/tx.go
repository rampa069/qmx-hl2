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

	state     txState
	tr        *dsp.ToneTracker
	lastMox   time.Time
	onSince   time.Time
	dial      uint32
	toneOn    bool
	lastTone  float64
	lastSent  time.Time
	highSWR   int
	armedFreq uint32

	lastPower, lastSWRVal float64
	// per-transmission statistics, logged at key-up
	statKeyDowns, statTones int
	statMaxLevel            float64
	statRawWatts            float64
	keyed                   atomic.Bool // read by meterLoop

	cls     *classifier
	pending []float32 // SSB audio captured while classifying
	voice   bool      // current over uses the SSB path
	ssb     *ssbAudio // nil when no playback stream (tone only)
	scratch []float32
	dump    io.Writer // debug: raw MOX-frame TX IQ (int16 LE pairs), if set
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
	defer t.keyUp("shutdown")
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
}

// meterLoop polls power and SWR every 300 ms while the QMX is keyed.
func (t *transmitter) meterLoop(ctx context.Context, out chan<- meterReading) {
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
		select {
		case out <- meterReading{watts: watts, swr: swr, ok: err1 == nil && err2 == nil}:
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
	if !r.ok || swr == 0 || !t.toneOn || t.now().Sub(t.onSince) < t.cfg.SWRGrace {
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
	if !f.mox {
		if t.state != txIdle {
			t.keyUp("MOX released")
		}
		return
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
		// The real part of the analytic signal is the audio; the QMX's SSB modulator picks
		// the sideband, so the same audio serves USB and LSB.
		t.scratch = append(t.scratch, float32(clamp1(i*t.cfg.SSBGain)))
	}
	if t.state == txArming && t.ssb != nil {
		t.pending = append(t.pending, t.scratch...)
	}
	if t.state == txOn && t.voice {
		t.ssb.Push(t.scratch)
		return
	}
	if !t.tr.Ready() {
		return
	}
	if l := t.tr.Level(); l > t.statMaxLevel {
		t.statMaxLevel = l
	}
	signal := t.tr.Level() >= t.cfg.GateLevel
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
			if len(t.pending) > hpsdr.TXSampleRate/4 { // should not happen: decide() settles by 150 ms
				kind = txVoice
			} else {
				return
			}
		}
		if kind == txVoice && t.ssb != nil {
			t.keyDownSSB(f.txFreq, now)
			return
		}
		t.keyDown(f.txFreq, freq, now)
	case txOn:
		t.follow(f.txFreq, freq, signal, now)
	}
}

func (t *transmitter) keyDown(txFreq uint32, bb float64, now time.Time) {
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
	t.highSWR = 0
	t.toneOn = false
	if t.active != nil {
		t.active(true)
	}
	slog.Info("TX on", "dial", t.dial, "tone", float64(txFreq)+bb-float64(t.dial))
	t.follow(txFreq, bb, true, now)
}

// keyDownSSB transmits the client's audio through the QMX's SSB modulator.
func (t *transmitter) keyDownSSB(txFreq uint32, now time.Time) {
	usb := t.cls.upperSideband()
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
	// that the drift servo (max 1000 ppm) could not drain within an over (2026-09-27).
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
	t.highSWR = 0
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
	if t.state == txIdle {
		return
	}
	now := t.now()
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
	was := t.state
	t.state = txIdle
	t.keyed.Store(false)
	if was == txOn {
		if t.voice {
			u, ppm := t.ssb.Stats()
			in, out, disc := t.ssb.OverStats()
			slog.Info("SSB audio", "underruns", u, "drift_ppm", math.Round(ppm),
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
		if t.active != nil {
			t.active(false)
		}
	} else if was != txIdle {
		slog.Info("TX request ended", "reason", reason)
	}
}

// QMX MD values (kept local so tests need not import the qmx package).
const (
	qmxModeLSB  = 1
	qmxModeUSB  = 2
	qmxModeDigi = 6
)
