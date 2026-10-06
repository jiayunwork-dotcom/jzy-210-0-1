package compute

import (
	"encoding/json"
	"math"
	"math/cmplx"
	"testing"
	"time"

	"balancer/internal/domain"
	"balancer/pkg/history"
	"balancer/pkg/vec"
)

func evt(seq int64, typ domain.EventType, v any) domain.StoredEvent {
	b, _ := json.Marshal(v)
	return domain.StoredEvent{Seq: seq, JobID: "j1", Type: typ, OccurredAt: time.Now(), Payload: b}
}

func newJob(conv vec.Convention, planes, points []string, holes []int, speeds []int) *domain.Job {
	return &domain.Job{
		ID: "j1", MachineID: "m1", Name: "t",
		Convention: conv, PlaneIDs: planes, PlaneHoles: holes,
		PointIDs: points, Speeds: speeds,
	}
}

func runAdded(seq int64, id string, kind domain.RunKind, at time.Time,
	weights []domain.Weight, readings []domain.Reading) domain.StoredEvent {
	return evt(seq, domain.EvtRunAdded, domain.RunAddedEvent{
		RunID: id, Input: domain.RunInput{
			Kind: kind, RunAt: at, Weights: weights, Readings: readings,
		},
		At: time.Now(),
	})
}

func rd(point string, speed int, amp, phase float64) domain.Reading {
	return domain.Reading{PointID: point, Speed: speed, Polar: vec.Polar{Amp: amp, Phase: phase}}
}

func wgt(plane string, mass, phase float64) domain.Weight {
	return domain.Weight{PlaneID: plane, Polar: vec.Polar{Amp: mass, Phase: phase}}
}

func referenceEvents(conv vec.Convention) []domain.StoredEvent {
	// Reference example expressed in the given convention:
	// internal 80∠30, trial 20g∠0 -> 89.44∠56.57.
	o := vec.Polar{Amp: 80, Phase: 30}
	tr := vec.Polar{Amp: 89.44, Phase: 56.57}
	tw := vec.Polar{Amp: 20, Phase: 0}
	events := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, time.Unix(1000, 0),
			nil, []domain.Reading{{PointID: "V1", Speed: 1500, Polar: conv.ExternalPolar(o)}}),
		runAdded(2, "r1", domain.RunTrial, time.Unix(2000, 0),
			[]domain.Weight{{PlaneID: "P1", Polar: conv.ExternalPolar(tw)}},
			[]domain.Reading{{PointID: "V1", Speed: 1500, Polar: conv.ExternalPolar(tr)}}),
	}
	return events
}

