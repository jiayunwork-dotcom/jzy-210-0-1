// Package vec implements vibration vectors (amplitude + phase) and the per-machine
// phase convention.
//
// The service has one internal ("canonical") angular convention: the keyphasor
// marker defines 0° and angles increase AGAINST the shaft rotation direction
// (i.e. the standard rotating-machinery convention). Every value coming from a
// machine whose convention differs is converted on ingestion; every value sent
// back to the client is converted back. Because all machines are canonical
// internally, influence coefficients estimated for one machine can be compared
// and stored without keeping track of mixed conventions.
package vec

import (
	"math"
	"math/cmplx"
)

// Direction is the direction in which phase angles increase relative to shaft
// rotation.
type Direction string

const (
	AgainstRotation Direction = "against_rotation" // canonical
	WithRotation    Direction = "with_rotation"
)

func (d Direction) Valid() bool {
	return d == AgainstRotation || d == WithRotation
}

// Convention describes how one particular machine numbers angles.
//
//	ReferenceDeg: physical internal angle (canonical frame) of the marker the
//	              machine's 0° refers to.
//	Direction:    whether the machine's angles increase with or against the
//	              shaft rotation.
type Convention struct {
	ReferenceDeg float64   `json:"reference_deg"`
	Direction    Direction `json:"direction"`
}

// Canonical returns the service's internal convention (0° at the keyphasor,
// against rotation).
func Canonical() Convention { return Convention{ReferenceDeg: 0, Direction: AgainstRotation} }

// Valid reports whether the convention is usable.
func (c Convention) Valid() bool {
	return c.Direction.Valid() && !math.IsNaN(c.ReferenceDeg) && !math.IsInf(c.ReferenceDeg, 0)
}

// ToInternal converts an angle expressed in the machine convention to the
// canonical internal angle.
func (c Convention) ToInternal(externalDeg float64) float64 {
	if c.Direction == AgainstRotation {
		return Norm(c.ReferenceDeg + externalDeg)
	}
	return Norm(c.ReferenceDeg - externalDeg)
}

// ToExternal converts a canonical internal angle back to the machine
// convention.
func (c Convention) ToExternal(internalDeg float64) float64 {
	if c.Direction == AgainstRotation {
		return Norm(internalDeg - c.ReferenceDeg)
	}
	return Norm(c.ReferenceDeg - internalDeg)
}

// Norm normalizes an angle to [0, 360).
func Norm(deg float64) float64 {
	d := math.Mod(deg, 360)
	if d < 0 {
		d += 360
	}
	return d
}

// Polar is an amplitude/phase pair; Phase is in degrees in whatever frame the
// caller has agreed on (external on input/output, internal inside the service).
type Polar struct {
	Amp   float64 `json:"amp"`
	Phase float64 `json:"phase_deg"`
}

// C converts a polar vector to a complex number: real axis = 0°, positive
// imaginary = positive angle direction.
func (p Polar) C() complex128 {
	r := p.Phase * math.Pi / 180
	return complex(p.Amp*math.Cos(r), p.Amp*math.Sin(r))
}

// FromC converts a complex number back to polar form with phase in [0,360).
func FromC(z complex128) Polar {
	return Polar{Amp: cmplx.Abs(z), Phase: Norm(cmplx.Phase(z) * 180 / math.Pi)}
}

// Mul scales a polar vector.
func (p Polar) Mul(k float64) Polar { return Polar{Amp: p.Amp * k, Phase: p.Phase} }
