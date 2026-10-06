// Package domain holds the core types, validation rules and the event model
// of the balancing service. It depends on no infrastructure packages.
package domain

import (
	"time"

	"balancer/pkg/vec"
)

// Limits enforced by Validate* below; the exact numbers are part of the
// service contract.
const (
	MinPlanes, MaxPlanes = 1, 2
	MinPoints, MaxPoints = 1, 4
	MinSpeeds            = 1
	MinHoles             = 3
	MaxPhase             = 360.0
)

// RunKind categorizes a rotor run within a balancing job.
type RunKind string

const (
	RunOriginal   RunKind = "original"   // baseline, no attached weights
	RunTrial      RunKind = "trial"      // one plane carries a known trial weight
	RunVerify     RunKind = "verify"     // verification after correction (not fitted)
	RunCorrection RunKind = "correction" // additional trim run (fitted, not verify)
)

// Plane is a correction face. HoleCount == 0 means weights may be placed at
// any continuous angle; otherwise only the equally spaced holes exist.
type Plane struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Radius    float64 `json:"radiusMm,omitempty"`
	HoleCount int     `json:"holeCount"` // 0 = free angle, otherwise >= 3
}

// Point is a vibration measurement location (bearing, direction).
type Point struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Machine configuration. Angles in the API use Convention; internally they
// are converted to the canonical CCW frame.
type Machine struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Planes     []Plane        `json:"planes"`
	Points     []Point        `json:"points"`
	Speeds     []int          `json:"speedsRpm"` // one or more working speeds; first is primary
	Convention vec.Convention `json:"convention"`
	// ReadingChangeFloorUm is the minimum |trial-original| change considered
	// measurable at this machine (ill-conditioning rejection). <=0 uses the
	// service default.
	ReadingChangeFloorUm float64   `json:"readingChangeFloorUm,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// PrimarySpeed is the speed used for single-speed recommendations.
func (m *Machine) PrimarySpeed() int { return m.Speeds[0] }

// PlaneIndex and PointIndex resolve ids; -1 when absent.
func (m *Machine) PlaneIndex(id string) int {
	for i, p := range m.Planes {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (m *Machine) PointIndex(id string) int {
	for i, p := range m.Points {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (m *Machine) SpeedIndex(rpm int) int {
	for i, s := range m.Speeds {
		if s == rpm {
			return i
		}
	}
	return -1
}

func (m *Machine) HoleCount(planeID string) int {
	if i := m.PlaneIndex(planeID); i >= 0 {
		return m.Planes[i].HoleCount
	}
	return 0
}

// Weight is an attached mass expressed in the machine's field convention.
type Weight struct {
	PlaneID string    `json:"planeId"`
	Polar   vec.Polar `json:"polar"` // amp = grams, phase = field degrees
}

// Reading is one point's vibration at one speed, field convention.
type Reading struct {
	PointID string    `json:"pointId"`
	Speed   int       `json:"speedRpm"`
	Polar   vec.Polar `json:"polar"` // amp = µm, phase = field degrees
}

// RunInput is a submitted run record.
type RunInput struct {
	ClientID string    `json:"clientId"` // idempotency/dedup key (optional)
	Kind     RunKind   `json:"kind"`
	RunAt    time.Time `json:"runAt"`
	Note     string    `json:"note,omitempty"`
	Weights  []Weight  `json:"weights"`
	Readings []Reading `json:"readings"`
}

// RunFix replaces the editable parts of a previously accepted run.
type RunFix struct {
	Weights  *[]Weight  `json:"weights,omitempty"`
	Readings *[]Reading `json:"readings,omitempty"`
	RunAt    *time.Time `json:"runAt,omitempty"`
	Note     *string    `json:"note,omitempty"`
}

// Job is a balancing job for one machine. Convention and layout are snapshotted
// from the machine at creation time so event replay stays deterministic even
// if the machine configuration changes later.
type Job struct {
	ID         string         `json:"id"`
	MachineID  string         `json:"machineId"`
	Name       string         `json:"name"`
	Convention vec.Convention `json:"convention"`
	PlaneIDs   []string       `json:"planeIds"`
	PlaneHoles []int          `json:"planeHoles"`
	PointIDs   []string       `json:"pointIds"`
	Speeds     []int          `json:"speedsRpm"`
	UseHistory bool           `json:"useHistory"` // started from stored coefficients
	// Reading rejection thresholds snapshotted from the machine.
	ReadingChangeFloorUm float64   `json:"readingChangeFloorUm"`
	ReadingChangeRel     float64   `json:"readingChangeRel"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// SnapshotConfig carries the layout needed to validate and project a job.
type SnapshotConfig struct {
	Convention           vec.Convention
	PlaneIDs             []string
	PlaneHoles           []int
	PointIDs             []string
	Speeds               []int
	ReadingChangeFloorUm float64
	ReadingChangeRel     float64
}

// Snapshot returns the layout/convention captured at job creation.
func (j *Job) Snapshot() SnapshotConfig {
	return SnapshotConfig{
		Convention: j.Convention, PlaneIDs: j.PlaneIDs, PlaneHoles: j.PlaneHoles,
		PointIDs: j.PointIDs, Speeds: j.Speeds,
		ReadingChangeFloorUm: j.ReadingChangeFloorUm,
		ReadingChangeRel:     j.ReadingChangeRel,
	}
}
