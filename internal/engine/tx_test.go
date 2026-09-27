package engine

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/rampa/qmx-hl2/internal/hpsdr"
)

type fakeTXRadio struct {
	log []string
	swr float64
}

func (r *fakeTXRadio) SetFreqA(hz uint32) error {
	r.log = append(r.log, fmt.Sprintf("FA%d", hz))
	return nil
}
func (r *fakeTXRadio) TX() error { r.log = append(r.log, "TX"); return nil }
func (r *fakeTXRadio) RX() error { r.log = append(r.log, "RX"); return nil }
func (r *fakeTXRadio) Tone(hz float64) error {
	r.log = append(r.log, fmt.Sprintf("TA%.2f", hz))
	return nil
}
func (r *fakeTXRadio) SetMode(m int) error {
	r.log = append(r.log, fmt.Sprintf("MD%d", m))
	return nil
}
func (r *fakeTXRadio) SWR(context.Context) (float64, error)      { return r.swr, nil }
func (r *fakeTXRadio) PowerOut(context.Context) (float64, error) { return 3.8, nil }

func (r *fakeTXRadio) count(prefix string) int {
	n := 0
	for _, l := range r.log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// harness drives a transmitter synchronously with a fake clock.
type harness struct {
	t         *testing.T
	tx        *transmitter
	radio     *fakeTXRadio
	clock     time.Time
	phase     float64
	active    []bool
	lastMeter time.Time
	meters    [][2]float64
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, radio: &fakeTXRadio{swr: 1.2}, clock: time.Unix(1000, 0)}
	cfg := DefaultTXConfig()
	cfg.Enabled = true
	cfg.SwapIQ = false // the harness generates I=cos, Q=sin for positive frequencies
	h.tx = newTransmitter(cfg, h.radio, func(on bool) { h.active = append(h.active, on) })
	h.tx.meter = func(w, swr float64) { h.meters = append(h.meters, [2]float64{w, swr}) }
	h.tx.now = func() time.Time { return h.clock }
	return h
}

// send feeds frames of a tone at bb Hz (amplitude amp) for d, 63 samples per frame, ticking
// the controller as time passes.
func (h *harness) send(mox bool, txFreq uint32, bb, amp float64, d time.Duration) {
	frames := int(d.Seconds() * 48000 / hpsdr.SamplesPerEP2Frm)
	for i := 0; i < frames; i++ {
		var f txFrame
		f.mox, f.txFreq = mox, txFreq
		for k := range f.iq {
			h.phase += 2 * math.Pi * bb / 48000
			f.iq[k] = [2]int16{int16(amp * 32767 * math.Cos(h.phase)), int16(amp * 32767 * math.Sin(h.phase))}
		}
		h.tx.frame(context.Background(), f)
		h.clock = h.clock.Add(time.Second * hpsdr.SamplesPerEP2Frm / 48000)
		h.tx.tick(context.Background())
		// Stand in for meterLoop: a reading every 300 ms while keyed.
		if h.tx.keyed.Load() && h.clock.Sub(h.lastMeter) >= 300*time.Millisecond {
			h.lastMeter = h.clock
			h.tx.onMeter(meterReading{watts: 3.8, swr: h.radio.swr, ok: true})
		}
	}
}

func (h *harness) idle(d time.Duration) {
	for end := h.clock.Add(d); h.clock.Before(end); h.clock = h.clock.Add(10 * time.Millisecond) {
		h.tx.tick(context.Background())
	}
}

func lastTone(log []string) float64 {
	for i := len(log) - 1; i >= 0; i-- {
		var v float64
		if _, err := fmt.Sscanf(log[i], "TA%f", &v); err == nil && v != 0 {
			return v
		}
	}
	return 0
}

