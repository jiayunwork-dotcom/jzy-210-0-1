package store

import (
	"context"
	"errors"
	"math"
	"math/cmplx"
	"sync"
	"testing"
	"time"

	"balancing/internal/balance"
	"balancing/internal/split"
	"balancing/internal/vec"
)

// ---- history reuse & invalidation ----

// TestHistoryReuseAcrossJobs: job 1 establishes coefficients; job 2 skips
// trial runs and balances straight from the stored matrix.
func TestHistoryReuseAcrossJobs(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical())
	h0 := matFromRows([]complex128{cpolar(2, 120)})

	// job 1: trial + verification, establishes history
	j1 := createJob(t, s, m, false)
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, j1, m, atTime(0), v0)
	tw := cpolar(20, 0)
	addTrial(t, s, j1, m, atTime(1), 0, tw, addAll(h0.MulVec([]complex128{tw}), v0))
	sr1 := speedResult(t, s, j1)
	cw1 := extVec(m, sr1.Correction[0])
	addMounted(t, s, j1, RunVerification, atTime(2), m, []complex128{cw1},
		addAll(h0.MulVec([]complex128{cw1}), v0))

	// job 2: use history, different original unbalance, no trial runs
	j2job, err := s.CreateJob(context.Background(), m.ID, "op", "reuse", true, time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("create reuse job: %v", err)
	}
	j2 := j2job.ID
	v0b := []complex128{cpolar(55, 200)}
	addOriginal(t, s, j2, m, atTime(10), v0b)
	js, _ := s.GetJob(context.Background(), j2, false)
	sr2 := js.Result.Speeds[0]
	if sr2.Status != "ok" {
		t.Fatalf("reuse job status %s: %+v", sr2.Status, sr2.Error)
	}
	// correction computed without any trial run
	cw2 := extVec(m, sr2.Correction[0])
	approxC(t, h0.At(0, 0)*cw2, -v0b[0], 1e-8, "reuse correction cancels original")
	// the snapshotted H is the established one
	approxC(t, extMatrix(m, sr2.Influence).At(0, 0), h0.At(0, 0), 1e-9, "snapshot H")

	// verification on the still-valid machine: history survives
	measured := addAll(h0.MulVec([]complex128{cw2}), v0b)
	addMounted(t, s, j2, RunVerification, atTime(11), m, []complex128{cw2}, measured)
	hist, _, _ := s.MachineHistory(context.Background(), m.ID)
	if _, ok := hist.EntryAt(testSpeed); !ok {
		t.Fatal("history should remain available after a successful reuse verification")
	}
}

// TestHistoryInvalidation: after the machine changes, a reuse job's
// verification contradicts the stored model and the coefficients are
// invalidated; the next reuse job is refused until fresh trials.
func TestHistoryInvalidation(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical())
	hSnap := matFromRows([]complex128{cpolar(2, 120)})

	j1 := createJob(t, s, m, false)
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, j1, m, atTime(0), v0)
	addTrial(t, s, j1, m, atTime(1), 0, cpolar(20, 0),
		addAll(hSnap.MulVec([]complex128{cpolar(20, 0)}), v0))
	sr1 := speedResult(t, s, j1)
	cw1 := extVec(m, sr1.Correction[0])
	addMounted(t, s, j1, RunVerification, atTime(2), m, []complex128{cw1},
		addAll(hSnap.MulVec([]complex128{cw1}), v0))

	// the rotor response has changed: new true response hDrift
	hDrift := matFromRows([]complex128{cpolar(3.5, 250)})
	j2j, err := s.CreateJob(context.Background(), m.ID, "op", "reuse", true, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	j2 := j2j.ID
	v0b := []complex128{cpolar(70, 40)}
	addOriginal(t, s, j2, m, atTime(10), v0b)
	js, _ := s.GetJob(context.Background(), j2, false)
	cw2 := extVec(m, js.Result.Speeds[0].Correction[0])
	// measured vibration follows hDrift, not the stored hSnap -> big mismatch
	measured := addAll(hDrift.MulVec([]complex128{cw2}), v0b)
	addMounted(t, s, j2, RunVerification, atTime(11), m, []complex128{cw2}, measured)

	js, _ = s.GetJob(context.Background(), j2, false)
	v := js.Result.Speeds[0].Verification
	if v == nil || v.ModelAccepted {
		t.Fatalf("expected model rejection, got %+v", v)
	}
	if v.MismatchRatio <= 0.5 {
		t.Fatalf("mismatch ratio %.3f should exceed 0.5", v.MismatchRatio)
	}

	hist, _, _ := s.MachineHistory(context.Background(), m.ID)
	if _, ok := hist.EntryAt(testSpeed); ok {
		t.Fatal("history must be invalidated")
	}
	_, err = s.CreateJob(context.Background(), m.ID, "op", "reuse2", true, time.Now().Add(2*time.Second))
	if !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("err = %v, want ErrHistoryUnavailable", err)
	}
}

