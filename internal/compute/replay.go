// Package compute turns a job's append-only event stream into its computed
// state: internal-frame runs, per-speed influence coefficient fits,
// correction and trim recommendations (with hole splitting), and the
// verification verdict that decides whether stored history stays trusted.
//
// All projection is pure: Replay over the same events always produces the
// same State, which is what makes run corrections indistinguishable from
// correct initial entry and what lets the service rebuild every result after
// a correction event.
package compute

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"balancer/internal/domain"
	"balancer/pkg/history"
	"balancer/pkg/vec"
)

// DefaultCondLimit rejects fits/solves whose matrix condition number exceeds
// 10^3 (two independent planes must be distinguishable to ~0.1%).
const DefaultCondLimit = 3.0

// Run is a run record projected into internal coordinates.
type Run struct {
	RunID    string         `json:"runId"`
	ClientID string         `json:"clientId,omitempty"`
	Kind     domain.RunKind `json:"kind"`
	RunAt    time.Time      `json:"runAt"`
	Note     string         `json:"note,omitempty"`
	Seq      int64          `json:"seq"` // insertion sequence (tie-breaker)
	// Weights[planeIdx] complex g in internal frame (0 = no attached weight).
	Weights []complex128 `json:"-"`
	// Readings[speedIdx][pointIdx] complex µm in internal frame.
	Readings [][]complex128 `json:"-"`
	// External forms kept for serialization.
	ExtWeights  []domain.Weight  `json:"weights,omitempty"`
	ExtReadings []domain.Reading `json:"readings,omitempty"`
}

// Fit is one speed's influence coefficient result.
type Fit struct {
	Speed     int           `json:"speedRpm"`
	FromRun   bool          `json:"fromRuns"` // false = supplied from history
	Re, Im    [][]float64   `json:"-"`        // points × planes
	Uncert    [][]float64   `json:"-"`
	Log10Cond float64       `json:"log10Cond"`
	Rank      int           `json:"rank"`
	FitRSS    []float64     `json:"fitResidualRss"`
	ExtA      [][]vec.Polar `json:"influence"` // external polar per [point][plane]
	ExtSigma  [][]float64   `json:"uncertainty"`
	Err       string        `json:"error,omitempty"`
}

// SpeedPlan is a single-speed correction recommendation.
type SpeedPlan struct {
	Speed       int           `json:"speedRpm"`
	Weights     []vec.Polar   `json:"weights"`           // external, per plane
	Residual    []vec.Polar   `json:"predictedResidual"` // per point, external
	ResidualAmp []float64     `json:"residualAmpUm"`
	Holes       [][]HoleSplit `json:"holes,omitempty"` // per plane when holes fixed
	Err         string        `json:"error,omitempty"`
}

// HoleSplit mirrors split.Result for the API.
type HoleSplit struct {
	First          Hole    `json:"first"`
	Second         Hole    `json:"second"`
	ResultantMass  float64 `json:"resultantMass"`
	ResultantAngle float64 `json:"resultantAngle"`
}

type Hole struct {
	Index int     `json:"index"`
	Angle float64 `json:"angle"`
	Mass  float64 `json:"mass"`
}

// TrimPlan adds correction weights on top of what the rotor currently
// carries (the latest non-original run).
type TrimPlan struct {
	BasedOnRun string         `json:"basedOnRunId"`
	Kind       domain.RunKind `json:"kind"`
	AddWeights []vec.Polar    `json:"addWeights"`
	Installed  []vec.Polar    `json:"installedWeights"`
	Residual   []vec.Polar    `json:"predictedResidual"`
	Holes      [][]HoleSplit  `json:"holes,omitempty"`
	Err        string         `json:"error,omitempty"`
}

// State is the full projection of a job.
type State struct {
	Job           *domain.Job                  `json:"job"`
	Runs          []*Run                       `json:"runs"`
	OriginalRunID string                       `json:"originalRunId"`
	Fits          map[int]*Fit                 `json:"fitsBySpeed"`
	Plans         map[int]*SpeedPlan           `json:"plansBySpeed"`
	MultiSpeed    *SpeedPlan                   `json:"multiSpeedPlan,omitempty"`
	Trim          *TrimPlan                    `json:"trimPlan,omitempty"`
	Trust         *history.TrustReport         `json:"trust,omitempty"`
	TrustBySpeed  map[int]*history.TrustReport `json:"-"`
	TrustOK       bool                         `json:"trustOk"`
	TrustChecked  bool                         `json:"-"`
	Finalized     *domain.FinalizedEvent       `json:"finalized,omitempty"`
	HistoryEntry  map[int]*history.Entry       `json:"-"`
	HistoryUsed   bool                         `json:"usedHistory"`
	Warnings      []string                     `json:"warnings,omitempty"`
	Errors        []string                     `json:"errors,omitempty"`
}

