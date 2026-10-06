package store

import (
	"context"
	"math"
	"math/cmplx"
	"testing"

	"balancing/internal/balance"
	"balancing/internal/split"
	"balancing/internal/vec"
)

func matFromRows(rows ...[]complex128) balance.Matrix { return balance.MatrixFromRows(rows...) }

func addAll(a, b []complex128) []complex128 {
	out := make([]complex128, len(a))
	for i := range a {
		out[i] = a[i] + b[i]
	}
	return out
}

// TestSinglePlaneReferenceEndToEnd drives the full store for the spec
// reference case and checks H, correction and (after mounting it) residual.
func TestSinglePlaneReferenceEndToEnd(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical())
	job := createJob(t, s, m, false)

	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, job, m, atTime(0), v0)
	tw := cpolar(20, 0)
	v1 := []complex128{cpolar(89.44, 56.57)}
	addTrial(t, s, job, m, atTime(1), 0, tw, v1)

	sr := speedResult(t, s, job)
	if sr.Influence[0][0].Amp < 1.99 || sr.Influence[0][0].Amp > 2.01 {
		t.Errorf("|H| = %.5f, want ~2", sr.Influence[0][0].Amp)
	}
	if math.Abs(sr.Influence[0][0].Phase-120) > 1e-2 {
		t.Errorf("angle H = %.4f, want 120", sr.Influence[0][0].Phase)
	}
	if math.Abs(sr.Correction[0].Amp-40) > 1e-2 {
		t.Errorf("correction = %v, want 40g", sr.Correction[0])
	}
	if math.Abs(sr.Correction[0].Phase-90) > 1e-2 {
		t.Errorf("correction angle = %.4f, want 90", sr.Correction[0].Phase)
	}
	if sr.ResidualRMS > 1e-2 {
		t.Errorf("predicted residual %.4f, want ~0", sr.ResidualRMS)
	}
	if !sr.HistoryEstimate {
		t.Error("single-plane trial should contribute to history")
	}

	// Mount the proposed correction (trial weight removed) and record a
	// verification run: model predicts ~zero vibration.
	cw := extVec(m, sr.Correction[0])
	vv := []complex128{v0[0] + extMatrix(m, sr.Influence).At(0, 0)*cw}
	addMounted(t, s, job, RunVerification, atTime(2), m, []complex128{cw}, vv)

	js, _ := s.GetJob(context.Background(), job, false)
	sr2 := js.Result.Speeds[0]
	if sr2.Verification == nil {
		t.Fatal("expected verification verdict")
	}
	if !sr2.Verification.ModelAccepted {
		t.Errorf("model rejected: %+v", sr2.Verification)
	}
	if cmplx.Abs(vv[0]) > 1e-6 {
		t.Errorf("verification vibration %.3e, want ~0", cmplx.Abs(vv[0]))
	}

	// The machine history must now carry this coefficient.
	hist, _, err := s.MachineHistory(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := hist.EntryAt(testSpeed)
	if !ok {
		t.Fatal("history unavailable after verified trial job")
	}
	if !e.Verified {
		t.Error("history entry should be marked verified")
	}
}

// TestTwoPlaneTwoPointSynthetic is the double-plane/double-point combined
// case: a planted 2x2 H is recovered from the two trial runs, correction zeros
// both points, and verification confirms it.
func TestTwoPlaneTwoPointSynthetic(t *testing.T) {
	s, m := newSetup(t, 2, 2, vec.Canonical())
	job := createJob(t, s, m, false)

	h0 := matFromRows(
		[]complex128{cpolar(1.4, 45), cpolar(0.7, -20)},
		[]complex128{cpolar(0.5, 160), cpolar(1.1, 210)},
	)
	v0 := []complex128{cpolar(60, 20), cpolar(45, 200)}
	addOriginal(t, s, job, m, atTime(0), v0)

	w1 := []complex128{cpolar(18, 0), 0}
	w2 := []complex128{0, cpolar(14, 30)}
	addTrial(t, s, job, m, atTime(1), 0, w1[0], addAll(h0.MulVec(w1), v0))
	addTrial(t, s, job, m, atTime(2), 1, w2[1], addAll(h0.MulVec(w2), v0))

	sr := speedResult(t, s, job)
	got := extMatrix(m, sr.Influence)
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			approxC(t, got.At(i, j), h0.At(i, j), 1e-9, "H recovered")
		}
	}
	cw := make([]complex128, 2)
	for p, pol := range sr.Correction {
		cw[p] = extVec(m, pol)
	}
	res := make([]complex128, 2)
	for i := range res {
		res[i] = v0[i] + got.At(i, 0)*cw[0] + got.At(i, 1)*cw[1]
	}
	for i := range res {
		approxC(t, res[i], 0, 1e-8, "zero residual")
	}

	addMounted(t, s, job, RunVerification, atTime(3), m, cw, res)
	js, _ := s.GetJob(context.Background(), job, false)
	if v := js.Result.Speeds[0].Verification; v == nil || !v.ModelAccepted || v.Reduction < 0.999 {
		t.Fatalf("verification = %+v", v)
	}
}

