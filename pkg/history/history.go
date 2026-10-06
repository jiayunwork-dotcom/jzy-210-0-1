// Package history manages per-machine, per-speed influence coefficient
// estimates across balancing jobs.
//
// Policy (see DESIGN.md for the rationale):
//
//   - Every completed job that actually carried trial runs yields a new
//     influence matrix estimate with an uncertainty on every entry (standard
//     error of the complex least-squares fit).
//   - Estimates are fused by inverse variance: each entry contributes with
//     weight 1/(σ²+σ_floor²) · 0.5^(age/halfLife), so precise and recent
//     estimates dominate. The fused entry keeps a fused variance, the number
//     of fused contributions and the timestamp of the newest contribution.
//   - A history entry used "blind" (no trial runs) must survive a verification
//     check: the verification run's measured vibration must agree with the
//     model prediction and must reduce the original vibration by a configured
//     margin. Failure marks the entry suspect; a suspect entry is never
//     offered for reuse and forces a fresh trial run.
//   - A fresh trial estimate replaces a suspect/retired entry rather than
//     fusing with it, and becomes active after its own verification passes.
package history

import (
	"math"
	"time"
)

// Status of a stored coefficient entry.
const (
	StatusActive  = "active"  // usable without trial runs
	StatusSuspect = "suspect" // failed a verification check; new trial run required
	StatusRetired = "retired" // manually or automatically invalidated
)

// Policy controls fusion aging, uncertainty floors and trust thresholds.
type Policy struct {
	// HalfLifeDays exponentially down-weights older fused information.
	HalfLifeDays float64
	// Uncertainty floor per coefficient entry: σ_eff² = σ² + (abs+rel·|A|)².
	MinUncertAbs float64 // µm/g
	MinUncertRel float64 // fraction of |A|
	// Trust: reuse passes when every measurement point satisfies
	//   |v_meas - v_pred| / max(|v0|,eps) <= MaxRelModelError
	// and, where |v0| is appreciable,
	//   1 - |v_meas|/|v0| >= MinReduction.
	MaxRelModelError float64
	MinReduction     float64
	// SignificantVibration is the |v0| level below which only the model
	// error check is applied (the rotor was already balanced).
	SignificantVibration float64
}

// DefaultPolicy returns the field-tuned defaults.
func DefaultPolicy() Policy {
	return Policy{
		HalfLifeDays:         365,
		MinUncertAbs:         0.05,
		MinUncertRel:         0.05,
		MaxRelModelError:     0.35,
		MinReduction:         0.50,
		SignificantVibration: 10, // µm
	}
}

// Entry is one stored coefficient matrix (internal complex representation is
// flattened to real/imag pairs at the storage boundary).
type Entry struct {
	MachineID string
	Speed     int
	PlaneIDs  []string
	PointIDs  []string
	// Re[entry r*nPlanes+c], Im[...] — points are rows, planes columns.
	Re, Im []float64
	// Uncert is the fused standard error per entry (≥ floor).
	Uncert []float64
	Status string
	// Contributions fused into this entry.
	NContrib int
	// UpdatedAt is the timestamp of the newest contribution.
	UpdatedAt time.Time
	// CreatedAt is when the entry was first formed.
	CreatedAt time.Time
}

func (e *Entry) at(r, c int) complex128 {
	k := r*len(e.PlaneIDs) + c
	return complex(e.Re[k], e.Im[k])
}

func (e *Entry) set(r, c int, z complex128, u float64) {
	k := r*len(e.PlaneIDs) + c
	e.Re[k] = real(z)
	e.Im[k] = imag(z)
	e.Uncert[k] = u
}

// Estimate is a newly fitted coefficient matrix offered for fusion.
type Estimate struct {
	MachineID string
	Speed     int
	PlaneIDs  []string
	PointIDs  []string
	Re, Im    []float64
	Uncert    []float64
	At        time.Time
}

// ageWeight returns 0.5^(ageDays / halfLife).
func ageWeight(then, now time.Time, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		return 1
	}
	days := now.Sub(then).Hours() / 24
	if days < 0 {
		days = 0
	}
	return math.Pow(0.5, days/halfLifeDays)
}

func (p Policy) floor(absA float64) float64 {
	return p.MinUncertAbs + p.MinUncertRel*absA
}

