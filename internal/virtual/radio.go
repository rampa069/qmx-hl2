// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// Package virtual is a software stand-in for the QMX, used by -parrot. It answers the CAT
// commands the daemon sends, delivers noise as the QMX's I/Q, and records what the daemon made
// it transmit: a carrier at dial + TA in Digi mode, or the USB audio as a single sideband in
// USB/LSB mode. After a delay each recording is replayed into the I/Q at its RF frequency, so a
// client hears its own transmission through the whole daemon chain (classifier, tone tracking,
// SSB FIFO) without a radio or RF.
//
// Everything is paced by the host clock. The QMX's own quirks (its fast capture clock while
// playing, ignoring TA during USB audio) are not modelled.
package virtual

import (
	"fmt"
	"log/slog"
	"math"
	"math/cmplx"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rampa069/qmx-hl2/internal/audio"
)

const (
	ifOffset   = 12000 // the QMX's IQ centre is this far below the dial in Digi mode
	noiseSigma = 1e-4  // I/Q noise per component (about -80 dBFS)
	signalAmp  = 1e-2  // replayed signal amplitude (about -40 dBFS)
	idleEnd    = 700 * time.Millisecond
	maxRecord  = 5 * time.Minute
	minGap     = 300 * time.Millisecond // replay starts at least this long after the over ends
	txWatts    = 3.0
	txSWR      = 1.1
	modeLSB    = 1
	modeUSB    = 2
	modeCW     = 3
	modeDigi   = 6
	modeCWR    = 7
	cwOffset   = 700 // Hz, reported by MMCW|CW offset;
)

// Radio is a virtual QMX. Use CAT, Capture and Playback to plug it into the daemon.
type Radio struct {
	rate   int
	frames int
	delay  time.Duration
	now    func() time.Time

	mu    sync.Mutex
	fa    int64
	md    int
	q9    bool
	tx    bool
	ta    float64
	other map[string]string // CAT settings kept but not modelled (QB, QC, SS...)
	out   []byte
	outCh chan struct{}

	rec      *recording
	idleFrom time.Time // when the current recording stopped transmitting
	phase    float64   // tone synthesis
	queue    []*recording
	rxPhase  float64 // replay mixer
	rng      *rand.Rand
}

type recording struct {
	start time.Time
	ref   int64 // RF frequency of 0 Hz in iq
	ssb   bool
	lsb   bool
	audio []float32   // SSB: the USB audio as played
	iq    []complex64 // tone: synthesised; SSB: made from audio when the over ends
	at    time.Time   // replay start
	pos   int
}

// New returns a virtual QMX on 14.074 MHz Digi that replays each transmission after delay.
func New(rate, frames int, delay time.Duration) *Radio {
	return &Radio{
		rate: rate, frames: frames, delay: delay, now: time.Now,
		fa: 14074000, md: modeDigi,
		other: map[string]string{"QB": "QB0;", "QC": "QC0;", "SS": "SS0;"},
		outCh: make(chan struct{}, 1),
		rng:   rand.New(rand.NewPCG(1, 2)),
	}
}

// ---- CAT -------------------------------------------------------------------

// CAT returns the virtual serial port (qmx.ReadWriter).
func (r *Radio) CAT() *CATPort { return &CATPort{r} }

// CATPort is the virtual QMX's serial port.
type CATPort struct{ r *Radio }

// Write executes CAT commands.
func (p *CATPort) Write(b []byte) (int, error) {
	for _, cmd := range strings.SplitAfter(string(b), ";") {
		cmd = strings.TrimSpace(cmd)
		if len(cmd) >= 3 && strings.HasSuffix(cmd, ";") {
			p.r.command(cmd)
		}
	}
	return len(b), nil
}

// Read returns replies, waiting briefly for one so the CAT client's loop can see its context
// end.
func (p *CATPort) Read(b []byte) (int, error) {
	r := p.r
	r.mu.Lock()
	if len(r.out) == 0 {
		r.mu.Unlock()
		select {
		case <-r.outCh:
		case <-time.After(20 * time.Millisecond):
		}
		r.mu.Lock()
	}
	n := copy(b, r.out)
	r.out = r.out[n:]
	r.mu.Unlock()
	return n, nil
}

func (r *Radio) reply(s string) {
	r.out = append(r.out, s...)
	select {
	case r.outCh <- struct{}{}:
	default:
	}
}

