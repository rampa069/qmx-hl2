package audio

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gordonklaus/portaudio"
)

// PABackend is the PortAudio implementation of Backend.
type PABackend struct{}

// NewPABackend returns a PortAudio backend. Call Init before use.
func NewPABackend() *PABackend { return &PABackend{} }

func (pa *PABackend) Init() error { return portaudio.Initialize() }

func (pa *PABackend) Terminate() { _ = portaudio.Terminate() }

func (pa *PABackend) ListDevices() ([]DeviceInfo, error) {
	devs, err := portaudio.Devices()
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	out := make([]DeviceInfo, len(devs))
	for i, d := range devs {
		out[i] = DeviceInfo{
			Name:       d.Name,
			Index:      i,
			MaxInput:   d.MaxInputChannels,
			MaxOutput:  d.MaxOutputChannels,
			SampleRate: d.DefaultSampleRate,
		}
	}
	return out, nil
}

func (pa *PABackend) OpenCapture(device string, rate, framesPerBuffer int) (CaptureStream, error) {
	dev, err := findDevice(device, true)
	if err != nil {
		return nil, err
	}
	buf := make([]float32, framesPerBuffer*Channels)
	params := portaudio.StreamParameters{
		Input: portaudio.StreamDeviceParameters{
			Device:   dev,
			Channels: Channels,
			Latency:  dev.DefaultLowInputLatency,
		},
		SampleRate:      float64(rate),
		FramesPerBuffer: framesPerBuffer,
	}
	stream, err := portaudio.OpenStream(params, &buf)
	if err != nil {
		return nil, fmt.Errorf("open capture on %q: %w", dev.Name, err)
	}
	if err := stream.Start(); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("start capture on %q: %w", dev.Name, err)
	}
	slog.Info("audio capture opened", "device", dev.Name, "rate", rate, "frames", framesPerBuffer)
	return &paCapture{stream: stream, buf: buf, frames: framesPerBuffer}, nil
}

func (pa *PABackend) OpenPlayback(device string, rate, framesPerBuffer int) (PlaybackStream, error) {
	dev, err := findDevice(device, false)
	if err != nil {
		return nil, err
	}
	buf := make([]float32, framesPerBuffer*Channels)
	params := portaudio.StreamParameters{
		Output: portaudio.StreamDeviceParameters{
			Device:   dev,
			Channels: Channels,
			Latency:  dev.DefaultLowOutputLatency,
		},
		SampleRate:      float64(rate),
		FramesPerBuffer: framesPerBuffer,
	}
	stream, err := portaudio.OpenStream(params, &buf)
	if err != nil {
		return nil, fmt.Errorf("open playback on %q: %w", dev.Name, err)
	}
	if err := stream.Start(); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("start playback on %q: %w", dev.Name, err)
	}
	slog.Info("audio playback opened", "device", dev.Name, "rate", rate, "frames", framesPerBuffer)
	return &paPlayback{stream: stream, buf: buf}, nil
}

type paCapture struct {
	stream *portaudio.Stream
	buf    []float32
	frames int
}

func (c *paCapture) FramesPerBuffer() int { return c.frames }

func (c *paCapture) Read(dst []float32) (int, error) {
	if len(dst) < len(c.buf) {
		return 0, fmt.Errorf("capture dst too small: %d < %d", len(dst), len(c.buf))
	}
	err := c.stream.Read()
	if err != nil && !errors.Is(err, portaudio.InputOverflowed) {
		return 0, err
	}
	copy(dst, c.buf)
	if err != nil {
		return c.frames, ErrOverflow
	}
	return c.frames, nil
}

func (c *paCapture) Close() error {
	_ = c.stream.Stop()
	return c.stream.Close()
}

type paPlayback struct {
	stream *portaudio.Stream
	buf    []float32
}

func (p *paPlayback) Write(src []float32) error {
	if len(src)%Channels != 0 {
		return fmt.Errorf("playback write: %d samples is not a whole number of frames", len(src))
	}
	var underflow bool
	for off := 0; off < len(src); off += len(p.buf) {
		n := copy(p.buf, src[off:])
		// Zero the tail of a short final chunk so stale samples are never replayed.
		clear(p.buf[n:])
		if err := p.stream.Write(); err != nil {
			if !errors.Is(err, portaudio.OutputUnderflowed) {
				return err
			}
			underflow = true
		}
	}
	if underflow {
		return ErrUnderflow
	}
	return nil
}

func (p *paPlayback) Pause() error  { return p.stream.Stop() }
func (p *paPlayback) Resume() error { return p.stream.Start() }

func (p *paPlayback) Close() error {
	_ = p.stream.Stop()
	return p.stream.Close()
}

// findDevice picks a device by exact name, then by case-insensitive substring. Unlike
// audioStreamer it never falls back to the system default: silently capturing the Mac's
// microphone as "IQ" would be worse than failing.
func findDevice(name string, wantInput bool) (*portaudio.DeviceInfo, error) {
	devs, err := portaudio.Devices()
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	usable := func(d *portaudio.DeviceInfo) bool {
		if wantInput {
			return d.MaxInputChannels >= Channels
		}
		return d.MaxOutputChannels >= Channels
	}
	for _, d := range devs {
		if d.Name == name && usable(d) {
			return d, nil
		}
	}
	lname := strings.ToLower(name)
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.Name), lname) && usable(d) {
			return d, nil
		}
	}
	dir := "output"
	if wantInput {
		dir = "input"
	}
	return nil, fmt.Errorf("no stereo %s device matching %q (run with -list)", dir, name)
}