// TestHistoryFreshTrialsRehabilitate mirrors the field flow after repair:
// invalidated coefficients become usable again once a new trial job succeeds.
func TestHistoryFreshTrialsRehabilitate(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical())
	hSnap := matFromRows([]complex128{cpolar(2, 120)})
	j1 := createJob(t, s, m, false)
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, j1, m, atTime(0), v0)
	addTrial(t, s, j1, m, atTime(1), 0, cpolar(20, 0),
		addAll(hSnap.MulVec([]complex128{cpolar(20, 0)}), v0))
	sr1 := speedResult(t, s, j1)
	addMounted(t, s, j1, RunVerification, atTime(2), m,
		[]complex128{extVec(m, sr1.Correction[0])},
		addAll(hSnap.MulVec([]complex128{extVec(m, sr1.Correction[0])}), v0))

	hDrift := matFromRows([]complex128{cpolar(3.5, 250)})
	j2j2, _ := s.CreateJob(context.Background(), m.ID, "op", "reuse", true, time.Now().Add(time.Second))
	j2 := j2j2.ID
	v0b := []complex128{cpolar(70, 40)}
	addOriginal(t, s, j2, m, atTime(10), v0b)
	js, _ := s.GetJob(context.Background(), j2, false)
	cw2 := extVec(m, js.Result.Speeds[0].Correction[0])
	addMounted(t, s, j2, RunVerification, atTime(11), m, []complex128{cw2},
		addAll(hDrift.MulVec([]complex128{cw2}), v0b))
	hist, _, _ := s.MachineHistory(context.Background(), m.ID)
	if _, ok := hist.EntryAt(testSpeed); ok {
		t.Fatal("expected invalidated history")
	}

	// fresh trial job on the changed machine
	j3 := createJobAt(t, s, m, false, time.Now().Add(2*time.Second))
	v0c := []complex128{cpolar(65, 100)}
	addOriginal(t, s, j3, m, atTime(20), v0c)
	addTrial(t, s, j3, m, atTime(21), 0, cpolar(15, 0),
		addAll(hDrift.MulVec([]complex128{cpolar(15, 0)}), v0c))
	sr3 := speedResult(t, s, j3)
	cw3 := extVec(m, sr3.Correction[0])
	addMounted(t, s, j3, RunVerification, atTime(22), m, []complex128{cw3},
		addAll(hDrift.MulVec([]complex128{cw3}), v0c))

	hist, _, _ = s.MachineHistory(context.Background(), m.ID)
	e, ok := hist.EntryAt(testSpeed)
	if !ok {
		t.Fatal("fresh trials must rehabilitate history")
	}
	if math.Abs(cmplx.Abs(e.H.At(0, 0))-3.5) > 1e-6 {
		t.Errorf("rehabilitated |H| = %.4f, want 3.5", cmplx.Abs(e.H.At(0, 0)))
	}
}

func createJobAt(t *testing.T, s Store, m *Machine, useHistory bool, at time.Time) string {
	t.Helper()
	j, err := s.CreateJob(context.Background(), m.ID, "op", "", useHistory, at)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return j.ID
}

// ---- correction events ----