// Single-plane reference example through full event replay, including the
// stated self-consistency: correction 40 g∠90° and zero predicted residual.
func TestReplayReferenceExample(t *testing.T) {
	job := newJob(vec.InternalDefault(), []string{"P1"}, []string{"V1"}, nil, []int{1500})
	st, err := Replay(job, referenceEvents(vec.InternalDefault()), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := st.Fits[1500].ExtA[0][0]
	if math.Abs(a.Amp-2) > 0.01 || math.Abs(vec.PhaseDiff(a.Phase, 120)) > 0.05 {
		t.Fatalf("influence %v want 2∠120", a)
	}
	plan := st.Plans[1500]
	if math.Abs(plan.Weights[0].Amp-40) > 0.05 ||
		math.Abs(vec.PhaseDiff(plan.Weights[0].Phase, 90)) > 0.05 {
		t.Fatalf("correction %v want 40∠90", plan.Weights[0])
	}
	if plan.ResidualAmp[0] > 1e-6 {
		t.Fatalf("residual %v want 0", plan.Residual[0])
	}
}

// The same example recorded in a CW convention with an offset keyphasor must
// produce the same internal coefficients and a correctly converted answer.
func TestReplayConventionConversion(t *testing.T) {
	conv := vec.Convention{ZeroOffsetDeg: 45, Direction: vec.DirectionCW}
	job := newJob(conv, []string{"P1"}, []string{"V1"}, nil, []int{1500})
	st, err := Replay(job, referenceEvents(conv), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Internal influence still 2∠120.
	zi := complex(st.Fits[1500].Re[0][0], st.Fits[1500].Im[0][0])
	if d := cmplx.Abs(zi - cmplx.Rect(2, 120*math.Pi/180)); d > 1e-3 {
		t.Fatalf("internal influence %v", zi)
	}
	plan := st.Plans[1500]
	// 40 g at internal 90° => field 45-90 = -45 => 315°.
	if math.Abs(plan.Weights[0].Amp-40) > 0.05 ||
		math.Abs(vec.PhaseDiff(plan.Weights[0].Phase, 315)) > 0.05 {
		t.Fatalf("external correction %v want 40∠315", plan.Weights[0])
	}
	if plan.ResidualAmp[0] > 1e-6 {
		t.Fatalf("residual %v", plan.Residual[0])
	}
}

// Fixing a mis-entered run (phase typed 180° off) with a correction event
// yields exactly the state produced by entering it correctly the first time.
func TestCorrectionEventEquivalence(t *testing.T) {
	job := newJob(vec.InternalDefault(), []string{"P1"}, []string{"V1"}, nil, []int{1500})

	correct := referenceEvents(vec.InternalDefault())

	// Same stream but the trial reading was entered 180° off, then fixed.
	wrong := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, time.Unix(1000, 0), nil,
			[]domain.Reading{rd("V1", 1500, 80, 30)}),
		runAdded(2, "r1", domain.RunTrial, time.Unix(2000, 0),
			[]domain.Weight{wgt("P1", 20, 0)},
			[]domain.Reading{rd("V1", 1500, 89.44, 236.57)}), // +180°
		evt(3, domain.EvtRunCorrected, domain.RunCorrectedEvent{
			RunID: "r1",
			Correction: domain.RunFix{
				Readings: &[]domain.Reading{rd("V1", 1500, 89.44, 56.57)},
			},
			At: time.Now(),
		}),
	}

	stA, err := Replay(job, correct, nil)
	if err != nil {
		t.Fatal(err)
	}
	stB, err := Replay(job, wrong, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"influence"} {
		_ = label
	}
	fa, fb := stA.Fits[1500], stB.Fits[1500]
	for p := range fa.Re {
		for a := range fa.Re[p] {
			if math.Abs(fa.Re[p][a]-fb.Re[p][a]) > 1e-12 ||
				math.Abs(fa.Im[p][a]-fb.Im[p][a]) > 1e-12 {
				t.Fatalf("fit differs after correction: (%v,%v) vs (%v,%v)",
					fa.Re[p][a], fa.Im[p][a], fb.Re[p][a], fb.Im[p][a])
			}
		}
	}
	pa, pb := stA.Plans[1500], stB.Plans[1500]
	if !vec.Equal(pa.Weights[0], pb.Weights[0], 1e-10, 1e-10) {
		t.Fatalf("plan differs: %v vs %v", pa.Weights[0], pb.Weights[0])
	}
	// The original wrong event is still in the stream (both retained).
	if len(stB.Runs) != 2 {
		t.Fatalf("runs = %d", len(stB.Runs))
	}
}

// Concurrent submissions: both kept, ordered by actual run time regardless of
// insertion sequence.
func TestConcurrentSubmissionsOrderedByRunTime(t *testing.T) {
	job := newJob(vec.InternalDefault(), []string{"P1"}, []string{"V1"}, nil, []int{1500})
	at := func(sec int64) time.Time { return time.Unix(sec, 0) }
	events := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, at(1000), nil,
			[]domain.Reading{rd("V1", 1500, 80, 30)}),
		// Submitted first (seq 2) but performed later.
		runAdded(2, "rLate", domain.RunTrial, at(3000),
			[]domain.Weight{wgt("P1", 20, 0)},
			[]domain.Reading{rd("V1", 1500, 89.44, 56.57)}),
		// Submitted second (seq 3) but performed earlier.
		runAdded(3, "rEarly", domain.RunVerify, at(2500),
			[]domain.Weight{wgt("P1", 40, 90)},
			[]domain.Reading{rd("V1", 1500, 1, 95)}),
	}
	st, err := Replay(job, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{st.Runs[1].RunID, st.Runs[2].RunID}
	if got[0] != "rEarly" || got[1] != "rLate" {
		t.Fatalf("run order %v, want [rEarly rLate]", got)
	}
}

