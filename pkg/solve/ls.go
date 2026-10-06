// Package solve solves the complex-valued influence-coefficient equations of
// field balancing.
//
// The linear model is
//
//	v = A·w + v0
//
// where v0 is the original (unbalanced) vibration, A is the influence
// coefficient matrix (rows = measurement points/speeds, columns = correction
// planes, entries complex µm/g), and w the added weights per plane (complex
// grams, i.e. amplitude and angle).
//
// When there are more measurement conditions (rows) than planes (columns)
// the system is overdetermined; correction weights minimize the weighted
// residual sum of squares (weighted least squares). See DESIGN.md for the
// rationale of choosing WLS (and why it bounds the residual against any
// single-point-only solution).
package solve

import (
	"errors"
	"fmt"
	"math"
	"math/cmplx"
)

// ErrIllConditioned is returned when the design matrix is (numerically)
// singular or too ill-conditioned to derive influence coefficients from.
var (
	ErrIllConditioned = errors.New("influence coefficient matrix is ill-conditioned (trial effect too small or planes not independent)")
	ErrDimension      = errors.New("dimension mismatch")
)

// CMatrix is a dense complex matrix in row-major order.
type CMatrix struct {
	Rows, Cols int
	Data       []complex128
}

func NewCMatrix(r, c int) *CMatrix { return &CMatrix{Rows: r, Cols: c, Data: make([]complex128, r*c)} }

func (m *CMatrix) At(i, j int) complex128     { return m.Data[i*m.Cols+j] }
func (m *CMatrix) Set(i, j int, v complex128) { m.Data[i*m.Cols+j] = v }

func (m *CMatrix) at(i, j int) complex128     { return m.Data[i*m.Cols+j] }
func (m *CMatrix) set(i, j int, v complex128) { m.Data[i*m.Cols+j] = v }

// LSResult holds a weighted least squares solution X ≈ argmin ||W(A X - B)||₂.
type LSResult struct {
	X           *CMatrix // solution, cols == B.Cols
	Residual    *CMatrix // A X - B (unweighted)
	Rank        int
	Sigma2      []float64 // residual variance estimate per right-hand side column
	Log10Cond   float64   // estimated log10 condition number of A (NaN if rank deficient)
	ColNorms    []float64 // original column 2-norms (for uncertainty scaling)
	Permutation []int     // pivot permutation applied during QR
}

// WeightedLeastSquares solves min_X || W (A X - B) ||₂ for each column of B.
// weights may be nil (unit weights) and scales rows; zero weight excludes the
// row. condLimit rejects solutions whose estimated condition number exceeds
// 10^condLimit (use a large value to disable).
//
// The QR factorization uses Householder reflectors with column pivoting,
// which handles square systems (exact solution up to round-off) and
// overdetermined systems uniformly, and exposes rank deficiency.
func WeightedLeastSquares(A, B *CMatrix, weights []float64, condLimit float64) (*LSResult, error) {
	if A.Rows != B.Rows {
		return nil, fmt.Errorf("%w: A has %d rows, B %d", ErrDimension, A.Rows, B.Rows)
	}
	nrhs := B.Cols

	M := NewCMatrix(A.Rows, A.Cols)
	RHS := NewCMatrix(B.Rows, nrhs)
	for i := 0; i < A.Rows; i++ {
		s := 1.0
		if i < len(weights) {
			s = weights[i]
		}
		if s < 0 {
			return nil, fmt.Errorf("weight %d negative", i)
		}
		for j := 0; j < A.Cols; j++ {
			M.set(i, j, A.at(i, j)*complex(s, 0))
		}
		for k := 0; k < nrhs; k++ {
			RHS.set(i, k, B.at(i, k)*complex(s, 0))
		}
	}

	colNorms := make([]float64, M.Cols)
	for j := range colNorms {
		var sum float64
		for i := 0; i < M.Rows; i++ {
			v := M.at(i, j)
			sum += real(v)*real(v) + imag(v)*imag(v)
		}
		colNorms[j] = math.Sqrt(sum)
	}

	qr, tau, perm := householderQR(M)
	k := min(M.Rows, M.Cols)

	// Apply Qᴴ to each RHS column.
	for r := 0; r < nrhs; r++ {
		y := make([]complex128, M.Rows)
		for i := range y {
			y[i] = RHS.at(i, r)
		}
		for p := 0; p < k; p++ {
			applyHouseholderLeft(qr, p, tau[p], y)
		}
		for i := range y {
			RHS.set(i, r, y[i])
		}
	}

	// Rank from relative diagonal of R, plus a condition estimate.
	rank := 0
	var rmax, rmin float64
	rmax = 0
	for p := 0; p < k; p++ {
		d := cmplx.Abs(qr.at(p, p))
		if d > rmax {
			rmax = d
		}
	}
	rmin = math.Inf(1)
	diagTol := rmax * 1e-10
	for p := 0; p < k; p++ {
		d := cmplx.Abs(qr.at(p, p))
		if d > diagTol && d > math.SmallestNonzeroFloat64 {
			rank++
			if d < rmin {
				rmin = d
			}
		}
	}
	log10Cond := math.NaN()
	if rank == M.Cols && M.Cols > 0 {
		log10Cond = math.Log10(rmax / rmin)
		if log10Cond > condLimit {
			return nil, fmt.Errorf("%w: estimated condition number 1e%.1f exceeds 1e%.1f",
				ErrIllConditioned, log10Cond, condLimit)
		}
	}
	if rank < M.Cols {
		return nil, fmt.Errorf("%w: rank %d of %d planes independent", ErrIllConditioned, rank, M.Cols)
	}

	X := NewCMatrix(M.Cols, nrhs)
	Sigma2 := make([]float64, nrhs)
	Res := NewCMatrix(A.Rows, nrhs)
	dof := float64(A.Rows - rank)
	if dof < 1 {
		dof = 1
	}
	for r := 0; r < nrhs; r++ {
		xp := backSolve(qr, RHS, r) // length M.Cols in pivoted order
		// Undo permutation: column perm[p] of the original holds x_p.
		x := make([]complex128, M.Cols)
		for p, orig := range perm {
			x[orig] = xp[p]
		}
		for j := 0; j < M.Cols; j++ {
			X.set(j, r, x[j])
		}
		var rss float64
		for i := 0; i < A.Rows; i++ {
			var dot complex128
			for j := 0; j < A.Cols; j++ {
				dot += A.at(i, j) * x[j]
			}
			e := dot - B.at(i, r)
			Res.set(i, r, e)
			rss += real(e)*real(e) + imag(e)*imag(e)
		}
		Sigma2[r] = rss / dof
	}
	return &LSResult{X: X, Residual: Res, Rank: rank, Sigma2: Sigma2,
		Log10Cond: log10Cond, ColNorms: colNorms, Permutation: perm}, nil
}

