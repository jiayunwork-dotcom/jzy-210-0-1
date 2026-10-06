package api

import (
	"github.com/labstack/echo/v4"

	"balancing/internal/balance"
	"balancing/internal/store"
	"balancing/internal/vec"
)

// noopValidator satisfies echo's Validator interface. All semantic validation
// (including the named-field rejections) is performed explicitly against the
// store types so the same rules apply over HTTP and in direct use.
type noopValidator struct{}

func (noopValidator) Validate(any) error { return nil }

// matrixView renders an internal influence matrix in the machine's external
// angular convention.
func matrixView(m *store.Machine, h balance.Matrix) [][]vec.Polar {
	out := make([][]vec.Polar, h.R)
	for i := 0; i < h.R; i++ {
		out[i] = make([]vec.Polar, h.C)
		for j := 0; j < h.C; j++ {
			p := vec.FromC(h.At(i, j))
			if m.Direction == vec.WithRotation {
				p.Phase = vec.Norm(-p.Phase)
			}
			out[i][j] = p
		}
	}
	return out
}

var _ echo.Validator = noopValidator{}