// Replay rebuilds a job state from its event stream. historyEntries maps speed
// rpm to the active stored coefficient (nil entries / nil map for jobs
// started from scratch); its layouts must match the job snapshot.
func Replay(job *domain.Job, events []domain.StoredEvent, historyEntries map[int]*history.Entry) (*State, error) {
	cfg := job.Snapshot()
	st := &State{
		Job:          job,
		Fits:         map[int]*Fit{},
		Plans:        map[int]*SpeedPlan{},
		HistoryEntry: historyEntries,
	}

	runs := map[string]*Run{}
	var order []string // run ids in (RunAt, seq) order

	for _, ev := range events {
		switch ev.Type {
		case domain.EvtJobCreated:
			// Job is passed in explicitly; ignore payload duplication.
		case domain.EvtRunAdded:
			var e domain.RunAddedEvent
			if err := json.Unmarshal(ev.Payload, &e); err != nil {
				return nil, fmt.Errorf("seq %d: %w", ev.Seq, err)
			}
			if _, dup := runs[e.RunID]; dup {
				return nil, fmt.Errorf("seq %d: duplicate run id %s", ev.Seq, e.RunID)
			}
			r := projectRun(cfg, e.RunID, e.ClientID, e.Input, ev.Seq)
			runs[e.RunID] = r
			order = append(order, e.RunID)
		case domain.EvtRunCorrected:
			var e domain.RunCorrectedEvent
			if err := json.Unmarshal(ev.Payload, &e); err != nil {
				return nil, fmt.Errorf("seq %d: %w", ev.Seq, err)
			}
			r, ok := runs[e.RunID]
			if !ok {
				return nil, fmt.Errorf("seq %d: correction for unknown run %s", ev.Seq, e.RunID)
			}
			applyCorrection(cfg, r, e.Correction)
		case domain.EvtFinalized:
			var e domain.FinalizedEvent
			if err := json.Unmarshal(ev.Payload, &e); err != nil {
				return nil, fmt.Errorf("seq %d: %w", ev.Seq, err)
			}
			f := e
			st.Finalized = &f
		default:
			return nil, fmt.Errorf("seq %d: unknown event type %q", ev.Seq, ev.Type)
		}
	}

	// Order runs by actual run time, insertion sequence as tie-breaker:
	// concurrent submissions are both kept, ordered by field time.
	sort.SliceStable(order, func(i, j int) bool {
		a, b := runs[order[i]], runs[order[j]]
		if a.RunAt.Equal(b.RunAt) {
			return a.Seq < b.Seq
		}
		return a.RunAt.Before(b.RunAt)
	})
	st.Runs = make([]*Run, 0, len(order))
	for _, id := range order {
		st.Runs = append(st.Runs, runs[id])
		if runs[id].Kind == domain.RunOriginal {
			st.OriginalRunID = id
		}
	}

	computeState(st, cfg, history.DefaultPolicy())
	return st, nil
}

func projectRun(cfg domain.SnapshotConfig, runID, clientID string, in domain.RunInput, seq int64) *Run {
	nP, nQ, nS := len(cfg.PlaneIDs), len(cfg.PointIDs), len(cfg.Speeds)
	r := &Run{
		RunID: runID, ClientID: clientID, Kind: in.Kind, RunAt: in.RunAt,
		Note: in.Note, Seq: seq,
		Weights:     make([]complex128, nP),
		Readings:    make([][]complex128, nS),
		ExtWeights:  in.Weights,
		ExtReadings: in.Readings,
	}
	for s := range r.Readings {
		r.Readings[s] = make([]complex128, nQ)
	}
	for _, w := range in.Weights {
		if i := indexOf(cfg.PlaneIDs, w.PlaneID); i >= 0 {
			p := cfg.Convention.InternalPolar(w.Polar)
			r.Weights[i] = p.Complex()
		}
	}
	for _, rd := range in.Readings {
		si := indexOfInt(cfg.Speeds, rd.Speed)
		pi := indexOf(cfg.PointIDs, rd.PointID)
		if si < 0 || pi < 0 {
			continue // validation rejects this; projection stays defensive
		}
		r.Readings[si][pi] = cfg.Convention.InternalPolar(rd.Polar).Complex()
	}
	return r
}

func applyCorrection(cfg domain.SnapshotConfig, r *Run, c domain.RunFix) {
	if c.Weights != nil {
		r.ExtWeights = *c.Weights
		for i := range r.Weights {
			r.Weights[i] = 0
		}
		for _, w := range *c.Weights {
			if i := indexOf(cfg.PlaneIDs, w.PlaneID); i >= 0 {
				r.Weights[i] = cfg.Convention.InternalPolar(w.Polar).Complex()
			}
		}
	}
	if c.Readings != nil {
		r.ExtReadings = *c.Readings
		for s := range r.Readings {
			for p := range r.Readings[s] {
				r.Readings[s][p] = 0
			}
		}
		for _, rd := range *c.Readings {
			si := indexOfInt(cfg.Speeds, rd.Speed)
			pi := indexOf(cfg.PointIDs, rd.PointID)
			if si >= 0 && pi >= 0 {
				r.Readings[si][pi] = cfg.Convention.InternalPolar(rd.Polar).Complex()
			}
		}
	}
	if c.RunAt != nil {
		r.RunAt = *c.RunAt
	}
	if c.Note != nil {
		r.Note = *c.Note
	}
}

// ProjectForCheck converts a candidate run input into internal coordinates
// without appending it, so the service can apply cross-run checks (such as
// the trial effect-size screen) before accepting the run.
func ProjectForCheck(cfg domain.SnapshotConfig, in *domain.RunInput) *Run {
	return projectRun(cfg, "", "", *in, 0)
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func indexOfInt(xs []int, x int) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
