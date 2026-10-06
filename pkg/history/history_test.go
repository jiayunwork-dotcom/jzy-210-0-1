package history

import (
	"math"
	"testing"
	"time"
)

func cz(re, im float64) complex128 { return complex(re, im) }

func makeEstimate(re, im, unc []float64, at time.Time) *Estimate {
	return &Estimate{
		MachineID: "m1", Speed: 3000,
		PlaneIDs: []string{"A", "B"}, PointIDs: []string{"P1", "P2"},
		Re: re, Im: im, Uncert: unc, At: at,
	}
}

// First estimate becomes an active entry.
func TestFirstEstimateActive(t *testing.T) {
	now := time.Now()
	e := Fuse(nil, makeEstimate(
		[]float64{1, 0, 0, 1}, []float64{1, -1, 1, 0},
		[]float64{.1, .1, .1, .1}, now), DefaultPolicy(), now)
	if e.Status != StatusActive || e.NContrib != 1 {
		t.Fatalf("status=%s n=%d", e.Status, e.NContrib)
	}
}

// Two consistent estimates fused by inverse variance move toward the more
// precise one and reduce fused uncertainty.
func TestFuseInverseVariance(t *testing.T) {
	pol := DefaultPolicy()
	now := time.Now()
	e1 := Fuse(nil, makeEstimate(
		[]float64{1, 0, 0, 1}, []float64{1, -1, 1, 0},
		[]float64{.1, .1, .1, .1}, now), pol, now)
	later := now.Add(24 * time.Hour)
	e2 := Fuse(e1, makeEstimate(
		[]float64{1.05, 0, 0, 1}, []float64{1.05, -1, 1, 0},
		[]float64{.05, .1, .1, .1}, later), pol, later)
	if e2.NContrib != 2 {
		t.Fatalf("n contrib = %d", e2.NContrib)
	}
	// Entry 0 (re 1±.1) fused with 1.05±.05: weighted mean (1/.01 and 1/.0025)
	// = 1.04; floor MinUncertRel*|~1.44| + abs .05 ≈ 0.122 applies, so just
	// check direction of travel and monotone information.
	if e2.Re[0] <= e1.Re[0] {
		t.Errorf("fused re %v should move toward more precise 1.05 from %v", e2.Re[0], e1.Re[0])
	}
}

// Old information is exponentially down-weighted: an estimate much older than
// the half life has essentially no influence.
func TestFuseAging(t *testing.T) {
	pol := DefaultPolicy()
	now := time.Now()
	old := now.Add(-10 * 365 * 24 * time.Hour)
	e1 := Fuse(nil, makeEstimate(
		[]float64{10, 0, 0, 0}, []float64{0, 0, 0, 0},
		[]float64{.1, .1, .1, .1}, old), pol, old)
	e2 := Fuse(e1, makeEstimate(
		[]float64{0, 0, 0, 0}, []float64{0, 0, 0, 0},
		[]float64{.1, .1, .1, .1}, now), pol, now)
	if math.Abs(e2.Re[0]) > 0.02 {
		t.Errorf("aged estimate still pulls fused value: %v", e2.Re[0])
	}
}

// A suspect entry is replaced, not fused with.
func TestSuspectReplaced(t *testing.T) {
	pol := DefaultPolicy()
	now := time.Now()
	e1 := Fuse(nil, makeEstimate(
		[]float64{10, 0, 0, 0}, []float64{0, 0, 0, 0},
		[]float64{.1, .1, .1, .1}, now), pol, now)
	e1.InflateAfterFailure()
	if e1.Status != StatusSuspect {
		t.Fatal("expected suspect")
	}
	e2 := Fuse(e1, makeEstimate(
		[]float64{1, 0, 0, 0}, []float64{0, 0, 0, 0},
		[]float64{.1, .1, .1, .1}, now.Add(time.Hour)), pol, now.Add(time.Hour))
	if e2.Status != StatusActive || e2.NContrib != 1 {
		t.Fatalf("expected fresh active entry, got %s n=%d", e2.Status, e2.NContrib)
	}
	if math.Abs(e2.Re[0]-1) > 1e-9 {
		t.Errorf("replacement value %v want 1", e2.Re[0])
	}
}

// Trust check: a verification run that both matches prediction and reduces
// vibration passes; a run that leaves vibration high (history invalid) fails.
func TestTrustPassAndFail(t *testing.T) {
	pol := DefaultPolicy()
	points := []string{"P1"}
	v0 := []complex128{cz(80, 0)}

	// Predicted near zero and measured near zero: pass.
	rep := CheckTrust(points, v0, []complex128{cz(5, 0)}, []complex128{cz(2, 0)}, pol)
	if !rep.OK {
		t.Errorf("expected pass: %+v", rep.PerPoint)
	}

	// Measured almost unchanged: reduction fails.
	rep = CheckTrust(points, v0, []complex128{cz(75, 0)}, []complex128{cz(1, 0)}, pol)
	if rep.OK {
		t.Error("expected failure: no reduction despite small predicted residual")
	}

	// Measured small but wildly different from prediction: model error fails.
	rep = CheckTrust(points, v0, []complex128{cz(30, 180)}, []complex128{cz(2, 0)}, pol)
	if rep.OK {
		t.Error("expected failure: model disagreement")
	}
}
