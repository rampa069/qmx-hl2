package engine

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"

	"github.com/rampa/qmx-hl2/internal/audio"
)

// ssbAudio plays SSB transmit audio to the QMX's USB sound card.
//
// Samples arrive paced by the QMX capture clock (the client sends TX IQ in step with EP6), but
// the QMX consumes playback at the host's USB clock; on the bench the two differ by about
// 165 ppm. A FIFO with a fill-level target absorbs network jitter, and a fractional resampler
// whose ratio is steered by the fill level absorbs the clock difference.
//
// Outside SSB overs the stream is paused: bench 2026-09-27 showed the QMX ignores CAT TA
// tones (0 W) while USB audio is streaming, even silence.
type ssbAudio struct {
	mu      sync.Mutex
	fifo    []float32
	playing bool
	target  int // samples buffered before playback starts, and the drift-control set point
	max     int // hard cap; older samples are discarded beyond it

	underruns int
	pos       float64 // fractional read position in fifo
	ratio     float64 // input samples consumed per output sample
	integ     float64 // PI integrator

	active bool
	wake   chan struct{}
}

func newSSBAudio(sampleRate int) *ssbAudio {
	return &ssbAudio{
		target: sampleRate * 60 / 1000, // 60 ms
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

// Stats returns the underrun count and the estimated clock difference in ppm (the servo's
// integral term, i.e. the long-term resampling ratio).
func (a *ssbAudio) Stats() (underruns int, ppm float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.underruns, a.integ * 1e6
}

// fill writes len(out) mono samples to out, resampling the FIFO by a ratio close to 1 that a
// PI controller steers to hold the fill level at target. Cubic (Catmull-Rom) interpolation
// keeps the tiny rate change inaudible, unlike dropping or repeating samples.
func (a *ssbAudio) fill(out []float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.playing && len(a.fifo) >= a.target {
		a.playing = true
		a.pos = 1 // one sample of history for the interpolator
		// The integrator is kept across overs: the clock difference does not change.
	}
	if !a.playing {
		clear(out)
		return
	}
	// Controller: error in seconds of buffer, relative to the target.
	e := float64(len(a.fifo)-a.target) / float64(a.target)
	a.integ = math.Max(-maxPPM, math.Min(maxPPM, a.integ+ki*e))
	a.ratio = 1 + math.Max(-maxPPM, math.Min(maxPPM, kp*e+a.integ))

	for n := range out {
		k := int(a.pos)
		if k+2 >= len(a.fifo) {
			clear(out[n:])
			a.playing = false
			a.underruns++
			a.fifo = a.fifo[:0]
			return
		}
		f := float32(a.pos - float64(k))
		y0, y1, y2, y3 := a.fifo[k-1], a.fifo[k], a.fifo[k+1], a.fifo[k+2]
		out[n] = y1 + 0.5*f*(y2-y0+f*(2*y0-5*y1+4*y2-y3+f*(3*(y1-y2)+y3-y0)))
		a.pos += a.ratio
	}
	// Drop consumed samples, keeping one for history.
	if k := int(a.pos) - 1; k > 0 {
		a.fifo = append(a.fifo[:0], a.fifo[k:]...)
		a.pos -= float64(k)
	}
}

// Controller tuning: the ratio may deviate up to 1000 ppm (bench drift is ~165 ppm).
const (
	maxPPM = 1000e-6
	kp     = 3000e-6 // ratio offset per unit of relative fill error (~20 s time constant)
	ki     = 0.2e-6  // integrated each 5 ms chunk
)

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