func (r *Radio) command(cmd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.advance(now)
	key, val := cmd[:2], strings.TrimSuffix(cmd[2:], ";")
	if key == "MM" && !strings.Contains(val, "=") { // menu read: only the CW offset is asked
		r.reply("MM700;")
		return
	}
	if val == "" {
		r.reply(r.query(key))
		return
	}
	switch key {
	case "FA":
		if v, err := strconv.ParseInt(val, 10, 64); err == nil {
			r.fa = v
		}
	case "MD":
		if v, err := strconv.Atoi(val); err == nil {
			r.md = v
		}
	case "Q9":
		r.q9 = val == "1"
	case "TA":
		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return
		}
		if v < 10 { // TA0 returns the QMX to receive
			r.keyUp(now)
			return
		}
		r.ta = v
	default:
		r.other[key] = cmd
	}
}

// "TX;" and "RX;" have no value but are commands, not queries.
func (r *Radio) query(key string) string {
	now := r.now()
	switch key {
	case "TX":
		r.keyDown(now)
		return ""
	case "RX":
		r.keyUp(now)
		return ""
	case "FA":
		return fmt.Sprintf("FA%011d;", r.fa)
	case "MD":
		return fmt.Sprintf("MD%d;", r.md)
	case "Q9":
		return fmt.Sprintf("Q9%d;", b2i(r.q9))
	case "TQ":
		return fmt.Sprintf("TQ%d;", b2i(r.tx))
	case "PC":
		if r.tx {
			return fmt.Sprintf("PC%03d;", int(txWatts*10))
		}
		return "PC000;"
	case "SW":
		if r.tx {
			return fmt.Sprintf("SW%03d;", int(txSWR*100))
		}
		return "SW000;"
	case "VN":
		return "VN1_03_002;"
	}
	if v, ok := r.other[key]; ok {
		return v
	}
	return key + "0;"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- transmit and record ---------------------------------------------------

func (r *Radio) keyDown(now time.Time) {
	if r.tx {
		return
	}
	r.tx = true
	ssb := r.md == modeUSB || r.md == modeLSB
	if r.rec != nil && r.rec.ssb == ssb {
		return // a CW element after a short key-up: same recording
	}
	r.finish(now)
	r.rec = &recording{start: now, ref: r.fa, ssb: ssb, lsb: r.md == modeLSB}
	r.phase = 0
	slog.Info("parrot: recording", "rf", r.fa, "ssb", ssb, "lsb", r.md == modeLSB)
}

func (r *Radio) keyUp(now time.Time) {
	if !r.tx {
		return
	}
	r.tx = false
	r.ta = 0
	r.idleFrom = now
}

// advance brings the recording up to now: tone samples while keyed in Digi mode, silence
// while not keyed. It ends the recording after idleEnd without transmitting.
func (r *Radio) advance(now time.Time) {
	rec := r.rec
	if rec == nil {
		return
	}
	if !r.tx && now.Sub(r.idleFrom) >= idleEnd {
		r.finish(now)
		return
	}
	target := int(now.Sub(rec.start).Seconds() * float64(r.rate))
	if target > int(maxRecord.Seconds())*r.rate {
		r.finish(now)
		return
	}
	if rec.ssb {
		// The audio arrives through Playback while keyed; pad the gaps with silence.
		if !r.tx {
			for len(rec.audio) < target {
				rec.audio = append(rec.audio, 0)
			}
		}
		return
	}
	for len(rec.iq) < target {
		if r.tx && r.ta >= 10 {
			r.phase += 2 * math.Pi * (float64(r.fa-rec.ref) + r.ta) / float64(r.rate)
			r.phase = math.Remainder(r.phase, 2*math.Pi)
			rec.iq = append(rec.iq, complex64(cmplx.Exp(complex(0, r.phase))))
		} else {
			rec.iq = append(rec.iq, 0)
		}
	}
}

// finish closes the current recording and queues it for replay. An SSB recording is turned
// into its sideband outside the lock: a two-minute SSTV frame takes about half a second, which
// would otherwise stall the I/Q stream.
func (r *Radio) finish(now time.Time) {
	rec := r.rec
	if rec == nil {
		return
	}
	r.rec = nil
	if !rec.ssb {
		r.enqueue(rec, now)
		return
	}
	go func() {
		rec.iq = analytic(rec.audio, rec.lsb)
		rec.audio = nil
		r.mu.Lock()
		r.enqueue(rec, r.now())
		r.mu.Unlock()
	}()
}

// enqueue schedules a finished recording.
func (r *Radio) enqueue(rec *recording, now time.Time) {
	// Drop the silence of the idle wait (and of a short key-up at the end).
	end := len(rec.iq)
	for end > 0 && rec.iq[end-1] == 0 {
		end--
	}
	rec.iq = rec.iq[:end]
	if len(rec.iq) == 0 {
		return
	}
	rec.at = rec.start.Add(r.delay)
	if earliest := now.Add(minGap); rec.at.Before(earliest) {
		rec.at = earliest
	}
	if n := len(r.queue); n > 0 {
		last := r.queue[n-1]
		if end := last.at.Add(time.Duration(len(last.iq)) * time.Second / time.Duration(r.rate)); rec.at.Before(end) {
			rec.at = end // replays do not overlap
		}
	}
	r.queue = append(r.queue, rec)
	slog.Info("parrot: replay scheduled", "seconds", math.Round(float64(len(rec.iq))/float64(r.rate)*10)/10,
		"in", rec.at.Sub(now).Round(100*time.Millisecond))
}

// analytic turns real audio into its single-sideband complex baseband: positive frequencies
// for USB, negative for LSB.
func analytic(x []float32, lsb bool) []complex64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	buf := make([]complex128, n)
	for i, v := range x {
		buf[i] = complex(float64(v), 0)
	}
	fft(buf, false)
	for k := 1; k < n/2; k++ {
		buf[k] *= 2
	}
	for k := n/2 + 1; k < n; k++ {
		buf[k] = 0
	}
	fft(buf, true)
	out := make([]complex64, len(x))
	for i := range out {
		v := buf[i]
		if lsb {
			v = cmplx.Conj(v)
		}
		out[i] = complex64(v)
	}
	return out
}

