package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"balancing/internal/balance"
	"balancing/internal/history"
	"balancing/internal/vec"
)

// Errors returned by stores.
var (
	ErrNotFound                = storeErr("not found")
	ErrCorrectionMissingTarget = storeErr("correction targets a run that does not exist")
	ErrCorrectionAmbiguous     = storeErr("several runs share this run_time; specify target_seq")
	ErrHistoryUnavailable      = storeErr("no trusted influence coefficients for this machine/speed; trial runs are required")
)

type storeErr string

func (e storeErr) Error() string { return string(e) }

func newID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// buildMachineHistory replays every job of one machine into a history.State.
// Job order is creation order (a job only learns from jobs created before it).
func buildMachineHistory(jobs []*JobState) (*history.State, error) {
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].Job.CreatedAt.Before(jobs[j].Job.CreatedAt)
	})
	var strat history.Strategy = history.UncertaintyWeighted
	if len(jobs) > 0 {
		strat = jobs[0].Machine.FusionStrategy
	}
	st, err := history.NewState(strat)
	if err != nil {
		return nil, err
	}
	for _, js := range jobs {
		if js.Result == nil {
			continue
		}
		for _, sr := range js.Result.Speeds {
			if sr.Status != "ok" {
				continue
			}
			if js.Job.UseHistory {
				// reuse job: only its verification verdict affects history
				if sr.Verification != nil {
					st.AddCheck(history.CheckVerdict{
						JobID:       js.Job.ID,
						Speed:       sr.Speed,
						MismatchRMS: sr.Verification.MismatchRMS,
						ResidualRMS: sr.Verification.ResidualRMS,
						OriginalRMS: sr.Verification.OriginalRMS,
					})
				}
				continue
			}
			if !sr.HistoryEstimate {
				continue
			}
			h := matrixFromExternal(js.Machine, sr.Influence)
			st.AddTrial(history.Estimate{
				JobID:       js.Job.ID,
				Speed:       sr.Speed,
				H:           h,
				Uncertainty: sr.Uncertainty,
			})
			if sr.Verification != nil {
				st.MarkVerified(js.Job.ID, sr.Speed, sr.Verification.Reduction)
			}
		}
	}
	return st, nil
}

// matrixFromExternal converts an external influence matrix back to the
// internal frame (negate phases when angles grow with rotation).
func matrixFromExternal(m *Machine, ext [][]vec.Polar) balance.Matrix {
	h := balance.NewMatrix(len(ext), len(ext[0]))
	for i := range ext {
		for j := range ext[i] {
			p := ext[i][j]
			if m.Direction == vec.WithRotation {
				p.Phase = vec.Norm(-p.Phase)
			}
			h.Set(i, j, p.C())
		}
	}
	return h
}

// MemoryStore is the in-process Store, safe for concurrent use.
type MemoryStore struct {
	mu       sync.Mutex
	machines map[string]*Machine
	jobs     map[string]*jobRec
}

type jobRec struct {
	state  *JobState
	events []*Event
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{machines: map[string]*Machine{}, jobs: map[string]*jobRec{}}
}

func (s *MemoryStore) Ping(ctx context.Context) error { return nil }

func (s *MemoryStore) CreateMachine(ctx context.Context, m *Machine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = newID("m")
	}
	if _, exists := s.machines[m.ID]; exists {
		return storeErr("machine id exists")
	}
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	cp := *m
	s.machines[m.ID] = &cp
	return nil
}

func (s *MemoryStore) GetMachine(ctx context.Context, id string) (*Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (s *MemoryStore) ListMachines(ctx context.Context) ([]*Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Machine, 0, len(s.machines))
	for _, m := range s.machines {
		cp := *m
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) CreateJob(ctx context.Context, machineID, operator, note string, useHistory bool, at time.Time) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[machineID]
	if !ok {
		return nil, ErrNotFound
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	j := &Job{
		ID:         newID("j"),
		MachineID:  machineID,
		Operator:   operator,
		Note:       note,
		UseHistory: useHistory,
		CreatedAt:  at,
	}
	if useHistory {
		hist, err := s.rebuildHistoryLocked(m)
		if err != nil {
			return nil, err
		}
		j.HistoryInfluence = snapshotHistory(m, hist)
		if len(j.HistoryInfluence) != len(m.Speeds) {
			return nil, ErrHistoryUnavailable
		}
	}
	snap := *m
	st := &JobState{Job: j, Machine: &snap}
	rec := &jobRec{state: st}
	ev := &Event{
		JobID:     j.ID,
		Type:      EvJobCreated,
		Seq:       int64(len(rec.events)),
		RunTime:   at,
		CreatedAt: time.Now().UTC(),
		Payload:   marshalPayload(jobCreatedPayload{Job: *j, Machine: snap, At: at}),
	}
	rec.events = append(rec.events, ev)
	rs, err := replay(rec.events)
	if err != nil {
		return nil, err
	}
	rec.state = rs
	s.jobs[j.ID] = rec
	cp := *j
	return &cp, nil
}

func (s *MemoryStore) addEventLocked(rec *jobRec, evType EventType, runTime time.Time, payload any) *Event {
	ev := &Event{
		JobID:     rec.state.Job.ID,
		Type:      evType,
		Seq:       int64(len(rec.events)),
		RunTime:   runTime.UTC(),
		CreatedAt: time.Now().UTC(),
		Payload:   marshalPayload(payload),
	}
	rec.events = append(rec.events, ev)
	return ev
}

func (s *MemoryStore) AddRun(ctx context.Context, jobID string, in RunInput) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.jobs[jobID]
	if !ok {
		return nil, ErrNotFound
	}
	m := rec.state.Machine
	if ver := ValidateRunInput(in, m.PlaneCount, m.PointCount, speedSet(m)); ver != nil {
		return nil, ver
	}
	in.RunTime = in.RunTime.UTC()
	// Concurrent submissions with the same run time are both retained.
	if in.Kind == RunTrial {
		var orig *Run
		if rs, _ := replay(rec.events); rs != nil {
			for _, r := range rs.Runs {
				if r.Kind == RunOriginal {
					orig = r
					break
				}
			}
		}
		if orig != nil {
			if ver := checkTrialEffect(m, orig, in); ver != nil {
				return nil, ver
			}
		}
	}
	ev := s.addEventLocked(rec, EvRunRecorded, in.RunTime, runRecordedPayload{Input: in, At: time.Now().UTC()})
	rs, err := replay(rec.events)
	if err != nil {
		return nil, err
	}
	rec.state = rs
	newSeq := ev.Seq
	return findRun(rs, in.RunTime, &newSeq), nil
}