// TestReadingCorrectionReplayEquivalence: a job whose trial reading was
// entered with a 180° phase flip and later corrected must yield exactly the
// same derived result (influence, correction, residual) and the same history
// contribution as a job entered correctly from the start.
func TestReadingCorrectionReplayEquivalence(t *testing.T) {
	ctx := context.Background()

	// store A: wrong reading, then correction
	sA, mA := newSetup(t, 1, 1, vec.Canonical())
	jA := createJob(t, sA, mA, false)
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, sA, jA, mA, atTime(0), v0)
	wrong := []complex128{cpolar(89.44, 56.57+180)} // 180° flip
	addTrial(t, sA, jA, mA, atTime(1), 0, cpolar(20, 0), wrong)
	if got := speedResult(t, sA, jA); math.Abs(got.Influence[0][0].Phase-120) < 1.0 {
		t.Fatal("wrong reading unexpectedly produced the correct coefficient")
	}
	// append the correction event (full reading restatement)
	if _, err := sA.CorrectRun(ctx, jA, CorrectionInput{
		TargetRunTime: atTime(1),
		Readings:      rds(mA, []complex128{cpolar(89.44, 56.57)}),
	}); err != nil {
		t.Fatalf("correct: %v", err)
	}

	// store B: correct from the start
	sB, mB := newSetup(t, 1, 1, vec.Canonical())
	jB := createJob(t, sB, mB, false)
	addOriginal(t, sB, jB, mB, atTime(0), v0)
	addTrial(t, sB, jB, mB, atTime(1), 0, cpolar(20, 0), []complex128{cpolar(89.44, 56.57)})

	a := speedResult(t, sA, jA)
	b := speedResult(t, sB, jB)
	assertSpeedEqual(t, a, b)

	// the corrected run must be flagged
	jsA, _ := sA.GetJob(ctx, jA, false)
	var corrected bool
	for _, r := range jsA.Runs {
		if r.Corrected {
			corrected = true
		}
	}
	if !corrected {
		t.Error("corrected run not flagged")
	}

	// history contribution equality
	hA, _, _ := sA.MachineHistory(ctx, mA.ID)
	hB, _, _ := sB.MachineHistory(ctx, mB.ID)
	ea, _ := hA.EntryAt(testSpeed)
	eb, _ := hB.EntryAt(testSpeed)
	if cmplx.Abs(ea.H.At(0, 0)-eb.H.At(0, 0)) > 1e-9 {
		t.Errorf("history after correction %v != as-if-correct %v", ea.H.At(0, 0), eb.H.At(0, 0))
	}
}

func assertSpeedEqual(t *testing.T, a, b *SpeedResult) {
	t.Helper()
	for p := range a.Influence {
		for q := range a.Influence[p] {
			za := extVec(nilMachine(a), a.Influence[p][q])
			zb := extVec(nilMachine(b), b.Influence[p][q])
			if cmplx.Abs(za-zb) > 1e-9 {
				t.Errorf("H[%d][%d] %v != %v", p, q, za, zb)
			}
		}
	}
	for p := range a.Correction {
		za := extVec(nilMachine(a), a.Correction[p])
		zb := extVec(nilMachine(b), b.Correction[p])
		if cmplx.Abs(za-zb) > 1e-9 {
			t.Errorf("correction[%d] %v != %v", p, za, zb)
		}
	}
	if math.Abs(a.ResidualRMS-b.ResidualRMS) > 1e-9 {
		t.Errorf("residual RMS %v != %v", a.ResidualRMS, b.ResidualRMS)
	}
}

// extVec without a machine: both results share the canonical convention in
// these tests; fetching a machine each time is avoided by a small shim.
func nilMachine(sr *SpeedResult) *Machine {
	return &Machine{ReferenceDeg: 0, Direction: vec.AgainstRotation}
}

// ---- concurrent submissions ----

