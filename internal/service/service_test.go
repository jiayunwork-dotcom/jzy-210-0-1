package service

import (
	"context"
	"math"
	"testing"
	"time"

	"balancer/internal/domain"
	"balancer/internal/store"
	"balancer/pkg/vec"
)

func testService() *Service {
	s := New(store.NewMemory())
	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	n := 0
	s.Now = func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
	return s
}

func onePlaneMachine(t *testing.T, s *Service, holes int, conv vec.Convention, speeds []int) string {
	t.Helper()
	if holes == 0 && len(speeds) == 0 {
	}
	m, err := s.CreateMachine(context.Background(), &CreateMachineInput{
		Name:       "fan-1",
		Planes:     []domain.Plane{{ID: "A", Name: "impeller", HoleCount: holes}},
		Points:     []domain.Point{{ID: "V", Name: "drive end H"}},
		Speeds:     speeds,
		Convention: conv,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func weight(plane string, mass, phase float64) domain.Weight {
	return domain.Weight{PlaneID: plane, Polar: vec.Polar{Amp: mass, Phase: phase}}
}

func reading(point string, rpm int, amp, phase float64) domain.Reading {
	return domain.Reading{PointID: point, Speed: rpm, Polar: vec.Polar{Amp: amp, Phase: phase}}
}

func at(min int) time.Time {
	return time.Date(2026, 10, 6, 10, min, 0, 0, time.UTC)
}

// Reference example end-to-end through the service.
func TestServiceReferenceExample(t *testing.T) {
	s := testService()
	ctx := context.Background()
	mid := onePlaneMachine(t, s, 0, vec.InternalDefault(), []int{1500})
	job, err := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
	if err != nil {
		t.Fatal(err)
	}
	jid := job.Job.ID

	add := func(kind domain.RunKind, min int, ws []domain.Weight, rs ...domain.Reading) {
		_, err := s.AddRun(ctx, jid, &domain.RunInput{
			Kind: kind, RunAt: at(min), Weights: ws, Readings: rs,
		})
		if err != nil {
			t.Fatalf("add %s: %v", kind, err)
		}
	}
	add(domain.RunOriginal, 0, nil, reading("V", 1500, 80, 30))
	add(domain.RunTrial, 1, []domain.Weight{weight("A", 20, 0)}, reading("V", 1500, 89.44, 56.57))

	view, err := s.GetJobState(ctx, jid)
	if err != nil {
		t.Fatal(err)
	}
	plan := view.State.Plans[1500]
	if math.Abs(plan.Weights[0].Amp-40) > 0.05 || math.Abs(vec.PhaseDiff(plan.Weights[0].Phase, 90)) > 0.05 {
		t.Fatalf("correction %v want 40∠90", plan.Weights[0])
	}
	if plan.ResidualAmp[0] > 1e-6 {
		t.Fatalf("residual %v", plan.ResidualAmp[0])
	}

	// Finalize: contribution must be stored and coefficients available.
	if _, err := s.Finalize(ctx, jid); err != nil {
		t.Fatal(err)
	}
	coeffs, err := s.ListCoefficients(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(coeffs) != 1 || coeffs[0].Speed != 1500 || coeffs[0].Status != "active" {
		t.Fatalf("coeffs %+v", coeffs)
	}
}

// Field rejection matrix: negative amplitude, bad phase, out-of-range counts,
// zero trial weight, too few holes.
func TestServiceRejections(t *testing.T) {
	s := testService()
	ctx := context.Background()

	// Bad machine configurations.
	bad := func(planes []domain.Plane, points []domain.Point, speeds []int) error {
		_, err := s.CreateMachine(ctx, &CreateMachineInput{
			Name: "x", Planes: planes, Points: points, Speeds: speeds,
			Convention: vec.InternalDefault(),
		})
		return err
	}
	if err := bad(nil, []domain.Point{{ID: "V"}}, []int{1500}); err == nil {
		t.Error("0 planes must be rejected")
	} else if rj, ok := IsRejection(err); !ok || !hasField(rj, "planes") {
		t.Errorf("want planes field, got %v", err)
	}
	if err := bad(
		[]domain.Plane{{ID: "A"}, {ID: "B"}, {ID: "C"}},
		[]domain.Point{{ID: "V"}}, []int{1500}); err == nil {
		t.Error("3 planes must be rejected")
	}
	if err := bad(
		[]domain.Plane{{ID: "A"}},
		[]domain.Point{{ID: "V0"}, {ID: "V1"}, {ID: "V2"}, {ID: "V3"}, {ID: "V4"}},
		[]int{1500}); err == nil {
		t.Error("5 points must be rejected")
	}
	if err := bad(
		[]domain.Plane{{ID: "A", HoleCount: 2}},
		[]domain.Point{{ID: "V"}}, []int{1500}); err == nil {
		t.Error("2 holes must be rejected")
	}

	mid := onePlaneMachine(t, s, 0, vec.InternalDefault(), []int{1500})
	job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
	jid := job.Job.ID

	checkField := func(name string, in *domain.RunInput, field string) {
		t.Helper()
		_, err := s.AddRun(ctx, jid, in)
		rj, ok := IsRejection(err)
		if !ok {
			t.Errorf("%s: expected rejection, got nil", name)
			return
		}
		if !hasField(rj, field) {
			t.Errorf("%s: expected field %q in %v", name, field, rj.Fields)
		}
	}

	// Negative amplitude.
	checkField("negative amplitude", &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, -5, 30)}}, "readings[0].polar.amp")
	// Phase out of range.
	checkField("phase 360", &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, 80, 360)}}, "readings[0].polar.phase")
	checkField("phase -1", &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, 80, -1)}}, "readings[0].polar.phase")

	// Accept the original for subsequent checks.
	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
		t.Fatal(err)
	}

	// Zero trial weight.
	checkField("zero trial", &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 0, 0)},
		Readings: []domain.Reading{reading("V", 1500, 100, 60)}}, "weights")

	// Effect too small.
	checkField("tiny effect", &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("V", 1500, 80.3, 30.2)}}, "readings")

	// Unknown plane/point/speed.
	checkField("unknown plane", &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("Z", 20, 0)},
		Readings: []domain.Reading{reading("V", 1500, 100, 60)}}, "weights[0].planeId")
	checkField("unknown point", &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("Z", 1500, 100, 60)}}, "readings[0].pointId")
	checkField("unknown speed", &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("V", 9999, 100, 60)}}, "readings[0].speedRpm")
}

