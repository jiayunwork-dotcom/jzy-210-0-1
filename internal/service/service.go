// Package service is the transactional business layer: it validates input,
// appends events, replays job state and maintains per-machine fused
// influence coefficients.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"balancer/internal/compute"
	"balancer/internal/domain"
	"balancer/internal/store"
	"balancer/pkg/history"
	"balancer/pkg/vec"

	"crypto/rand"
	"encoding/hex"
)

// Rejection is a request the service refuses, with the offending fields.
type Rejection struct {
	StatusCode int
	Message    string
	Fields     []domain.FieldError
}

func (r *Rejection) Error() string {
	if len(r.Fields) == 0 {
		return r.Message
	}
	parts := make([]string, len(r.Fields))
	for i, f := range r.Fields {
		parts[i] = f.Field + ": " + f.Reason
	}
	return r.Message + " (" + strings.Join(parts, "; ") + ")"
}

// IsRejection reports whether err is a *Rejection.
func IsRejection(err error) (*Rejection, bool) {
	var r *Rejection
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

func reject(status int, msg string, fields ...domain.FieldError) error {
	return &Rejection{StatusCode: status, Message: msg, Fields: fields}
}

// Tunable default ill-conditioning screens.
const (
	DefaultFloorUm = 2.0  // µm minimum |trial-original| change at ≥1 point
	DefaultRel     = 0.05 // ...or 5% of the original amplitude, whichever larger
)

// Service orchestrates the store and the compute projection.
type Service struct {
	Store  store.Store
	Policy history.Policy
	Now    func() time.Time
}

// New constructs a service with default policy.
func New(s store.Store) *Service {
	return &Service{Store: s, Policy: history.DefaultPolicy(), Now: time.Now}
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// ---- Machines ----

// CreateMachineInput is the API-facing machine creation body.
type CreateMachineInput struct {
	Name                 string         `json:"name"`
	Planes               []domain.Plane `json:"planes"`
	Points               []domain.Point `json:"points"`
	Speeds               []int          `json:"speedsRpm"`
	Convention           vec.Convention `json:"convention"`
	ReadingChangeFloorUm float64        `json:"readingChangeFloorUm,omitempty"`
}

// CreateMachine validates and stores a machine.
func (s *Service) CreateMachine(ctx context.Context, in *CreateMachineInput) (*domain.Machine, error) {
	m := &domain.Machine{
		ID: newID("mac"), Name: in.Name, Planes: in.Planes, Points: in.Points,
		Speeds: in.Speeds, Convention: in.Convention,
		ReadingChangeFloorUm: in.ReadingChangeFloorUm,
	}
	if err := domain.ValidateMachine(m); err != nil {
		return nil, asRejection(err)
	}
	if m.ReadingChangeFloorUm == 0 {
		m.ReadingChangeFloorUm = DefaultFloorUm
	}
	now := s.Now()
	m.CreatedAt, m.UpdatedAt = now, now
	convJSON, _ := json.Marshal(m.Convention)
	rec := &store.MachineRecord{
		ID: m.ID, Name: m.Name, Planes: m.Planes, Points: m.Points, Speeds: m.Speeds,
		ConventionJSON: string(convJSON), ReadingChangeFloorUm: m.ReadingChangeFloorUm,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if err := s.Store.WithTx(ctx, func(tx store.Tx) error { return tx.SaveMachine(ctx, rec) }); err != nil {
		return nil, err
	}
	return m, nil
}

// GetMachine loads a machine with its configuration.
func (s *Service) GetMachine(ctx context.Context, id string) (*domain.Machine, error) {
	var out *domain.Machine
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		m, err := s.loadMachine(ctx, tx, id)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// ListMachines returns all machines.
func (s *Service) ListMachines(ctx context.Context) ([]*domain.Machine, error) {
	var out []*domain.Machine
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		recs, err := tx.ListMachines(ctx)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			m, err := machineFromRecord(rec)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

func (s *Service) loadMachine(ctx context.Context, tx store.Tx, id string) (*domain.Machine, error) {
	rec, err := tx.GetMachine(ctx, id)
	if err != nil {
		var nf *store.ErrNotFound
		if errors.As(err, &nf) {
			return nil, reject(404, "machine not found", domain.FieldError{Field: "machineId", Reason: nf.Error()})
		}
		return nil, err
	}
	return machineFromRecord(rec)
}

func machineFromRecord(rec *store.MachineRecord) (*domain.Machine, error) {
	var conv vec.Convention
	if rec.ConventionJSON != "" {
		if err := json.Unmarshal([]byte(rec.ConventionJSON), &conv); err != nil {
			return nil, err
		}
	}
	if !conv.Valid() {
		conv = vec.InternalDefault()
	}
	return &domain.Machine{
		ID: rec.ID, Name: rec.Name, Planes: rec.Planes, Points: rec.Points, Speeds: rec.Speeds,
		Convention: conv, ReadingChangeFloorUm: rec.ReadingChangeFloorUm,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}, nil
}

func asRejection(err error) error {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return reject(422, "validation failed", ve.Errors...)
	}
	var cov *domain.ReadingCoverageError
	if errors.As(err, &cov) {
		return reject(422, cov.Error(), domain.FieldError{Field: "readings", Reason: cov.Error()})
	}
	return err
}

// ---- Jobs ----

// CreateJobInput starts a balancing job. When UseHistory is true the job
// starts from stored active coefficients; creation fails unless one exists
// for every configured speed.
type CreateJobInput struct {
	MachineID  string `json:"machineId"`
	Name       string `json:"name"`
	UseHistory bool   `json:"useHistory"`
}

// JobView is the returned job plus the current derived state.
type JobView struct {
	Job   *domain.Job    `json:"job"`
	State *compute.State `json:"state"`
}

// CreateJob snapshots the machine configuration and appends job_created.
func (s *Service) CreateJob(ctx context.Context, in *CreateJobInput) (*JobView, error) {
	var view *JobView
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		m, err := s.loadMachine(ctx, tx, in.MachineID)
		if err != nil {
			return err
		}
		var current map[int]*history.Entry
		if in.UseHistory {
			current, err = activeHistory(ctx, tx, m.ID, m.Speeds)
			if err != nil {
				return err
			}
		}
		now := s.Now()
		j := &domain.Job{
			ID: newID("job"), MachineID: m.ID,
			Name:                 defaultName(in.Name, "balancing job"),
			Convention:           m.Convention,
			PlaneIDs:             planeIDs(m.Planes),
			PlaneHoles:           holeCounts(m.Planes),
			PointIDs:             pointIDs(m.Points),
			Speeds:               append([]int(nil), m.Speeds...),
			UseHistory:           in.UseHistory,
			ReadingChangeFloorUm: m.ReadingChangeFloorUm,
			ReadingChangeRel:     DefaultRel,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		jrec := jobRecord(j)
		if err := tx.SaveJob(ctx, jrec); err != nil {
			return err
		}
		created := domain.JobCreatedEvent{
			JobID: j.ID, MachineID: j.MachineID, Name: j.Name,
			Convention: j.Convention, PlaneIDs: j.PlaneIDs, PlaneHoles: j.PlaneHoles,
			PointIDs: j.PointIDs, Speeds: j.Speeds, UseHistory: j.UseHistory, At: now,
		}
		payload, _ := json.Marshal(created)
		if _, err := tx.AppendEvent(ctx, &store.EventRecord{
			JobID: j.ID, Type: domain.EvtJobCreated, Payload: payload, OccurredAt: now,
		}); err != nil {
			return err
		}
		events, err := tx.ListEvents(ctx, j.ID)
		if err != nil {
			return err
		}
		st, err := compute.Replay(j, toDomainEvents(events), current)
		if err != nil {
			return err
		}
		view = &JobView{Job: j, State: st}
		return nil
	})
	return view, err
}

func defaultName(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func planeIDs(ps []domain.Plane) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func holeCounts(ps []domain.Plane) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = p.HoleCount
	}
	return out
}

func pointIDs(ps []domain.Point) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func jobRecord(j *domain.Job) *store.JobRecord {
	return &store.JobRecord{
		ID: j.ID, MachineID: j.MachineID, Name: j.Name,
		Snapshot: j.Snapshot(), UseHistory: j.UseHistory,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

func jobFromRecord(rec *store.JobRecord) *domain.Job {
	return &domain.Job{
		ID: rec.ID, MachineID: rec.MachineID, Name: rec.Name,
		Convention: rec.Snapshot.Convention,
		PlaneIDs:   rec.Snapshot.PlaneIDs, PlaneHoles: rec.Snapshot.PlaneHoles,
		PointIDs: rec.Snapshot.PointIDs, Speeds: rec.Snapshot.Speeds,
		UseHistory:           rec.UseHistory,
		ReadingChangeFloorUm: rec.Snapshot.ReadingChangeFloorUm,
		ReadingChangeRel:     rec.Snapshot.ReadingChangeRel,
		CreatedAt:            rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
}

func toDomainEvents(rows []store.EventRecord) []domain.StoredEvent {
	out := make([]domain.StoredEvent, len(rows))
	for i, r := range rows {
		out[i] = domain.StoredEvent{
			Seq: r.Seq, JobID: r.JobID, Type: r.Type,
			OccurredAt: r.OccurredAt, Payload: r.Payload,
		}
	}
	return out
}

// activeHistory loads active stored coefficients for all required speeds.
func activeHistory(ctx context.Context, tx store.Tx, machineID string, speeds []int) (map[int]*history.Entry, error) {
	recs, err := tx.ListCurrentCoeffs(ctx, machineID)
	if err != nil {
		return nil, err
	}
	bySpeed := map[int]store.CurrentCoeffRecord{}
	for _, r := range recs {
		bySpeed[r.Speed] = r
	}
	out := map[int]*history.Entry{}
	var missing []domain.FieldError
	for _, sp := range speeds {
		r, ok := bySpeed[sp]
		if !ok || r.Status != history.StatusActive {
			missing = append(missing, domain.FieldError{
				Field:  "useHistory",
				Reason: fmt.Sprintf("no trusted stored influence coefficients for %d rpm; a trial run is required", sp),
			})
			continue
		}
		out[sp] = entryFromRecord(r)
	}
	if len(missing) > 0 {
		return nil, reject(409, "stored coefficients unavailable", missing...)
	}
	return out, nil
}

func entryFromRecord(r store.CurrentCoeffRecord) *history.Entry {
	return &history.Entry{
		MachineID: r.MachineID, Speed: r.Speed,
		PlaneIDs: append([]string(nil), r.PlaneIDs...),
		PointIDs: append([]string(nil), r.PointIDs...),
		Re:       append([]float64(nil), r.Re...), Im: append([]float64(nil), r.Im...),
		Uncert: append([]float64(nil), r.Uncert...),
		Status: r.Status, NContrib: r.NContrib,
		UpdatedAt: r.UpdatedAt, CreatedAt: r.CreatedAt,
	}
}