func TestTXFT8LikeUSB(t *testing.T) {
	h := newHarness(t)
	const txFreq = 21074000
	// WSJT-X style: PTT first with silence, then tones.
	h.send(true, txFreq, 0, 0, 100*time.Millisecond)
	if h.radio.count("TX") != 0 {
		t.Fatal("keyed without audio")
	}
	h.send(true, txFreq, 1500, 0.5, 200*time.Millisecond)
	if h.radio.log[0] != "FA21074000" || h.radio.log[1] != "TX" {
		t.Fatalf("key-down sequence %v", h.radio.log[:2])
	}
	if f := lastTone(h.radio.log); math.Abs(f-1500) > 0.5 {
		t.Fatalf("tone %g", f)
	}
	h.send(true, txFreq, 1518.75, 0.5, 200*time.Millisecond)
	if f := lastTone(h.radio.log); math.Abs(f-1518.75) > 0.05 {
		t.Fatalf("tone after step %g", f)
	}
	h.send(false, txFreq, 0, 0, 10*time.Millisecond)
	if l := h.radio.log; l[len(l)-1] != "RX" || l[len(l)-2] != "TA0.00" {
		t.Fatalf("key-up %v", l[len(l)-3:])
	}
	if len(h.active) != 2 || !h.active[0] || h.active[1] {
		t.Fatalf("active callbacks %v", h.active)
	}
}

func TestTXLSBAndCarrierPlacement(t *testing.T) {
	h := newHarness(t)
	// LSB-style tone 1000 Hz below the carrier: dial = 7074000 - 1000 - 1500.
	h.send(true, 7074000, -1000, 0.3, 50*time.Millisecond)
	if h.radio.log[0] != "FA7071500" {
		t.Fatalf("dial %v", h.radio.log[0])
	}
	if f := lastTone(h.radio.log); math.Abs(f-1500) > 0.5 {
		t.Fatalf("tone %g", f)
	}
	h.send(false, 7074000, 0, 0, 5*time.Millisecond)

	// CW as a DC carrier: dial 1500 below the carrier.
	h.radio.log = nil
	h.send(true, 7030000, 0, 0.8, 50*time.Millisecond)
	if h.radio.log[0] != "FA7028500" {
		t.Fatalf("CW dial %v", h.radio.log[0])
	}
}

func TestTXGapKeysUpToneButStaysInTX(t *testing.T) {
	h := newHarness(t)
	h.send(true, 14074000, 1200, 0.5, 100*time.Millisecond)
	h.send(true, 14074000, 0, 0, 100*time.Millisecond)
	if h.radio.count("RX") != 0 {
		t.Fatal("left TX during a gap")
	}
	if l := h.radio.log; !strings.HasPrefix(l[len(l)-1], "TA0") {
		t.Fatalf("gap did not key up the tone: %v", l[len(l)-3:])
	}
	// The dial was placed for the first tone (1200 Hz -> 1500), so +100 Hz becomes 1600.
	h.send(true, 14074000, 1300, 0.5, 50*time.Millisecond)
	if f := lastTone(h.radio.log); math.Abs(f-1600) > 0.5 {
		t.Fatalf("tone %g", f)
	}
	// TA0 drops the QMX to RX, so the next element re-sends TX.
	if h.radio.count("TX") != 2 {
		t.Fatalf("TX sent %d times, want 2", h.radio.count("TX"))
	}
}

func TestTXStarvationAndMaxTime(t *testing.T) {
	h := newHarness(t)
	h.send(true, 14074000, 1500, 0.5, 50*time.Millisecond)
	h.idle(200 * time.Millisecond) // host stops sending
	if h.radio.count("RX") != 1 {
		t.Fatalf("not unkeyed on starvation: %v", h.radio.log)
	}

	h = newHarness(t)
	h.tx.cfg.MaxTX = 500 * time.Millisecond
	h.send(true, 14074000, 1500, 0.5, time.Second)
	if h.radio.count("TX") != 1 || h.radio.count("RX") != 1 {
		t.Fatalf("max TX: %v", h.radio.log)
	}
	// Inhibited until MOX drops, then TX works again.
	h.send(false, 14074000, 0, 0, 5*time.Millisecond)
	h.send(true, 14074000, 1500, 0.5, 50*time.Millisecond)
	if h.radio.count("TX") != 2 {
		t.Fatal("did not re-arm after MOX release")
	}
}

func TestTXHighSWRAborts(t *testing.T) {
	h := newHarness(t)
	h.radio.swr = 4.5
	h.send(true, 14074000, 1500, 0.5, 400*time.Millisecond)
	if h.radio.count("RX") != 0 {
		t.Fatal("aborted inside the key-down grace period")
	}
	h.send(true, 14074000, 1500, 0.5, 1500*time.Millisecond)
	if h.radio.count("RX") != 1 || h.tx.state != txInhibit {
		t.Fatalf("no SWR abort: state %v log %v", h.tx.state, h.radio.log[len(h.radio.log)-3:])
	}
}