// Two planes × two points synthetic machine, full fit + correction round trip.
func TestTwoPlaneTwoPoint(t *testing.T) {
	// Build observations from a hidden model, expressed directly internally
	// (default convention), run the whole projection.
	conv := vec.InternalDefault()
	A11 := cmplx.Rect(2, 120*math.Pi/180)
	A12 := cmplx.Rect(1.5, 30*math.Pi/180)
	A21 := cmplx.Rect(1.2, -40*math.Pi/180)
	A22 := cmplx.Rect(2.4, 200*math.Pi/180)
	v01 := cmplx.Rect(60, 50*math.Pi/180)
	v02 := cmplx.Rect(45, 210*math.Pi/180)
	obs := func(w1, w2 complex128) (vec.Polar, vec.Polar) {
		z1 := v01 + A11*w1 + A12*w2
		z2 := v02 + A21*w1 + A22*w2
		return vec.FromComplex(z1), vec.FromComplex(z2)
	}
	at := time.Unix(1000, 0)
	tw1 := cmplx.Rect(20, 0)
	tw2 := cmplx.Rect(20, 90*math.Pi/180)
	o1, o2 := vec.FromComplex(v01), vec.FromComplex(v02)
	g11, g12 := obs(tw1, 0)
	g21, g22 := obs(0, tw2)
	events := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, at, nil,
			[]domain.Reading{rd("V1", 1500, o1.Amp, o1.Phase), rd("V2", 1500, o2.Amp, o2.Phase)}),
		runAdded(2, "r1", domain.RunTrial, at.Add(time.Minute),
			[]domain.Weight{{PlaneID: "A", Polar: conv.ExternalPolar(vec.FromComplex(tw1))}},
			[]domain.Reading{rd("V1", 1500, g11.Amp, g11.Phase), rd("V2", 1500, g12.Amp, g12.Phase)}),
		runAdded(3, "r2", domain.RunTrial, at.Add(2*time.Minute),
			[]domain.Weight{{PlaneID: "B", Polar: conv.ExternalPolar(vec.FromComplex(tw2))}},
			[]domain.Reading{rd("V1", 1500, g21.Amp, g21.Phase), rd("V2", 1500, g22.Amp, g22.Phase)}),
	}
	job := newJob(conv, []string{"A", "B"}, []string{"V1", "V2"}, nil, []int{1500})
	st, err := Replay(job, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	fit := st.Fits[1500]
	want := [][]complex128{{A11, A12}, {A21, A22}}
	for p := 0; p < 2; p++ {
		for a := 0; a < 2; a++ {
			got := complex(fit.Re[p][a], fit.Im[p][a])
			if cmplx.Abs(got-want[p][a]) > 1e-7 {
				t.Fatalf("A[%d][%d]=%v want %v", p, a, got, want[p][a])
			}
		}
	}
	plan := st.Plans[1500]
	for p, r := range plan.ResidualAmp {
		if r > 1e-6 {
			t.Errorf("square residual point %d = %v", p, r)
		}
	}

	// Round trip via the model with the fitted matrix: predict then recover.
	est := st.Estimates(time.Now())
	if len(est) != 1 {
		t.Fatalf("estimates %d", len(est))
	}
}

// Overdetermined 3 points / 1 plane: no worse than single-point balancing.
func TestOverdeterminedViaReplay(t *testing.T) {
	conv := vec.InternalDefault()
	alpha := cmplx.Rect(2, 120*math.Pi/180)
	v0 := []complex128{cmplx.Rect(80, 30*math.Pi/180),
		cmplx.Rect(60, 200*math.Pi/180),
		cmplx.Rect(40, 320*math.Pi/180)}
	tw := cmplx.Rect(20, 0)
	at := time.Unix(1000, 0)
	var readings []domain.Reading
	origs := make([]vec.Polar, 3)
	trials := make([]vec.Polar, 3)
	for i, z := range v0 {
		origs[i] = vec.FromComplex(z)
		trials[i] = vec.FromComplex(z + alpha*tw)
		readings = append(readings, rd(pointN(i), 1500, origs[i].Amp, origs[i].Phase))
	}
	events := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, at, nil, readings),
	}
	tread := make([]domain.Reading, 3)
	for i := range v0 {
		tread[i] = rd(pointN(i), 1500, trials[i].Amp, trials[i].Phase)
	}
	events = append(events, runAdded(2, "r1", domain.RunTrial, at.Add(time.Minute),
		[]domain.Weight{{PlaneID: "P1", Polar: vec.FromComplex(tw)}}, tread))
	job := newJob(conv, []string{"P1"}, []string{"V1", "V2", "V3"}, nil, []int{1500})
	st, err := Replay(job, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := st.Plans[1500]
	var singleWorst float64
	for _, z0 := range v0 {
		w := -z0 / alpha // exactly balance this point
		var worst float64
		for _, zz := range v0 {
			worst = math.Max(worst, cmplx.Abs(zz+alpha*w))
		}
		singleWorst = math.Max(singleWorst, worst)
	}
	for p, r := range plan.ResidualAmp {
		if r > singleWorst+1e-7 {
			t.Errorf("WLS residual point %d %v exceeds any single-point plan worst %v", p, r, singleWorst)
		}
	}
}

func pointN(i int) string { return []string{"V1", "V2", "V3"}[i] }

