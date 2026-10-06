package store

import (
	"context"
	"os"
	"testing"
	"time"

	"balancing/internal/vec"
)

// TestMySQLIntegration runs the full event/replay/history flow against a real
// MySQL 8.0 database. It is skipped unless BALANCE_TEST_MYSQL_DSN is set:
//
//	BALANCE_TEST_MYSQL_DSN='balancer:balancer@tcp(127.0.0.1:3306)/balancing?parseTime=true&multiStatements=true'
//
// With docker compose running the db service, execute:
//
//	docker compose run -e BALANCE_TEST_MYSQL_DSN=... -v "$PWD":/src -w /src \
//	  golang:1.23-alpine go test ./internal/store/ -run TestMySQLIntegration -v
func TestMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("BALANCE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BALANCE_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	ctx := context.Background()
	s, err := NewMySQLStore(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// isolate the run with a unique machine name
	m := &Machine{
		Name: "it-" + time.Now().Format("150405.000000"), PlaneCount: 1, PointCount: 1,
		Speeds: []int{testSpeed}, Direction: vec.AgainstRotation,
		FusionStrategy: "uncertainty_weighted",
	}
	if err := s.CreateMachine(ctx, m); err != nil {
		t.Fatalf("create machine: %v", err)
	}

	j, err := s.CreateJob(ctx, m.ID, "it", "", false, time.Now().UTC())
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, j.ID, m, atTime(40), v0)
	h0 := matFromRows([]complex128{cpolar(2, 120)})
	tw := cpolar(20, 0)
	addTrial(t, s, j.ID, m, atTime(41), 0, tw, addAll(h0.MulVec([]complex128{tw}), v0))

	sr := speedResult(t, s, j.ID)
	if d := sr.Influence[0][0].Amp - 2; d > 1e-9 || d < -1e-9 {
		t.Fatalf("|H| = %.6f", sr.Influence[0][0].Amp)
	}

	// correction event replays identically to a correct-from-start job
	if _, err := s.CorrectRun(ctx, j.ID, CorrectionInput{
		TargetRunTime: atTime(41),
		Readings:      rds(m, []complex128{cpolar(89.44, 56.57)}),
	}); err != nil {
		t.Fatalf("correct: %v", err)
	}
	sr = speedResult(t, s, j.ID)
	if d := sr.Correction[0].Amp - 40; d > 1e-2 || d < -1e-2 {
		t.Fatalf("correction after event = %.4f", sr.Correction[0].Amp)
	}

	// events are persisted and reload yields the same derived state
	evs, err := s.ListEvents(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 { // created + original + trial + correction
		t.Fatalf("events = %d, want 4", len(evs))
	}
	reloaded, err := s.GetJob(ctx, j.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Result.Speeds[0].Correction[0].Amp != sr.Correction[0].Amp {
		t.Fatal("reloaded job result differs")
	}
}