func TestTXToneOutOfRange(t *testing.T) {
	h := newHarness(t)
	h.send(true, 14074000, 1500, 0.5, 50*time.Millisecond)
	// A jump of 7 kHz puts the tone at 8.5 kHz, beyond what the QMX accepts.
	h.send(true, 14074000, 8500, 0.5, 50*time.Millisecond)
	if h.tx.state != txInhibit || h.radio.count("RX") != 1 {
		t.Fatalf("state %v log %v", h.tx.state, h.radio.log)
	}
}

func TestTXMetersReported(t *testing.T) {
	h := newHarness(t)
	h.send(true, 14074000, 1500, 0.5, time.Second)
	if len(h.meters) < 2 || h.meters[len(h.meters)-1] != [2]float64{3.8, 1.2} {
		t.Fatalf("meters %v", h.meters)
	}
	// During a silent gap the client sees no power.
	h.send(true, 14074000, 0, 0, 400*time.Millisecond)
	if last := h.meters[len(h.meters)-1]; last != [2]float64{0, 0} {
		t.Fatalf("gap meter %v", last)
	}
}

func TestTXAutoVoiceUsesSSB(t *testing.T) {
	h := newHarness(t)
	h.tx.ssb = newSSBAudio(48000)
	// Two-tone "voice" in USB: 700 + 1900 Hz.
	send2 := func(d time.Duration, mox bool) {
		frames := int(d.Seconds() * 48000 / hpsdr.SamplesPerEP2Frm)
		for i := 0; i < frames; i++ {
			var f txFrame
			f.mox, f.txFreq = mox, 21300000
			for k := range f.iq {
				h.phase += 1
				tt := h.phase / 48000
				i1 := 0.4*math.Cos(2*math.Pi*700*tt) + 0.4*math.Cos(2*math.Pi*1900*tt)
				q1 := 0.4*math.Sin(2*math.Pi*700*tt) + 0.4*math.Sin(2*math.Pi*1900*tt)
				f.iq[k] = [2]int16{int16(i1 * 32767), int16(q1 * 32767)}
			}
			h.tx.frame(context.Background(), f)
			h.clock = h.clock.Add(time.Second * hpsdr.SamplesPerEP2Frm / 48000)
			h.tx.tick(context.Background())
		}
	}
	send2(300*time.Millisecond, true)
	want := []string{"MD2", "FA21300000", "TX"}
	for i, w := range want {
		if i >= len(h.radio.log) || h.radio.log[i] != w {
			t.Fatalf("SSB key-down %v, want prefix %v", h.radio.log, want)
		}
	}
	if h.radio.count("TA") != 0 {
		t.Fatal("SSB path sent TA")
	}
	h.tx.ssb.mu.Lock()
	buffered := len(h.tx.ssb.fifo)
	h.tx.ssb.mu.Unlock()
	if buffered < 48000*250/1000 {
		t.Fatalf("only %d audio samples queued", buffered)
	}
	send2(5*time.Millisecond, false)
	l := h.radio.log
	if l[len(l)-2] != "RX" || l[len(l)-1] != "MD6" {
		t.Fatalf("SSB key-up %v", l[len(l)-3:])
	}
	if !h.active[0] || h.active[1] {
		t.Fatalf("active %v", h.active)
	}
}

func TestTXAutoToneStaysTA(t *testing.T) {
	h := newHarness(t)
	h.tx.ssb = newSSBAudio(48000)
	h.send(true, 21074000, 1500, 0.5, 200*time.Millisecond)
	if h.radio.count("MD") != 0 || h.radio.count("TA1500") == 0 {
		t.Fatalf("FT8 tone in auto mode: %v", h.radio.log)
	}
}

func TestSSBAudioStartsAtTarget(t *testing.T) {
	a := newSSBAudio(48000)
	out := make([]float32, 240)
	a.Push(make([]float32, 1000))
	a.fill(out)
	if a.playing {
		t.Fatal("started before reaching target")
	}
	a.Push(make([]float32, 2000))
	a.fill(out)
	if !a.playing {
		t.Fatal("did not start at target")
	}
}

