package split

import (
	"math"
	"testing"

	"balancing/internal/vec"
)

func polar(amp, deg float64) complex128 { return vec.Polar{Amp: amp, Phase: deg}.C() }

func TestSplitExactOnHole(t *testing.T) {
	// 40 g at internal 90° lands exactly on an 8-hole ring's hole at 90°.
	hw, err := Plan(vec.Canonical(), Plane{Count: 8}, polar(40, 90))
	if err != nil {
		t.Fatal(err)
	}
	if len(hw) != 1 || hw[0].HoleIndex != 2 {
		t.Fatalf("got %+v, want single weight in hole 2 (90°)", hw)
	}
	if math.Abs(hw[0].Grams-40) > 1e-9 || math.Abs(hw[0].AngleDeg-90) > 1e-9 {
		t.Fatalf("got %+v, want 40g at 90°", hw[0])
	}
}

func TestSplitBetweenHoles(t *testing.T) {
	// 40 g at internal 112.5° splits equally over the holes at 90°/135°.
	hw, err := Plan(vec.Canonical(), Plane{Count: 8}, polar(40, 112.5))
	if err != nil {
		t.Fatal(err)
	}
	if len(hw) != 2 {
		t.Fatalf("got %d hole weights, want 2", len(hw))
	}
	want := 40 * math.Sin(22.5*math.Pi/180) / math.Sin(45*math.Pi/180)
	for i, ang := range []float64{90, 135} {
		if math.Abs(hw[i].Grams-want) > 1e-9 {
			t.Errorf("hole %d grams %.6f, want %.6f", i, hw[i].Grams, want)
		}
		if math.Abs(hw[i].AngleDeg-ang) > 1e-9 {
			t.Errorf("hole %d angle %.3f, want %.3f", i, hw[i].AngleDeg, ang)
		}
	}
	// the synthesis must be identical to the requested vector
	got := Reconstruct(vec.Canonical(), hw)
	wantVec := polar(40, 112.5)
	if math.Hypot(real(got)-real(wantVec), imag(got)-imag(wantVec)) > 1e-10 {
		t.Errorf("reconstructed %v, want %v", got, wantVec)
	}
}

func TestSplitGeneralSynthesisExact(t *testing.T) {
	// For many hole counts and target angles the split vector sum must equal
	// the requested vector, both weights non-negative.
	for _, n := range []int{3, 5, 6, 7, 12, 16, 24} {
		layout := Plane{Count: n, FirstHoleDeg: 7}
		for ang := 0.0; ang < 360; ang += 3.7 {
			target := polar(33.3, ang)
			hw, err := Plan(vec.Canonical(), layout, target)
			if err != nil {
				t.Fatalf("n=%d ang=%.1f: %v", n, ang, err)
			}
			if len(hw) < 1 || len(hw) > 2 {
				t.Fatalf("n=%d ang=%.1f: %d holes used", n, ang, len(hw))
			}
			for _, h := range hw {
				if h.Grams < -1e-9 {
					t.Fatalf("n=%d ang=%.1f: negative hole weight %v", n, ang, h)
				}
			}
			got := Reconstruct(vec.Canonical(), hw)
			if math.Hypot(real(got)-real(target), imag(got)-imag(target)) > 1e-8 {
				t.Fatalf("n=%d ang=%.1f: reconstructed %v != %v", n, ang, got, target)
			}
		}
	}
}

func TestSplitUnderConvention(t *testing.T) {
	// With-rotation machine, reference at internal 90°: an internal
	// correction at 90° is expressed at external 0° and must land on the
	// external hole ring accordingly.
	conv := vec.Convention{ReferenceDeg: 90, Direction: vec.WithRotation}
	layout := Plane{Count: 8, FirstHoleDeg: 0}
	hw, err := Plan(conv, layout, polar(40, 90)) // internal 90 = external 0
	if err != nil {
		t.Fatal(err)
	}
	if len(hw) != 1 || hw[0].HoleIndex != 0 || math.Abs(hw[0].AngleDeg) > 1e-9 {
		t.Fatalf("got %+v, want hole 0 at external 0°", hw)
	}
	if z := Reconstruct(conv, hw); math.Hypot(real(z), imag(z)-40) > 1e-9 {
		t.Fatalf("reconstructed %v, want 40i", z)
	}
}

func TestTooFewHoles(t *testing.T) {
	if _, err := Plan(vec.Canonical(), Plane{Count: 2}, polar(10, 45)); err != ErrTooFewHoles {
		t.Fatalf("err = %v, want ErrTooFewHoles", err)
	}
	if _, err := Plans(vec.Canonical(), []*Plane{{Count: 1}}, []complex128{polar(5, 0)}); err != ErrTooFewHoles {
		t.Fatalf("Plans err = %v", err)
	}
}

func TestArbitraryAnglePlane(t *testing.T) {
	hw, err := Plans(vec.Canonical(), []*Plane{nil}, []complex128{polar(12.5, 77.7)})
	if err != nil {
		t.Fatal(err)
	}
	if len(hw[0]) != 1 || hw[0][0].HoleIndex != -1 ||
		math.Abs(hw[0][0].Grams-12.5) > 1e-9 || math.Abs(hw[0][0].AngleDeg-77.7) > 1e-9 {
		t.Fatalf("got %+v", hw[0])
	}
}