// TestOverdeterminedThroughStore: 1 plane, 3 points; the proposed correction
// must beat balancing any single point on its own (in residual 2-norm).
func TestOverdeterminedThroughStore(t *testing.T) {
	s, m := newSetup(t, 1, 3, vec.Canonical())
	job := createJob(t, s, m, false)

	hs := matFromRows(
		[]complex128{cpolar(1.0, 10)},
		[]complex128{cpolar(1.3, 95)},
		[]complex128{cpolar(0.9, 200)},
	)
	v0 := []complex128{cpolar(70, 30), cpolar(50, 160), cpolar(55, 280)}
	addOriginal(t, s, job, m, atTime(0), v0)
	tw := cpolar(25, 0)
	addTrial(t, s, job, m, atTime(1), 0, tw, addAll(hs.MulVec([]complex128{tw}), v0))

	sr := speedResult(t, s, job)
	got := extMatrix(m, sr.Influence)
	cw := extVec(m, sr.Correction[0])
	lsRes := make([]complex128, 3)
	for k := range lsRes {
		lsRes[k] = v0[k] + got.At(k, 0)*cw
	}
	lsNorm := vecNorm(lsRes)
	for k := range v0 {
		wi := -v0[k] / got.At(k, 0)
		var n float64
		for q := range v0 {
			r := v0[q] + got.At(q, 0)*wi
			n += real(r)*real(r) + imag(r)*imag(r)
		}
		if math.Sqrt(n) < lsNorm-1e-9 {
			t.Errorf("LS residual %.4f worse than single point %d (%.4f)", lsNorm, k, math.Sqrt(n))
		}
	}
}

// TestConventionConversionEndToEnd uses a with-rotation machine whose 0° is at
// internal 90°. Internally planted vibrations/weights must come back in the
// machine's external angles, and feeding those external angles back must
// reconstruct the internal vectors exactly.
func TestConventionConversionEndToEnd(t *testing.T) {
	conv := vec.Convention{ReferenceDeg: 90, Direction: vec.WithRotation}
	s, m := newSetup(t, 1, 1, conv)
	job := createJob(t, s, m, false)

	// internal coefficient 2∠120 -> external: reference offset cancels,
	// direction mirror negates phase => 2∠240.
	h0 := matFromRows([]complex128{cpolar(2, 120)})
	v0 := []complex128{cpolar(80, 30)} // external = 90-30 = 60
	addOriginal(t, s, job, m, atTime(0), v0)
	tw := cpolar(20, 90) // internal 90 = external 0
	addTrial(t, s, job, m, atTime(1), 0, tw, addAll(h0.MulVec([]complex128{tw}), v0))

	sr := speedResult(t, s, job)
	if math.Abs(sr.Influence[0][0].Amp-2) > 1e-9 {
		t.Errorf("|H| external %.5f, want 2", sr.Influence[0][0].Amp)
	}
	if d := math.Abs(vec.Norm(sr.Influence[0][0].Phase) - 240); d > 1e-9 {
		t.Errorf("external H phase %.4f, want 240", sr.Influence[0][0].Phase)
	}
	// correction angle external: internal 90 -> external 0
	if math.Abs(sr.Correction[0].Amp-40) > 1e-8 {
		t.Errorf("correction mass %.5f, want 40", sr.Correction[0].Amp)
	}
	if d := math.Abs(vec.Norm(sr.Correction[0].Phase) - 0); d > 1e-8 && 360-d > 1e-8 {
		t.Errorf("correction external angle %.4f, want 0", sr.Correction[0].Phase)
	}
	// feeding external back must recover the internal H
	back := extMatrix(m, sr.Influence)
	approxC(t, back.At(0, 0), h0.At(0, 0), 1e-9, "H roundtrip")
}

// TestHoleSplitEndToEnd: a 12-hole plane, arbitrary-angle correction must be
// split into two adjacent holes whose vector sum is the proposed correction.
func TestHoleSplitEndToEnd(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical(), &split.Plane{Count: 12, FirstHoleDeg: 0})
	job := createJob(t, s, m, false)

	h0 := matFromRows([]complex128{cpolar(2, 120)})
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, job, m, atTime(0), v0)
	tw := cpolar(20, 0)
	addTrial(t, s, job, m, atTime(1), 0, tw, addAll(h0.MulVec([]complex128{tw}), v0))

	sr := speedResult(t, s, job)
	plans := sr.HolePlans[0]
	if len(plans) < 1 || len(plans) > 2 {
		t.Fatalf("split into %d holes", len(plans))
	}
	// 40g at 90° lands exactly on the 12-hole ring's hole 3 (90°).
	if len(plans) != 1 || plans[0].HoleIndex != 3 {
		t.Fatalf("plans = %+v, want single hole index 3", plans)
	}
	recon := split.Reconstruct(m.Convention(), plans)
	want := extVec(m, sr.Correction[0])
	if cmplx.Abs(recon-want) > 1e-9 {
		t.Errorf("hole synthesis %v != correction %v", recon, want)
	}
}

// TestHoleSplitGeneralFromStore checks an off-hole correction on an 8-hole
// ring splits into two positive weights with exact synthesis.
func TestHoleSplitGeneralFromStore(t *testing.T) {
	s, m := newSetup(t, 1, 1, vec.Canonical(), &split.Plane{Count: 8, FirstHoleDeg: 15})
	job := createJob(t, s, m, false)
	h0 := matFromRows([]complex128{cpolar(2, 120)})
	v0 := []complex128{cpolar(80, 30)}
	addOriginal(t, s, job, m, atTime(0), v0)
	addTrial(t, s, job, m, atTime(1), 0, cpolar(20, 0), addAll(h0.MulVec([]complex128{cpolar(20, 0)}), v0))

	sr := speedResult(t, s, job)
	plans := sr.HolePlans[0]
	if len(plans) != 2 {
		t.Fatalf("expected two-hole split, got %+v", plans)
	}
	for _, h := range plans {
		if h.Grams <= 0 {
			t.Fatalf("non-positive hole weight %+v", h)
		}
	}
	recon := split.Reconstruct(m.Convention(), plans)
	want := extVec(m, sr.Correction[0])
	if cmplx.Abs(recon-want) > 1e-9 {
		t.Errorf("synthesis %v != %v", recon, want)
	}
}