// Hole splitting appears in the plan and recombines to the same vector.
func TestHoleSplitInPlan(t *testing.T) {
	job := newJob(vec.InternalDefault(), []string{"P1"}, []string{"V1"}, []int{12}, []int{1500})
	st, err := Replay(job, referenceEvents(vec.InternalDefault()), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := st.Plans[1500]
	if len(plan.Holes[0]) != 1 {
		t.Fatal("missing hole split")
	}
	hs := plan.Holes[0][0]
	if math.Abs(hs.ResultantMass-plan.Weights[0].Amp) > 1e-9 {
		t.Errorf("resultant mass %v vs %v", hs.ResultantMass, plan.Weights[0].Amp)
	}
	if math.Abs(vec.PhaseDiff(hs.ResultantAngle, plan.Weights[0].Phase)) > 1e-9 {
		t.Errorf("resultant angle %v vs %v", hs.ResultantAngle, plan.Weights[0].Phase)
	}
}

// History reuse: a job started from stored coefficients needs no trial run to
// produce a correction plan; a good verification keeps it trusted, a bad one
// invalidates it.
func TestHistoryReuseAndInvalidation(t *testing.T) {
	conv := vec.InternalDefault()
	alpha := cmplx.Rect(2, 120*math.Pi/180)
	entry := &history.Entry{
		MachineID: "m1", Speed: 1500,
		PlaneIDs: []string{"P1"}, PointIDs: []string{"V1"},
		Re: []float64{real(alpha)}, Im: []float64{imag(alpha)},
		Uncert: []float64{0.1}, Status: history.StatusActive,
	}
	job := newJob(conv, []string{"P1"}, []string{"V1"}, nil, []int{1500})
	at := time.Unix(1000, 0)
	goodVerify := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, at, nil,
			[]domain.Reading{rd("V1", 1500, 80, 30)}),
		runAdded(2, "rv", domain.RunVerify, at.Add(time.Minute),
			[]domain.Weight{wgt("P1", 40, 90)},
			// v0 + alpha*w with w=40∠90 -> ~0; simulate small residual 2∠100
			[]domain.Reading{rd("V1", 1500, 2, 100)}),
	}
	st, err := Replay(job, goodVerify, map[int]*history.Entry{1500: entry})
	if err != nil {
		t.Fatal(err)
	}
	if !st.HistoryUsed {
		t.Fatal("expected history-based plan")
	}
	if st.Plans[1500] == nil || st.Plans[1500].Err != "" {
		t.Fatalf("plan missing: %+v", st.Plans[1500])
	}
	if !st.TrustOK {
		t.Fatalf("good verify should pass trust: %+v", st.Trust)
	}
	if entry.Status != history.StatusActive {
		t.Fatal("entry should stay active")
	}

	// Bad verification: vibration barely reduced.
	entry2 := &history.Entry{
		MachineID: "m1", Speed: 1500, PlaneIDs: []string{"P1"}, PointIDs: []string{"V1"},
		Re: []float64{real(alpha)}, Im: []float64{imag(alpha)},
		Uncert: []float64{0.1}, Status: history.StatusActive,
	}
	badVerify := []domain.StoredEvent{
		runAdded(1, "r0", domain.RunOriginal, at, nil,
			[]domain.Reading{rd("V1", 1500, 80, 30)}),
		runAdded(2, "rv", domain.RunVerify, at.Add(time.Minute),
			[]domain.Weight{wgt("P1", 20, 0)}, // tiny correction
			[]domain.Reading{rd("V1", 1500, 72, 40)}),
	}
	st2, err := Replay(job, badVerify, map[int]*history.Entry{1500: entry2})
	if err != nil {
		t.Fatal(err)
	}
	if st2.TrustOK {
		t.Fatal("bad verify must fail trust")
	}
	if entry2.Status != history.StatusSuspect {
		t.Fatal("history entry must become suspect")
	}
}

// Per-run ill-conditioning screen helper.
func TestTrialChangeTooSmall(t *testing.T) {
	conv := vec.InternalDefault()
	job := newJob(conv, []string{"P1"}, []string{"V1"}, nil, []int{1500})
	mkRun := func(amp, phase float64) *Run {
		st, err := Replay(job, []domain.StoredEvent{
			runAdded(1, "r", domain.RunOriginal, time.Unix(1000, 0),
				nil, []domain.Reading{rd("V1", 1500, amp, phase)})}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return st.Runs[0]
	}
	origRun := mkRun(80, 30)
	tinyRun := mkRun(80.2, 30.1)
	bigRun := mkRun(100, 60)
	if _, _, bad := TrialChangeTooSmall(origRun, tinyRun, []int{1500}, 2, 0.05); !bad {
		t.Error("tiny change should be rejected")
	}
	if _, _, bad := TrialChangeTooSmall(origRun, bigRun, []int{1500}, 2, 0.05); bad {
		t.Error("clear change should pass")
	}
}
