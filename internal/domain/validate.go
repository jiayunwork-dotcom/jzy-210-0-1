package domain

import (
	"fmt"
	"sort"
	"strings"

	"balancer/pkg/vec"
)

// FieldError identifies one rejected field.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ValidationError aggregates field errors; the API maps it to HTTP 422.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Field + ": " + fe.Reason
	}
	return "validation failed (" + strings.Join(parts, "; ") + ")"
}

func (e *ValidationError) add(field, reason string, args ...any) {
	e.Errors = append(e.Errors, FieldError{Field: field, Reason: fmt.Sprintf(reason, args...)})
}

// Has reports whether the given field is among the errors.
func (e *ValidationError) Has(field string) bool {
	for _, fe := range e.Errors {
		if fe.Field == field {
			return true
		}
	}
	return false
}

// ValidateMachine validates machine configuration fields.
func ValidateMachine(m *Machine) error {
	ve := &ValidationError{}
	if strings.TrimSpace(m.Name) == "" {
		ve.add("name", "must not be empty")
	}
	if len(m.Planes) < MinPlanes || len(m.Planes) > MaxPlanes {
		ve.add("planes", "correction plane count must be between %d and %d, got %d", MinPlanes, MaxPlanes, len(m.Planes))
	}
	if len(m.Points) < MinPoints || len(m.Points) > MaxPoints {
		ve.add("points", "measurement point count must be between %d and %d, got %d", MinPoints, MaxPoints, len(m.Points))
	}
	if len(m.Speeds) < MinSpeeds {
		ve.add("speedsRpm", "at least one working speed is required")
	}
	seenIDs := map[string]bool{}
	for i, p := range m.Planes {
		f := fmt.Sprintf("planes[%d]", i)
		if strings.TrimSpace(p.ID) == "" {
			ve.add(f+".id", "must not be empty")
		} else if seenIDs["plane:"+p.ID] {
			ve.add(f+".id", "duplicate plane id %q", p.ID)
		}
		seenIDs["plane:"+p.ID] = true
		if p.HoleCount != 0 && p.HoleCount < MinHoles {
			ve.add(f+".holeCount", "weight-hole count must be 0 (free angle) or at least %d, got %d", MinHoles, p.HoleCount)
		}
	}
	for i, p := range m.Points {
		f := fmt.Sprintf("points[%d]", i)
		if strings.TrimSpace(p.ID) == "" {
			ve.add(f+".id", "must not be empty")
		} else if seenIDs["point:"+p.ID] {
			ve.add(f+".id", "duplicate point id %q", p.ID)
		}
		seenIDs["point:"+p.ID] = true
	}
	seenSpeed := map[int]bool{}
	for i, s := range m.Speeds {
		if s <= 0 {
			ve.add(fmt.Sprintf("speedsRpm[%d]", i), "speed must be positive rpm, got %d", s)
		}
		if seenSpeed[s] {
			ve.add(fmt.Sprintf("speedsRpm[%d]", i), "duplicate speed %d", s)
		}
		seenSpeed[s] = true
	}
	if !m.Convention.Valid() {
		ve.add("convention.direction", "must be %d (CCW) or %d (CW)", vec.DirectionCCW, vec.DirectionCW)
	}
	if m.ReadingChangeFloorUm < 0 {
		ve.add("readingChangeFloorUm", "must be non-negative")
	}
	if len(ve.Errors) > 0 {
		return ve
	}
	return nil
}

