package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"balancer/internal/domain"
)

// MySQL is the MySQL 8 backend.
type MySQL struct {
	db *sql.DB
}

// OpenMySQL opens (creating it if needed), runs migrations and pings.
func OpenMySQL(ctx context.Context, dsn string, migrationsDir string) (*MySQL, error) {
	rootCfg, err := parseRootCfg(dsn)
	if err != nil {
		return nil, err
	}
	root, err := sql.Open("mysql", rootCfg.String())
	if err != nil {
		return nil, err
	}
	if _, err := root.ExecContext(ctx,
		fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4", rootCfg.dbName)); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create database: %w", err)
	}
	_ = root.Close()

	appCfg := rootCfg.raw
	appCfg.DBName = rootCfg.dbName
	appCfg.ParseTime = true
	appCfg.MultiStatements = true
	db, err := sql.Open("mysql", appCfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	m := &MySQL{db: db}
	if err := m.migrate(ctx, migrationsDir); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *MySQL) migrate(ctx context.Context, dir string) error {
	if dir == "" {
		dir = "/migrations"
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := m.db.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("apply migration %s: %w", f, err)
		}
	}
	return nil
}

func (m *MySQL) Close() error { return m.db.Close() }

// WithTx runs fn inside one REPEATABLE READ transaction.
func (m *MySQL) WithTx(ctx context.Context, fn func(tx Tx) error) error {
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&mysqlTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type mysqlTx struct{ tx *sql.Tx }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// ---- Machines ----

const machineCols = `id, name, planes, points, speeds, convention, reading_change_floor_um, created_at, updated_at`

func scanMachine(scanner interface{ Scan(...any) error }) (*MachineRecord, error) {
	var rec MachineRecord
	var planes, points, speeds, convention string
	if err := scanner.Scan(&rec.ID, &rec.Name, &planes, &points, &speeds, &convention,
		&rec.ReadingChangeFloorUm, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(planes), &rec.Planes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(points), &rec.Points); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(speeds), &rec.Speeds); err != nil {
		return nil, err
	}
	rec.ConventionJSON = convention
	return &rec, nil
}

