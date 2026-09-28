// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package engine

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rampa069/qmx-hl2/internal/audio"
)

// ssbAudio plays SSB transmit audio to the QMX's USB sound card.
//
// While an over plays, the engine paces EP6 from Consumed (the frames the QMX has taken), and
// clients pace their TX IQ from EP6, so audio arrives at exactly the rate the QMX plays it.
// The FIFO then only absorbs network jitter and its latency stays constant. That matters for
// SSTV: every millisecond the latency moves during an over shifts the picture sideways. A
// steered resampler (the previous design) moved the latency by about 16 ms within one SSTV
// frame and slanted it (received on air 2026-09-28).
//
// The resampler is kept as a safety net for a client that does not pace TX from EP6: it
// corrects only when the smoothed fill leaves a dead band around the target, so a locked
// client is played at a ratio of exactly 1.
//
// Outside SSB overs the stream is paused: bench 2026-09-27 showed the QMX ignores CAT TA
// tones (0 W) while USB audio is streaming, even silence.
type ssbAudio struct {
	mu      sync.Mutex
	fifo    []float32
	playing bool
	target  int // samples buffered before playback starts, and the drift-control set point
	max     int // hard cap; older samples are discarded beyond it

	// per-over accounting, from the first played sample
	underruns, discarded int
	pushed, played       int
	playStart            time.Time
	ratioSum             float64 // sum of ratio over fill() calls while playing
	ratioN               int

	// lost counts the silence played since an underrun. The client still owes those samples
	// (EP6 kept running), and when they arrive late they are dropped, so the audio after the
	// gap stays where it would have been: an SSTV picture keeps its alignment and the fill
	// returns to its level instead of pushing the resampler out of its dead band.
	lost    int
	stalled bool // between an underrun and the late audio arriving (or giving up on it)

	pos   float64 // fractional read position in fifo
	ratio float64 // input samples consumed per output sample
	avg   float64 // smoothed fill level, samples

	active   bool
	resumed  time.Time    // when the playback stream last started
	consumed atomic.Int64 // frames written to the playback stream (the QMX DAC clock)
	wake     chan struct{}

	wavDir string // if set, each over is saved there as a WAV file
}

func newSSBAudio(sampleRate int) *ssbAudio {
	return &ssbAudio{
		// 150 ms: Zeus's SSTV encoder stalls for ~50 ms between the VIS header and the first
		// picture line, which underran a 60 ms FIFO and shifted the picture (2026-09-28).
		// Latency does not matter on TX.
		target: sampleRate * 150 / 1000,
		ratio:  1,
		max:    sampleRate / 2,
		wake:   make(chan struct{}, 1),
	}
}

// Push appends mono audio samples.
func (a *ssbAudio) Push(x []float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fifo = append(a.fifo, x...)
	if a.playing {
		a.pushed += len(x)
	}
	if over := len(a.fifo) - a.max; over > 0 {
		a.fifo = append(a.fifo[:0], a.fifo[over:]...)
		a.discarded += over
	}
}

// Reset discards buffered audio and returns to silence.
func (a *ssbAudio) Reset() {
	a.mu.Lock()
	a.fifo = a.fifo[:0]
	a.playing = false
	a.mu.Unlock()
}

