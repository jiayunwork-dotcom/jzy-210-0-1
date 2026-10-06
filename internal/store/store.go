// Package store defines persistence for machines, jobs, the append-only job
// event stream and per-job influence-coefficient contributions. Two backends
// exist: an in-memory backend (tests and zero-dependency runs) and MySQL 8.
package store

import (
	"context"
	"time"

	"balancer/internal/domain"
)

// MachineRecord is the persisted machine configuration.
type MachineRecord struct {
	ID                   string
	Name                 string
	Planes               []domain.Plane
	Points               []domain.Point
	Speeds               []int
	ConventionJSON       string
	ReadingChangeFloorUm float64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// JobRecord snapshots everything a job needs, so later machine edits never
// alter an existing job's replay.
type JobRecord struct {
	ID         string
	MachineID  string
	Name       string
	Snapshot   domain.SnapshotConfig
	UseHistory bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// EventRecord is an appended event row.
type EventRecord struct {
	Seq        int64
	JobID      string
	Type       domain.EventType
	Payload    []byte
	RunID      string // "" for non run_added events
	ClientID   string // "" unless the client supplied an idempotency key
	OccurredAt time.Time
	CreatedAt  time.Time
}

// ErrNotFound is returned for missing rows.
type ErrNotFound struct{ What string }

func (e *ErrNotFound) Error() string { return e.What + " not found" }

// ErrDuplicateClientID is returned when a run_added with the same clientId
// already exists for a job. Callers may treat it as idempotent success.
type ErrDuplicateClientID struct {
	JobID    string
	ClientID string
	Existing EventRecord
}

func (e *ErrDuplicateClientID) Error() string {
	return "duplicate clientId " + e.ClientID + " for job " + e.JobID
}

// EstimateRecord is a finalized job's coefficient contribution.
type EstimateRecord struct {
	JobID     string
	MachineID string
	Speed     int
	PlaneIDs  []string
	PointIDs  []string
	Re        []float64
	Im        []float64
	Uncert    []float64
	TrustOK   bool
	UpdatedAt time.Time
}

// CurrentCoeffRecord is the fused coefficient later jobs may reuse. It is
// derived from all trust-ok EstimateRecords and rewritten wholesale whenever
// a job finalizes or a finalized job is corrected.
type CurrentCoeffRecord struct {
	MachineID string
	Speed     int
	PlaneIDs  []string
	PointIDs  []string
	Re        []float64
	Im        []float64
	Uncert    []float64
	NContrib  int
	Status    string
	Reason    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Tx is a transactional persistence handle.
type Tx interface {
	// Machines.
	SaveMachine(ctx context.Context, m *MachineRecord) error
	GetMachine(ctx context.Context, id string) (*MachineRecord, error)
	ListMachines(ctx context.Context) ([]*MachineRecord, error)

	// Jobs.
	SaveJob(ctx context.Context, j *JobRecord) error
	GetJob(ctx context.Context, id string, forUpdate bool) (*JobRecord, error)
	ListJobsByMachine(ctx context.Context, machineID string) ([]*JobRecord, error)

	// Events. Seq is assigned per job in insertion order starting at 1.
	AppendEvent(ctx context.Context, e *EventRecord) (*EventRecord, error)
	ListEvents(ctx context.Context, jobID string) ([]EventRecord, error)
	FindEventByClientID(ctx context.Context, jobID, clientID string) (*EventRecord, error)

	// Coefficient contributions and the current fused coefficient.
	UpsertEstimate(ctx context.Context, r *EstimateRecord) error
	ListEstimates(ctx context.Context, machineID string) ([]EstimateRecord, error)
	// DeleteEstimatesForJob removes a job's coefficient contributions
	// (used when rebuilding machine history after a correction).
	DeleteEstimatesForJob(ctx context.Context, jobID string) error
	PutCurrentCoeff(ctx context.Context, r *CurrentCoeffRecord) error
	ListCurrentCoeffs(ctx context.Context, machineID string) ([]CurrentCoeffRecord, error)
	// DeleteCurrentCoeffsNotIn removes current coefficient rows of a machine
	// whose speed is not in keep (used when history is rebuilt and a speed
	// lost every contribution).
	DeleteCurrentCoeffsNotIn(ctx context.Context, machineID string, keep []int) error
}

// Store is the root handle; transactions serialize concurrent writers.
type Store interface {
	Tx
	// WithTx runs fn inside one transaction (committed on nil error).
	WithTx(ctx context.Context, fn func(tx Tx) error) error
	Close() error
}