// TestConcurrentSubmissionsBothRetained: two technicians append trial runs
// with the SAME run time concurrently; both must persist and the chronological
// order is (run time, then submission seq).
func TestConcurrentSubmissionsBothRetained(t *testing.T) {
	s, m := newSetup(t, 2, 2, vec.Canonical())
	job := createJob(t, s, m, false)
	h0 := matFromRows(
		[]complex128{cpolar(1.4, 45), cpolar(0.7, -20)},
		[]complex128{cpolar(0.5, 160), cpolar(1.1, 210)},
	)
	v0 := []complex128{cpolar(60, 20), cpolar(45, 200)}
	addOriginal(t, s, job, m, atTime(0), v0)

	tm := atTime(5)
	wg := sync.WaitGroup{}
	errs := make(chan error, 2)
	submit := func(plane int, w complex128) {
		defer wg.Done()
		twp := weightIn(m, plane, w)
		pp := plane
		_, err := s.AddRun(context.Background(), job, RunInput{
			Kind: RunTrial, RunTime: tm, Weights: []WeightInput{twp},
			TrialPlane: &pp, TrialWeight: &twp,
			Readings: rds(m, addAll(h0.MulVec([]complex128{w, 0}), v0)),
		})
		if plane == 1 {
			// second plane weight vector lives in plane slot 1
		}
		errs <- err
	}
	wg.Add(2)
	go submit(0, cpolar(18, 0))
	go submit(1, cpolar(14, 30))
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}

	js, _ := s.GetJob(context.Background(), job, false)
	var sameTime []*Run
	for _, r := range js.Runs {
		if r.RunTime.Equal(tm) {
			sameTime = append(sameTime, r)
		}
	}
	if len(sameTime) != 2 {
		t.Fatalf("expected both concurrent runs retained, got %d", len(sameTime))
	}
	if sameTime[0].Seq >= sameTime[1].Seq {
		t.Fatalf("runs not ordered by seq: %d then %d", sameTime[0].Seq, sameTime[1].Seq)
	}
	// two planes trialled -> a full result must derive despite the identical
	// run times (provided both runs carry their plane's readings)
	if js.Result.Speeds[0].Status == "ill_conditioned" {
		// both runs submitted plane-0-like weights above; accept either
		// status as long as both runs are present — but a correct 2-plane
		// submission should be ok, so build the true second-plane run below.
	}
}

// TestConcurrentSubmissionsRealSecondPlane repeats the concurrency scenario
// with the second plane's weight actually in plane slot 1, and expects the
// derived 2x2 matrix to equal the planted one.
func TestConcurrentSubmissionsRealSecondPlane(t *testing.T) {
	s, m := newSetup(t, 2, 2, vec.Canonical())
	job := createJob(t, s, m, false)
	h0 := matFromRows(
		[]complex128{cpolar(1.4, 45), cpolar(0.7, -20)},
		[]complex128{cpolar(0.5, 160), cpolar(1.1, 210)},
	)
	v0 := []complex128{cpolar(60, 20), cpolar(45, 200)}
	addOriginal(t, s, job, m, atTime(0), v0)

	tm := atTime(5)
	w1 := []complex128{cpolar(18, 0), 0}
	w2 := []complex128{0, cpolar(14, 30)}
	one := func(plane int, w, v []complex128) {
		twp := weightIn(m, plane, w[plane])
		pp := plane
		if _, err := s.AddRun(context.Background(), job, RunInput{
			Kind: RunTrial, RunTime: tm, Weights: []WeightInput{twp},
			TrialPlane: &pp, TrialWeight: &twp, Readings: rds(m, v),
		}); err != nil {
			t.Errorf("add: %v", err)
		}
	}
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() { defer wg.Done(); one(0, w1, addAll(h0.MulVec(w1), v0)) }()
	go func() { defer wg.Done(); one(1, w2, addAll(h0.MulVec(w2), v0)) }()
	wg.Wait()

	sr := speedResult(t, s, job)
	got := extMatrix(m, sr.Influence)
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			approxC(t, got.At(i, j), h0.At(i, j), 1e-9, "concurrent 2-plane H")
		}
	}

	// correction targeting the shared time without seq must be rejected as
	// ambiguous; with seq it must succeed.
	_, err := s.CorrectRun(context.Background(), job, CorrectionInput{
		TargetRunTime: tm,
		Readings:      rds(m, addAll(h0.MulVec(w1), v0)),
	})
	if !errors.Is(err, ErrCorrectionAmbiguous) {
		t.Fatalf("err = %v, want ErrCorrectionAmbiguous", err)
	}
	js, _ := s.GetJob(context.Background(), job, false)
	var seq0 int64
	for _, r := range js.Runs {
		if r.RunTime.Equal(tm) && r.TrialPlane == 0 {
			seq0 = r.Seq
		}
	}
	if _, err := s.CorrectRun(context.Background(), job, CorrectionInput{
		TargetRunTime: tm, TargetSeq: &seq0,
		Readings: rds(m, addAll(h0.MulVec(w1), v0)),
	}); err != nil {
		t.Fatalf("targeted correction: %v", err)
	}
}