// fft is an in-place radix-2 FFT; len(a) must be a power of two. inverse scales by 1/n.
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	sign := -1.0
	if inverse {
		sign = 1
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, sign*2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := a[start+k], a[start+k+size/2]*wk
				a[start+k], a[start+k+size/2] = u+v, u-v
				wk *= w
			}
		}
	}
	if inverse {
		for i := range a {
			a[i] /= complex(float64(n), 0)
		}
	}
}

// ---- audio streams ---------------------------------------------------------

// Capture returns the virtual QMX I/Q stream.
func (r *Radio) Capture() audio.CaptureStream { return &capture{r: r} }

type capture struct {
	r      *Radio
	t0     time.Time
	frames int64
}

func (c *capture) FramesPerBuffer() int { return c.r.frames }
func (c *capture) Close() error         { return nil }

// Read delivers one buffer of noise plus any replay due, paced by the host clock.
func (c *capture) Read(dst []float32) (int, error) {
	r := c.r
	if c.t0.IsZero() {
		c.t0 = r.now()
	}
	c.frames += int64(r.frames)
	time.Sleep(time.Until(c.t0.Add(time.Duration(c.frames) * time.Second / time.Duration(r.rate))))

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.advance(now)
	lo := float64(r.fa - ifOffset)
	switch r.md { // CW modes move the LO by the CW offset, as the real QMX does
	case modeCW:
		lo -= cwOffset
	case modeCWR:
		lo += cwOffset
	}
	for i := 0; i < r.frames; i++ {
		x := complex(noiseSigma*r.rng.NormFloat64(), noiseSigma*r.rng.NormFloat64())
		// A replay waits while the client transmits: the daemon mutes receive then.
		if len(r.queue) > 0 && !r.tx {
			rec := r.queue[0]
			t := now.Add(time.Duration(i) * time.Second / time.Duration(r.rate))
			if !t.Before(rec.at) {
				r.rxPhase += 2 * math.Pi * (float64(rec.ref) - lo) / float64(r.rate)
				r.rxPhase = math.Remainder(r.rxPhase, 2*math.Pi)
				x += signalAmp * complex128(rec.iq[rec.pos]) * cmplx.Exp(complex(0, r.rxPhase))
				rec.pos++
				if rec.pos == len(rec.iq) {
					r.queue = r.queue[1:]
					slog.Info("parrot: replay done")
				}
			}
		}
		dst[2*i], dst[2*i+1] = float32(real(x)), float32(imag(x))
	}
	return r.frames, nil
}

// Playback returns the virtual QMX USB audio input.
func (r *Radio) Playback() audio.PlaybackStream { return &playback{r: r} }

type playback struct {
	r      *Radio
	t0     time.Time
	frames int64
}

// Write takes stereo audio at the host clock rate; while keyed in USB/LSB it is recorded.
func (p *playback) Write(src []float32) error {
	r := p.r
	if p.t0.IsZero() {
		p.t0 = r.now()
	}
	n := len(src) / audio.Channels
	p.frames += int64(n)
	time.Sleep(time.Until(p.t0.Add(time.Duration(p.frames) * time.Second / time.Duration(r.rate))))

	r.mu.Lock()
	defer r.mu.Unlock()
	if rec := r.rec; rec != nil && rec.ssb && r.tx {
		for i := 0; i < n; i++ {
			rec.audio = append(rec.audio, src[i*audio.Channels])
		}
	}
	return nil
}

func (p *playback) Pause() error  { p.t0, p.frames = time.Time{}, 0; return nil }
func (p *playback) Resume() error { return nil }
func (p *playback) Close() error  { return nil }
