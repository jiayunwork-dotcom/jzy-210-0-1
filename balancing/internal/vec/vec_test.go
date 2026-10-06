package vec

import (
	"math"
	"testing"
)

func approxE(t *testing.T, got, want, tol float64, name string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f", name, got, want)
	}
}

func TestNorm(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0, 0}, {360, 0}, {-90, 270}, {720, 0}, {359.999, 359.999},
	} {
		approxE(t, Norm(c.in), c.want, 1e-9, "Norm")
	}
}

func TestCanonicalRoundtrip(t *testing.T) {
	c := Canonical()
	for _, a := range []float64{0, 30, 120.5, 270, 359.9} {
		approxE(t, c.ToExternal(c.ToInternal(a)), a, 1e-12, "canonical roundtrip")
	}
}

// A machine whose 0° sits at internal 60° and whose angles increase WITH
// rotation: internal = reference - external.
func TestReferenceOffsetAndDirection(t *testing.T) {
	c := Convention{ReferenceDeg: 60, Direction: WithRotation}
	cases := []struct{ ext, internal float64 }{
		{0, 60},   // external 0 = the keyphasor at internal 60
		{90, 330}, // going 90° with rotation = internal 60-90 = -30 = 330
		{120, 300},
		{300, 120},
	}
	for _, tc := range cases {
		approxE(t, c.ToInternal(tc.ext), tc.internal, 1e-9, "ToInternal")
		approxE(t, c.ToExternal(tc.internal), tc.ext, 1e-9, "ToExternal")
	}

	// Against-rotation variant: internal = reference + external.
	a := Convention{ReferenceDeg: 60, Direction: AgainstRotation}
	approxE(t, a.ToInternal(90), 150, 1e-9, "against ToInternal")
	approxE(t, a.ToExternal(150), 90, 1e-9, "against ToExternal")
}

func TestPolarComplexRoundtrip(t *testing.T) {
	for _, p := range []Polar{{80, 30}, {89.44, 56.57}, {0, 0}, {2, 120}} {
		z := p.C()
		q := FromC(z)
		approxE(t, q.Amp, p.Amp, 1e-10, "amp")
		approxE(t, q.Phase, p.Phase, 1e-9, "phase")
	}
}
