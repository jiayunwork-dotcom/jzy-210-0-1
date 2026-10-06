package store

import (
	"context"
	"math"
	"math/cmplx"
	"testing"
	"time"

	"balancing/internal/balance"
	"balancing/internal/history"
	"balancing/internal/split"
	"balancing/internal/vec"
)

const testSpeed = 1500

func mkMachine(t *testing.T, planes, points int, conv vec.Convention, holes ...*split.Plane) *Machine {
	t.Helper()
	layout := make([]*split.Plane, planes)
	for i, h := range holes {
		layout[i] = h
	}
	m := &Machine{
		Name: "fan-" + t.Name(), PlaneCount: planes, PointCount: points,
		Speeds:       []int{testSpeed},
		ReferenceDeg: conv.ReferenceDeg, Direction: conv.Direction,
		HoleLayout:     layout,
		FusionStrategy: history.UncertaintyWeighted,
	}
	if err := NewMemoryStore().CreateMachine(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newSetup(t *testing.T, planes, points int, conv vec.Convention, holes ...*split.Plane) (*MemoryStore, *Machine) {
	t.Helper()
	s := NewMemoryStore()
	layout := make([]*split.Plane, planes)
	for i, h := range holes {
		layout[i] = h
	}
	m := &Machine{
		Name: "m", PlaneCount: planes, PointCount: points, Speeds: []int{testSpeed},
		ReferenceDeg: conv.ReferenceDeg, Direction: conv.Direction,
		HoleLayout: layout, FusionStrategy: history.UncertaintyWeighted,
	}
	if err := s.CreateMachine(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return s, m
}

func atTime(min int) time.Time {
	return time.Date(2026, time.March, 2, 10, min, 0, 0, time.UTC)
}

func rds(m *Machine, internal []complex128) []ReadingInput {
	conv := m.Convention()
	out := make([]ReadingInput, len(internal))
	for i, z := range internal {
		p := vec.FromC(z)
		p.Phase = conv.ToExternal(p.Phase)
		out[i] = ReadingInput{Speed: testSpeed, Point: i, Amp: p.Amp, Phase: p.Phase}
	}
	return out
}

func weightIn(m *Machine, plane int, w complex128) WeightInput {
	p := vec.FromC(w)
	p.Phase = m.Convention().ToExternal(p.Phase)
	return WeightInput{Plane: plane, Grams: p.Amp, Angle: p.Phase}
}

func addOriginal(t *testing.T, s Store, jobID string, m *Machine, tm time.Time, v0 []complex128) {
	t.Helper()
	if _, err := s.AddRun(context.Background(), jobID, RunInput{
		Kind: RunOriginal, RunTime: tm, Readings: rds(m, v0),
	}); err != nil {
		t.Fatalf("original run: %v", err)
	}
}

func addTrial(t *testing.T, s Store, jobID string, m *Machine, tm time.Time, plane int, tw complex128, v []complex128) {
	t.Helper()
	twp := weightIn(m, plane, tw)
	ws := []WeightInput{twp}
	if _, err := s.AddRun(context.Background(), jobID, RunInput{
		Kind: RunTrial, RunTime: tm, Weights: ws,
		TrialPlane: &plane, TrialWeight: &twp, Readings: rds(m, v),
	}); err != nil {
		t.Fatalf("trial run: %v", err)
	}
}

func addMounted(t *testing.T, s Store, jobID string, kind RunKind, tm time.Time, m *Machine, w []complex128, v []complex128) {
	t.Helper()
	ws := make([]WeightInput, 0, len(w))
	for p, z := range w {
		if cmplx.Abs(z) > 0 {
			ws = append(ws, weightIn(m, p, z))
		}
	}
	if _, err := s.AddRun(context.Background(), jobID, RunInput{
		Kind: kind, RunTime: tm, Weights: ws, Readings: rds(m, v),
	}); err != nil {
		t.Fatalf("%s run: %v", kind, err)
	}
}

func createJob(t *testing.T, s Store, m *Machine, useHistory bool) string {
	t.Helper()
	j, err := s.CreateJob(context.Background(), m.ID, "op", "", useHistory, time.Now().UTC())
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return j.ID
}

func speedResult(t *testing.T, s Store, jobID string) *SpeedResult {
	t.Helper()
	js, err := s.GetJob(context.Background(), jobID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(js.Result.Speeds) != 1 {
		t.Fatalf("got %d speed results", len(js.Result.Speeds))
	}
	sr := js.Result.Speeds[0]
	if sr.Status != "ok" {
		t.Fatalf("status %s: %+v", sr.Status, sr.Error)
	}
	return sr
}

// externalToInternal converts an external polar result back to the internal
// frame (vectors and weights).
func extVec(m *Machine, p vec.Polar) complex128 {
	return vec.Polar{Amp: p.Amp, Phase: m.Convention().ToInternal(p.Phase)}.C()
}

func extMatrix(m *Machine, ext [][]vec.Polar) balance.Matrix {
	return matrixFromExternal(m, ext)
}

func approxC(t *testing.T, got, want complex128, tol float64, name string) {
	t.Helper()
	if d := cmplx.Abs(got - want); d > tol {
		t.Errorf("%s = %.5f∠%.4f°, want %.5f∠%.4f° (Δ%.2e)", name,
			cmplx.Abs(got), degC(got), cmplx.Abs(want), degC(want), d)
	}
}

func degC(z complex128) float64 { return vec.Norm(math.Atan2(imag(z), real(z)) * 180 / math.Pi) }

func cpolar(amp, phase float64) complex128 { return vec.Polar{Amp: amp, Phase: phase}.C() }

func vecNorm(v []complex128) float64 {
	var s float64
	for _, z := range v {
		s += real(z)*real(z) + imag(z)*imag(z)
	}
	return math.Sqrt(s)
}
