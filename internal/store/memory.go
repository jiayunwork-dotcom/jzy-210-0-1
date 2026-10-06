package store

import (
	"context"
	"sort"
	"strconv"
	"sync"
)

// Memory is an in-process Store. All access is serialized by one mutex, so
// WithTx is effectively serializable isolation — the same level the MySQL
// backend provides with SELECT ... FOR UPDATE on the job row.
type Memory struct {
	mu        sync.Mutex
	machines  map[string]*MachineRecord
	jobs      map[string]*JobRecord
	events    map[string][]EventRecord // jobID -> rows, appended in seq order
	seq       map[string]int64
	estimates map[string]map[string]EstimateRecord // machine -> job|speed key
	current   map[string]map[int]CurrentCoeffRecord
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		machines:  map[string]*MachineRecord{},
		jobs:      map[string]*JobRecord{},
		events:    map[string][]EventRecord{},
		seq:       map[string]int64{},
		estimates: map[string]map[string]EstimateRecord{},
		current:   map[string]map[int]CurrentCoeffRecord{},
	}
}

type memoryTx struct{ m *Memory }

// WithTx runs fn with the global write lock held.
func (m *Memory) WithTx(ctx context.Context, fn func(tx Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(&memoryTx{m})
}

func (m *Memory) Close() error { return nil }

// Single-operation wrappers satisfying the embedded Tx of Store.
func (m *Memory) SaveMachine(ctx context.Context, rec *MachineRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.SaveMachine(ctx, rec) })
}
func (m *Memory) GetMachine(ctx context.Context, id string) (*MachineRecord, error) {
	var out *MachineRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.GetMachine(ctx, id)
		return e
	})
	return out, err
}
func (m *Memory) ListMachines(ctx context.Context) ([]*MachineRecord, error) {
	var out []*MachineRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListMachines(ctx)
		return e
	})
	return out, err
}
func (m *Memory) SaveJob(ctx context.Context, rec *JobRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.SaveJob(ctx, rec) })
}
func (m *Memory) GetJob(ctx context.Context, id string, forUpdate bool) (*JobRecord, error) {
	var out *JobRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.GetJob(ctx, id, forUpdate)
		return e
	})
	return out, err
}
func (m *Memory) ListJobsByMachine(ctx context.Context, machineID string) ([]*JobRecord, error) {
	var out []*JobRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListJobsByMachine(ctx, machineID)
		return e
	})
	return out, err
}
func (m *Memory) AppendEvent(ctx context.Context, e *EventRecord) (*EventRecord, error) {
	var out *EventRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var x error
		out, x = tx.AppendEvent(ctx, e)
		return x
	})
	return out, err
}
func (m *Memory) ListEvents(ctx context.Context, jobID string) ([]EventRecord, error) {
	var out []EventRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListEvents(ctx, jobID)
		return e
	})
	return out, err
}
func (m *Memory) FindEventByClientID(ctx context.Context, jobID, clientID string) (*EventRecord, error) {
	var out *EventRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.FindEventByClientID(ctx, jobID, clientID)
		return e
	})
	return out, err
}
func (m *Memory) UpsertEstimate(ctx context.Context, r *EstimateRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.UpsertEstimate(ctx, r) })
}
func (m *Memory) ListEstimates(ctx context.Context, machineID string) ([]EstimateRecord, error) {
	var out []EstimateRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListEstimates(ctx, machineID)
		return e
	})
	return out, err
}
func (m *Memory) DeleteEstimatesForJob(ctx context.Context, jobID string) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.DeleteEstimatesForJob(ctx, jobID) })
}
func (m *Memory) PutCurrentCoeff(ctx context.Context, r *CurrentCoeffRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.PutCurrentCoeff(ctx, r) })
}
func (m *Memory) ListCurrentCoeffs(ctx context.Context, machineID string) ([]CurrentCoeffRecord, error) {
	var out []CurrentCoeffRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListCurrentCoeffs(ctx, machineID)
		return e
	})
	return out, err
}
func (m *Memory) DeleteCurrentCoeffsNotIn(ctx context.Context, machineID string, keep []int) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.DeleteCurrentCoeffsNotIn(ctx, machineID, keep) })
}

func (t *memoryTx) SaveMachine(ctx context.Context, rec *MachineRecord) error {
	cp := *rec
	t.m.machines[rec.ID] = &cp
	return nil
}

func (t *memoryTx) GetMachine(ctx context.Context, id string) (*MachineRecord, error) {
	rec, ok := t.m.machines[id]
	if !ok {
		return nil, &ErrNotFound{What: "machine " + id}
	}
	cp := *rec
	return &cp, nil
}

