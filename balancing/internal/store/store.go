// Package store is the domain model, event-sourced store, replay engine and
// balancing computation pipeline.
//
// Everything that matters about a balancing job lives as an append-only event
// (job creation, run recording, run correction). The job's influence
// coefficients, correction proposal and its contribution to the machine's
// coefficient history are ALL derived by replaying the event stream, so
// correcting a mis-entered reading is just appending a correction event:
// replay then produces exactly the state that would have existed had the
// reading been entered correctly initially.
package store

import (
	"context"
	"time"

	"balancing/internal/balance"
	"balancing/internal/history"
	"balancing/internal/split"
	"balancing/internal/vec"
)

// RunKind classifies a rotor run.
type RunKind string

const (
	RunOriginal     RunKind = "original"     // baseline, no attached weight
	RunTrial        RunKind = "trial"        // known trial weight on one plane
	RunCorrection   RunKind = "correction"   // proposed correction mounted
	RunVerification RunKind = "verification" // post-correction check run
)

func (k RunKind) Valid() bool {
	switch k {
	case RunOriginal, RunTrial, RunCorrection, RunVerification:
		return true
	}
	return false
}

// Machine is the configured asset. Geometry and the phase convention are fixed
// per machine and snapshotted into each job so later machine edits never alter
// historical job results.
type Machine struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	PlaneCount     int              `json:"plane_count"`
	PointCount     int              `json:"point_count"`
	Speeds         []int            `json:"speeds"`          // rpm, ascending, unique
	ReferenceDeg   float64          `json:"reference_deg"`   // external 0° at this internal angle
	Direction      vec.Direction    `json:"phase_direction"` // external angle growth vs rotation
	HoleLayout     []*split.Plane   `json:"hole_layout"`     // per plane, nil = arbitrary angles
	FusionStrategy history.Strategy `json:"fusion_strategy"` // how job estimates are merged
	PointWeights   []float64        `json:"point_weights"`   // per measurement point, default 1
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// Convention returns the machine's phase convention.
func (m *Machine) Convention() vec.Convention {
	return vec.Convention{ReferenceDeg: m.ReferenceDeg, Direction: m.Direction}
}

// Job is one balancing campaign on a machine.
type Job struct {
	ID         string    `json:"id"`
	MachineID  string    `json:"machine_id"`
	Operator   string    `json:"operator"`
	Note       string    `json:"note"`
	UseHistory bool      `json:"use_history"` // started from stored coefficients
	CreatedAt  time.Time `json:"created_at"`
	// HistoryInfluence snapshots, for every speed, the historical influence
	// matrix (external frame, points x planes) the job started from. A reuse
	// job's derived state must be a pure function of its own events, so the
	// coefficients are captured at creation time rather than re-read later.
	HistoryInfluence map[int][][]vec.Polar `json:"history_influence,omitempty"`
}

// ---- external (wire) payload types, angles in the machine convention ----

// ReadingInput is one vibration vector reading.
type ReadingInput struct {
	Speed int     `json:"speed"`
	Point int     `json:"point"`
	Amp   float64 `json:"amp_um"`
	Phase float64 `json:"phase_deg"`
}

// WeightInput is one attached weight.
type WeightInput struct {
	Plane int     `json:"plane"`
	Grams float64 `json:"grams"`
	Angle float64 `json:"angle_deg"`
}

// RunInput is a submitted run. Weights describes ALL weights currently on the
// rotor; omitted planes count as zero.
type RunInput struct {
	Kind        RunKind        `json:"kind"`
	RunTime     time.Time      `json:"run_time"`
	Operator    string         `json:"operator"`
	Note        string         `json:"note"`
	Readings    []ReadingInput `json:"readings"`
	Weights     []WeightInput  `json:"weights"`
	TrialPlane  *int           `json:"trial_plane,omitempty"`
	TrialWeight *WeightInput   `json:"trial_weight,omitempty"`
}

// CorrectionInput amends a previously recorded run, identified by its run
// time. Nil slices leave that part of the run untouched. When two technicians
// submitted runs with the same run time, TargetSeq disambiguates; otherwise
// the (unique) run at that time is assumed.
type CorrectionInput struct {
	TargetRunTime time.Time      `json:"target_run_time"`
	TargetSeq     *int64         `json:"target_seq,omitempty"`
	Operator      string         `json:"operator"`
	Note          string         `json:"note"`
	Readings      []ReadingInput `json:"readings,omitempty"`
	Weights       []WeightInput  `json:"weights,omitempty"`
	TrialPlane    *int           `json:"trial_plane,omitempty"`
	TrialWeight   *WeightInput   `json:"trial_weight,omitempty"`
}

// EventType tags an append-only event.
type EventType string

const (
	EvJobCreated   EventType = "job_created"
	EvRunRecorded  EventType = "run_recorded"
	EvRunCorrected EventType = "run_corrected"
)

// Event is one stored append-only record.
type Event struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	Type      EventType `json:"type"`
	Seq       int64     `json:"seq"`      // per-job submission order, gapless
	RunTime   time.Time `json:"run_time"` // ordering key for runs/corrections
	Payload   []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// FieldError points at one rejected input field.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError is the rejected-submission error carrying every bad field.
