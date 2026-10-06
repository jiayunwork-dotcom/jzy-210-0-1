package balance

import (
	"math"
	"math/cmplx"
)

// Matrix is a dense r x c complex matrix (r rows, c columns).
type Matrix struct {
	R, C int
	v    []complex128 // row-major
}

// NewMatrix returns an r x c zero matrix.
func NewMatrix(r, c int) Matrix { return Matrix{R: r, C: c, v: make([]complex128, r*c)} }

// MatrixFromRows builds a matrix from row slices.
func MatrixFromRows(rows ...[]complex128) Matrix {
	m := NewMatrix(len(rows), len(rows[0]))
	for i, row := range rows {
		copy(m.v[i*m.C:(i+1)*m.C], row)
	}
	return m
}

func (m Matrix) at(i, j int) complex128     { return m.v[i*m.C+j] }
func (m Matrix) set(i, j int, z complex128) { m.v[i*m.C+j] = z }

// At returns the element (i, j).
func (m Matrix) At(i, j int) complex128 { return m.at(i, j) }

// Set assigns the element (i, j).
func (m Matrix) Set(i, j int, z complex128) { m.set(i, j, z) }

// Col returns a copy of column j.
func (m Matrix) Col(j int) []complex128 {
	out := make([]complex128, m.R)
	for i := 0; i < m.R; i++ {
		out[i] = m.at(i, j)
	}
	return out
}

// T returns the conjugate transpose (the "adjoint" of a complex matrix).
func (m Matrix) T() Matrix {
	t := NewMatrix(m.C, m.R)
	for i := 0; i < m.R; i++ {
		for j := 0; j < m.C; j++ {
			t.set(j, i, cmplx.Conj(m.at(i, j)))
		}
	}
	return t
}

// Mul returns m*n.
func (m Matrix) Mul(n Matrix) Matrix {
	if m.C != n.R {
		panic("matrix dimension mismatch")
	}
	o := NewMatrix(m.R, n.C)
	for i := 0; i < m.R; i++ {
		for k := 0; k < m.C; k++ {
			a := m.at(i, k)
			for j := 0; j < n.C; j++ {
				o.set(i, j, o.at(i, j)+a*n.at(k, j))
			}
		}
	}
	return o
}

// MulVec returns m*v.
func (m Matrix) MulVec(v []complex128) []complex128 {
	if m.C != len(v) {
		panic("matrix/vector dimension mismatch")
	}
	o := make([]complex128, m.R)
	for i := 0; i < m.R; i++ {
		var s complex128
		for j := 0; j < m.C; j++ {
			s += m.at(i, j) * v[j]
		}
		o[i] = s
	}
	return o
}

// Frobenius returns the Frobenius norm.
func (m Matrix) Frobenius() float64 {
	var s float64
	for _, z := range m.v {
		s += real(z)*real(z) + imag(z)*imag(z)
	}
	return math.Sqrt(s)
}
