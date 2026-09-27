package hpsdr

import "math"

// HL2 telemetry encodings, inverted from the formulas clients use (Thetis/Zeus/piHPSDR):
//
//	power:       volts = (raw - 6) / 4095 * 3.3,  watts = volts^2 / 1.5
//	temperature: celsius = (3.26 * raw / 4096 - 0.5) / 0.01   (TMP36)
//
// See doc/hl2/protocol1.md section 6.2 and Zeus Contracts/RadioCalibration.cs (HermesLite2).

// PowerRaw returns the 12-bit forward/reverse power reading that clients show as watts.
func PowerRaw(watts float64) uint16 {
	if watts <= 0 {
		return 0
	}
	raw := 6 + math.Sqrt(watts*1.5)/3.3*4095
	return clamp12(raw)
}

// ReversePowerRaw returns the reverse-power reading for a forward power and SWR.
func ReversePowerRaw(fwdWatts, swr float64) uint16 {
	if fwdWatts <= 0 || swr <= 1 {
		return 0
	}
	rho := (swr - 1) / (swr + 1)
	return PowerRaw(fwdWatts * rho * rho)
}

// TempRaw returns the 12-bit temperature reading for celsius.
func TempRaw(celsius float64) uint16 {
	return clamp12((celsius*0.01 + 0.5) * 4096 / 3.26)
}

func clamp12(v float64) uint16 {
	if v < 0 {
		return 0
	}
	if v > 4095 {
		return 4095
	}
	return uint16(math.Round(v))
}
