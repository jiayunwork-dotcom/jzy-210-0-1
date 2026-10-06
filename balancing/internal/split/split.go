// Package split distributes a continuous correction vector over a rotor's
// discrete, equally spaced balance holes.
//
// Many rotors cannot carry a weight at an arbitrary angle: they offer a fixed
// number N of equiangular holes. The correction vector is then realized by
// placing weights in the two holes bracketing the desired angle, chosen so
// that the vector sum of the two hole weights equals the desired vector
// exactly (up to floating-point tolerance). With N >= 3 the two adjacent
// hole vectors span the sector containing the target and both coefficients
// come out non-negative, i.e. no "remove metal" needed.
package split

import (
	"errors"
	"math"

	"balancing/internal/vec"
)

// MinHoles is the smallest acceptable hole count. With fewer than three
// equiangular holes a sector is wider than 120° and the two-hole decomposition
// can require a negative (impossible) weight.
const MinHoles = 3

var (
	// ErrTooFewHoles is returned for a plane with fewer than MinHoles holes.
	ErrTooFewHoles = errors.New("balance hole count must be at least 3")
	// ErrPlaneMissing is returned when no layout is given for a plane that has
	// a correction weight.
	ErrPlaneMissing = errors.New("no hole layout for correction plane")
)

// Plane describes the equiangular hole pattern of one correction plane, in the
// machine's external angular convention:
//
//	Count       number of holes (>= 3)
//	FirstHoleDeg external angle of hole 0; hole k sits at
//	             FirstHoleDeg + k*360/Count (external, wrapping at 360).
type Plane struct {
	Count        int     `json:"count"`
	FirstHoleDeg float64 `json:"first_hole_deg"`
}

// HoleWeight is a single weight placed in one hole.
type HoleWeight struct {
	HoleIndex int     `json:"hole_index"`
	AngleDeg  float64 `json:"angle_deg"` // hole angle, external convention
	Grams     float64 `json:"grams"`
}

// Plan splits one correction vector for one plane. w is expressed in the
// internal canonical frame; returned hole angles are in the machine's external
// frame. A zero-magnitude weight yields no holes.
func Plan(conv vec.Convention, holes Plane, w complex128) ([]HoleWeight, error) {
	if holes.Count < MinHoles {
		return nil, ErrTooFewHoles
	}
	amp := math.Hypot(real(w), imag(w))
	if amp == 0 {
		return nil, nil
	}
	// Work in the machine's external frame, where hole positions are
	// FirstHole + k*360/N.
	internalAng := vec.Norm(math.Atan2(imag(w), real(w)) * 180 / math.Pi)
	target := conv.ToExternal(internalAng)

	step := 360.0 / float64(holes.Count)
	// Continuous index of the target on the hole ring; holes are at integer
	// indices k at angle FirstHole + k*step.
	f := vec.Norm(target-holes.FirstHoleDeg) / step
	k0 := int(math.Floor(f))
	delta := f - float64(k0) // 0 <= delta < 1, fraction toward k0+1

	a0 := holes.FirstHoleDeg + float64(k0)*step
	a1 := a0 + step
	// Decompose the target vector into the two adjacent unit hole vectors
	// using the sine rule in the triangle they form:
	//	m0/sin((1-delta)*step) == m1/sin(delta*step) == amp/sin(step)
	rad := math.Pi / 180
	s := 1 / (2 * math.Sin(step*rad/2))
	// chord coefficients from standard 2-vector resolution:
	m0 := amp * math.Sin((1-delta)*step*rad) / math.Sin(step*rad)
	m1 := amp * math.Sin(delta*step*rad) / math.Sin(step*rad)
	_ = s

	out := []HoleWeight{{
		HoleIndex: k0 % holes.Count,
		AngleDeg:  vec.Norm(a0),
		Grams:     m0,
	}}
	if m1 > 1e-12 {
		out = append(out, HoleWeight{
			HoleIndex: (k0 + 1) % holes.Count,
			AngleDeg:  vec.Norm(a1),
			Grams:     m1,
		})
	}
	return out, nil
}

// Plans splits one correction vector per plane. holeLayout[p] == nil means
// "arbitrary angle allowed" for that plane: the weight is returned as a single
// entry at its exact external angle (hole index -1).
func Plans(conv vec.Convention, holeLayout []*Plane, weights []complex128) ([][]HoleWeight, error) {
	out := make([][]HoleWeight, len(weights))
	for p, w := range weights {
		amp := math.Hypot(real(w), imag(w))
		if amp == 0 {
			continue
		}
		if p >= len(holeLayout) || holeLayout[p] == nil {
			ang := vec.Norm(math.Atan2(imag(w), real(w)) * 180 / math.Pi)
			out[p] = []HoleWeight{{
				HoleIndex: -1,
				AngleDeg:  conv.ToExternal(ang),
				Grams:     amp,
			}}
			continue
		}
		hw, err := Plan(conv, *holeLayout[p], w)
		if err != nil {
			return nil, err
		}
		out[p] = hw
	}
	return out, nil
}

// Reconstruct returns the vector sum of hole weights for one plane, in the
// internal frame. Used to verify that splitting is exact.
func Reconstruct(conv vec.Convention, hw []HoleWeight) complex128 {
	var z complex128
	for _, h := range hw {
		r := conv.ToInternal(h.AngleDeg) * math.Pi / 180
		z += complex(h.Grams*math.Cos(r), h.Grams*math.Sin(r))
	}
	return z
}
