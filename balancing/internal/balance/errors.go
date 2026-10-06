package balance

import "errors"

var (
	// ErrSingular is returned when a matrix to invert is (numerically) singular.
	ErrSingular = errors.New("matrix is singular or too ill-conditioned")
	// ErrDimension is returned for mismatched matrix/vector dimensions.
	ErrDimension = errors.New("dimension mismatch")
)

// MinRcond is the lowest accepted reciprocal condition number. A value of
// 1e-4 tolerates condition numbers up to 10^4; beyond that the trial weights
// are effectively collinear (e.g. two planes trialled in nearly the same
// direction) and the correction weight becomes unstable.
const MinRcond = 1e-4

// MinSignalToNoise is the accepted ratio between the vibration change caused
// by a trial weight and the measurement noise floor. At ratio < 3 the trial
// effect is indistinguishable from noise and the resulting influence
// coefficient is unreliable — reject the run instead of producing a wild
// coefficient.
const MinSignalToNoise = 3.0

// NoiseFloorUM is the assumed RMS scatter of a vibration reading (μm) when no
// explicit noise estimate is supplied. Used only for the signal/noise
// rejection check on trial-run effects.
const NoiseFloorUM = 2.0