// householderQR overwrites m with R in the upper triangle and the essential
// Householder vectors below the diagonal. tau[p] is the scalar such that
// H_p = I - tau_p v_p v_pᴴ. Columns are pivoted (Businger–Golub): perm[j]
// is the original column index of current column j.
func householderQR(m *CMatrix) (qr *CMatrix, tau []complex128, perm []int) {
	qr = NewCMatrix(m.Rows, m.Cols)
	copy(qr.Data, m.Data)
	perm = make([]int, m.Cols)
	for j := range perm {
		perm[j] = j
	}
	colNorm := make([]float64, m.Cols)
	for j := 0; j < m.Cols; j++ {
		var sum float64
		for i := 0; i < m.Rows; i++ {
			v := qr.at(i, j)
			sum += real(v)*real(v) + imag(v)*imag(v)
		}
		colNorm[j] = sum // squared
	}
	k := min(m.Rows, m.Cols)
	tau = make([]complex128, k)
	for p := 0; p < k; p++ {
		// Pivot: pick the largest-norm column among p..cols-1 using the
		// tracked (squared) norms.
		best := p
		for j := p + 1; j < m.Cols; j++ {
			if colNorm[j] > colNorm[best] {
				best = j
			}
		}
		if best != p {
			for i := 0; i < m.Rows; i++ {
				a := qr.at(i, p)
				qr.set(i, p, qr.at(i, best))
				qr.set(i, best, a)
			}
			colNorm[p], colNorm[best] = colNorm[best], colNorm[p]
			perm[p], perm[best] = perm[best], perm[p]
		}

		var normx float64
		for i := p; i < m.Rows; i++ {
			v := qr.at(i, p)
			normx += real(v)*real(v) + imag(v)*imag(v)
		}
		normx = math.Sqrt(normx)
		x0 := qr.at(p, p)
		if normx == 0 {
			tau[p] = 0
			continue
		}
		alpha := -cmplx.Rect(normx, cmplx.Phase(x0)) // choose sign to avoid cancellation
		// H = I - tau v vᴴ with v[0] = 1, v[i] = x[i]/(x0-alpha) below,
		// tau = (alpha-x0)/alpha; this choice of alpha avoids cancellation.
		v0 := x0 - alpha
		tauP := (alpha - x0) / alpha
		tau[p] = tauP
		for i := p + 1; i < m.Rows; i++ {
			qr.set(i, p, qr.at(i, p)/v0) // store v (v[0]=1 implicitly)
		}
		qr.set(p, p, alpha)

		// Apply H to trailing columns: w = tau * V₂ᴴ R₂ ; R₂ -= V₂ wᴴ...
		for j := p + 1; j < m.Cols; j++ {
			var dot complex128
			dot += qr.at(p, j) // v[0] = 1
			for i := p + 1; i < m.Rows; i++ {
				dot += conj(qr.at(i, p)) * qr.at(i, j)
			}
			w := tauP * dot
			qr.set(p, j, qr.at(p, j)-w)
			for i := p + 1; i < m.Rows; i++ {
				qr.set(i, j, qr.at(i, j)-qr.at(i, p)*w)
			}
			// update tracked squared norm of column j
			var s float64
			for i := p + 1; i < m.Rows; i++ {
				v := qr.at(i, j)
				s += real(v)*real(v) + imag(v)*imag(v)
			}
			colNorm[j] = s
		}
	}
	return qr, tau, perm
}

// applyHouseholderLeft computes y ← H_p y (H is Hermitian, so Hᴴ = H) using
// the stored reflector of column p: v[0]=1, v[i]=qr.at(i,p) for i>p.
func applyHouseholderLeft(qr *CMatrix, p int, tau complex128, y []complex128) {
	if tau == 0 {
		return
	}
	var dot complex128
	dot += y[p]
	for i := p + 1; i < qr.Rows; i++ {
		dot += conj(qr.at(i, p)) * y[i]
	}
	w := tau * dot
	y[p] -= w
	for i := p + 1; i < qr.Rows; i++ {
		y[i] -= qr.at(i, p) * w
	}
}

func backSolve(qr *CMatrix, rhs *CMatrix, col int) []complex128 {
	n := qr.Cols
	x := make([]complex128, n)
	for i := n - 1; i >= 0; i-- {
		s := rhs.at(i, col)
		for j := i + 1; j < n; j++ {
			s -= qr.at(i, j) * x[j]
		}
		d := qr.at(i, i)
		if cmplx.Abs(d) == 0 {
			return x
		}
		x[i] = s / d
	}
	return x
}

func conj(z complex128) complex128 { return complex(real(z), -imag(z)) }
