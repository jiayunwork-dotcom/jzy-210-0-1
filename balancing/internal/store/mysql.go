package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"balancing/internal/balance"
	"balancing/internal/history"
	"balancing/internal/split"
	"balancing/internal/vec"
)

// MySQLStore persists machines and the append-only job event stream in MySQL.
// All derived state is obtained through the same replay engine the in-memory
// store uses, so both backends behave identically.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore opens (and if needed prepares) the database connection pool.
func NewMySQLStore(dsn string) (*MySQLStore, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	return &MySQLStore{db: db}, nil
}

// Migrate creates the schema. Safe to call repeatedly.
func (s *MySQLStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS machines (
  id                 VARCHAR(40)  NOT NULL PRIMARY KEY,
  name               VARCHAR(200) NOT NULL,
  plane_count        TINYINT      NOT NULL,
  point_count        TINYINT      NOT NULL,
  speeds_json        JSON         NOT NULL,
  reference_deg      DOUBLE       NOT NULL,
  phase_direction    VARCHAR(20)  NOT NULL,
  hole_layout_json   JSON         NULL,
  fusion_strategy    VARCHAR(30)  NOT NULL,
  point_weights_json JSON         NULL,
  created_at         DATETIME(6)  NOT NULL,
  updated_at         DATETIME(6)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jobs (
  id         VARCHAR(40) NOT NULL PRIMARY KEY,
  machine_id VARCHAR(40) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  KEY idx_jobs_machine (machine_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS job_events (
  id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  job_id     VARCHAR(40)  NOT NULL,
  seq        BIGINT       NOT NULL,
  type       VARCHAR(30)  NOT NULL,
  run_time   DATETIME(6)  NOT NULL,
  payload    MEDIUMTEXT   NOT NULL,
  created_at DATETIME(6)  NOT NULL,
  UNIQUE KEY uq_job_seq (job_id, seq),
  KEY idx_job_events_job (job_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

func (s *MySQLStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *MySQLStore) Close() error { return s.db.Close() }

// ---- machines ----

func (s *MySQLStore) CreateMachine(ctx context.Context, m *Machine) error {
	if m.ID == "" {
		m.ID = newID("m")
	}
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	speeds, _ := json.Marshal(m.Speeds)
	holes, _ := json.Marshal(m.HoleLayout)
	weights, _ := json.Marshal(m.PointWeights)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO machines (id,name,plane_count,point_count,speeds_json,reference_deg,phase_direction,
                      hole_layout_json,fusion_strategy,point_weights_json,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.Name, m.PlaneCount, m.PointCount, string(speeds), m.ReferenceDeg, string(m.Direction),
		nullJSON(holes), string(m.FusionStrategy), nullJSON(weights), m.CreatedAt, m.UpdatedAt)
	return err
}

func nullJSON(b []byte) any {
	if string(b) == "null" {
		return nil
	}
	return string(b)
}

func scanMachine(row interface {
	Scan(...any) error
}) (*Machine, error) {
	m := &Machine{}
	var speeds, holes, weights sql.NullString
	var dir, fusionStr string
	if err := row.Scan(&m.ID, &m.Name, &m.PlaneCount, &m.PointCount, &speeds, &m.ReferenceDeg, &dir,
		&holes, &fusionStr, &weights, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Direction = vec.Direction(dir)
	m.FusionStrategy = history.Strategy(fusionStr)
	_ = json.Unmarshal([]byte(speeds.String), &m.Speeds)
	if holes.Valid && holes.String != "" {
		var hl []*split.Plane
		// JSON nulls inside the array decode into nil *split.Plane.
		_ = json.Unmarshal([]byte(holes.String), &hl)
		m.HoleLayout = hl
	}
	if weights.Valid && weights.String != "" {
		_ = json.Unmarshal([]byte(weights.String), &m.PointWeights)
	}
	return m, nil
}

const machineCols = `id,name,plane_count,point_count,speeds_json,reference_deg,phase_direction,
hole_layout_json,fusion_strategy,point_weights_json,created_at,updated_at`

func (s *MySQLStore) GetMachine(ctx context.Context, id string) (*Machine, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+machineCols+` FROM machines WHERE id=?`, id)
	m, err := scanMachine(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *MySQLStore) ListMachines(ctx context.Context) ([]*Machine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+machineCols+` FROM machines ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- jobs & events ----

func (s *MySQLStore) CreateJob(ctx context.Context, machineID, operator, note string, useHistory bool, at time.Time) (*Job, error) {
	m, err := s.GetMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	j := &Job{ID: newID("j"), MachineID: machineID, Operator: operator, Note: note, UseHistory: useHistory, CreatedAt: at}
	if useHistory {
		hs, err := s.machineHistoryFor(ctx, m)
		if err != nil {
			return nil, err
		}
		j.HistoryInfluence = snapshotHistory(m, hs)
		if len(j.HistoryInfluence) != len(m.Speeds) {
			return nil, ErrHistoryUnavailable
		}
	}
	snap := *m
	payload := marshalPayload(jobCreatedPayload{Job: *j, Machine: snap, At: at})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (id,machine_id,created_at) VALUES (?,?,?)`, j.ID, machineID, at); err != nil {
		return nil, err
	}
	if err := insertEvent(ctx, tx, j.ID, EvJobCreated, at, payload); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return j, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, jobID string, typ EventType, runTime time.Time, payload []byte) error {
	var nextSeq int64
	// MAX(seq) over the existing job rows locks the job's sequence under
	// InnoDB, serializing concurrent submissions to the same job.
	row := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM job_events WHERE job_id=? FOR UPDATE`, jobID)
	if err := row.Scan(&nextSeq); err != nil {
		return err
	}
	return insertEventAtSeq(ctx, tx, jobID, typ, nextSeq, runTime, payload)
}

func insertEventAtSeq(ctx context.Context, tx *sql.Tx, jobID string, typ EventType, seq int64, runTime time.Time, payload []byte) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO job_events (job_id,seq,type,run_time,payload,created_at) VALUES (?,?,?,?,?,?)`,
		jobID, seq, string(typ), runTime.UTC(), string(payload), time.Now().UTC())
	return err
}

func (s *MySQLStore) loadEvents(ctx context.Context, jobID string) ([]*Event, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id,job_id,seq,type,run_time,payload,created_at FROM job_events WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		ev := &Event{}
		var typ string
		var payload string
		if err := rows.Scan(&ev.ID, &ev.JobID, &ev.Seq, &typ, &ev.RunTime, &payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.Type = EventType(typ)
		ev.Payload = []byte(payload)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *MySQLStore) replayJob(ctx context.Context, jobID string) (*JobState, error) {
	events, err := s.loadEvents(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	return replay(events)
}

func (s *MySQLStore) GetJob(ctx context.Context, id string, includeEvents bool) (*JobState, error) {
	js, err := s.replayJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if !includeEvents {
		js.Events = nil
	}
	return js, nil
}

func (s *MySQLStore) ListEvents(ctx context.Context, jobID string) ([]*Event, error) {
	if _, err := s.replayJob(ctx, jobID); err != nil {
		return nil, err
	}
	return s.loadEvents(ctx, jobID)
}

func (s *MySQLStore) ListJobs(ctx context.Context, machineID string) ([]*JobState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM jobs WHERE machine_id=? ORDER BY created_at`, machineID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*JobState, 0, len(ids))
	for _, id := range ids {
		js, err := s.replayJob(ctx, id)
		if err != nil {
			return nil, err
		}
		js.Events = nil
		out = append(out, js)
	}
	return out, nil
}

func (s *MySQLStore) AddRun(ctx context.Context, jobID string, in RunInput) (*Run, error) {
	var newSeq int64
	err := s.withJobTx(ctx, jobID, func(m *Machine, rec []*Event, tx *sql.Tx) error {
		if ver := ValidateRunInput(in, m.PlaneCount, m.PointCount, speedSet(m)); ver != nil {
			return ver
		}
		in.RunTime = in.RunTime.UTC()
		js, err := replay(rec)
		if err != nil {
			return err
		}
		if in.Kind == RunTrial {
			for _, r := range js.Runs {
				if r.Kind == RunOriginal {
					if ver := checkTrialEffect(m, r, in); ver != nil {
						return ver
					}
					break
				}
			}
		}
		newSeq = nextSeq(rec)
		return insertEventAtSeq(ctx, tx, jobID, EvRunRecorded, newSeq, in.RunTime,
			marshalPayload(runRecordedPayload{Input: in, At: time.Now().UTC()}))
	})
	if err != nil {
		return nil, err
	}
	js, err := s.replayJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return findRun(js, in.RunTime, &newSeq), nil
}

func nextSeq(rec []*Event) int64 {
	var maxSeq int64
	for _, ev := range rec {
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	return maxSeq + 1
}

func (s *MySQLStore) CorrectRun(ctx context.Context, jobID string, in CorrectionInput) (*Run, error) {
	err := s.withJobTx(ctx, jobID, func(m *Machine, rec []*Event, tx *sql.Tx) error {
		if ver := ValidateCorrectionInput(in, m.PlaneCount, m.PointCount, speedSet(m)); ver != nil {
			return ver
		}
		js, err := replay(rec)
		if err != nil {
			return err
		}
		if in.TargetSeq != nil {
			if findRun(js, in.TargetRunTime, in.TargetSeq) == nil {
				return ErrCorrectionMissingTarget
			}
		} else {
			var n int
			for _, r := range js.Runs {
				if r.RunTime.Equal(in.TargetRunTime.UTC()) {
					n++
				}
			}
			if n == 0 {
				return ErrCorrectionMissingTarget
			}
			if n > 1 {
				return ErrCorrectionAmbiguous
			}
		}
		return insertEventAtSeq(ctx, tx, jobID, EvRunCorrected, nextSeq(rec), in.TargetRunTime,
			marshalPayload(runCorrectedPayload{Input: in, At: time.Now().UTC()}))
	})
	if err != nil {
		return nil, err
	}
	js, err := s.replayJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return findRun(js, in.TargetRunTime, in.TargetSeq), nil
}

// withJobTx locks the job's event rows, loads the machine snapshot and runs
// fn inside the transaction.
func (s *MySQLStore) withJobTx(ctx context.Context, jobID string, fn func(m *Machine, rec []*Event, tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var machineID string
	row := tx.QueryRowContext(ctx, `SELECT machine_id FROM jobs WHERE id=? FOR UPDATE`, jobID)
	if err := row.Scan(&machineID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	m, err := s.GetMachine(ctx, machineID)
	if err != nil {
		return err
	}
	// Lock the event sequence for the job before validating/inserting.
	var lock int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM job_events WHERE job_id=? FOR UPDATE`, jobID).Scan(&lock); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT seq,type,run_time,payload FROM job_events WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return err
	}
	var rec []*Event
	for rows.Next() {
		ev := &Event{JobID: jobID}
		var typ, payload string
		if err := rows.Scan(&ev.Seq, &typ, &ev.RunTime, &payload); err != nil {
			rows.Close()
			return err
		}
		ev.Type = EventType(typ)
		ev.Payload = []byte(payload)
		rec = append(rec, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := fn(m, rec, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- history ----

func (s *MySQLStore) machineHistoryFor(ctx context.Context, m *Machine) (*history.State, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM jobs WHERE machine_id=? ORDER BY created_at`, m.ID)
	if err != nil {
		return nil, err
	}
	var states []*JobState
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		js, err := s.replayJob(ctx, id)
		if err != nil {
			rows.Close()
			return nil, err
		}
		states = append(states, js)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildMachineHistory(states)
}

func (s *MySQLStore) MachineHistory(ctx context.Context, machineID string) (*history.State, *Machine, error) {
	m, err := s.GetMachine(ctx, machineID)
	if err != nil {
		return nil, nil, err
	}
	st, err := s.machineHistoryFor(ctx, m)
	if err != nil {
		return nil, nil, err
	}
	return st, m, nil
}

func (s *MySQLStore) HistoryMatrix(ctx context.Context, machineID string, speed int) (balance.Matrix, vec.Convention, bool, error) {
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

// guard against drift if Machine changes
var _ = fmt.Sprintf
