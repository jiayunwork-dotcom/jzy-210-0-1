package domain

import (
	"time"

	"balancer/pkg/vec"
)

// EventType tags the append-only job event stream.
type EventType string

const (
	EvtJobCreated EventType = "job_created"
	EvtRunAdded   EventType = "run_added"
	// EvtRunCorrected replaces editable fields of a prior run. Corrections
	// are new events; the original event is never deleted. Replaying the
	// whole stream yields exactly the same state as if the run had been
	// entered correctly the first time.
	EvtRunCorrected EventType = "run_corrected"
	EvtFinalized    EventType = "job_finalized"
)

// StoredEvent is one row of a job's event stream. Seq is assigned by the
// store in insertion order; Payload is the JSON-encoded typed event below.
type StoredEvent struct {
	Seq        int64     `json:"seq"`
	JobID      string    `json:"jobId"`
	Type       EventType `json:"type"`
	OccurredAt time.Time `json:"occurredAt"`
	Payload    []byte    `json:"payload"`
}

// JobCreatedEvent captures the immutable snapshot of a job.
type JobCreatedEvent struct {
	JobID      string         `json:"jobId"`
	MachineID  string         `json:"machineId"`
	Name       string         `json:"name"`
	Convention vec.Convention `json:"convention"`
	PlaneIDs   []string       `json:"planeIds"`
	PlaneHoles []int          `json:"planeHoles"`
	PointIDs   []string       `json:"pointIds"`
	Speeds     []int          `json:"speedsRpm"`
	UseHistory bool           `json:"useHistory"`
	At         time.Time      `json:"at"`
}

// RunAddedEvent appends a run. Two technicians submitting concurrently both
// become events; replay orders runs by RunAt (then seq) rather than by seq
// alone.
type RunAddedEvent struct {
	RunID    string    `json:"runId"`
	ClientID string    `json:"clientId,omitempty"`
	Input    RunInput  `json:"input"`
	At       time.Time `json:"at"`
}

// RunCorrectedEvent overwrites the selected fields of run RunID.
type RunCorrectedEvent struct {
	RunID      string    `json:"runId"`
	Correction RunFix    `json:"correction"`
	At         time.Time `json:"at"`
}

// FinalizedEvent marks the job done; when Fused is true the job's fitted
// coefficients were written into machine history.
type FinalizedEvent struct {
	At             time.Time `json:"at"`
	Fused          bool      `json:"fused"`
	VerificationOK bool      `json:"verificationOk"`
}
