package solve

import (
	"math"
)

// FitResult is an influence matrix estimate for one speed.
type FitResult struct {
	A          *CMatrix // nPoints × nPlanes complex µm/g
	Sigma2     []float64
	Uncert     [][]float64 // Uncert[point][plane] standard error of A entry (complex, µm/g)
	Log10Cond  float64
	Rank       int
	Residual   *CMatrix // fitted residual per run and point
	RowWeights []float64
}

// FitInfluence estimates the influence matrix A for one speed from runs with
// known added weights.
//
//   - weights[r][a] is the complex weight present on plane a during run r (g)
//   - vib[r][p] is the vibration measured at point p during run r (µm)
//   - v0[p] is the original-run vibration; the model fitted is
//     vib[r][p] - v0[p] = Σ_a A[p][a]·weights[r][a].
//
// At least nPlanes linearly independent runs are required. rowWeights may be
// nil (unit weights). Uncertainty per entry follows the standard complex
// least-squares covariance σ²_p (WᴴW)⁻¹; a floor is applied later by the
// history layer when fusing estimates.
func FitInfluence(weights [][]complex128, vib [][]complex128, v0 []complex128,
	rowWeights []float64, condLimit float64) (*FitResult, error) {
	nRuns := len(weights)
	if nRuns == 0 || len(vib) != nRuns {
		return nil, ErrDimension
	}
	nPlanes := len(weights[0])
	nPoints := len(vib[0])
	for r := 0; r < nRuns; r++ {
		if len(weights[r]) != nPlanes || len(vib[r]) != nPoints {
			return nil, ErrDimension
		}
	}
	if nRuns < nPlanes {
		return nil, ErrIllConditioned
	}

	// Design matrix W (runs × planes); right-hand side ΔV (runs × points).
	W := NewCMatrix(nRuns, nPlanes)
	DV := NewCMatrix(nRuns, nPoints)
	for r := 0; r < nRuns; r++ {
		for a := 0; a < nPlanes; a++ {
			W.set(r, a, weights[r][a])
		}
		for p := 0; p < nPoints; p++ {
			DV.set(r, p, vib[r][p]-v0[p])
		}
	}

	ls, err := WeightedLeastSquares(W, DV, rowWeights, condLimit)
	if err != nil {
		return nil, err
	}

	// ls.X is planes × points (coefficients for ΔV columns); transpose to
	// A with points × planes.
	A := NewCMatrix(nPoints, nPlanes)
	for p := 0; p < nPoints; p++ {
		for a := 0; a < nPlanes; a++ {
			A.set(p, a, ls.X.at(a, p))
		}
	}

	// Covariance diagonal of the coefficient estimate: σ²_p (R⁻¹R⁻ᴴ)_aa.
	// Recompute the (already weighted) design's R independently via the same
	// pivoted QR; ColNorms/perm from ls are permuted, so factor again and
	// invert R in pivoted order, then map back.
	covDiag := coefficientCovDiag(W, rowWeights)

	uncert := make([][]float64, nPoints)
	for p := range uncert {
		uncert[p] = make([]float64, nPlanes)
		for a := 0; a < nPlanes; a++ {
			if ls.Sigma2[p] > 0 && covDiag[a] > 0 {
				uncert[p][a] = math.Sqrt(ls.Sigma2[p] * covDiag[a])
			}
		}
	}

	return &FitResult{
		A:          A,
		Sigma2:     ls.Sigma2,
		Uncert:     uncert,
		Log10Cond:  ls.Log10Cond,
		Rank:       ls.Rank,
		Residual:   ls.Residual,
		RowWeights: rowWeights,
	}, nil
}