// The producer runs 165 ppm fast with bursty delivery (like the bench QMX clocks and
// network jitter); the controller must hold the fill near target without underruns.
func TestSSBAudioDriftServo(t *testing.T) {
	a := newSSBAudio(48000)
	out := make([]float32, 240)
	const inRate = 48000 * (1 + 165e-6)
	var produced, phase float64
	chunk := make([]float32, 0, 1024)
	for step := 0; step < 200*120; step++ { // 120 s of 5 ms output chunks
		// Producer: deliver what is due, in 126-sample packets, bursty every 20 ms.
		if step%4 == 0 {
			due := float64(step+4)*240*inRate/48000 - produced
			chunk = chunk[:0]
			for ; due >= 126; due -= 126 {
				for k := 0; k < 126; k++ {
					phase += 2 * math.Pi * 1000 / inRate
					chunk = append(chunk, float32(0.5*math.Sin(phase)))
				}
				produced += 126
			}
			a.Push(chunk)
		}
		a.fill(out)
	}
	u, ppm := a.Stats()
	if u != 0 {
		t.Errorf("underruns = %d", u)
	}
	if math.Abs(ppm-165) > 40 {
		t.Errorf("servo ratio %+.0f ppm, want about +165", ppm)
	}
	a.mu.Lock()
	fill := len(a.fifo)
	a.mu.Unlock()
	if d := fill - a.target; d < -960 || d > 960 {
		t.Errorf("fill %d samples, target %d", fill, a.target)
	}
}

func TestSSBAudioResamplerIsClean(t *testing.T) {
	a := newSSBAudio(48000)
	x := make([]float32, 48000)
	for i := range x {
		x[i] = float32(0.5 * math.Sin(2*math.Pi*1000*float64(i)/48000))
	}
	a.Push(x)
	out := make([]float32, 240)
	var y []float32
	for i := 0; i < 150; i++ {
		a.fill(out)
		y = append(y, out...)
	}
	// Fit the output to a 1 kHz sine (the ratio is within 1000 ppm of 1) and check the
	// residual over a short stretch.
	var errPow, sigPow float64
	for i := 1000; i < 2000; i++ {
		// Local reference: the ideal sine through neighbouring output samples.
		ref := (y[i-1] + y[i+1]) / float32(2*math.Cos(2*math.Pi*1000/48000))
		d := float64(y[i] - ref)
		errPow += d * d
		sigPow += float64(y[i]) * float64(y[i])
	}
	if snr := 10 * math.Log10(sigPow/errPow); snr < 60 {
		t.Errorf("resampled sine SNR %.1f dB, want > 60", snr)
	}
}

func TestTXSSBStartsAtTargetAfterLongSilence(t *testing.T) {
	h := newHarness(t)
	h.tx.ssb = newSSBAudio(48000)
	h.send(true, 14230000, 0, 0, 1500*time.Millisecond) // MOX with silence
	// Voice-like two-tone.
	frames := int(0.2 * 48000 / hpsdr.SamplesPerEP2Frm)
	for i := 0; i < frames; i++ {
		var f txFrame
		f.mox, f.txFreq = true, 14230000
		for k := range f.iq {
			h.phase += 1
			tt := h.phase / 48000
			i1 := 0.4*math.Cos(2*math.Pi*700*tt) + 0.4*math.Cos(2*math.Pi*1900*tt)
			q1 := 0.4*math.Sin(2*math.Pi*700*tt) + 0.4*math.Sin(2*math.Pi*1900*tt)
			f.iq[k] = [2]int16{int16(i1 * 32767), int16(q1 * 32767)}
		}
		h.tx.frame(context.Background(), f)
	}
	if !h.tx.voice {
		t.Fatal("not in SSB")
	}
	h.tx.ssb.mu.Lock()
	fill, disc := len(h.tx.ssb.fifo), h.tx.ssb.discarded
	h.tx.ssb.mu.Unlock()
	if disc != 0 || fill > h.tx.ssb.target+48000/5 {
		t.Fatalf("FIFO fill %d (target %d), discarded %d", fill, h.tx.ssb.target, disc)
	}
}
