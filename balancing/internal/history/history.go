// Package history maintains the per-machine, per-speed influence coefficient
// history and produces the coefficient set a new balancing job should start
// from.
//
// All state in this package is a pure function of the job event stream (see
// package store): Replay ingests, in chronological order, the coefficient
// estimate each trial job produced and the verdict each reuse job obtained
// when it checked the stored coefficients against a fresh verification run.
// That makes corrections of old readings propagate identically: replay from
// the corrected stream yields exactly the state as if the reading had been
// entered correctly the first time.
package history

import (
	"errors"
	"math"

	"balancing/internal/balance"
)

// Strategy selects how historical estimates are combined.
type Strategy string

const (
	// Latest uses only the estimate of the most recent successful trial job.
	Latest Strategy = "latest"
	// SlidingWeighted combines up to Window recent estimates with weights
	// 1, 1/2, 1/3, ... (older counts exponentially less).
	SlidingWeighted Strategy = "sliding_weighted"
	// UncertaintyWeighted is inverse-variance fusion: each estimate's weight
	// is 1/u^2 where u is its estimated uncertainty, so a clean, well-excited
	// job dominates over a noisy one. Default.
	UncertaintyWeighted Strategy = "uncertainty_weighted"
)

// Window is the number of recent estimates used by the sliding strategy.
const Window = 5

func (s Strategy) Valid() bool {
	return s == Latest || s == SlidingWeighted || s == UncertaintyWeighted
}

// Estimate is one job's influence coefficient estimate at one speed.
type Estimate struct {
	JobID string
	Speed int
	H     balance.Matrix
	// Uncertainty is the relative standard error of the estimate (fraction of
	// |H|): 0 means "known exactly" (synthetic/test data), typical measured
	// values are 0.1..0.5. Used by uncertainty-weighted fusion and as the
	// soft prior when flagging a failed verification.
	Uncertainty float64
	// Verified marks estimates whose own correction/verification run confirmed
	// them (residual reduction met ReductionThreshold).
	Verified bool
}

// CheckVerdict is what a reuse job (a job that skipped trial runs and used the
// stored coefficients directly) observed on its verification run.
type CheckVerdict struct {
	JobID string
	Speed int
	// MismatchRMS is RMS over points of |v_measured - (v_original + H*w)|,
	// i.e. how badly the stored model failed to predict the actual effect.
	MismatchRMS float64
	// ResidualRMS is the achieved RMS residual vibration.
	ResidualRMS float64
	// OriginalRMS is the RMS original vibration before correction.
	OriginalRMS float64
}

// FailMismatchRatio invalidates stored coefficients when the prediction
// mismatch exceeds this fraction of the original vibration. Set at 0.5: if the
// historical model cannot even predict the correction effect to within half of
// what was to be corrected, trusting it further risks adding a worsening
// weight — the next job must perform fresh trial runs.
const FailMismatchRatio = 0.5

// ReductionThreshold is the fraction of original vibration that must remain
// below 1 - x for an estimate to count as verified (i.e. residual/original
// <= 0.5). Achieving at least 50% reduction is the usual "balance effective"
// criterion in field practice.
const ReductionThreshold = 0.5

// fusedEstimate carries the weight assigned to one Estimate.
type fusedEstimate struct {
	e Estimate
	w float64
}

// Entry is the history state at one speed.
type Entry struct {
	Speed       int
	H           balance.Matrix
	Uncertainty float64
	Verified    bool
	Available   bool
	// FusedFrom lists the jobs whose estimates were combined.
	FusedFrom []string
	pool      []fusedEstimate
	// invalid means a reuse job disproved these coefficients at or after the
	// last trial job; fresh trial runs are mandatory.
	invalid bool
	// latest points at the newest accepted trial estimate (used even when
	// unverified, when no verified one exists).
	latest *Estimate
}

// State is the per-machine history over all speeds.
type State struct {
	strategy Strategy
	entries  map[int]*Entry
	order    []int // speed order
}

// NewState returns an empty history using the given fusion strategy.
func NewState(strategy Strategy) (*State, error) {
	if !strategy.Valid() {
		return nil, errors.New("unknown fusion strategy")
	}
	return &State{strategy: strategy, entries: map[int]*Entry{}}, nil
}

func (s *State) entry(speed int) *Entry {
	e, ok := s.entries[speed]
	if !ok {
		e = &Entry{Speed: speed}
		s.entries[speed] = e
		s.order = append(s.order, speed)
	}
	return e
}

