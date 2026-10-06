package balance

import (
	"math"
	"math/cmplx"
	"testing"

	"balancing/internal/vec"
)

func cPolar(amp, phaseDeg float64) complex128 { return vec.Polar{Amp: amp, Phase: phaseDeg}.C() }

func pApproxE(t *testing.T, got, want complex128, tol float64, name string) {
	t.Helper()
	if cmplx.Abs(got-want) > tol {
		t.Errorf("%s = %.4f∠%.3f°, want %.4f∠%.3f° (Δ%.2e)",
			name, cmplx.Abs(got), deg(got), cmplx.Abs(want), deg(want), cmplx.Abs(got-want))
	}
}

func deg(z complex128) float64 { return math.Atan2(imag(z), real(z)) * 180 / math.Pi }

// TestReferenceExample reproduces the single-plane reference case from the
// specification:
//
//	original 80 μm∠30°; 20 g trial at 0° -> 89.44 μm∠56.57°
//	influence coefficient 2 μm/g∠120°; correction 40 g∠90°.
func TestReferenceExample(t *testing.T) {
	v0 := []complex128{cPolar(80, 30)}
	runs := []LoadedRun{{
		Planes: 1,
		W:      []complex128{complex(20, 0)},
		V:      []complex128{cPolar(89.44, 56.57)},
	}}
	h, rcond, err := EstimateInfluence(v0, runs, nil)
	if err != nil {
		t.Fatalf("EstimateInfluence: %v", err)
	}
	if rcond < MinRcond {
		t.Fatalf("rcond %g below minimum", rcond)
	}
	coef := h.At(0, 0)
	if math.Abs(cmplx.Abs(coef)-2) > 1e-3 {
		t.Errorf("|H| = %.6f μm/g, want 2", cmplx.Abs(coef))
	}
	if d := math.Abs(vec.Norm(deg(coef)) - 120); d > 1e-2 {
		t.Errorf("angle(H) = %.4f°, want 120°", deg(coef))
	}

	w, residual, _, err := Correct(h, v0, nil)
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if math.Abs(cmplx.Abs(w[0])-40) > 1e-2 {
		t.Errorf("correction mass = %.6f g, want 40", cmplx.Abs(w[0]))
	}
	if d := math.Abs(vec.Norm(deg(w[0])) - 90); d > 1e-2 {
		t.Errorf("correction angle = %.4f°, want 90°", deg(w[0]))
	}
	// Inputs are rounded (89.44 μm∠56.57°), so the exact-input square
	// residual is small but not machine-zero here.
	if cmplx.Abs(residual[0]) > 1e-2 {
		t.Errorf("square residual = %.3e, want ~0", cmplx.Abs(residual[0]))
	}
}

// TestSquareResidualZero checks several square systems, including a full
// 2-plane / 2-point combined case: with two diagonal trial runs the estimate
// recovers the planted matrix, and the correction zeros every point.
func TestSquareResidualZero(t *testing.T) {
	h0 := MatrixFromRows(
		[]complex128{cPolar(1.5, 40), cPolar(0.8, -30)},
		[]complex128{cPolar(0.6, 150), cPolar(1.2, 200)},
	)
	v0 := []complex128{cPolar(60, 20), cPolar(45, 210)}
	// two trial runs, one weight per plane
	runs := []LoadedRun{
		{Planes: 2, W: []complex128{complex(15, 0), 0}, V: add(v0, h0.MulVec([]complex128{complex(15, 0), 0}))},
		{Planes: 2, W: []complex128{0, complex(12, 0)}, V: add(v0, h0.MulVec([]complex128{0, complex(12, 0)}))},
	}
	h, _, err := EstimateInfluence(v0, runs, nil)
	if err != nil {
		t.Fatalf("EstimateInfluence: %v", err)
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			pApproxE(t, h.At(i, j), h0.At(i, j), 1e-9, "recovered H")
		}
	}
	w, residual, _, err := Correct(h, v0, nil)
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if len(w) != 2 || cmplx.Abs(residual[0]) > 1e-8 || cmplx.Abs(residual[1]) > 1e-8 {
		t.Fatalf("residual = %v, want zero; w=%v", residual, w)
	}
}

func add(a, b []complex128) []complex128 {
	out := make([]complex128, len(a))
	for i := range a {
		out[i] = a[i] + b[i]
	}
	return out
}

