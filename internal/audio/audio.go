// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// Package audio gives the daemon stereo float32 access to the QMX USB sound card.
//
// Samples are interleaved stereo float32 in [-1, 1). In QMX IQ mode, left carries I and right
// carries Q. The device delivers 24-bit samples, and float32 holds 24 bits exactly, so the
// conversion loses nothing.
package audio

import "errors"

// Channels is fixed at 2: I/Q on capture, L/R on playback.
const Channels = 2

var (
	// ErrOverflow means the capture side dropped samples because we read too late.
	// The frames returned with it are still valid.
	ErrOverflow = errors.New("audio input overflow")
	// ErrUnderflow means playback ran dry before our write arrived.
	ErrUnderflow = errors.New("audio output underflow")
)

// Backend opens audio streams. PortAudio is the only implementation today.
type Backend interface {
	Init() error
	Terminate()
	ListDevices() ([]DeviceInfo, error)
	// OpenCapture opens a stereo input stream. framesPerBuffer sets the size of each Read.
	OpenCapture(device string, rate, framesPerBuffer int) (CaptureStream, error)
	// OpenPlayback opens a stereo output stream.
	OpenPlayback(device string, rate, framesPerBuffer int) (PlaybackStream, error)
}

// CaptureStream is a blocking reader. Each Read returns exactly FramesPerBuffer frames, and the
// rhythm of those returns is the device clock that paces the rest of the daemon.
type CaptureStream interface {
	// Read fills dst (len >= FramesPerBuffer*Channels) and returns the number of frames written.
	// It may return frames together with ErrOverflow.
	Read(dst []float32) (int, error)
	FramesPerBuffer() int
	Close() error
}

// PlaybackStream is a blocking writer.
type PlaybackStream interface {
	// Write plays interleaved stereo frames. len(src) must be a multiple of Channels.
	Write(src []float32) error
	// Pause stops the stream so the device sees no audio at all (not just silence);
	// Resume restarts it. The QMX ignores CAT TA tones while USB audio is streaming.
	Pause() error
	Resume() error
	Close() error
}

// DeviceInfo describes one audio device.
type DeviceInfo struct {
	Name       string
	Index      int
	MaxInput   int
	MaxOutput  int
	SampleRate float64
}
