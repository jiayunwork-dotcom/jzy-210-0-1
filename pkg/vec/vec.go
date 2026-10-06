// Package vec implements vibration vectors (amplitude + phase) and the phase
// convention (相位口径) conversion used throughout the balancer.
//
// Internally every angle is stored in a canonical frame: mathematical
// convention, degrees measured counter-clockwise (CCW) from the keyphasor
// reference mark. A machine's Convention describes how field angles are
// expressed instead:
//
//	zeroOffsetDeg — field angle of the keyphasor mark (where field 0 actually
//	                is, expressed in internal coordinates)
//	direction     — DirectionCCW (1) or DirectionCW (-1): the direction in
//	                which field phase increases relative to rotor rotation,
//	                expressed internally
//
// The conversion is a pure affine map, so it is its own involution: applying
// InternalFromExternal to a value produced by ExternalFromInternal (or vice
// versa) returns the original value.
package vec

import (
	"fmt"
	"math"
)

// Rotation direction of increasing field phase, expressed in internal
// coordinates (internal is always CCW positive).
const (
	DirectionCCW = 1
	DirectionCW  = -1
)

// Polar is a vibration vector: amplitude (µm for readings, g for weights)
// and phase in degrees. A zero amplitude carries no meaningful phase; callers
// should normalize such vectors with Normalize before comparing.
type Polar struct {
	Amp   float64 `json:"amp"`
	Phase float64 `json:"phase"`
}

// String renders the common "amp∠phase" form.
func (p Polar) String() string {
	return fmt.Sprintf("%.4g∠%.4g°", p.Amp, p.Phase)
}

// Complex converts to a complex number, treating phase as degrees in the
// frame the caller already implies (this function performs no convention
// conversion).
func (p Polar) Complex() complex128 {
	if p.Amp == 0 {
		return 0
	}
	r := p.Phase * math.Pi / 180
	return complex(p.Amp*math.Cos(r), p.Amp*math.Sin(r))
}

// FromComplex converts a complex number back to polar form in the same frame.
// A (near-)zero vector yields {0, 0}.
func FromComplex(z complex128) Polar {
	amp := cmplxAbs(z)
	if amp < 1e-12 {
		return Polar{}
	}
	return Polar{Amp: amp, Phase: WrapPos(cmplxPhaseDeg(z))}
}

// Normalize zeroes the phase of a (near-)zero vector.
func Normalize(p Polar) Polar {
	if p.Amp < 1e-12 {
		return Polar{}
	}
	p.Phase = WrapPos(p.Phase)
	return p
}

// Convention describes the machine-specific phase convention of field data.
type Convention struct {
	// ZeroOffsetDeg is the internal angle at which the field zero mark
	// (keyphasor) sits.
	ZeroOffsetDeg float64 `json:"zeroOffsetDeg"`
	// Direction is DirectionCCW or DirectionCW.
	Direction int `json:"direction"`
}

// InternalDefault is the canonical convention used inside the service.
func InternalDefault() Convention {
	return Convention{ZeroOffsetDeg: 0, Direction: DirectionCCW}
}

// Valid reports whether the convention is usable.
func (c Convention) Valid() bool {
	return c.Direction == DirectionCCW || c.Direction == DirectionCW
}

// ExternalFromInternal maps an internal phase to the machine's field phase.
func (c Convention) ExternalFromInternal(internalDeg float64) float64 {
	// field = dir * (internal - zeroOffset)
	return WrapPos(float64(c.Direction) * WrapFull(internalDeg-c.ZeroOffsetDeg))
}

// InternalFromExternal maps a machine field phase to the internal frame.
func (c Convention) InternalFromExternal(fieldDeg float64) float64 {
	// internal = zeroOffset + dir * field  (dir is self-inverse: +/-1)
	return WrapPos(c.ZeroOffsetDeg + float64(c.Direction)*WrapFull(fieldDeg))
}

// ExternalPolar converts a full polar vector from internal to field frame.
func (c Convention) ExternalPolar(p Polar) Polar {
	z := p.Complex()
	if cmplxAbs(z) < 1e-12 {
		return Polar{Amp: p.Amp}
	}
	return Polar{Amp: p.Amp, Phase: c.ExternalFromInternal(p.Phase)}
}

// InternalPolar converts a full polar vector from field frame to internal.
func (c Convention) InternalPolar(p Polar) Polar {
	if p.Amp < 1e-12 {
		return Polar{Amp: p.Amp}
	}
	return Polar{Amp: p.Amp, Phase: c.InternalFromExternal(p.Phase)}
}

// WrapPos reduces an angle to [0, 360).
func WrapPos(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	if a >= 360-1e-9 { // protect against 359.999999999... rounding up
		a = 0
	}
	return a
}

// WrapFull reduces an angle to (-180, 180].
func WrapFull(a float64) float64 {
	a = WrapPos(a)
	if a > 180 {
		a -= 360
	}
	return a
}

// PhaseDiff returns the smallest signed CCW-independent angle b-a in (-180,180].
func PhaseDiff(a, b float64) float64 {
	return WrapFull(b - a)
}

// Equal compares two polar vectors with tolerances (amplitude absolute +
// relative, phase in degrees, ignored for near-zero vectors).
func Equal(a, b Polar, ampTol, phaseTolDeg float64) bool {
	if math.Abs(a.Amp-b.Amp) > ampTol+1e-9*math.Max(1, math.Max(a.Amp, b.Amp)) {
		return false
	}
	if a.Amp < 1e-9 && b.Amp < 1e-9 {
		return true
	}
	return math.Abs(PhaseDiff(a.Phase, b.Phase)) <= phaseTolDeg
}

func cmplxAbs(z complex128) float64 { return math.Hypot(real(z), imag(z)) }

func cmplxPhaseDeg(z complex128) float64 {
	return math.Atan2(imag(z), real(z)) * 180 / math.Pi
}