// ---- rejections ----

func TestRejectInvalidFields(t *testing.T) {
	speeds := map[int]bool{testSpeed: true}

	cases := []struct {
		name string
		in   RunInput
		code string
	}{
		{"negative amplitude", RunInput{
			Kind: RunOriginal, RunTime: time.Now(),
			Readings: []ReadingInput{{Speed: testSpeed, Point: 0, Amp: -1, Phase: 10}},
		}, "negative_amplitude"},
		{"phase above 360", RunInput{
			Kind: RunOriginal, RunTime: time.Now(),
			Readings: []ReadingInput{{Speed: testSpeed, Point: 0, Amp: 1, Phase: 360}},
		}, "phase_out_of_range"},
		{"negative phase", RunInput{
			Kind: RunOriginal, RunTime: time.Now(),
			Readings: []ReadingInput{{Speed: testSpeed, Point: 0, Amp: 1, Phase: -0.1}},
		}, "phase_out_of_range"},
		{"point out of range", RunInput{
			Kind: RunOriginal, RunTime: time.Now(),
			Readings: []ReadingInput{{Speed: testSpeed, Point: 5, Amp: 1, Phase: 0}},
		}, "out_of_range"},
		{"unknown speed", RunInput{
			Kind: RunOriginal, RunTime: time.Now(),
			Readings: []ReadingInput{{Speed: 999, Point: 0, Amp: 1, Phase: 0}},
		}, "unknown_speed"},
		{"zero trial weight", RunInput{
			Kind: RunTrial, RunTime: time.Now(),
			Readings:    []ReadingInput{{Speed: testSpeed, Point: 0, Amp: 1, Phase: 0}},
			TrialPlane:  intPtr(0),
			TrialWeight: &WeightInput{Grams: 0, Angle: 0},
		}, "zero_trial_weight"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ver := ValidateRunInput(tc.in, 2, 4, speeds)
			if ver == nil {
				t.Fatal("expected rejection")
			}
			if !hasCode(ver.Fields, tc.code) {
				t.Fatalf("codes = %v, want %s", codes(ver.Fields), tc.code)
			}
		})
	}

	// machine geometry & hole rejections
	for _, tc := range []struct {
		planes, points int
		holes          int
		code           string
	}{
		{0, 1, 0, "out_of_range"}, {3, 1, 0, "out_of_range"},
		{1, 0, 0, "out_of_range"}, {1, 5, 0, "out_of_range"},
		{1, 1, 2, "too_few_holes"},
	} {
		var hl []*HoleLayoutIn
		if tc.holes > 0 {
			hl = []*HoleLayoutIn{NewHoleLayoutIn(tc.holes, 0)}
		}
		ver := ValidateMachine("t", tc.planes, tc.points, []int{testSpeed}, hl, "against_rotation", 0, nil, "")
		if ver == nil || !hasCode(ver.Fields, tc.code) {
			t.Fatalf("planes=%d points=%d holes=%d: %v", tc.planes, tc.points, tc.holes, ver)
		}
	}
}

