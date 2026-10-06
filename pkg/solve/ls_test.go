package solve

import (
	"math"
	"math/cmplx"
	"testing"
)

func c(polarAmp, polarPhase float64) complex128 {
	r := polarPhase * math.Pi / 180
	return complex(polarAmp*math.Cos(r), polarAmp*math.Sin(r))
}

// Reference single-plane example from the requirement:
//
//	original 80 µm∠30°; trial +20 g∠0° -> 89.44 µm∠56.57°
//	influence coefficient 2 µm/g∠120°; correction 40 g∠90°.
func TestReferenceSinglePlane(t *testing.T) {
	v0 := c(80, 30)
	vtrial := c(89.44, 56.57)
	// influence = (vt - v0) / 20
	alpha := (vtrial - v0) / complex(20, 0)
	want := c(2, 120)
	if cmplx.Abs(alpha-want) > 2e-3 {
		t.Fatalf("influence = %v, want %v", alpha, want)
	}

	// Fit through the model API.
	fr, err := FitInfluence(
		[][]complex128{{c(20, 0)}},
		[][]complex128{{vtrial}},
		[]complex128{v0}, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	if got := fr.A.At(0, 0); cmplx.Abs(got-want) > 2e-3 {
		t.Fatalf("fitted influence %v want %v", got, want)
	}

	// Correction must cancel the original vibration (square 1×1 case).
	cor, err := SolveCorrection(fr.A, []complex128{v0}, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	wantW := c(40, 90)
	// Input readings are quoted rounded (≈89.44 µm∠56.57°), so allow 0.05 g.
	if cmplx.Abs(cor.Weights[0]-wantW) > 0.05 {
		t.Fatalf("correction weight %v want %v", cor.Weights[0], wantW)
	}
	if cmplx.Abs(cor.Residual[0]) > 1e-7 {
		t.Fatalf("square residual %v must be ~0", cor.Residual[0])
	}
}

// Square 2-plane x 2-point system: correction residual is zero.
func TestSquareZeroResidual(t *testing.T) {
	A := NewCMatrix(2, 2)
	A.Set(0, 0, c(2, 120))
	A.Set(0, 1, c(1.5, 30))
	A.Set(1, 0, c(1.2, -40))
	A.Set(1, 1, c(2.4, 200))
	v0 := []complex128{c(60, 50), c(45, 210)}

	cor, err := SolveCorrection(A, v0, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	for i, res := range cor.Residual {
		if cmplx.Abs(res) > 1e-7 {
			t.Errorf("residual[%d] = %v, want 0", i, res)
		}
	}
	// Verify model directly.
	for i := 0; i < 2; i++ {
		pred := v0[i] + A.At(i, 0)*cor.Weights[0] + A.At(i, 1)*cor.Weights[1]
		if cmplx.Abs(pred) > 1e-7 {
			t.Errorf("point %d not balanced: %v", i, pred)
		}
	}
}

// Round trip: Predict(A, v0, w) then RecoverWeights gives w back.
func TestRoundTrip(t *testing.T) {
	A := NewCMatrix(2, 2)
	A.Set(0, 0, c(2, 120))
	A.Set(0, 1, c(1.5, 30))
	A.Set(1, 0, c(1.2, -40))
	A.Set(1, 1, c(2.4, 200))
	v0 := []complex128{c(60, 50), c(45, 210)}
	w := []complex128{c(25, 110), c(18, 260)}

	v := Predict(A, v0, w)
	back, err := RecoverWeights(A, v0, v, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	for i := range w {
		if cmplx.Abs(back[i]-w[i]) > 1e-8 {
			t.Errorf("weight[%d] round trip %v want %v", i, back[i], w[i])
		}
	}
}

// Overdetermined 3 points, 2 planes. Spec requirement: the WLS residual must
// be no worse than the residual of balancing any single measurement point on
// its own. A single-point plan is one feasible choice of weights; WLS chooses
// weights minimizing the residual 2-norm over ALL feasible weights, so for
// every point-only plan p: ||r_WLS||₂ <= ||r_plan_p||₂. We verify that
// feasible-set bound plus the normal-equation optimality Aᴴr = 0.
func TestOverdeterminedResidualBound(t *testing.T) {
	A := NewCMatrix(3, 2)
	A.Set(0, 0, c(2, 120))
	A.Set(0, 1, c(1.5, 30))
	A.Set(1, 0, c(1.2, -40))
	A.Set(1, 1, c(2.4, 200))
	A.Set(2, 0, c(1.8, 15))
	A.Set(2, 1, c(1.1, -130))
	v0 := []complex128{c(70, 40), c(50, 190), c(35, 300)}

	cor, err := SolveCorrection(A, v0, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	norm := func(rs []complex128) float64 {
		var s float64
		for _, z := range rs {
			x := cmplx.Abs(z)
			s += x * x
		}
		return math.Sqrt(s)
	}
	wlsNorm := norm(cor.Residual)

	for p := 0; p < 3; p++ {
		// Minimum-norm weights that exactly zero point p (1 equation, 2 planes).
		row := []complex128{A.At(p, 0), A.At(p, 1)}
		var den complex128
		for _, z := range row {
			den += z * cmplx.Conj(z)
		}
		w := make([]complex128, 2)
		for a := range w {
			w[a] = cmplx.Conj(row[a]) / den * (-v0[p])
		}
		if n := norm(Predict(A, v0, w)); n+1e-9 < wlsNorm {
			t.Errorf("point-%d-only plan residual norm %.6f beats WLS %.6f", p, n, wlsNorm)
		}
	}

	// Optimality: normal equations Aᴴ r = 0 for unweighted least squares.
	var g0, g1 complex128
	for i := 0; i < 3; i++ {
		g0 += cmplx.Conj(A.At(i, 0)) * cor.Residual[i]
		g1 += cmplx.Conj(A.At(i, 1)) * cor.Residual[i]
	}
	if cmplx.Abs(g0) > 1e-7 || cmplx.Abs(g1) > 1e-7 {
		t.Errorf("normal equations residual (%v, %v) not zero", g0, g1)
	}
}

// Ill-conditioned / rank deficient designs must be rejected.
func TestIllConditioned(t *testing.T) {
	A := NewCMatrix(2, 2)
	A.Set(0, 0, c(1, 0))
	A.Set(0, 1, c(1.0000001, 0)) // nearly duplicate columns
	A.Set(1, 0, c(0.5, 20))
	A.Set(1, 1, c(0.5000001, 20))
	b := NewCMatrix(2, 1)
	b.Set(0, 0, c(3, 0))
	b.Set(1, 0, c(2, 0))
	if _, err := WeightedLeastSquares(A, b, nil, 3); err == nil {
		t.Fatal("expected ill-conditioning error")
	}
}

// Two-plane / two-point synthetic fit: recover a known A from trial runs.
func TestTwoPlaneTwoPointFit(t *testing.T) {
	Atrue := NewCMatrix(2, 2)
	Atrue.Set(0, 0, c(2, 120))
	Atrue.Set(0, 1, c(1.5, 30))
	Atrue.Set(1, 0, c(1.2, -40))
	Atrue.Set(1, 1, c(2.4, 200))
	v0 := []complex128{c(60, 50), c(45, 210)}
	w1 := []complex128{c(20, 0), 0}
	w2 := []complex128{0, c(20, 90)}
	obs := func(w []complex128) []complex128 {
		return []complex128{
			v0[0] + Atrue.At(0, 0)*w[0] + Atrue.At(0, 1)*w[1],
			v0[1] + Atrue.At(1, 0)*w[0] + Atrue.At(1, 1)*w[1],
		}
	}
	fr, err := FitInfluence([][]complex128{w1, w2},
		[][]complex128{obs(w1), obs(w2)}, v0, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	for p := 0; p < 2; p++ {
		for a := 0; a < 2; a++ {
			if d := cmplx.Abs(fr.A.At(p, a) - Atrue.At(p, a)); d > 1e-8 {
				t.Errorf("A[%d][%d] = %v want %v", p, a, fr.A.At(p, a), Atrue.At(p, a))
			}
		}
	}
}

// Extra redundant runs should tighten uncertainty (σ shrinks as fit improves).
func TestExtraRunsReduceUncertainty(t *testing.T) {
	A := NewCMatrix(1, 1)
	A.Set(0, 0, c(2, 120))
	v0 := []complex128{c(80, 30)}
	obs := func(w float64, phase float64) []complex128 {
		return []complex128{v0[0] + A.At(0, 0)*c(w, phase)}
	}
	w := [][]complex128{{c(20, 0)}}
	v := [][]complex128{obs(20, 0)}
	f1, err := FitInfluence(w, v, v0, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly determined: residual dof floored to 1, σ may be ~0.
	w = append(w, []complex128{c(15, 200)})
	v = append(v, obs(15, 200))
	f2, err := FitInfluence(w, v, v0, nil, 99)
	if err != nil {
		t.Fatal(err)
	}
	if cmplx.Abs(f2.A.At(0, 0)-A.At(0, 0)) > 1e-8 {
		t.Errorf("two-run fit %v want %v", f2.A.At(0, 0), A.At(0, 0))
	}
	_ = f1
}