func (t *mysqlTx) SaveMachine(ctx context.Context, rec *MachineRecord) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO machines (id, name, planes, points, speeds, convention, reading_change_floor_um, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE name=VALUES(name), planes=VALUES(planes), points=VALUES(points),
  speeds=VALUES(speeds), convention=VALUES(convention),
  reading_change_floor_um=VALUES(reading_change_floor_um), updated_at=VALUES(updated_at)`,
		rec.ID, rec.Name, mustJSON(rec.Planes), mustJSON(rec.Points), mustJSON(rec.Speeds),
		rec.ConventionJSON, rec.ReadingChangeFloorUm, rec.CreatedAt, rec.UpdatedAt)
	return err
}

func (t *mysqlTx) GetMachine(ctx context.Context, id string) (*MachineRecord, error) {
	row := t.tx.QueryRowContext(ctx, "SELECT "+machineCols+" FROM machines WHERE id = ?", id)
	rec, err := scanMachine(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ErrNotFound{What: "machine " + id}
	}
	return rec, err
}

func (t *mysqlTx) ListMachines(ctx context.Context) ([]*MachineRecord, error) {
	rows, err := t.tx.QueryContext(ctx, "SELECT "+machineCols+" FROM machines ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MachineRecord
	for rows.Next() {
		rec, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ---- Jobs ----

const jobCols = `id, machine_id, name, snapshot, use_history, created_at, updated_at`

func scanJob(scanner interface{ Scan(...any) error }) (*JobRecord, error) {
	var rec JobRecord
	var snapshot string
	var useHistory int
	if err := scanner.Scan(&rec.ID, &rec.MachineID, &rec.Name, &snapshot, &useHistory,
		&rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(snapshot), &rec.Snapshot); err != nil {
		return nil, err
	}
	rec.UseHistory = useHistory == 1
	return &rec, nil
}

func (t *mysqlTx) SaveJob(ctx context.Context, rec *JobRecord) error {
	uh := 0
	if rec.UseHistory {
		uh = 1
	}
	if _, err := t.tx.ExecContext(ctx, `
INSERT INTO jobs (id, machine_id, name, snapshot, use_history, created_at, updated_at)
VALUES (?,?,?,?,?,?,?)`,
		rec.ID, rec.MachineID, rec.Name, mustJSON(rec.Snapshot), uh, rec.CreatedAt, rec.UpdatedAt); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, "INSERT IGNORE INTO job_event_seq (job_id) VALUES (?)", rec.ID)
	return err
}

func (t *mysqlTx) GetJob(ctx context.Context, id string, forUpdate bool) (*JobRecord, error) {
	q := "SELECT " + jobCols + " FROM jobs WHERE id = ?"
	if forUpdate {
		q += " FOR UPDATE"
	}
	rec, err := scanJob(t.tx.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ErrNotFound{What: "job " + id}
	}
	return rec, err
}

func (t *mysqlTx) ListJobsByMachine(ctx context.Context, machineID string) ([]*JobRecord, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT "+jobCols+" FROM jobs WHERE machine_id = ? ORDER BY updated_at", machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobRecord
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ---- Events ----

func (t *mysqlTx) AppendEvent(ctx context.Context, e *EventRecord) (*EventRecord, error) {
	if e.ClientID != "" {
		var jobID, clientID, typ string
		var seq int64
		row := t.tx.QueryRowContext(ctx,
			`SELECT job_id, client_id, type, seq FROM job_events WHERE job_id = ? AND client_id = ?`,
			e.JobID, e.ClientID)
		err := row.Scan(&jobID, &clientID, &typ, &seq)
		if err == nil {
			return nil, &ErrDuplicateClientID{JobID: e.JobID, ClientID: e.ClientID,
				Existing: EventRecord{Seq: seq, JobID: jobID, ClientID: clientID, Type: domain.EventType(typ)}}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	// Take the next per-job sequence atomically. Writers already hold the
	// FOR UPDATE lock on jobs, and UPDATE also row-locks job_event_seq, so the
	// numbering is gap-free under concurrency.
	if _, err := t.tx.ExecContext(ctx,
		"UPDATE job_event_seq SET last_seq = LAST_INSERT_ID(last_seq + 1) WHERE job_id = ?",
		e.JobID); err != nil {
		return nil, err
	}
	var seq int64
	if err := t.tx.QueryRowContext(ctx, "SELECT LAST_INSERT_ID()").Scan(&seq); err != nil {
		return nil, err
	}
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO job_events (job_id, seq, type, run_id, client_id, payload, occurred_at)
VALUES (?,?,?,?,?,?,?)`,
		e.JobID, seq, string(e.Type), e.RunID, nullString(e.ClientID),
		string(e.Payload), e.OccurredAt)
	if err != nil {
		if isDupEntry(err) && e.ClientID != "" {
			return nil, &ErrDuplicateClientID{JobID: e.JobID, ClientID: e.ClientID}
		}
		return nil, err
	}
	out := *e
	out.Seq = seq
	return &out, nil
}

func (t *mysqlTx) ListEvents(ctx context.Context, jobID string) ([]EventRecord, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT seq, job_id, type, run_id, COALESCE(client_id, ''), payload, occurred_at, created_at
FROM job_events WHERE job_id = ? ORDER BY seq`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var typ, payload string
		if err := rows.Scan(&e.Seq, &e.JobID, &typ, &e.RunID, &e.ClientID,
			&payload, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Type = domain.EventType(typ)
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (t *mysqlTx) FindEventByClientID(ctx context.Context, jobID, clientID string) (*EventRecord, error) {
	row := t.tx.QueryRowContext(ctx,
		`SELECT seq, job_id, type FROM job_events WHERE job_id = ? AND client_id = ?`,
		jobID, clientID)
	var e EventRecord
	var typ string
	if err := row.Scan(&e.Seq, &e.JobID, &typ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &ErrNotFound{What: "event clientId=" + clientID}
		}
		return nil, err
	}
	e.Type = domain.EventType(typ)
	e.ClientID = clientID
	return &e, nil
}

// ---- Contributions & status ----

func (t *mysqlTx) UpsertEstimate(ctx context.Context, r *EstimateRecord) error {
	trust := 0
	if r.TrustOK {
		trust = 1
	}
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO job_contributions (job_id, machine_id, speed, plane_ids, point_ids, re, im, uncert, trust_ok, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE machine_id=VALUES(machine_id), plane_ids=VALUES(plane_ids), point_ids=VALUES(point_ids),
  re=VALUES(re), im=VALUES(im), uncert=VALUES(uncert), trust_ok=VALUES(trust_ok), updated_at=VALUES(updated_at)`,
		r.JobID, r.MachineID, r.Speed, mustJSON(r.PlaneIDs), mustJSON(r.PointIDs),
		mustJSON(r.Re), mustJSON(r.Im), mustJSON(r.Uncert), trust, r.UpdatedAt)
	return err
}