// coefficientCovDiag returns diag((W_wᴴ W_w)⁻¹) mapped back to original
// column order, where W_w is the row-weighted design.
func coefficientCovDiag(W *CMatrix, rowWeights []float64) []float64 {
	n := W.Cols
	Ww := NewCMatrix(W.Rows, W.Cols)
	for i := 0; i < W.Rows; i++ {
		s := 1.0
		if i < len(rowWeights) {
			s = rowWeights[i]
		}
		for j := 0; j < n; j++ {
			Ww.set(i, j, W.at(i, j)*complex(s, 0))
		}
	}
	qr, _, perm := householderQR(Ww)
	Rinv := NewCMatrix(n, n)
	for c := 0; c < n; c++ { // invert R column by column (pivoted order)
		e := make([]complex128, n)
		e[c] = 1
		x := backSolveUpperR(qr, e)
		for r := 0; r < n; r++ {
			Rinv.set(r, c, x[r])
		}
	}
	// diag(Rinv Rinvᴴ) in pivoted order
	out := make([]float64, n)
	for pivCol := 0; pivCol < n; pivCol++ {
		var s float64
		for k := 0; k < n; k++ {
			v := Rinv.at(pivCol, k)
			s += real(v)*real(v) + imag(v)*imag(v)
		}
		out[perm[pivCol]] = s
	}
	return out
}

// backSolveUpperR solves R x = e using the R stored in the top of qr.
func backSolveUpperR(qr *CMatrix, e []complex128) []complex128 {
	n := qr.Cols
	x := make([]complex128, n)
	for i := n - 1; i >= 0; i-- {
		s := e[i]
		for j := i + 1; j < n; j++ {
			s -= qr.at(i, j) * x[j]
		}
		x[i] = s / qr.at(i, i)
	}
	return x
}

// Correction is a computed correction weight plan.
type Correction struct {
	Weights   []complex128 // per plane, complex g in internal frame
	Residual  []complex128 // predicted v0 + A·w per measurement condition
	Log10Cond float64
	Rank      int
}

// SolveCorrection solves min_w ||Wrow (A w + v0)||₂, i.e. the correction
// weights that cancel the original vibration v0 under the influence model.
// Stack rows of multiple speeds to get one multi-speed correction plan.
func SolveCorrection(A *CMatrix, v0 []complex128, rowWeights []float64, condLimit float64) (*Correction, error) {
	if A.Rows != len(v0) {
		return nil, ErrDimension
	}
	neg := NewCMatrix(A.Rows, 1)
	for i := range v0 {
		neg.set(i, 0, -v0[i])
	}
	ls, err := WeightedLeastSquares(A, neg, rowWeights, condLimit)
	if err != nil {
		return nil, err
	}
	w := make([]complex128, A.Cols)
	res := make([]complex128, A.Rows)
	for a := range w {
		w[a] = ls.X.at(a, 0)
	}
	for i := range res {
		res[i] = ls.Residual.at(i, 0) // A w - (-v0) = A w + v0
	}
	return &Correction{Weights: w, Residual: res, Log10Cond: ls.Log10Cond, Rank: ls.Rank}, nil
}

// Predict returns A·w + v0 for arbitrary weights — one half of the model
// round-trip check.
func Predict(A *CMatrix, v0, w []complex128) []complex128 {
	out := make([]complex128, A.Rows)
	for i := 0; i < A.Rows; i++ {
		s := v0[i]
		for a := 0; a < A.Cols; a++ {
			s += A.at(i, a) * w[a]
		}
		out[i] = s
	}
	return out
}

// RecoverWeights solves A w = v - v0 — the inverse half of the round-trip.
// For a square system it returns the exact weights; for overdetermined rows
// it returns the WLS weights.
func RecoverWeights(A *CMatrix, v0, v []complex128, rowWeights []float64, condLimit float64) ([]complex128, error) {
	b := NewCMatrix(A.Rows, 1)
	for i := range v {
		b.set(i, 0, v[i]-v0[i])
	}
	ls, err := WeightedLeastSquares(A, b, rowWeights, condLimit)
	if err != nil {
		return nil, err
	}
	w := make([]complex128, A.Cols)
	for a := range w {
		w[a] = ls.X.at(a, 0)
	}
	return w, nil
}