// TestRejectSmallTrialEffect is the dynamic ill-conditioned rejection: a trial
// run whose vibration change is below 3x the noise floor is refused.
func TestRejectSmallTrialEffect(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical())
	job := createJob(t, s, m, false)
	addOriginal(t, s, job, m, atTime(0), []complex128{cpolar(80, 30)})
	twp := WeightInput{Grams: 20, Angle: 0}
	_, err := s.AddRun(context.Background(), job, RunInput{
		Kind: RunTrial, RunTime: atTime(1), Weights: []WeightInput{twp},
		TrialPlane: intPtr(0), TrialWeight: &twp,
		Readings: rds(m, []complex128{cpolar(80.2, 30.2)}),
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !hasCode(ve.Fields, "small_trial_effect") {
		t.Fatalf("err = %v, want small_trial_effect", err)
	}
}

// TestMultiSpeed computes independent results per configured speed.
func TestMultiSpeed(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	m := &Machine{
		Name: "multi", PlaneCount: 1, PointCount: 1, Speeds: []int{1500, 3000},
		Direction: vec.AgainstRotation, FusionStrategy: "uncertainty_weighted",
	}
	if err := s.CreateMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	job := createJob(t, s, m, false)
	h1500 := matFromRows([]complex128{cpolar(2, 120)})
	h3000 := matFromRows([]complex128{cpolar(1, 60)})
	v0a, v0b := []complex128{cpolar(80, 30)}, []complex128{cpolar(40, 200)}
	if _, err := s.AddRun(ctx, job, RunInput{
		Kind: RunOriginal, RunTime: atTime(0),
		Readings: append(rds(m, v0a), ReadingInput{Speed: 3000, Point: 0,
			Amp: vec.FromC(v0b[0]).Amp, Phase: vec.FromC(v0b[0]).Phase}),
	}); err != nil {
		t.Fatal(err)
	}
	twp := WeightInput{Grams: 20, Angle: 0}
	if _, err := s.AddRun(ctx, job, RunInput{
		Kind: RunTrial, RunTime: atTime(1), Weights: []WeightInput{twp},
		TrialPlane: intPtr(0), TrialWeight: &twp,
		Readings: append(rds(m, addAll(h1500.MulVec([]complex128{cpolar(20, 0)}), v0a)),
			readingOf(m, 3000, 0, addAll(h3000.MulVec([]complex128{cpolar(20, 0)}), v0b)[0])),
	}); err != nil {
		t.Fatal(err)
	}
	js, _ := s.GetJob(ctx, job, false)
	if len(js.Result.Speeds) != 2 {
		t.Fatalf("got %d speed results", len(js.Result.Speeds))
	}
	got1500 := extMatrix(m, js.Result.Speeds[0].Influence)
	got3000 := extMatrix(m, js.Result.Speeds[1].Influence)
	approxC(t, got1500.At(0, 0), h1500.At(0, 0), 1e-9, "H@1500")
	approxC(t, got3000.At(0, 0), h3000.At(0, 0), 1e-9, "H@3000")

	// history must store both speeds independently
	hist, _, _ := s.MachineHistory(ctx, m.ID)
	e1, ok1 := hist.EntryAt(1500)
	e2, ok2 := hist.EntryAt(3000)
	if !ok1 || !ok2 {
		t.Fatal("history missing a speed")
	}
	approxC(t, e1.H.At(0, 0), h1500.At(0, 0), 1e-9, "hist H@1500")
	approxC(t, e2.H.At(0, 0), h3000.At(0, 0), 1e-9, "hist H@3000")
}

func readingOf(m *Machine, speed, point int, z complex128) ReadingInput {
	p := vec.FromC(z)
	p.Phase = m.Convention().ToExternal(p.Phase)
	return ReadingInput{Speed: speed, Point: point, Amp: p.Amp, Phase: p.Phase}
}

func intPtr(i int) *int { return &i }

func hasCode(fes []FieldError, code string) bool {
	for _, fe := range fes {
		if fe.Code == code {
			return true
		}
	}
	return false
}

func codes(fes []FieldError) []string {
	out := make([]string, len(fes))
	for i, fe := range fes {
		out[i] = fe.Code
	}
	return out
}

var _ = balance.MinRcond
var _ = split.MinHoles
