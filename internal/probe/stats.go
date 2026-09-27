// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// Package probe supports bench checks on a real QMX: IQ levels, DC offset and the QMX
// sample clock measured against the host clock.
package probe

import (
	"fmt"
	"math"
	"time"
)

// ChannelStats accumulates level statistics for one channel.
type ChannelStats struct {
	n     int64
	sum   float64
	sumSq float64
	peak  float64
}

// Add accumulates one sample.
func (c *ChannelStats) Add(v float32) {
	x := float64(v)
	c.n++
	c.sum += x
	c.sumSq += x * x
	if a := math.Abs(x); a > c.peak {
		c.peak = a
	}
}

// DC returns the mean sample value.
func (c *ChannelStats) DC() float64 {
	if c.n == 0 {
		return 0
	}
	return c.sum / float64(c.n)
}

// RMSdBFS returns the AC RMS level (with the DC removed) in dB relative to full scale.
func (c *ChannelStats) RMSdBFS() float64 {
	if c.n == 0 {
		return math.Inf(-1)
	}
	mean := c.DC()
	v := c.sumSq/float64(c.n) - mean*mean
	if v <= 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(v)
}

// PeakdBFS returns the absolute peak in dB relative to full scale.
func (c *ChannelStats) PeakdBFS() float64 {
	if c.peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(c.peak)
}

// IQStats accumulates interleaved stereo frames: left is I, right is Q.
type IQStats struct {
	I, Q ChannelStats
}

// AddInterleaved accumulates frames from interleaved stereo samples.
func (s *IQStats) AddInterleaved(buf []float32, frames int) {
	for f := 0; f < frames; f++ {
		s.I.Add(buf[2*f])
		s.Q.Add(buf[2*f+1])
	}
}

// Frames returns the number of frames accumulated.
func (s *IQStats) Frames() int64 { return s.I.n }

// String gives a one-line summary.
func (s *IQStats) String() string {
	return fmt.Sprintf("I: rms %6.1f dBFS peak %6.1f dBFS dc %+.5f | Q: rms %6.1f dBFS peak %6.1f dBFS dc %+.5f | I-Q imbalance %+.2f dB",
		s.I.RMSdBFS(), s.I.PeakdBFS(), s.I.DC(),
		s.Q.RMSdBFS(), s.Q.PeakdBFS(), s.Q.DC(),
		s.I.RMSdBFS()-s.Q.RMSdBFS())
}

// RateMeter measures the device sample rate against the host monotonic clock.
//
// Each Add records (arrival time, cumulative frames). Rate is the least-squares slope of frames
// over time, so buffer-arrival jitter averages out instead of dominating as it does with a
// simple frames/elapsed ratio. Start it after the stream has settled, since the first buffers
// arrive in a burst.
type RateMeter struct {
	start  time.Time
	frames int64
	// Online (Welford) regression state.
	n                      float64
	meanT, meanF, mTT, cTF float64
}

// Start begins a measurement at now.
func (r *RateMeter) Start(now time.Time) { *r = RateMeter{start: now} }

// Add counts frames that arrived at now.
func (r *RateMeter) Add(now time.Time, frames int) {
	r.frames += int64(frames)
	t := now.Sub(r.start).Seconds()
	f := float64(r.frames)
	r.n++
	dt := t - r.meanT
	r.meanT += dt / r.n
	df := f - r.meanF
	r.meanF += df / r.n
	r.mTT += dt * (t - r.meanT)
	r.cTF += dt * (f - r.meanF)
}

// Frames returns the total frames added.
func (r *RateMeter) Frames() int64 { return r.frames }

// Rate returns the regression slope in frames/second and its offset from nominal in ppm.
// It returns zeros until there are at least two points spread over time.
func (r *RateMeter) Rate(nominal int) (rate, ppm float64) {
	if r.n < 2 || r.mTT <= 0 {
		return 0, 0
	}
	rate = r.cTF / r.mTT
	return rate, (rate/float64(nominal) - 1) * 1e6
}