func hasField(rj *Rejection, field string) bool {
	for _, f := range rj.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

// Reading correction produces results identical to correct initial entry and
// updates machine history identically.
func TestServiceCorrectionEquivalence(t *testing.T) {
	s, ctx := testService(), context.Background()

	build := func(correctFromStart bool) (string, *store.CurrentCoeffRecord) {
		mid := onePlaneMachine(t, s, 0, vec.InternalDefault(), []int{1500})
		job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
		jid := job.Job.ID
		phase := 56.57
		if !correctFromStart {
			phase = 236.57 // 180° typo
		}
		if _, err := s.AddRun(ctx, jid, &domain.RunInput{
			Kind: domain.RunOriginal, RunAt: at(0),
			Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
			t.Fatal(err)
		}
		res, err := s.AddRun(ctx, jid, &domain.RunInput{
			Kind: domain.RunTrial, RunAt: at(1),
			Weights:  []domain.Weight{weight("A", 20, 0)},
			Readings: []domain.Reading{reading("V", 1500, 89.44, phase)}})
		if err != nil {
			t.Fatal(err)
		}
		if !correctFromStart {
			rs := []domain.Reading{reading("V", 1500, 89.44, 56.57)}
			if err := s.CorrectRun(ctx, jid, res.RunID, domain.RunFix{Readings: &rs}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Finalize(ctx, jid); err != nil {
			t.Fatal(err)
		}
		coeffs, err := s.ListCoefficients(ctx, mid)
		if err != nil {
			t.Fatal(err)
		}
		if len(coeffs) != 1 {
			t.Fatalf("coeffs %d", len(coeffs))
		}
		c := coeffs[0]
		return jid, &c
	}

	_, good := build(true)
	_, fixed := build(false)
	if len(good.Re) != len(fixed.Re) {
		t.Fatal("matrix size mismatch")
	}
	for k := range good.Re {
		if math.Abs(good.Re[k]-fixed.Re[k]) > 1e-10 || math.Abs(good.Im[k]-fixed.Im[k]) > 1e-10 {
			t.Fatalf("history differs after correction at %d: (%v,%v) vs (%v,%v)",
				k, good.Re[k], good.Im[k], fixed.Re[k], fixed.Im[k])
		}
	}
	if good.NContrib != fixed.NContrib || good.Status != fixed.Status {
		t.Fatalf("meta differs: %+v vs %+v", good, fixed)
	}
}

// Concurrent submissions are both kept and ordered by run time.
func TestServiceConcurrentSubmissions(t *testing.T) {
	s, ctx := testService(), context.Background()
	mid := onePlaneMachine(t, s, 0, vec.InternalDefault(), []int{1500})
	job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
	jid := job.Job.ID

	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
		t.Fatal(err)
	}
	// Two technicians submit "simultaneously"; the store serializes the
	// transactions, but field times differ and must determine ordering.
	errs := make(chan error, 2)
	go func() {
		_, e := s.AddRun(ctx, jid, &domain.RunInput{
			ClientID: "tech-A", Kind: domain.RunTrial, RunAt: at(5),
			Weights:  []domain.Weight{weight("A", 20, 0)},
			Readings: []domain.Reading{reading("V", 1500, 89.44, 56.57)}})
		errs <- e
	}()
	go func() {
		_, e := s.AddRun(ctx, jid, &domain.RunInput{
			ClientID: "tech-B", Kind: domain.RunVerify, RunAt: at(2),
			Weights:  []domain.Weight{weight("A", 40, 90)},
			Readings: []domain.Reading{reading("V", 1500, 1, 100)}})
		errs <- e
	}()
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	view, err := s.GetJobState(ctx, jid)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.State.Runs) != 3 {
		t.Fatalf("runs kept = %d, want 3", len(view.State.Runs))
	}
	// Time ordering: original(0), verify(2), trial(5).
	order := []domain.RunKind{view.State.Runs[0].Kind, view.State.Runs[1].Kind, view.State.Runs[2].Kind}
	if order[0] != domain.RunOriginal || order[1] != domain.RunVerify || order[2] != domain.RunTrial {
		t.Fatalf("order %v", order)
	}

	// Replaying the same clientId is idempotent.
	res, err := s.AddRun(ctx, jid, &domain.RunInput{
		ClientID: "tech-A", Kind: domain.RunTrial, RunAt: at(5),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("V", 1500, 89.44, 56.57)}})
	if err != nil || !res.Dedupe {
		t.Fatalf("expected dedupe, got %v %+v", err, res)
	}
	view2, _ := s.GetJobState(ctx, jid)
	if len(view2.State.Runs) != 3 {
		t.Fatal("idempotent retry duplicated a run")
	}
}

// History reuse: second job on the same machine skips trials; a failed
// verification invalidates the coefficient and forces new trials.
func TestServiceHistoryReuseAndFailure(t *testing.T) {
	s, ctx := testService(), context.Background()
	mid := onePlaneMachine(t, s, 0, vec.InternalDefault(), []int{1500})

	// Job 1: full trial job, finalized.
	j1, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
	if _, err := s.AddRun(ctx, j1.Job.ID, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, j1.Job.ID, &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("V", 1500, 89.44, 56.57)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finalize(ctx, j1.Job.ID); err != nil {
		t.Fatal(err)
	}

	// Job 2 using history: correction plan available immediately after the
	// original run (no trial required).
	j2, err := s.CreateJob(ctx, &CreateJobInput{MachineID: mid, UseHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, j2.Job.ID, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(10),
		Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
		t.Fatal(err)
	}
	view, _ := s.GetJobState(ctx, j2.Job.ID)
	if !view.State.HistoryUsed {
		t.Fatal("expected coefficients from history")
	}
	p := view.State.Plans[1500]
	if p == nil || p.Err != "" || math.Abs(p.Weights[0].Amp-40) > 0.05 {
		t.Fatalf("history plan %+v", p)
	}

	// A bad verification (vibration barely reduced) invalidates history.
	if _, err := s.AddRun(ctx, j2.Job.ID, &domain.RunInput{
		Kind: domain.RunVerify, RunAt: at(11),
		Weights:  []domain.Weight{weight("A", 10, 0)},
		Readings: []domain.Reading{reading("V", 1500, 70, 45)}}); err != nil {
		t.Fatal(err)
	}
	coeffs, _ := s.ListCoefficients(ctx, mid)
	if len(coeffs) != 1 || coeffs[0].Status != "suspect" {
		t.Fatalf("expected suspect coefficient, %+v", coeffs)
	}

	// A new history-based job must now be refused.
	if _, err := s.CreateJob(ctx, &CreateJobInput{MachineID: mid, UseHistory: true}); err == nil {
		t.Fatal("history reuse must be rejected while suspect")
	} else if rj, ok := IsRejection(err); !ok || rj.StatusCode != 409 {
		t.Fatalf("want 409, got %v", err)
	}

	// A fresh trial job restores a good coefficient.
	j3, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
	if _, err := s.AddRun(ctx, j3.Job.ID, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(20),
		Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, j3.Job.ID, &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(21),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("V", 1500, 89.44, 56.57)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, j3.Job.ID, &domain.RunInput{
		Kind: domain.RunVerify, RunAt: at(22),
		Weights:  []domain.Weight{weight("A", 40, 90)},
		Readings: []domain.Reading{reading("V", 1500, 2, 100)}}); err != nil {
		t.Fatal(err)
	}
	coeffs, _ = s.ListCoefficients(ctx, mid)
	if len(coeffs) != 1 || coeffs[0].Status != "active" {
		t.Fatalf("expected restored active coefficient, %+v", coeffs)
	}
}

// Hole splitting shows up in the plan and preserves the resultant vector.
func TestServiceHoleSplit(t *testing.T) {
	s, ctx := testService(), context.Background()
	mid := onePlaneMachine(t, s, 12, vec.InternalDefault(), []int{1500})
	job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: mid})
	jid := job.Job.ID
	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{reading("V", 1500, 80, 30)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{reading("V", 1500, 89.44, 56.57)}}); err != nil {
		t.Fatal(err)
	}
	view, _ := s.GetJobState(ctx, jid)
	plan := view.State.Plans[1500]
	if len(plan.Holes) != 1 || len(plan.Holes[0]) != 1 {
		t.Fatal("missing hole split")
	}
	hs := plan.Holes[0][0]
	if math.Abs(hs.ResultantMass-plan.Weights[0].Amp) > 1e-9 {
		t.Errorf("mass %v vs %v", hs.ResultantMass, plan.Weights[0].Amp)
	}
	if math.Abs(vec.PhaseDiff(hs.ResultantAngle, plan.Weights[0].Phase)) > 1e-9 {
		t.Errorf("angle %v vs %v", hs.ResultantAngle, plan.Weights[0].Phase)
	}
}