func (t *mysqlTx) ListEstimates(ctx context.Context, machineID string) ([]EstimateRecord, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT job_id, machine_id, speed, plane_ids, point_ids, re, im, uncert, trust_ok, updated_at
FROM job_contributions WHERE machine_id = ? ORDER BY updated_at`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EstimateRecord
	for rows.Next() {
		var r EstimateRecord
		var planeIDs, pointIDs, re, im, un string
		var trust int
		if err := rows.Scan(&r.JobID, &r.MachineID, &r.Speed, &planeIDs, &pointIDs,
			&re, &im, &un, &trust, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(planeIDs), &r.PlaneIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(pointIDs), &r.PointIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(re), &r.Re); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(im), &r.Im); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(un), &r.Uncert); err != nil {
			return nil, err
		}
		r.TrustOK = trust == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (t *mysqlTx) PutCurrentCoeff(ctx context.Context, r *CurrentCoeffRecord) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO coeff_current
  (machine_id, speed, plane_ids, point_ids, re, im, uncert, n_contrib, status, reason, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE plane_ids=VALUES(plane_ids), point_ids=VALUES(point_ids),
  re=VALUES(re), im=VALUES(im), uncert=VALUES(uncert), n_contrib=VALUES(n_contrib),
  status=VALUES(status), reason=VALUES(reason), updated_at=VALUES(updated_at)`,
		r.MachineID, r.Speed, mustJSON(r.PlaneIDs), mustJSON(r.PointIDs),
		mustJSON(r.Re), mustJSON(r.Im), mustJSON(r.Uncert), r.NContrib,
		r.Status, r.Reason, r.CreatedAt, r.UpdatedAt)
	return err
}

func (t *mysqlTx) DeleteEstimatesForJob(ctx context.Context, jobID string) error {
	_, err := t.tx.ExecContext(ctx, "DELETE FROM job_contributions WHERE job_id = ?", jobID)
	return err
}

func (t *mysqlTx) DeleteCurrentCoeffsNotIn(ctx context.Context, machineID string, keep []int) error {
	switch len(keep) {
	case 0:
		_, err := t.tx.ExecContext(ctx, "DELETE FROM coeff_current WHERE machine_id = ?", machineID)
		return err
	case 1:
		_, err := t.tx.ExecContext(ctx,
			"DELETE FROM coeff_current WHERE machine_id = ? AND speed <> ?", machineID, keep[0])
		return err
	default:
		q := "DELETE FROM coeff_current WHERE machine_id = ? AND speed NOT IN (" +
			strings.Repeat("?,", len(keep)-1) + "?)"
		args := make([]any, 0, len(keep)+1)
		args = append(args, machineID)
		for _, s := range keep {
			args = append(args, s)
		}
		_, err := t.tx.ExecContext(ctx, q, args...)
		return err
	}
}

func (t *mysqlTx) ListCurrentCoeffs(ctx context.Context, machineID string) ([]CurrentCoeffRecord, error) {
	rows, err := t.tx.QueryContext(ctx, `
SELECT machine_id, speed, plane_ids, point_ids, re, im, uncert, n_contrib, status, reason, created_at, updated_at
FROM coeff_current WHERE machine_id = ? ORDER BY speed`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CurrentCoeffRecord
	for rows.Next() {
		var r CurrentCoeffRecord
		var planeIDs, pointIDs, re, im, un string
		if err := rows.Scan(&r.MachineID, &r.Speed, &planeIDs, &pointIDs,
			&re, &im, &un, &r.NContrib, &r.Status, &r.Reason, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		for _, pair := range []struct {
			s string
			d *[]string
		}{{planeIDs, &r.PlaneIDs}, {pointIDs, &r.PointIDs}} {
			if err := json.Unmarshal([]byte(pair.s), pair.d); err != nil {
				return nil, err
			}
		}
		if err := json.Unmarshal([]byte(re), &r.Re); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(im), &r.Im); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(un), &r.Uncert); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func isDupEntry(err error) bool {
	return strings.Contains(err.Error(), "Duplicate entry")
}

var _ = sort.Strings