// Fuse merges a fresh estimate into the existing entry (which may be nil).
// A suspect/retired existing entry is replaced rather than fused.
func Fuse(existing *Entry, est *Estimate, pol Policy, now time.Time) *Entry {
	nPlanes := len(est.PlaneIDs)
	nPoints := len(est.PointIDs)

	if existing != nil &&
		existing.Status == StatusActive &&
		sameLayout(existing.PlaneIDs, est.PlaneIDs) &&
		sameLayout(existing.PointIDs, est.PointIDs) {
		aw := ageWeight(existing.UpdatedAt, now, pol.HalfLifeDays)
		out := &Entry{
			MachineID: est.MachineID, Speed: est.Speed,
			PlaneIDs: est.PlaneIDs, PointIDs: est.PointIDs,
			Re: make([]float64, nPlanes*nPoints), Im: make([]float64, nPlanes*nPoints),
			Uncert:    make([]float64, nPlanes*nPoints),
			Status:    existing.Status,
			NContrib:  existing.NContrib + 1,
			UpdatedAt: now,
			CreatedAt: existing.CreatedAt,
		}
		for k := 0; k < nPlanes*nPoints; k++ {
			r, c := k/nPlanes, k%nPlanes
			zOld := existing.at(r, c)
			oldVar := existing.Uncert[k] * existing.Uncert[k]
			wOld := aw / oldVar
			zNew := complex(est.Re[k], est.Im[k])
			newS := math.Max(est.Uncert[k], pol.floor(math.Hypot(real(zNew), imag(zNew))))
			wNew := 1 / (newS * newS)
			z := (zOld*complex(wOld, 0) + zNew*complex(wNew, 0)) / complex(wOld+wNew, 0)
			s := 1 / math.Sqrt(wOld+wNew)
			// Fused uncertainty must remain at least the policy floor.
			if f := pol.floor(math.Hypot(real(z), imag(z))); s < f {
				s = f
			}
			out.set(r, c, z, s)
		}
		return out
	}

	out := &Entry{
		MachineID: est.MachineID, Speed: est.Speed,
		PlaneIDs: est.PlaneIDs, PointIDs: est.PointIDs,
		Re: make([]float64, nPlanes*nPoints), Im: make([]float64, nPlanes*nPoints),
		Uncert:    make([]float64, nPlanes*nPoints),
		Status:    StatusActive,
		NContrib:  1,
		UpdatedAt: now,
		CreatedAt: now,
	}
	for k := 0; k < nPlanes*nPoints; k++ {
		r, c := k/nPlanes, k%nPlanes
		z := complex(est.Re[k], est.Im[k])
		s := math.Max(est.Uncert[k], pol.floor(math.Hypot(real(z), imag(z))))
		out.set(r, c, z, s)
	}
	return out
}

func sameLayout(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TrustReport is the per-point result of checking a history-based correction
// against a verification run.
type TrustReport struct {
	PerPoint []PointTrust `json:"perPoint"`
	OK       bool         `json:"ok"`
}

type PointTrust struct {
	PointID   string  `json:"pointId"`
	Original  float64 `json:"originalAmp"`
	Measured  float64 `json:"measuredAmp"`
	Predicted float64 `json:"predictedAmp"`
	RelError  float64 `json:"relModelError"`
	Reduction float64 `json:"reduction"`
	Passed    bool    `json:"passed"`
	Reason    string  `json:"reason,omitempty"`
}

// CheckTrust compares the verification measurement with what the stored model
// predicts for the installed weights. v0, vMeas, vPred are complex, aligned
// with pointIDs.
func CheckTrust(pointIDs []string, v0, vMeas, vPred []complex128, pol Policy) TrustReport {
	rep := TrustReport{OK: true, PerPoint: make([]PointTrust, len(pointIDs))}
	for i, id := range pointIDs {
		a0 := cmag(v0[i])
		am := cmag(vMeas[i])
		ap := cmag(vPred[i])
		scale := math.Max(a0, 1e-9)
		relerr := cmag(vMeas[i]-vPred[i]) / scale
		red := 0.0
		if a0 > 0 {
			red = 1 - am/a0
		}
		t := PointTrust{
			PointID: id, Original: a0, Measured: am, Predicted: ap,
			RelError: relerr, Reduction: red,
		}
		t.Passed = true
		if relerr > pol.MaxRelModelError {
			t.Passed = false
			t.Reason = "model prediction error exceeds limit"
		}
		if a0 >= pol.SignificantVibration && red < pol.MinReduction {
			t.Passed = false
			t.Reason = "vibration reduction below limit"
		}
		if !t.Passed {
			rep.OK = false
		}
		rep.PerPoint[i] = t
	}
	return rep
}

// InflateAfterFailure raises the uncertainty of a failed entry so that even
// if later re-fused manually it carries little weight; status goes suspect.
func (e *Entry) InflateAfterFailure() {
	e.Status = StatusSuspect
	for k := range e.Uncert {
		e.Uncert[k] *= 10
	}
}

func cmag(z complex128) float64 { return math.Hypot(real(z), imag(z)) }
