// Package split decomposes a computed correction weight (continuous angle)
// into two weights placed in the adjacent holes of a rotor whose holes are
// fixed in number and equally spaced.
//
// The decomposition preserves the resultant vector exactly (in the complex
// sense): w_h1 at hole angle h1 plus w_h2 at h2 equals the requested w.
// Angles are handled in whatever frame the caller passes in; hole angles are
// generated in that same frame, so the convention conversion stays in the
// vec layer.
package split

import (
	"errors"
	"fmt"
	"math"
)

// ErrBadHoleCount is returned when fewer than 3 holes are configured.
var ErrBadHoleCount = errors.New("a correction plane needs at least 3 equally spaced holes")

// Hole describes one placed weight.
type Hole struct {
	Index int     `json:"index"`
	Angle float64 `json:"angle"` // degrees, same frame as the input vector
	Mass  float64 `json:"mass"`  // grams, non-negative
}

// Split expresses the weight (mass grams at angle degrees) using two adjacent
// holes of holeCount equally spaced holes (hole 0 at 0°, angles increase in
// the positive direction of the caller's frame, spacing 360/holeCount).
//
// The two resultant components and the recombined resultant are returned so
// callers can display the (tiny) round-off. ResultantMass/ResultantAngle
// equal the input up to floating-point error.
type Result struct {
	First          Hole    `json:"first"`
	Second         Hole    `json:"second"`
	ResultantMass  float64 `json:"resultantMass"`
	ResultantAngle float64 `json:"resultantAngle"`
}

func Split(angle, mass float64, holeCount int) (*Result, error) {
	if holeCount < 3 {
		return nil, ErrBadHoleCount
	}
	if mass < 0 {
		return nil, fmt.Errorf("mass must be non-negative, got %g", mass)
	}
	angle = wrapPos(angle)
	spacing := 360.0 / float64(holeCount)
	pos := angle / spacing
	i := int(math.Floor(pos + 1e-12))
	frac := angle - float64(i)*spacing
	if frac < 0 {
		frac += spacing
	}
	// Snap-on-hole guard: if the angle is effectively on a hole, put
	// everything there (second mass exactly 0).
	if frac < 1e-9 || spacing-frac < 1e-9 {
		idx := i % holeCount
		if spacing-frac < 1e-9 {
			idx = (i + 1) % holeCount
		}
		next := (idx + 1) % holeCount
		holeAngle := float64(idx) * spacing
		return &Result{
			First:          Hole{Index: idx, Angle: holeAngle, Mass: mass},
			Second:         Hole{Index: next, Angle: float64(next) * spacing, Mass: 0},
			ResultantMass:  mass,
			ResultantAngle: wrapPos(holeAngle),
		}, nil
	}
	b := frac * math.Pi / 180
	s := math.Sin(spacing * math.Pi / 180)
	m1 := mass * math.Sin(spacing*math.Pi/180-b) / s
	m2 := mass * math.Sin(b) / s
	i1 := i % holeCount
	i2 := (i + 1) % holeCount
	r := &Result{
		First:          Hole{Index: i1, Angle: float64(i1) * spacing, Mass: m1},
		Second:         Hole{Index: i2, Angle: float64(i2) * spacing, Mass: m2},
		ResultantMass:  mass,
		ResultantAngle: angle,
	}
	return r, nil
}

// Recombine is the inverse: sum the two placed weight vectors. Returned so
// tests (and the API) can assert the resultant matches the requested vector.
func Recombine(r *Result) (mass, angle float64) {
	x := r.First.Mass*math.Cos(r.First.Angle*math.Pi/180) +
		r.Second.Mass*math.Cos(r.Second.Angle*math.Pi/180)
	y := r.First.Mass*math.Sin(r.First.Angle*math.Pi/180) +
		r.Second.Mass*math.Sin(r.Second.Angle*math.Pi/180)
	m := math.Hypot(x, y)
	if m < 1e-12 {
		return 0, 0
	}
	return m, wrapPos(math.Atan2(y, x) * 180 / math.Pi)
}

func wrapPos(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}
