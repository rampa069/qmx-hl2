package engine

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/rampa/qmx-hl2/internal/dsp"
	"github.com/rampa/qmx-hl2/internal/hpsdr"
)

// TXRadio is the part of the QMX CAT client the transmitter needs.
type TXRadio interface {
	SetFreqA(hz uint32) error
	TX() error
	RX() error
	Tone(hz float64) error
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

	state     txState
	tr        *dsp.ToneTracker
	lastMox   time.Time
	onSince   time.Time
	dial      uint32
	toneOn    bool
	lastTone  float64
	lastSent  time.Time
	lastSWR   time.Time
	highSWR   int
	armedFreq uint32

	lastPower, lastSWRVal float64
}

func newTransmitter(cfg TXConfig, radio TXRadio, active func(bool)) *transmitter {
	return &transmitter{
		cfg:    cfg,
		radio:  radio,
		frames: make(chan txFrame, 256),
		now:    time.Now,
		active: active,
		tr:     dsp.NewToneTracker(hpsdr.TXSampleRate, cfg.Window),
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
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-t.frames:
			t.frame(ctx, f)
		case <-tick.C:
			t.tick(ctx)
		}
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
	switch t.state {
	case txInhibit:
		return
	case txIdle:
		t.state = txArming
		t.tr.Reset()
		t.armedFreq = f.txFreq
		slog.Info("TX requested by host", "tx_freq", f.txFreq)
	}
	for _, s := range f.iq {
		i, q := float64(s[0])/32768, float64(s[1])/32768
		if t.cfg.SwapIQ {
			i, q = q, i
		}
		t.tr.Add(complex(i, q))
	}
	if !t.tr.Ready() {
		return
	}
	signal := t.tr.Level() >= t.cfg.GateLevel
	freq := t.tr.Freq()
	switch t.state {
	case txArming:
		if !signal {
			return // MOX without audio yet (e.g. WSJT-X keys PTT before the tones)
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
	t.onSince = now
	t.highSWR = 0
	t.lastSWR = now
	t.toneOn = false
	if t.active != nil {
		t.active(true)
	}
	slog.Info("TX on", "dial", t.dial, "tone", float64(txFreq)+bb-float64(t.dial))
	t.follow(txFreq, bb, true, now)
}

func (t *transmitter) follow(txFreq uint32, bb float64, signal bool, now time.Time) {
	if !signal {
		if t.toneOn {
			_ = t.radio.Tone(0) // shaped key-up; the QMX stays in TX
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
	// Keep the QMX CAT watchdog fed even during long silent gaps.
	if !t.toneOn && now.Sub(t.lastSent) >= 250*time.Millisecond {
		_ = t.radio.Tone(0)
		t.lastSent = now
	}
	if t.toneOn && now.Sub(t.onSince) >= t.cfg.SWRGrace && now.Sub(t.lastSWR) >= 500*time.Millisecond {
		t.lastSWR = now
		swr, err := t.radio.SWR(ctx)
		pwr, _ := t.radio.PowerOut(ctx)
		t.lastPower, t.lastSWRVal = pwr, swr
		slog.Debug("TX meter", "power_w", pwr, "swr", swr, "tone", t.lastTone)
		if err != nil || swr == 0 {
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
}

// abort keys up and ignores MOX until the host releases it.
func (t *transmitter) abort(reason string) {
	t.keyUp(reason)
	t.state = txInhibit
}

func (t *transmitter) keyUp(reason string) {
	was := t.state
	t.state = txIdle
	if was == txOn {
		_ = t.radio.Tone(0)
		if err := t.radio.RX(); err != nil {
			slog.Error("TX: RX command failed", "err", err)
		}
		t.toneOn = false
		slog.Info("TX off", "reason", reason, "duration", t.now().Sub(t.onSince).Round(time.Millisecond))
		if t.active != nil {
			t.active(false)
		}
	} else if was != txIdle {
		slog.Info("TX request ended", "reason", reason)
	}
}