// ValidateRunInput validates a run against the job snapshot. It checks all
// individually checkable fields; cross-run rules (existence of the original
// run, ill-conditioning vs. the original) are checked by the service layer,
// which has the projected job state.
func ValidateRunInput(cfg SnapshotConfig, in *RunInput) error {
	ve := &ValidationError{}
	switch in.Kind {
	case RunOriginal, RunTrial, RunVerify, RunCorrection:
	default:
		ve.add("kind", "must be one of original, trial, verify, correction; got %q", in.Kind)
	}
	if in.RunAt.IsZero() {
		ve.add("runAt", "must not be zero")
	}
	validateWeights(cfg, in.Weights, ve)
	validateReadings(cfg, in.Readings, ve)
	if in.Kind == RunOriginal && len(in.Weights) != 0 {
		ve.add("weights", "the original run must record no attached weights, got %d", len(in.Weights))
	}
	if in.Kind == RunTrial {
		nz := 0
		for _, w := range in.Weights {
			if w.Polar.Amp != 0 {
				nz++
			}
		}
		if nz == 0 {
			ve.add("weights", "a trial run requires a non-zero trial weight; trial weight must not be zero")
		}
	}
	if len(ve.Errors) > 0 {
		return ve
	}
	return nil
}

func validateWeights(cfg SnapshotConfig, ws []Weight, ve *ValidationError) {
	for i, w := range ws {
		f := fmt.Sprintf("weights[%d]", i)
		if indexOf(cfg.PlaneIDs, w.PlaneID) < 0 {
			ve.add(f+".planeId", "unknown plane %q for this job", w.PlaneID)
		}
		if w.Polar.Amp < 0 {
			ve.add(f+".polar.amp", "weight amplitude must not be negative, got %g", w.Polar.Amp)
		}
		if w.Polar.Phase < 0 || w.Polar.Phase >= MaxPhase {
			ve.add(f+".polar.phase", "phase must be within [0, 360) degrees, got %g", w.Polar.Phase)
		}
	}
	seen := map[string]bool{}
	for _, w := range ws {
		if seen[w.PlaneID] {
			ve.add("weights", "at most one weight entry per plane, plane %q repeated", w.PlaneID)
		}
		seen[w.PlaneID] = true
	}
}

func validateReadings(cfg SnapshotConfig, rs []Reading, ve *ValidationError) {
	if len(rs) == 0 {
		ve.add("readings", "at least one reading is required")
	}
	seen := map[string]bool{}
	for i, r := range rs {
		f := fmt.Sprintf("readings[%d]", i)
		if indexOf(cfg.PointIDs, r.PointID) < 0 {
			ve.add(f+".pointId", "unknown measurement point %q for this job", r.PointID)
		}
		if indexOfInt(cfg.Speeds, r.Speed) < 0 {
			ve.add(f+".speedRpm", "speed %d is not configured for this machine", r.Speed)
		}
		if r.Polar.Amp < 0 {
			ve.add(f+".polar.amp", "amplitude must not be negative, got %g", r.Polar.Amp)
		}
		if r.Polar.Phase < 0 || r.Polar.Phase >= MaxPhase {
			ve.add(f+".polar.phase", "phase must be within [0, 360) degrees, got %g", r.Polar.Phase)
		}
		key := r.PointID + "@" + fmt.Sprint(r.Speed)
		if seen[key] {
			ve.add(f, "duplicate reading for point %q at %d rpm", r.PointID, r.Speed)
		}
		seen[key] = true
	}
}

// ReadingCoverageError lists point/speed combinations missing from a run that
// participates in coefficient fitting (fitted runs must be complete).
type ReadingCoverageError struct {
	Missing []string
}

func (e *ReadingCoverageError) Error() string {
	return "readings incomplete, missing " + strings.Join(e.Missing, ", ")
}

// CheckCoverage ensures readings include every point × speed combination.
func CheckCoverage(cfg SnapshotConfig, rs []Reading) error {
	have := map[string]bool{}
	for _, r := range rs {
		have[r.PointID+"@"+fmt.Sprint(r.Speed)] = true
	}
	var missing []string
	for _, s := range cfg.Speeds {
		for _, p := range cfg.PointIDs {
			k := p + "@" + fmt.Sprint(s)
			if !have[k] {
				missing = append(missing, k)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return &ReadingCoverageError{Missing: missing}
	}
	return nil
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
