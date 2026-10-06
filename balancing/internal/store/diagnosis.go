package store

import "math"

// checkTrialEffect performs the synchronous rejection when a trial run is
// submitted after the original run is already present. The vibration change
// caused by the trial weight must exceed MinSignalToNoise times the noise
// floor at EVERY speed the original run covers; otherwise the derived
// influence coefficient would be dominated by noise (the most common field
// cause: a phase entered 180° off or in the inverted convention).
func checkTrialEffect(m *Machine, orig *Run, in RunInput) *ValidationError {
	conv := m.Convention()
	var fes []FieldError
	for _, speed := range m.Speeds {
		ov, ok := originalVector(m, orig, speed)
		if !ok {
			continue
		}
		// trial-run readings at this speed
		rv := make([]complex128, m.PointCount)
		have := 0
		for _, rd := range in.Readings {
			if rd.Speed != speed || rd.Point < 0 || rd.Point >= m.PointCount {
				continue
			}
			rv[rd.Point] = vecPol(conv, rd.Amp, rd.Phase)
			have++
		}
		if have != m.PointCount {
			continue
		}
		eff := make([]complex128, m.PointCount)
		for i := range eff {
			eff[i] = rv[i] - ov[i]
		}
		snr := rmsC(eff) / noiseFloor(ov)
		if snr < balanceMinSNR {
			fes = append(fes, FieldError{
				Field:   "readings",
				Code:    "small_trial_effect",
				Message: "trial effect at " + itoa(speed) + " rpm is " + trimFloat(3, snr) + "x the noise floor (< 3x); use a larger trial weight or recheck phase (180° flip / convention)",
			})
		}
	}
	if len(fes) > 0 {
		return ve(fes...)
	}
	return nil
}

const balanceMinSNR = 3.0

func vecPol(conv interface {
	ToInternal(float64) float64
}, amp, extPhase float64) complex128 {
	r := conv.ToInternal(extPhase) * math.Pi / 180
	return complex(amp*math.Cos(r), amp*math.Sin(r))
}

func rmsC(v []complex128) float64 {
	var s float64
	for _, z := range v {
		s += real(z)*real(z) + imag(z)*imag(z)
	}
	return math.Sqrt(s / float64(len(v)))
}

func trimFloat(prec int, x float64) string {
	mul := math.Pow(10, float64(prec))
	v := math.Round(x*mul) / mul
	b := make([]byte, 0, prec+3)
	if v < 0 {
		b = append(b, '-')
		v = -v
	}
	n := int64(v)
	if n == 0 {
		b = append(b, '0')
	} else {
		var d [20]byte
		k := len(d)
		for n > 0 {
			k--
			d[k] = byte('0' + n%10)
			n /= 10
		}
		b = append(b, d[k:]...)
	}
	if prec > 0 {
		b = append(b, '.')
		frac := v - math.Floor(v)
		for i := 0; i < prec; i++ {
			frac *= 10
			d := int(frac)
			b = append(b, byte('0'+d))
			frac -= float64(d)
		}
	}
	return string(b)
}