type ValidationError struct {
	Fields []FieldError `json:"fields"`
}

func (e *ValidationError) Error() string { return "validation failed" }

func ve(es ...FieldError) *ValidationError { return &ValidationError{Fields: es} }

// TrialCheck reports the signal/noise judgement on one trial run.
type TrialCheck struct {
	RunTime   time.Time `json:"run_time"`
	Plane     int       `json:"plane"`
	EffectRMS float64   `json:"effect_rms_um"`
	SNR       float64   `json:"signal_to_noise"`
	OK        bool      `json:"ok"`
}

// SpeedResult is the fully derived computation at one speed.
type SpeedResult struct {
	Speed             int                  `json:"speed"`
	Status            string               `json:"status"` // ok | insufficient_data | ill_conditioned
	Error             *FieldError          `json:"error,omitempty"`
	Original          []vec.Polar          `json:"original"`  // external, per point
	Influence         [][]vec.Polar        `json:"influence"` // points x planes, external
	Rcond             float64              `json:"rcond"`
	Uncertainty       float64              `json:"uncertainty"`
	Correction        []vec.Polar          `json:"correction"`         // per plane, external
	PredictedResidual []vec.Polar          `json:"predicted_residual"` // per point, external
	OriginalRMS       float64              `json:"original_rms_um"`
	ResidualRMS       float64              `json:"predicted_residual_rms_um"`
	HolePlans         [][]split.HoleWeight `json:"hole_plans"` // per plane
	TrialChecks       []TrialCheck         `json:"trial_checks"`
	// HistoryEstimate is true when this speed's influence matrix is good
	// enough to contribute to the machine history (every plane trialled and
	// enough independent runs).
	HistoryEstimate bool `json:"history_estimate"`
	// Verification, when the job contains verification runs, reports how the
	// pre-verification model performed against held-out data.
	Verification *Verification `json:"verification,omitempty"`
}

// Verification is the held-out verification verdict at one speed.
type Verification struct {
	MismatchRMS   float64 `json:"mismatch_rms_um"`
	ResidualRMS   float64 `json:"residual_rms_um"`
	OriginalRMS   float64 `json:"original_rms_um"`
	Reduction     float64 `json:"reduction"` // 1 - residual/original
	MismatchRatio float64 `json:"mismatch_ratio"`
	ModelAccepted bool    `json:"model_accepted"`
}

// JobResult is the derived state of a job: one result per configured speed.
type JobResult struct {
	Speeds []*SpeedResult `json:"speeds"`
}

// JobState is a job plus its ordered runs and derived result.
type JobState struct {
	Job     *Job       `json:"job"`
	Machine *Machine   `json:"machine"`
	Runs    []*Run     `json:"runs"`
	Result  *JobResult `json:"result"`
	Events  []*Event   `json:"events,omitempty"`
}

// Run is the replayed (and possibly corrected) state of one rotor run.
// Vectors are in the internal canonical frame and are not JSON-serializable;
// the HTTP layer converts them with RunView.
type Run struct {
	Kind        RunKind                    `json:"kind"`
	RunTime     time.Time                  `json:"run_time"`
	Operator    string                     `json:"operator"`
	Note        string                     `json:"note"`
	Readings    map[int]map[int]complex128 `json:"-"`           // speed -> point -> vector
	Weights     map[int]complex128         `json:"-"`           // plane -> total weight
	TrialPlane  int                        `json:"trial_plane"` // -1 if not a trial
	TrialWeight complex128                 `json:"-"`
	HasTrial    bool                       `json:"has_trial"`
	Seq         int64                      `json:"seq"`
	Corrected   bool                       `json:"corrected"`
}

// Store is the persistence interface. Two implementations exist: an in-memory
// one used by the unit tests and a MySQL one used in deployment; the replay
// engine is shared, so their semantics are identical.
type Store interface {
	Ping(ctx context.Context) error
	CreateMachine(ctx context.Context, m *Machine) error
	GetMachine(ctx context.Context, id string) (*Machine, error)
	ListMachines(ctx context.Context) ([]*Machine, error)
	CreateJob(ctx context.Context, machineID, operator, note string, useHistory bool, at time.Time) (*Job, error)
	GetJob(ctx context.Context, id string, includeEvents bool) (*JobState, error)
	ListJobs(ctx context.Context, machineID string) ([]*JobState, error)
	AddRun(ctx context.Context, jobID string, in RunInput) (*Run, error)
	CorrectRun(ctx context.Context, jobID string, in CorrectionInput) (*Run, error)
	ListEvents(ctx context.Context, jobID string) ([]*Event, error)
	MachineHistory(ctx context.Context, machineID string) (*history.State, *Machine, error)
	// BalanceMatrix helpers exposed for tests/clients:
	HistoryMatrix(ctx context.Context, machineID string, speed int) (balance.Matrix, vec.Convention, bool, error)
	Close() error
}