// AddTrial records a trial job's estimate. A fresh successful trial
// immediately rehabilitates coefficients that had been invalidated.
func (s *State) AddTrial(e Estimate) {
	en := s.entry(e.Speed)
	en.invalid = false
	cur := e
	en.latest = &cur
	// Uncertainty 0 (exact) would give infinite weight; treat very small
	// values as a floor of 1% relative noise.
	u := math.Max(e.Uncertainty, 0.01)
	en.pool = append(en.pool, fusedEstimate{e: e, w: 1 / (u * u)})
	if len(en.pool) > Window {
		en.pool = en.pool[len(en.pool)-Window:]
	}
	en.recompute(s.strategy)
}

// AddCheck records the verdict of a reuse job's verification. A failed
// prediction invalidates the stored coefficients for that speed.
func (s *State) AddCheck(v CheckVerdict) {
	en := s.entry(v.Speed)
	if !en.Available {
		return
	}
	fail := v.MismatchRMS > FailMismatchRatio*v.OriginalRMS
	// A poor achieved reduction (even if the prediction itself was accurate)
	// does not invalidate the model — the machine may simply be unbalanceable
	// in one correction — but an outright prediction failure does.
	if fail {
		en.invalid = true
		en.Available = false
		en.H = balance.Matrix{}
		en.FusedFrom = nil
		// Previous estimates described the old machine; discard them so a
		// later fresh trial starts the pool from scratch instead of fusing
		// with disproven coefficients.
		en.pool = nil
		en.latest = nil
		en.Verified = false
	}
}

// MarkVerified is called when the trial job's own verification run confirms
// its estimate.
func (s *State) MarkVerified(jobID string, speed int, reduction float64) {
	en := s.entry(speed)
	for i := range en.pool {
		if en.pool[i].e.JobID == jobID && reduction >= (1-ReductionThreshold) {
			e := en.pool[i].e
			e.Verified = true
			en.pool[i].e = e
		}
	}
	if en.latest != nil && en.latest.JobID == jobID && reduction >= (1-ReductionThreshold) {
		e := *en.latest
		e.Verified = true
		en.latest = &e
	}
	en.recompute(s.strategy)
}

// Recompute the fused H for a speed.
func (en *Entry) recompute(strategy Strategy) {
	pool := en.pool
	if len(pool) == 0 {
		return
	}
	var chosen []fusedEstimate
	switch strategy {
	case Latest:
		chosen = pool[len(pool)-1:]
	case SlidingWeighted:
		chosen = pool
		for i := range chosen {
			chosen[i].w = 1 / float64(len(chosen)-i) // newest has weight 1
		}
	case UncertaintyWeighted:
		chosen = pool // weights already 1/u²
	}

	// Prefer verified estimates; if none verified yet, use what we have so
	// the first job can still be reused.
	verified := filterVerified(chosen)
	used := chosen
	allVerified := false
	if len(verified) > 0 {
		used = verified
		allVerified = true
	}

	r, c := used[0].e.H.R, used[0].e.H.C
	h := balance.NewMatrix(r, c)
	var wsum, usum float64
	jobs := make([]string, 0, len(used))
	for _, fe := range used {
		if fe.e.H.R != r || fe.e.H.C != c {
			continue // machine geometry changed; ignore stale estimate
		}
		jobs = append(jobs, fe.e.JobID)
		wsum += fe.w
		usum += fe.w * fe.e.Uncertainty
	}
	if wsum == 0 {
		return
	}
	for _, fe := range used {
		if fe.e.H.R != r || fe.e.H.C != c {
			continue
		}
		scale := complex(fe.w/wsum, 0)
		for i := 0; i < r; i++ {
			for j := 0; j < c; j++ {
				h.Set(i, j, h.At(i, j)+fe.e.H.At(i, j)*scale)
			}
		}
	}
	en.H = h
	en.Uncertainty = usum / wsum
	en.Verified = allVerified
	en.Available = true
	en.FusedFrom = jobs
}

func filterVerified(in []fusedEstimate) []fusedEstimate {
	out := in[:0:0]
	for _, fe := range in {
		if fe.e.Verified {
			out = append(out, fe)
		}
	}
	return out
}

// EntryAt returns the history state at one speed. When the coefficients are
// invalid the entry's Available is false and a new job must trial fresh.
func (s *State) EntryAt(speed int) (Entry, bool) {
	en, ok := s.entries[speed]
	if !ok {
		return Entry{Speed: speed}, false
	}
	return *en, ok && en.Available && !en.invalid
}

// Speeds returns the speeds with history, in insertion order.
func (s *State) Speeds() []int { return append([]int(nil), s.order...) }