// TestPredictCorrectRoundtrip: for ANY weight vector, predicting the vibration
// with the model and then solving for a correction that yields that vibration
// must return the same vector. Equivalently Correct(H, Predict(H,v0,w)) = -w.
func TestPredictCorrectRoundtrip(t *testing.T) {
	for n := 1; n <= 2; n++ {
		h0 := randomMatrix(n, n, int64(n)+7)
		v0 := make([]complex128, n)
		for i := range v0 {
			v0[i] = cPolar(30+float64(20*i), float64(37*i+10))
		}
		wantW := make([]complex128, n)
		for i := range wantW {
			wantW[i] = cPolar(10+float64(5*i), float64(53*(i+1)))
		}
		vpred := Predict(h0, v0, wantW)
		// correction that would produce vpred from v0 is exactly wantW;
		// use Correct on (vpred - v0) via a synthetic target: solve H w = vpred-v0.
		b := make([]complex128, n)
		for i := range b {
			b[i] = vpred[i] - v0[i]
		}
		got, _, err := SolveSquare(h0, b)
		if err != nil {
			t.Fatalf("SolveSquare: %v", err)
		}
		for i := range got {
			pApproxE(t, got[i], wantW[i], 1e-9, "roundtrip weight")
		}

		// And the stated self-consistency: substituting the proposed
		// correction into the model gives ~zero residual.
		wCorr, residual, _, err := Correct(h0, v0, nil)
		if err != nil {
			t.Fatalf("Correct: %v", err)
		}
		for _, z := range residual {
			if cmplx.Abs(z) > 1e-8 {
				t.Errorf("n=%d residual %.3e, want 0", n, cmplx.Abs(z))
			}
		}
		check := Predict(h0, v0, wCorr)
		for i := range check {
			pApproxE(t, check[i], 0, 1e-8, "model substitution")
		}
	}
}

// TestOverdeterminedResidual: with 3 measurement points and 1 plane, the
// least-squares residual norm must be no larger than balancing against ANY
// single point alone.
func TestOverdeterminedResidual(t *testing.T) {
	h := MatrixFromRows(
		[]complex128{cPolar(1.0, 10)},
		[]complex128{cPolar(1.3, 95)},
		[]complex128{cPolar(0.9, 200)},
	)
	v0 := []complex128{cPolar(70, 30), cPolar(50, 160), cPolar(55, 280)}
	w, lsResidual, rcond, err := Correct(h, v0, nil)
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if rcond <= MinRcond {
		t.Fatalf("rcond %g too small", rcond)
	}
	lsNorm := vectorNorm(lsResidual)

	for i := 0; i < len(v0); i++ {
		// weight that cancels point i alone
		wi := -v0[i] / h.At(i, 0)
		ri := make([]complex128, len(v0))
		for k := range v0 {
			ri[k] = v0[k] + h.At(k, 0)*wi
		}
		if vectorNorm(ri) < lsNorm-1e-9 {
			t.Errorf("LS residual %.6f larger than balancing point %d alone (%.6f); w=%v",
				lsNorm, i, vectorNorm(ri), w)
		}
	}
	// The LS residual must be orthogonal to the design column: hᴴ r = 0.
	var dot complex128
	for i := range lsResidual {
		dot += cmplx.Conj(h.At(i, 0)) * lsResidual[i]
	}
	if cmplx.Abs(dot) > 1e-8 {
		t.Errorf("residual not orthogonal to H column: %v", dot)
	}
}

// TestWeightedLeastSquares trusts only the first two points (zero weight would
// be invalid; use a large relative weight) and checks point 3 dominates the
// residual while points 1-2 are nearly cancelled.
func TestWeightedLeastSquares(t *testing.T) {
	h := MatrixFromRows(
		[]complex128{cPolar(1, 0)},
		[]complex128{cPolar(1, 0)},
		[]complex128{cPolar(1, 0)},
	)
	v0 := []complex128{complex(10, 0), complex(10, 0), complex(100, 0)}
	weights := []float64{100, 100, 0.01}
	w, residual, _, err := Correct(h, v0, weights)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(real(w[0])+10) > 0.01 || math.Abs(imag(w[0])) > 1e-9 {
		t.Errorf("weighted correction = %v, want ~-10", w)
	}
	if cmplx.Abs(residual[0]) > 0.05 || cmplx.Abs(residual[1]) > 0.05 {
		t.Errorf("trusted points not cancelled: %v %v", residual[0], residual[1])
	}
	if cmplx.Abs(residual[2]-90) > 0.05 {
		t.Errorf("distrusted point residual = %v, want ~90", residual[2])
	}
}

func vectorNorm(v []complex128) float64 {
	var s float64
	for _, z := range v {
		s += real(z)*real(z) + imag(z)*imag(z)
	}
	return math.Sqrt(s)
}

// simple deterministic pseudo-random complex matrix for tests
func randomMatrix(r, c int, seed int64) Matrix {
	m := NewMatrix(r, c)
	st := seed
	next := func() float64 {
		st = st*6364136223846793005 + 1442695040888963407
		u := uint64(st) >> 11
		return float64(u%10000) / 10000
	}
	for i := 0; i < r; i++ {
		for j := 0; j < c; j++ {
			m.Set(i, j, complex(2*next()-1, 2*next()-1))
		}
	}
	return m
}
