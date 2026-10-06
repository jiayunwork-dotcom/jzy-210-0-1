package balance

import (
	"math"
	"math/cmplx"
)

const eps = 1e-14

// solveSquare solves A x = b for a square complex matrix A using Gaussian
// elimination with partial pivoting (by complex magnitude). It also returns an
// estimate of the reciprocal 1-norm condition number of A. Returns
// ErrSingular when A is (numerically) singular.
func solveSquare(a Matrix, b []complex128) ([]complex128, float64, error) {
	n := a.R
	m := NewMatrix(n, n)
	copy(m.v, a.v)
	x := make([]complex128, n)
	copy(x, b)

	// Row scale/perm tracking for the 1-norm inverse estimate.
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}

	for k := 0; k < n; k++ {
		// pivot
		piv := k
		best := cmplx.Abs(m.at(k, k))
		for i := k + 1; i < n; i++ {
			if v := cmplx.Abs(m.at(i, k)); v > best {
				best, piv = v, i
			}
		}
		if best <= eps {
			return nil, 0, ErrSingular
		}
		if piv != k {
			for j := 0; j < n; j++ {
				tmp := m.at(k, j)
				m.set(k, j, m.at(piv, j))
				m.set(piv, j, tmp)
			}
			x[k], x[piv] = x[piv], x[k]
			perm[k], perm[piv] = perm[piv], perm[k]
		}
		akk := m.at(k, k)
		for i := k + 1; i < n; i++ {
			f := m.at(i, k) / akk
			if f != 0 {
				m.set(i, k, 0)
				for j := k + 1; j < n; j++ {
					m.set(i, j, m.at(i, j)-f*m.at(k, j))
				}
				x[i] -= f * x[k]
			}
		}
	}

	// back substitution
	for i := n - 1; i >= 0; i-- {
		s := x[i]
		for j := i + 1; j < n; j++ {
			s -= m.at(i, j) * x[j]
		}
		x[i] = s / m.at(i, i)
	}

	return x, rcondOneNorm(m, perm), nil
}

// rcondOneNorm estimates 1/cond1(A) from the upper-triangular factor produced
// by solveSquare (m holds U; the elimination pivots encoded in perm). The
// estimate uses the cheap "inverse of U" bound: ||U^-1||1 bounded row-wise
// without forming the inverse, combined with the observed pivot growth. This
// is an engineering estimate sufficient for deciding whether the effect matrix
// is degenerate (e.g. trial weights mounted in nearly identical directions).
func rcondOneNorm(u Matrix, perm []int) float64 {
	n := u.R
	u1 := oneNorm(u)
	if u1 == 0 {
		return 0
	}
	// Solve U X = I column by column, tracking the maximum column 1-norm
	// (estimates ||U^-1||_1 without storing the inverse).
	var invNorm float64
	for col := 0; col < n; col++ {
		e := make([]complex128, n)
		e[col] = 1
		for i := n - 1; i >= 0; i-- {
			s := e[i]
			for j := i + 1; j < n; j++ {
				s -= u.at(i, j) * e[j]
			}
			p := cmplx.Abs(u.at(i, i))
			if p <= eps {
				return 0
			}
			e[i] = s / u.at(i, i)
		}
		var cn float64
		for _, z := range e {
			cn += cmplx.Abs(z)
		}
		invNorm = math.Max(invNorm, cn)
	}
	// Rows were pivoted; permutation does not change norms.
	rcond := 1 / (u1 * invNorm)
	if rcond > 1 {
		rcond = 1
	}
	return rcond
}

func oneNorm(m Matrix) float64 {
	best := 0.0
	for j := 0; j < m.C; j++ {
		s := 0.0
		for i := 0; i < m.R; i++ {
			s += cmplx.Abs(m.at(i, j))
		}
		best = math.Max(best, s)
	}
	return best
}

// LeastSquares solves min_x ||W(A x - b)||_2 for complex A (m rows, n cols,
// m >= n) via the normal equations (AᴴW²A)x = AᴴW²b.
//
// Weighted complex least squares is the chosen criterion for overdetermined
// systems (more measurement points than correction planes): it minimizes the
// weighted sum of squared residual vibration amplitudes, which is the maximum-
// likelihood estimate when sensor noise is zero-mean, and it yields the unique
// correction whose residual norm is provably no larger than that produced by
// balancing the system against any single measurement point alone (the
// single-point solution is a feasible x, and the LS minimizer can only do
// better). Per-point weights let an intrinsically noisy bearing be trusted
// less; all weights are 1 by default.
//
// Returns the solution and an estimate of the reciprocal condition number of
// the normal-equations matrix.
func LeastSquares(a Matrix, b []complex128, weights []float64) ([]complex128, float64, error) {
	m, n := a.R, a.C
	if m < n || len(b) != m {
		return nil, 0, ErrDimension
	}
	w := make([]float64, m)
	for i := range w {
		w[i] = 1
		if i < len(weights) && weights[i] > 0 {
			w[i] = weights[i]
		}
	}
	wa := NewMatrix(m, n)
	wb := make([]complex128, m)
	for i := 0; i < m; i++ {
		for j := 0; j < n; j++ {
			wa.set(i, j, a.at(i, j)*complex(w[i], 0))
		}
		wb[i] = b[i] * complex(w[i], 0)
	}
	g := wa.T().Mul(wa) // n x n
	d := wa.T().MulVec(wb)
	x, rcond, err := solveSquare(g, d)
	if err != nil {
		return nil, rcond, err
	}
	return x, math.Sqrt(rcond), nil // rcond(A) ≈ sqrt(rcond(AᴴA))
}

// SolveSquare solves a square complex system A x = b.
func SolveSquare(a Matrix, b []complex128) ([]complex128, float64, error) {
	if a.R != a.C || a.R != len(b) {
		return nil, 0, ErrDimension
	}
	return solveSquare(a, b)
}