func findRun(rs *JobState, t time.Time, seq *int64) *Run {
	for _, r := range rs.Runs {
		if !r.RunTime.Equal(t.UTC()) {
			continue
		}
		if seq != nil && r.Seq != *seq {
			continue
		}
		return r
	}
	return nil
}

func (s *MemoryStore) CorrectRun(ctx context.Context, jobID string, in CorrectionInput) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.jobs[jobID]
	if !ok {
		return nil, ErrNotFound
	}
	m := rec.state.Machine
	if ver := ValidateCorrectionInput(in, m.PlaneCount, m.PointCount, speedSet(m)); ver != nil {
		return nil, ver
	}
	var target *Run
	if in.TargetSeq != nil {
		for _, r := range rec.state.Runs {
			if r.RunTime.Equal(in.TargetRunTime.UTC()) && r.Seq == *in.TargetSeq {
				target = r
				break
			}
		}
	} else {
		var matches []*Run
		for _, r := range rec.state.Runs {
			if r.RunTime.Equal(in.TargetRunTime.UTC()) {
				matches = append(matches, r)
			}
		}
		if len(matches) > 1 {
			return nil, ErrCorrectionAmbiguous
		}
		if len(matches) == 1 {
			target = matches[0]
		}
	}
	if target == nil {
		return nil, ErrCorrectionMissingTarget
	}
	s.addEventLocked(rec, EvRunCorrected, in.TargetRunTime, runCorrectedPayload{Input: in, At: time.Now().UTC()})
	rs, err := replay(rec.events)
	if err != nil {
		return nil, err
	}
	rec.state = rs
	return findRun(rs, in.TargetRunTime, in.TargetSeq), nil
}

func (s *MemoryStore) GetJob(ctx context.Context, id string, includeEvents bool) (*JobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	rs, err := replay(rec.events)
	if err != nil {
		return nil, err
	}
	if !includeEvents {
		rs.Events = nil
	}
	return rs, nil
}

func (s *MemoryStore) ListEvents(ctx context.Context, jobID string) ([]*Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.jobs[jobID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]*Event, len(rec.events))
	copy(out, rec.events)
	return out, nil
}

func (s *MemoryStore) ListJobs(ctx context.Context, machineID string) ([]*JobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[machineID]
	if !ok {
		return nil, ErrNotFound
	}
	var recs []*jobRec
	for _, rec := range s.jobs {
		if rec.state.Job.MachineID == machineID {
			recs = append(recs, rec)
		}
	}
	states := make([]*JobState, 0, len(recs))
	for _, rec := range recs {
		rs, err := replay(rec.events)
		if err != nil {
			return nil, err
		}
		rs.Events = nil
		states = append(states, rs)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Job.CreatedAt.Before(states[j].Job.CreatedAt) })
	_ = m
	return states, nil
}

func (s *MemoryStore) rebuildHistoryLocked(m *Machine) (*history.State, error) {
	var states []*JobState
	for _, rec := range s.jobs {
		if rec.state.Job.MachineID != m.ID {
			continue
		}
		rs, err := replay(rec.events)
		if err != nil {
			return nil, err
		}
		states = append(states, rs)
	}
	return buildMachineHistory(states)
}

func (s *MemoryStore) MachineHistory(ctx context.Context, machineID string) (*history.State, *Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.machines[machineID]
	if !ok {
		return nil, nil, ErrNotFound
	}
	st, err := s.rebuildHistoryLocked(m)
	if err != nil {
		return nil, nil, err
	}
	cp := *m
	return st, &cp, nil
}

func (s *MemoryStore) HistoryMatrix(ctx context.Context, machineID string, speed int) (balance.Matrix, vec.Convention, bool, error) {
	st, m, err := s.MachineHistory(ctx, machineID)
	if err != nil {
		return balance.Matrix{}, vec.Convention{}, false, err
	}
	entry, ok := st.EntryAt(speed)
	if !ok {
		return balance.Matrix{}, m.Convention(), false, nil
	}
	return entry.H, m.Convention(), true, nil
}

func (s *MemoryStore) Close() error { return nil }

func speedSet(m *Machine) map[int]bool {
	out := map[int]bool{}
	for _, sp := range m.Speeds {
		out[sp] = true
	}
	return out
}
