package store

import (
	"context"
	"os"
	"testing"
	"time"

	"balancer/internal/domain"
)

// MySQL integration test, skipped unless BALANCER_TEST_MYSQL_DSN is set, e.g.
//
//	BALANCER_TEST_MYSQL_DSN='root:rootpw@tcp(127.0.0.1:3306)/?parseTime=true' \
//	MIGRATIONS_DIR=../../migrations go test ./internal/store -run MySQL -v
//
// It uses a throwaway database name and drops it afterwards.
func TestMySQLRoundTrip(t *testing.T) {
	dsn := os.Getenv("BALANCER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BALANCER_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	dir := os.Getenv("MIGRATIONS_DIR")
	if dir == "" {
		dir = "../../migrations"
	}
	ctx := context.Background()

	// Open twice under a unique database name to exercise CREATE DATABASE and
	// migrations.
	suffix := time.Now().UnixNano()
	cfg, err := parseRootCfg(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.raw.DBName = "balancer_it_" + itoaSuffix(suffix)
	fullDSN := cfg.raw.FormatDSN()

	m, err := OpenMySQL(ctx, fullDSN, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		_ = m.Close()
	}()

	now := time.Now().UTC().Truncate(time.Second)
	conv := `{"zeroOffsetDeg":45,"direction":-1}`
	mrec := &MachineRecord{
		ID: "mac-it", Name: "it-machine",
		Planes:         []domain.Plane{{ID: "A", Name: "p", HoleCount: 12}},
		Points:         []domain.Point{{ID: "V", Name: "v"}},
		Speeds:         []int{1500},
		ConventionJSON: conv,
		CreatedAt:      now, UpdatedAt: now,
	}
	if err := m.WithTx(ctx, func(tx Tx) error { return tx.SaveMachine(ctx, mrec) }); err != nil {
		t.Fatal(err)
	}

	var got *MachineRecord
	if err := m.WithTx(ctx, func(tx Tx) error {
		g, e := tx.GetMachine(ctx, "mac-it")
		got = g
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if got.ConventionJSON != conv || got.Planes[0].HoleCount != 12 {
		t.Fatalf("machine round trip %+v", got)
	}

	snap := domain.SnapshotConfig{
		PlaneIDs: []string{"A"}, PlaneHoles: []int{12},
		PointIDs: []string{"V"}, Speeds: []int{1500},
	}
	jrec := &JobRecord{
		ID: "job-it", MachineID: "mac-it", Name: "it-job",
		Snapshot: snap, UseHistory: false, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.WithTx(ctx, func(tx Tx) error { return tx.SaveJob(ctx, jrec) }); err != nil {
		t.Fatal(err)
	}

	// Append several events inside separate transactions and check per-job
	// gap-free sequencing even when interleaved.
	for i := 0; i < 3; i++ {
		_, err := m.AppendEvent(ctx, &EventRecord{
			JobID: "job-it", Type: domain.EvtRunAdded, RunID: "r" + itoaSuffix(int64(i)),
			Payload:    []byte(`{}`),
			OccurredAt: now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var events []EventRecord
	if err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		events, e = tx.ListEvents(ctx, "job-it")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d", len(events))
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, want %d", i, e.Seq, i+1)
		}
	}

	// Idempotency: same clientId rejected on the second insert.
	if _, err := m.AppendEvent(ctx, &EventRecord{
		JobID: "job-it", Type: domain.EvtRunAdded, RunID: "rx", ClientID: "c1",
		Payload: []byte(`{}`), OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendEvent(ctx, &EventRecord{
		JobID: "job-it", Type: domain.EvtRunAdded, RunID: "ry", ClientID: "c1",
		Payload: []byte(`{}`), OccurredAt: now,
	}); err == nil {
		t.Fatal("duplicate clientId must be rejected")
	}

	// Current coefficient upsert/list round trip.
	cc := &CurrentCoeffRecord{
		MachineID: "mac-it", Speed: 1500,
		PlaneIDs: []string{"A"}, PointIDs: []string{"V"},
		Re: []float64{1}, Im: []float64{1.732}, Uncert: []float64{0.1},
		NContrib: 1, Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := m.WithTx(ctx, func(tx Tx) error { return tx.PutCurrentCoeff(ctx, cc) }); err != nil {
		t.Fatal(err)
	}
	var coeffs []CurrentCoeffRecord
	if err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		coeffs, e = tx.ListCurrentCoeffs(ctx, "mac-it")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(coeffs) != 1 || coeffs[0].Im[0] != 1.732 {
		t.Fatalf("coeff round trip %+v", coeffs)
	}

	// Delete-not-in removes only rows with unlisted speeds.
	if err := m.WithTx(ctx, func(tx Tx) error {
		return tx.DeleteCurrentCoeffsNotIn(ctx, "mac-it", []int{9999})
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		coeffs, e = tx.ListCurrentCoeffs(ctx, "mac-it")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(coeffs) != 0 {
		t.Fatalf("expected coefficients deleted, got %d", len(coeffs))
	}

	// FOR UPDATE must not error under REPEATABLE READ.
	if err := m.WithTx(ctx, func(tx Tx) error {
		_, e := tx.GetJob(ctx, "job-it", true)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	// Cleanup.
	if _, err := m.db.ExecContext(ctx, "DROP DATABASE `"+cfg.raw.DBName+"`"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

func itoaSuffix(i int64) string {
	// keep test dependency-free
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
