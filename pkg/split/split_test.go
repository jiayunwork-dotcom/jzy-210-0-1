package split

import (
	"math"
	"testing"
)

func TestRequiresThreeHoles(t *testing.T) {
	for _, n := range []int{0, 1, 2, -3} {
		if _, err := Split(90, 10, n); err != ErrBadHoleCount {
			t.Errorf("holeCount %d: want ErrBadHoleCount, got %v", n, err)
		}
	}
}

// Splitting then recombining must reproduce the requested vector, and both
// masses must be non-negative for an angle strictly between the holes.
func TestSplitRecombine(t *testing.T) {
	cases := []struct {
		angle, mass float64
		holes       int
	}{
		{37, 40, 12}, {10, 25.5, 8}, {350, 12, 12}, {1.5, 99, 36}, {123.4, 7, 5},
	}
	for _, tc := range cases {
		r, err := Split(tc.angle, tc.mass, tc.holes)
		if err != nil {
			t.Fatal(err)
		}
		if r.First.Mass < -1e-9 || r.Second.Mass < -1e-9 {
			t.Errorf("%+v: negative mass %v, %v", tc, r.First.Mass, r.Second.Mass)
		}
		m, a := Recombine(r)
		if math.Abs(m-tc.mass) > 1e-9 {
			t.Errorf("angle %v holes %d: resultant mass %v want %v", tc.angle, tc.holes, m, tc.mass)
		}
		d := a - tc.angle
		for d > 180 {
			d -= 360
		}
		for d < -180 {
			d += 360
		}
		if math.Abs(d) > 1e-9 {
			t.Errorf("angle %v holes %d: resultant angle %v", tc.angle, tc.holes, a)
		}
		// Holes are actually adjacent and at valid indices.
		if (r.First.Index+1)%tc.holes != r.Second.Index && r.Second.Mass > 1e-12 {
			t.Errorf("holes %d and %d not adjacent", r.First.Index, r.Second.Index)
		}
	}
}

// Angle exactly on a hole: all mass in that hole, zero in the neighbor.
func TestSplitOnHole(t *testing.T) {
	r, err := Split(90, 40, 12)
	if err != nil {
		t.Fatal(err)
	}
	if r.First.Index != 3 || math.Abs(r.First.Mass-40) > 1e-12 {
		t.Errorf("got %+v", r.First)
	}
	if r.Second.Mass != 0 {
		t.Errorf("second mass should be 0, got %v", r.Second.Mass)
	}
}

// Midway between two equal-direction holes: equal split.
func TestSplitMidway(t *testing.T) {
	r, err := Split(15, 40, 12)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.First.Mass-r.Second.Mass) > 1e-9 {
		t.Errorf("midway split not equal: %v vs %v", r.First.Mass, r.Second.Mass)
	}
}

// The resultant of the two placed weight vectors exactly equals the requested
// vector computed independently in complex arithmetic.
func TestSplitVectorExact(t *testing.T) {
	angle, mass, n := 73.0, 33.3, 8
	r, err := Split(angle, mass, n)
	if err != nil {
		t.Fatal(err)
	}
	sum := func(idx int, mg float64) complex128 {
		ph := float64(idx) * 360 / float64(n) * math.Pi / 180
		return complex(mg*math.Cos(ph), mg*math.Sin(ph))
	}
	z := sum(r.First.Index, r.First.Mass) + sum(r.Second.Index, r.Second.Mass)
	ph := angle * math.Pi / 180
	want := complex(mass*math.Cos(ph), mass*math.Sin(ph))
	if math.Abs(real(z)-real(want)) > 1e-9 || math.Abs(imag(z)-imag(want)) > 1e-9 {
		t.Errorf("vector mismatch: %v vs %v", z, want)
	}
}
