// Package dsp holds the small signal-processing blocks the RX path needs: DC blocking,
// integer-factor interpolation, and NCO frequency shifting of complex baseband.
package dsp

import "math"

// DCBlocker is a one-pole high-pass filter applied to I and Q independently.
// y[n] = x[n] - x[n-1] + a*y[n-1].
type DCBlocker struct {
	a      float64
	xi, xq float64
	yi, yq float64
}

// NewDCBlocker returns a blocker whose -3 dB corner is roughly cornerHz at sampleRate.
func NewDCBlocker(cornerHz, sampleRate float64) *DCBlocker {
	return &DCBlocker{a: 1 - 2*math.Pi*cornerHz/sampleRate}
}

// Process filters one sample in place.
func (d *DCBlocker) Process(c complex128) complex128 {
	i, q := real(c), imag(c)
	d.yi = i - d.xi + d.a*d.yi
	d.yq = q - d.xq + d.a*d.yq
	d.xi, d.xq = i, q
	return complex(d.yi, d.yq)
}

// Interpolator raises the sample rate of complex baseband by an integer factor with a
// windowed-sinc polyphase low-pass filter. The passband keeps about 90% of the input Nyquist
// band (±21.6 kHz for 48 kHz in); everything above is attenuated by the filter.
type Interpolator struct {
	L    int
	taps int          // taps per phase
	h    [][]float64  // [phase][tap]
	hist []complex128 // circular delay line of the last taps inputs
	pos  int
}

// NewInterpolator builds an interpolator for factor L (1 is a pass-through).
func NewInterpolator(L int) *Interpolator {
	if L < 1 {
		L = 1
	}
	it := &Interpolator{L: L}
	if L == 1 {
		return it
	}
	const tapsPerPhase = 24
	n := tapsPerPhase * L
	fc := 0.45 / float64(L) // cutoff as a fraction of the output rate (0.5 = Nyquist)
	proto := make([]float64, n)
	mid := float64(n-1) / 2
	for k := range proto {
		x := float64(k) - mid
		sinc := 2 * fc
		if x != 0 {
			sinc = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		// Blackman window.
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(k)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(k)/float64(n-1))
		proto[k] = sinc * w * float64(L) // gain L restores the amplitude lost to zero-stuffing
	}
	it.taps = tapsPerPhase
	it.h = make([][]float64, L)
	for p := 0; p < L; p++ {
		it.h[p] = make([]float64, tapsPerPhase)
		for m := 0; m < tapsPerPhase; m++ {
			it.h[p][m] = proto[p+m*L]
		}
	}
	it.hist = make([]complex128, tapsPerPhase)
	return it
}

// Process takes one input sample and writes L output samples to out (len(out) >= L).
func (it *Interpolator) Process(x complex128, out []complex128) {
	if it.L == 1 {
		out[0] = x
		return
	}
	it.pos--
	if it.pos < 0 {
		it.pos = it.taps - 1
	}
	it.hist[it.pos] = x
	for p := 0; p < it.L; p++ {
		var accR, accI float64
		h := it.h[p]
		j := it.pos
		for m := 0; m < it.taps; m++ {
			v := it.hist[j]
			accR += h[m] * real(v)
			accI += h[m] * imag(v)
			j++
			if j == it.taps {
				j = 0
			}
		}
		out[p] = complex(accR, accI)
	}
}

// NCO shifts complex baseband by a frequency, using a recursive phasor that is renormalised
// periodically to stop amplitude drift.
type NCO struct {
	step  complex128
	ph    complex128
	count int
	freq  float64
	rate  float64
}

// NewNCO returns an NCO at sampleRate with shift 0.
func NewNCO(sampleRate float64) *NCO {
	n := &NCO{rate: sampleRate, ph: 1}
	n.SetFreq(0)
	return n
}

// SetFreq sets the shift in Hz; positive moves the spectrum up. The phase is continuous.
func (n *NCO) SetFreq(hz float64) {
	n.freq = hz
	w := 2 * math.Pi * hz / n.rate
	n.step = complex(math.Cos(w), math.Sin(w))
}

// Freq returns the current shift.
func (n *NCO) Freq() float64 { return n.freq }

// Mix multiplies x by the current phasor and advances it.
func (n *NCO) Mix(x complex128) complex128 {
	if n.freq == 0 {
		return x
	}
	y := x * n.ph
	n.ph *= n.step
	n.count++
	if n.count >= 1024 {
		n.count = 0
		m := math.Hypot(real(n.ph), imag(n.ph))
		n.ph = complex(real(n.ph)/m, imag(n.ph)/m)
	}
	return y
}