// Two-plane/two-point combined job: fitted coefficients recover the hidden
// model, the square system balances to (numerically) zero at both points, and
// a verify run agrees with the model prediction.
func TestServiceTwoPlaneTwoPoint(t *testing.T) {
	s, ctx := testService(), context.Background()
	m, err := s.CreateMachine(ctx, &CreateMachineInput{
		Name: "pump", Convention: vec.InternalDefault(), Speeds: []int{3000},
		Planes: []domain.Plane{{ID: "A"}, {ID: "B"}},
		Points: []domain.Point{{ID: "V1"}, {ID: "V2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: m.ID})
	jid := job.Job.ID

	c := func(amp, phase float64) complex128 {
		r := phase * math.Pi / 180
		return complex(amp*math.Cos(r), amp*math.Sin(r))
	}
	A11, A12 := c(2, 120), c(1.5, 30)
	A21, A22 := c(1.2, -40), c(2.4, 200)
	v01, v02 := c(60, 50), c(45, 210)
	obs := func(w1, w2 complex128) (vec.Polar, vec.Polar) {
		return vec.FromComplex(v01 + A11*w1 + A12*w2),
			vec.FromComplex(v02 + A21*w1 + A22*w2)
	}
	o1, o2 := vec.FromComplex(v01), vec.FromComplex(v02)
	tw1, tw2 := c(20, 0), c(20, 90)
	g11, g12 := obs(tw1, 0)
	g21, g22 := obs(0, tw2)

	add := func(kind domain.RunKind, min int, ws []domain.Weight, r1, r2 vec.Polar) {
		if _, err := s.AddRun(ctx, jid, &domain.RunInput{
			Kind: kind, RunAt: at(min), Weights: ws,
			Readings: []domain.Reading{
				reading("V1", 3000, r1.Amp, r1.Phase),
				reading("V2", 3000, r2.Amp, r2.Phase),
			}}); err != nil {
			t.Fatalf("add %s: %v", kind, err)
		}
	}
	add(domain.RunOriginal, 0, nil, o1, o2)
	add(domain.RunTrial, 1, []domain.Weight{weight("A", 20, 0)}, g11, g12)
	add(domain.RunTrial, 2, []domain.Weight{weight("B", 20, 90)}, g21, g22)

	view, _ := s.GetJobState(ctx, jid)
	plan := view.State.Plans[3000]
	if plan.Err != "" {
		t.Fatal(plan.Err)
	}
	for p, r := range plan.ResidualAmp {
		if r > 1e-6 {
			t.Errorf("square residual point %d = %v", p, r)
		}
	}
	// Correction weight vector then predicts near-zero verify readings.
	wc1, wc2 := plan.Weights[0], plan.Weights[1]
	v1, v2 := obs(c(wc1.Amp, wc1.Phase), c(wc2.Amp, wc2.Phase))
	addW := []domain.Weight{weight("A", wc1.Amp, wc1.Phase), weight("B", wc2.Amp, wc2.Phase)}
	add(domain.RunVerify, 3, addW, v1, v2)
	view, _ = s.GetJobState(ctx, jid)
	if view.State.TrustChecked && !view.State.TrustOK {
		t.Fatalf("verify should agree with model: %+v", view.State.Trust)
	}
}

// Multi-speed machine: one combined plan stacked over both speeds, and stored
// coefficients are kept per speed.
func TestServiceMultiSpeed(t *testing.T) {
	s, ctx := testService(), context.Background()
	// A single-plane machine whose coefficient differs at two speeds.
	m, err := s.CreateMachine(ctx, &CreateMachineInput{
		Name: "two-speed fan", Convention: vec.InternalDefault(), Speeds: []int{1500, 3000},
		Planes: []domain.Plane{{ID: "A"}},
		Points: []domain.Point{{ID: "V"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: m.ID})
	jid := job.Job.ID

	c := func(amp, phase float64) complex128 {
		r := phase * math.Pi / 180
		return complex(amp*math.Cos(r), amp*math.Sin(r))
	}
	// Distinct coefficients per speed.
	a1500, a3000 := c(2, 120), c(1.5, -60)
	v1500, v3000 := c(80, 30), c(55, 250)
	tw := c(20, 0)
	p1 := vec.FromComplex(v1500)
	q1 := vec.FromComplex(v1500 + a1500*tw)
	p2 := vec.FromComplex(v3000)
	q2 := vec.FromComplex(v3000 + a3000*tw)

	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0),
		Readings: []domain.Reading{
			reading("V", 1500, p1.Amp, p1.Phase),
			reading("V", 3000, p2.Amp, p2.Phase),
		}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights: []domain.Weight{weight("A", 20, 0)},
		Readings: []domain.Reading{
			reading("V", 1500, q1.Amp, q1.Phase),
			reading("V", 3000, q2.Amp, q2.Phase),
		}}); err != nil {
		t.Fatal(err)
	}
	view, _ := s.GetJobState(ctx, jid)
	if view.State.Plans[1500].Err != "" || view.State.Plans[3000].Err != "" {
		t.Fatalf("per-speed plans: %v / %v", view.State.Plans[1500].Err, view.State.Plans[3000].Err)
	}
	if view.State.MultiSpeed == nil || view.State.MultiSpeed.Err != "" {
		t.Fatalf("multi-speed plan: %+v", view.State.MultiSpeed)
	}
	// Two stacked rows, one plane: overdetermined, so residuals need not be
	// exactly zero, but both finite and computed.
	if len(view.State.MultiSpeed.ResidualAmp) != 2 {
		t.Fatal("multi-speed plan must report both conditions")
	}
	if _, err := s.Finalize(ctx, jid); err != nil {
		t.Fatal(err)
	}
	coeffs, _ := s.ListCoefficients(ctx, m.ID)
	gotSpeeds := map[int]bool{}
	for _, cf := range coeffs {
		gotSpeeds[cf.Speed] = true
	}
	if !gotSpeeds[1500] || !gotSpeeds[3000] {
		t.Fatalf("expected coefficients per speed, got %v", gotSpeeds)
	}
}

func TestServiceOverdeterminedBound(t *testing.T) {
	s, ctx := testService(), context.Background()
	// 1 plane, 3 points: WLS must not leave a residual worse than a
	// single-point plan at any point (feasible-set optimum).
	m, err := s.CreateMachine(ctx, &CreateMachineInput{
		Name: "fan-3points", Convention: vec.InternalDefault(), Speeds: []int{1500},
		Planes: []domain.Plane{{ID: "A"}},
		Points: []domain.Point{{ID: "V1"}, {ID: "V2"}, {ID: "V3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := s.CreateJob(ctx, &CreateJobInput{MachineID: m.ID})
	jid := job.Job.ID

	c := func(amp, phase float64) complex128 {
		r := phase * math.Pi / 180
		return complex(amp*math.Cos(r), amp*math.Sin(r))
	}
	alpha := c(2, 120)
	v0 := []complex128{c(80, 30), c(60, 200), c(40, 320)}
	tw := c(20, 0)
	origs := make([]domain.Reading, 3)
	trials := make([]domain.Reading, 3)
	for i, z := range v0 {
		p := vec.FromComplex(z)
		q := vec.FromComplex(z + alpha*tw)
		origs[i] = reading("V"+string(rune('1'+i)), 1500, p.Amp, p.Phase)
		trials[i] = reading("V"+string(rune('1'+i)), 1500, q.Amp, q.Phase)
	}
	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunOriginal, RunAt: at(0), Readings: origs}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun(ctx, jid, &domain.RunInput{
		Kind: domain.RunTrial, RunAt: at(1),
		Weights:  []domain.Weight{weight("A", 20, 0)},
		Readings: trials}); err != nil {
		t.Fatal(err)
	}
	view, _ := s.GetJobState(ctx, jid)
	plan := view.State.Plans[1500]
	var singleWorst float64
	for _, z0 := range v0 {
		w := -z0 / alpha
		for _, zz := range v0 {
			singleWorst = math.Max(singleWorst, cmplxAbs(zz+alpha*w))
		}
	}
	for p, r := range plan.ResidualAmp {
		if r > singleWorst+1e-7 {
			t.Errorf("WLS residual[%d]=%v exceeds single-point worst %v", p, r, singleWorst)
		}
	}
}

func cmplxAbs(z complex128) float64 { return math.Hypot(real(z), imag(z)) }
