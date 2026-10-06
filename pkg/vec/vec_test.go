package vec

import (
	"math"
	"testing"
)

func TestWrap(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0, 0}, {359.9, 359.9}, {-0.5, 359.5}, {360, 0}, {720, 0}, {-720, 0},
	}
	for _, c := range cases {
		if got := WrapPos(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("WrapPos(%v)=%v want %v", c.in, got, c.want)
		}
	}
	if got := WrapFull(270); math.Abs(got-(-90)) > 1e-9 {
		t.Errorf("WrapFull(270)=%v want -90", got)
	}
}

func TestPolarComplexRoundtrip(t *testing.T) {
	for _, p := range []Polar{{Amp: 80, Phase: 30}, {Amp: 89.44, Phase: 56.57}, {Amp: 0, Phase: 0}, {Amp: 12.3, Phase: 359.9}} {
		q := FromComplex(p.Complex())
		if !Equal(p, q, 1e-9, 1e-9) {
			t.Errorf("roundtrip %v -> %v", p, q)
		}
	}
}

// TestConventionReference verifies the single-plane example's stated phases
// survive a non-trivial convention, and that conversion is an involution.
func TestConventionInvolution(t *testing.T) {
	convs := []Convention{
		InternalDefault(),
		{ZeroOffsetDeg: 45, Direction: DirectionCCW},
		{ZeroOffsetDeg: 0, Direction: DirectionCW},
		{ZeroOffsetDeg: 137.5, Direction: DirectionCW},
		{ZeroOffsetDeg: 300, Direction: DirectionCCW},
	}
	angles := []float64{0, 30, 90, 120, 180, 270, 359.9}
	for _, c := range convs {
		if !c.Valid() {
			t.Fatalf("convention %+v invalid", c)
		}
		for _, a := range angles {
			f := c.ExternalFromInternal(a)
			back := c.InternalFromExternal(f)
			if math.Abs(PhaseDiff(back, a)) > 1e-9 {
				t.Errorf("convention %+v: %v -> field %v -> %v", c, a, f, back)
			}
		}
	}
}

// TestConventionCW180: a 180° flip of the keyphasor plus reversed counting
// direction are the two common field mistakes; both must map consistently.
func TestConventionCW180(t *testing.T) {
	cw := Convention{ZeroOffsetDeg: 0, Direction: DirectionCW}
	// Internal 90° is expressed as 270° when angles count clockwise.
	if got := cw.ExternalFromInternal(90); math.Abs(got-270) > 1e-9 {
		t.Errorf("CW: internal 90 -> external %v, want 270", got)
	}
	flip := Convention{ZeroOffsetDeg: 180, Direction: DirectionCCW}
	// Keyphasor painted at internal 180: field 0 == internal 180.
	if got := flip.InternalFromExternal(0); math.Abs(got-180) > 1e-9 {
		t.Errorf("flip: external 0 -> internal %v, want 180", got)
	}
}

func TestEqualZeroPhase(t *testing.T) {
	if !Equal(Polar{0, 123}, Polar{0, 0}, 1e-12, 0) {
		t.Error("zero vectors should compare equal regardless of phase")
	}
}