func (t *memoryTx) ListMachines(ctx context.Context) ([]*MachineRecord, error) {
	out := make([]*MachineRecord, 0, len(t.m.machines))
	for _, rec := range t.m.machines {
		cp := *rec
		out = append(out, &cp)
	}
	return out, nil
}

func (t *memoryTx) SaveJob(ctx context.Context, rec *JobRecord) error {
	cp := *rec
	t.m.jobs[rec.ID] = &cp
	if _, ok := t.m.events[rec.ID]; !ok {
		t.m.events[rec.ID] = nil
	}
	return nil
}

func (t *memoryTx) GetJob(ctx context.Context, id string, forUpdate bool) (*JobRecord, error) {
	rec, ok := t.m.jobs[id]
	if !ok {
		return nil, &ErrNotFound{What: "job " + id}
	}
	cp := *rec
	return &cp, nil
}

func (t *memoryTx) ListJobsByMachine(ctx context.Context, machineID string) ([]*JobRecord, error) {
	var out []*JobRecord
	for _, rec := range t.m.jobs {
		if rec.MachineID == machineID {
			cp := *rec
			out = append(out, &cp)
		}
	}
	sortJobsByUpdated(out)
	return out, nil
}

func (t *memoryTx) AppendEvent(ctx context.Context, e *EventRecord) (*EventRecord, error) {
	if e.ClientID != "" {
		for _, ex := range t.m.events[e.JobID] {
			if ex.ClientID == e.ClientID {
				return nil, &ErrDuplicateClientID{JobID: e.JobID, ClientID: e.ClientID, Existing: ex}
			}
		}
	}
	t.m.seq[e.JobID]++
	row := *e
	row.Seq = t.m.seq[e.JobID]
	t.m.events[e.JobID] = append(t.m.events[e.JobID], row)
	out := row
	return &out, nil
}

func (t *memoryTx) ListEvents(ctx context.Context, jobID string) ([]EventRecord, error) {
	rows := t.m.events[jobID]
	out := make([]EventRecord, len(rows))
	copy(out, rows)
	return out, nil
}

func (t *memoryTx) FindEventByClientID(ctx context.Context, jobID, clientID string) (*EventRecord, error) {
	for _, ex := range t.m.events[jobID] {
		if ex.ClientID == clientID {
			cp := ex
			return &cp, nil
		}
	}
	return nil, &ErrNotFound{What: "event clientId=" + clientID}
}

func (t *memoryTx) UpsertEstimate(ctx context.Context, r *EstimateRecord) error {
	byMachine := t.m.estimates[r.MachineID]
	if byMachine == nil {
		byMachine = map[string]EstimateRecord{}
		t.m.estimates[r.MachineID] = byMachine
	}
	byMachine[r.JobID+"#"+strconv.Itoa(r.Speed)] = *r
	return nil
}

func (t *memoryTx) ListEstimates(ctx context.Context, machineID string) ([]EstimateRecord, error) {
	var out []EstimateRecord
	for _, r := range t.m.estimates[machineID] {
		out = append(out, r)
	}
	return out, nil
}

func (t *memoryTx) DeleteEstimatesForJob(ctx context.Context, jobID string) error {
	for _, byMachine := range t.m.estimates {
		for k, r := range byMachine {
			if r.JobID == jobID {
				delete(byMachine, k)
			}
		}
	}
	return nil
}

func (t *memoryTx) PutCurrentCoeff(ctx context.Context, r *CurrentCoeffRecord) error {
	byMachine := t.m.current[r.MachineID]
	if byMachine == nil {
		byMachine = map[int]CurrentCoeffRecord{}
		t.m.current[r.MachineID] = byMachine
	}
	byMachine[r.Speed] = *r
	return nil
}

func (t *memoryTx) ListCurrentCoeffs(ctx context.Context, machineID string) ([]CurrentCoeffRecord, error) {
	var out []CurrentCoeffRecord
	for _, r := range t.m.current[machineID] {
		out = append(out, r)
	}
	return out, nil
}

func (t *memoryTx) DeleteCurrentCoeffsNotIn(ctx context.Context, machineID string, keep []int) error {
	byMachine := t.m.current[machineID]
	ok := map[int]bool{}
	for _, s := range keep {
		ok[s] = true
	}
	for speed := range byMachine {
		if !ok[speed] {
			delete(byMachine, speed)
		}
	}
	return nil
}

func sortJobsByUpdated(js []*JobRecord) {
	sort.Slice(js, func(i, j int) bool { return js[i].UpdatedAt.Before(js[j].UpdatedAt) })
}