// Start begins an SSB over: the playback stream is resumed.
func (a *ssbAudio) Start() {
	a.mu.Lock()
	a.active = true
	a.underruns, a.discarded, a.pushed, a.played = 0, 0, 0, 0
	a.ratioSum, a.ratioN = 0, 0
	a.playStart = time.Time{}
	a.ratio = 1
	a.avg = float64(a.target)
	a.lost, a.stalled = 0, false
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Stop ends the over: buffered audio is dropped and the stream is paused.
func (a *ssbAudio) Stop() {
	a.mu.Lock()
	a.active = false
	a.resumed = time.Time{}
	a.fifo = a.fifo[:0]
	a.playing = false
	a.mu.Unlock()
}

func (a *ssbAudio) isActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// Clock returns the frames the playback stream has taken so far, once it has been running
// long enough for the device buffer to be full (before that, writes return at once and the
// count runs ahead of the QMX). ok is false outside an over.
func (a *ssbAudio) Clock() (frames int64, ok bool) {
	a.mu.Lock()
	ok = a.active && !a.resumed.IsZero() && time.Since(a.resumed) >= clockSettle
	a.mu.Unlock()
	return a.consumed.Load(), ok
}

// OverStats summarises the current over from its first played sample: underruns, input and
// output rates, samples discarded because the FIFO overflowed, and the mean resampling
// offset in ppm (0 when the client is locked to the QMX clock).
func (a *ssbAudio) OverStats() (underruns int, inRate, outRate float64, discarded int, ppm float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ratioN > 0 {
		ppm = (a.ratioSum/float64(a.ratioN) - 1) * 1e6
	}
	if a.playStart.IsZero() {
		return a.underruns, 0, 0, a.discarded, ppm
	}
	el := time.Since(a.playStart).Seconds()
	if el <= 0 {
		return a.underruns, 0, 0, a.discarded, ppm
	}
	return a.underruns, float64(a.pushed) / el, float64(a.played) / el, a.discarded, ppm
}

// fill writes len(out) mono samples to out, resampling the FIFO by a ratio close to 1 that a
// PI controller steers to hold the fill level at target. Cubic (Catmull-Rom) interpolation
// keeps the tiny rate change inaudible, unlike dropping or repeating samples.
func (a *ssbAudio) fill(out []float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stalled && a.lost > maxLost { // the late audio is not coming: start afresh
		a.lost, a.stalled = 0, false
	}
	if a.stalled && len(a.fifo) >= a.lost+a.target/2 {
		// The late audio has arrived: drop what the silence replaced and resume, keeping one
		// sample of interpolator history before the next one due.
		d := max(0, a.lost-1)
		a.fifo = append(a.fifo[:0], a.fifo[d:]...)
		a.lost, a.stalled = 0, false
		a.playing = true
		a.pos = 1
	}
	if !a.playing && !a.stalled && len(a.fifo) >= a.target {
		a.playing = true
		a.pos = 1 // one sample of history for the interpolator
		if a.playStart.IsZero() {
			a.playStart = time.Now()
		}
	}
	if !a.playing {
		clear(out)
		if a.stalled {
			a.lost += len(out)
		}
		return
	}
	// Dead-band controller on the smoothed fill: inside the band the ratio is exactly 1.
	a.avg += (float64(len(a.fifo)) - a.avg) * avgAlpha
	e := (a.avg - float64(a.target)) / float64(a.target)
	switch {
	case e > deadBand:
		a.ratio = 1 + math.Min(maxPPM, kp*(e-deadBand))
	case e < -deadBand:
		a.ratio = 1 - math.Min(maxPPM, kp*(-e-deadBand))
	default:
		a.ratio = 1
	}
	a.ratioSum += a.ratio
	a.ratioN++

	for n := range out {
		k := int(a.pos)
		if k+2 >= len(a.fifo) {
			clear(out[n:])
			a.playing = false
			a.underruns++
			a.stalled = true
			// Silence replaces the rest of this chunk; the few samples left unplayed in the
			// FIFO are dropped now and count towards what the late audio must skip.
			a.lost += len(out) - n - (len(a.fifo) - k)
			a.fifo = a.fifo[:0]
			return
		}
		f := float32(a.pos - float64(k))
		y0, y1, y2, y3 := a.fifo[k-1], a.fifo[k], a.fifo[k+1], a.fifo[k+2]
		out[n] = y1 + 0.5*f*(y2-y0+f*(2*y0-5*y1+4*y2-y3+f*(3*(y1-y2)+y3-y0)))
		a.played++
		a.pos += a.ratio
	}
	// Drop consumed samples, keeping one for history.
	if k := int(a.pos) - 1; k > 0 {
		a.fifo = append(a.fifo[:0], a.fifo[k:]...)
		a.pos -= float64(k)
	}
}

// Controller tuning. A client locked to Consumed keeps the fill within the dead band
// (jitter of a few packets); one on its own clock (bench drift ~165 ppm) settles just
// outside it.
const (
	deadBand    = 0.5     // ±75 ms around the 150 ms target
	maxPPM      = 1000e-6 // largest ratio offset
	kp          = 2000e-6 // ratio offset per unit of relative fill error beyond the band
	avgAlpha    = 0.005   // fill smoothing per 5 ms chunk (~1 s)
	clockSettle = 250 * time.Millisecond
	maxLost     = 48000 // give up waiting for late audio after 1 s of silence
)

// run writes to the playback stream until ctx ends. frames is the chunk size.
func (a *ssbAudio) run(ctx context.Context, pb audio.PlaybackStream, frames int) {
	mono := make([]float32, frames)
	stereo := make([]float32, frames*audio.Channels)
	warned := false
	running := true // PortAudio streams start running when opened
	var wav *wavWriter
	defer func() { wav.Close() }()
	for ctx.Err() == nil {
		if !a.isActive() {
			wav.Close()
			wav = nil
			if running {
				if err := pb.Pause(); err != nil {
					slog.Warn("SSB audio pause failed", "err", err)
				}
				running = false
			}
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
			}
			continue
		}
		if !running {
			if err := pb.Resume(); err != nil {
				slog.Error("SSB audio resume failed", "err", err)
				return
			}
			running = true
			a.mu.Lock()
			a.resumed = time.Now()
			a.mu.Unlock()
			if a.wavDir != "" {
				name := filepath.Join(a.wavDir, "ssb-"+time.Now().Format("20060102_150405")+".wav")
				var err error
				if wav, err = createWAV(name, 48000); err != nil {
					slog.Warn("SSB WAV not saved", "err", err)
				} else {
					slog.Info("saving SSB over", "file", name)
				}
			}
		}
		a.fill(mono)
		for i, v := range mono {
			stereo[2*i], stereo[2*i+1] = v, v
		}
		if err := pb.Write(stereo); err != nil && !errors.Is(err, audio.ErrUnderflow) {
			if !warned {
				slog.Error("SSB audio playback failed", "err", err)
				warned = true
			}
			return
		}
		a.consumed.Add(int64(len(mono)))
		if err := wav.Write(mono); err != nil {
			slog.Warn("SSB WAV write failed", "err", err)
			wav.Close()
			wav = nil
		}
	}
}

// wavWriter writes mono 16-bit PCM. Its methods accept a nil receiver, which does nothing.
type wavWriter struct {
	f *os.File
	w *bufio.Writer
	n uint32 // data bytes written
}

func createWAV(name string, rate int) (*wavWriter, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}
	w := &wavWriter{f: f, w: bufio.NewWriter(f)}
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	if _, err := w.w.Write(h); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func (w *wavWriter) Write(x []float32) error {
	if w == nil {
		return nil
	}
	var b [2]byte
	for _, v := range x {
		binary.LittleEndian.PutUint16(b[:], uint16(int16(math.Round(float64(max(-1, min(1, v)))*32767))))
		if _, err := w.w.Write(b[:]); err != nil {
			return err
		}
	}
	w.n += uint32(2 * len(x))
	return nil
}

// Close fills in the header sizes and closes the file.
func (w *wavWriter) Close() {
	if w == nil || w.f == nil {
		return
	}
	_ = w.w.Flush()
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], 36+w.n)
	_, _ = w.f.WriteAt(b[:], 4)
	binary.LittleEndian.PutUint32(b[:], w.n)
	_, _ = w.f.WriteAt(b[:], 40)
	_ = w.f.Close()
	w.f = nil
}
