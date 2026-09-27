package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/rampa/qmx-hl2/internal/audio"
)

// ssbAudio plays SSB transmit audio to the QMX's USB sound card.
//
// Samples arrive paced by the QMX capture clock (the client sends TX IQ in step with EP6), but
// the QMX consumes playback at the host's USB clock; on the bench the two differ by about
// 165 ppm. A FIFO with a fill-level target absorbs network jitter, and one sample is dropped or
// repeated per chunk whenever the fill wanders outside a band around the target (the Quisk
// approach).
//
// Outside SSB overs the stream is paused: bench 2026-09-27 showed the QMX ignores CAT TA
// tones (0 W) while USB audio is streaming, even silence.
type ssbAudio struct {
	mu      sync.Mutex
	fifo    []float32
	playing bool
	target  int // samples buffered before playback starts, and the drift-control set point
	band    int // allowed wander around target before correcting
	max     int // hard cap; older samples are discarded beyond it

	drops, dups, underruns int

	active bool
	wake   chan struct{}
}

func newSSBAudio(sampleRate int) *ssbAudio {
	return &ssbAudio{
		target: sampleRate * 60 / 1000, // 60 ms
		band:   sampleRate * 20 / 1000,
		max:    sampleRate / 2,
		wake:   make(chan struct{}, 1),
	}
}

// Push appends mono audio samples.
func (a *ssbAudio) Push(x []float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fifo = append(a.fifo, x...)
	if over := len(a.fifo) - a.max; over > 0 {
		a.fifo = append(a.fifo[:0], a.fifo[over:]...)
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
	a.fifo = a.fifo[:0]
	a.playing = false
	a.mu.Unlock()
}

func (a *ssbAudio) isActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// Stats returns the drift-correction and underrun counters.
func (a *ssbAudio) Stats() (drops, dups, underruns int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.drops, a.dups, a.underruns
}

// fill copies the next len(out) samples (mono) into out, applying drift correction; silence
// when not playing.
func (a *ssbAudio) fill(out []float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.playing && len(a.fifo) >= a.target {
		a.playing = true
	}
	if !a.playing {
		clear(out)
		return
	}
	n := len(out)
	switch f := len(a.fifo); {
	case f > a.target+a.band && f > n:
		a.fifo = a.fifo[1:] // drop one sample
		a.drops++
	case f < a.target-a.band && f > 0:
		a.fifo = append([]float32{a.fifo[0]}, a.fifo...) // repeat one sample
		a.dups++
	}
	k := copy(out, a.fifo)
	a.fifo = append(a.fifo[:0], a.fifo[k:]...)
	if k < n {
		clear(out[k:])
		a.playing = false
		a.underruns++
	}
}

// run writes to the playback stream until ctx ends. frames is the chunk size.
func (a *ssbAudio) run(ctx context.Context, pb audio.PlaybackStream, frames int) {
	mono := make([]float32, frames)
	stereo := make([]float32, frames*audio.Channels)
	warned := false
	running := true // PortAudio streams start running when opened
	for ctx.Err() == nil {
		if !a.isActive() {
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
	}
}
