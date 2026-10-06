package balance

import (
	"math"
)

// LoadedRun is one non-original run at a single speed, expressed in the
// internal angular frame.
type LoadedRun struct {
	// Weights[p] is the total attached-weight vector on correction plane p
	// (complex, grams at internal angle).
	Planes int
	// weights per plane
	W []complex128
	// V[point] is the measured vibration vector of the point.
	V []complex128
}

// RMS returns the root mean square magnitude of a complex vector.
func RMS(v []complex128) float64 {
	var s float64
	for _, z := range v {
		s += real(z)*real(z) + imag(z)*imag(z)
	}
	return math.Sqrt(s / float64(len(v)))
}

// Effect is the vector difference v - original of a run.
func Effect(original, v []complex128) []complex128 {
	d := make([]complex128, len(v))
	for i := range v {
		d[i] = v[i] - original[i]
	}
	return d
}

// EstimateInfluence estimates the influence coefficient matrix H (points x
// planes) from the original run and a set of loaded runs, under the linear
// model
//
//	v_run[point] = v_original[point] + sum_plane H[point,plane] * w_run[plane]
//
// All loaded runs are used simultaneously in one weighted complex least
// squares fit per measurement point (the usual one-plane-at-a-time trial runs
// are just the special case where the run design matrix is diagonal). Because
// the model is linear, verification/correction runs with combined weights may
// be included to refine the estimate.
//
// The returned reciprocal condition number is the minimum over the per-point
// normal-equation matrices; it diagnoses collinear trial directions (e.g. two
// planes trialled in nearly the same angular direction).
func EstimateInfluence(original []complex128, runs []LoadedRun, pointWeights []float64) (Matrix, float64, error) {
	if len(runs) == 0 {
		return Matrix{}, 0, ErrDimension
	}
	points := len(original)
	planes := runs[0].Planes
	for _, run := range runs {
		if run.Planes != planes || len(run.W) != planes || len(run.V) != points {
			return Matrix{}, 0, ErrDimension
		}
	}

	h := NewMatrix(points, planes)
	rcond := 1.0
	// A point's design matrix is identical for all points: rows are runs,
	// columns are planes, entries are weight vectors.
	a := NewMatrix(len(runs), planes)
	for r, run := range runs {
		for pl := 0; pl < planes; pl++ {
			a.set(r, pl, run.W[pl])
		}
	}
	for p := 0; p < points; p++ {
		b := make([]complex128, len(runs))
		for r, run := range runs {
			b[r] = run.V[p] - original[p]
		}
		// Every row belonging to point p gets the point's confidence weight.
		w := make([]float64, len(runs))
		for r := range w {
			w[r] = weightAt(pointWeights, p)
		}
		col, rc, err := LeastSquares(a, b, w)
		if err != nil {
			return Matrix{}, rc, err
		}
		rcond = math.Min(rcond, rc)
		for pl := 0; pl < planes; pl++ {
			h.set(p, pl, col[pl])
		}
	}
	return h, rcond, nil
}

func weightAt(weights []float64, i int) float64 {
	if i < len(weights) && weights[i] > 0 {
		return weights[i]
	}
	return 1
}

// Correct solves for the correction weight that minimizes the weighted
// residual
//
//	min_w ||W (H w + original)||
//
// i.e. it cancels the original vibration using the linear model. It returns
// the correction weight vector w (one complex entry per plane, grams in the
// internal frame), the predicted residual, and the reciprocal condition
// number. For a square H the residual is zero up to floating-point error.
func Correct(h Matrix, original []complex128, pointWeights []float64) (w, residual []complex128, rcond float64, err error) {
	if h.R != len(original) {
		return nil, nil, 0, ErrDimension
	}
	b := make([]complex128, h.R)
	for i := range original {
		b[i] = -original[i]
	}
	if h.R == h.C {
		w, rcond, err = SolveSquare(h, b)
	} else {
		w, rcond, err = LeastSquares(h, b, pointWeights)
	}
	if err != nil {
		return nil, nil, rcond, err
	}
	residual = make([]complex128, h.R)
	hw := h.MulVec(w)
	for i := range residual {
		residual[i] = original[i] + hw[i]
	}
	return w, residual, rcond, nil
}

// Predict applies the linear model: v = original + H w.
func Predict(h Matrix, original, w []complex128) []complex128 {
	hw := h.MulVec(w)
	v := make([]complex128, len(original))
	for i := range v {
		v[i] = original[i] + hw[i]
	}
	return v
}
