package history

import (
	"math"
	"testing"

	"balancing/internal/balance"
)

func row(z ...complex128) []complex128 { return z }

func sampleH(phase float64) balance.Matrix {
	// single point, single plane coefficient 1∠phase
	return balance.MatrixFromRows(row(complex(math.Cos(phase), math.Sin(phase))))
}

func TestLatestStrategy(t *testing.T) {
	st, err := NewState(Latest)
	if err != nil {
		t.Fatal(err)
	}
	st.AddTrial(Estimate{JobID: "a", Speed: 1500, H: sampleH(0.5), Uncertainty: 0.1, Verified: true})
	st.AddTrial(Estimate{JobID: "b", Speed: 1500, H: sampleH(1.0), Uncertainty: 0.1, Verified: true})
	e, ok := st.EntryAt(1500)
	if !ok {
		t.Fatal("history not available")
	}
	if math.Abs(math.Atan2(imag(e.H.At(0, 0)), real(e.H.At(0, 0)))-1.0) > 1e-9 {
		t.Errorf("latest strategy returned phase %v, want 1.0", e.H.At(0, 0))
	}
}

func TestUncertaintyFusionPrefersCleanEstimate(t *testing.T) {
	st, _ := NewState(UncertaintyWeighted)
	st.AddTrial(Estimate{JobID: "clean", Speed: 3000, H: sampleH(0), Uncertainty: 0.05, Verified: true})
	st.AddTrial(Estimate{JobID: "noisy", Speed: 3000, H: sampleH(math.Pi / 2), Uncertainty: 0.5, Verified: true})
	e, ok := st.EntryAt(3000)
	if !ok {
		t.Fatal("history not available")
	}
	// weights 400:4 -> the fused phase must stay near 0, not drift to 90°
	ph := math.Atan2(imag(e.H.At(0, 0)), real(e.H.At(0, 0)))
	if math.Abs(ph) > 0.08 {
		t.Errorf("fused phase %.4f rad, want close to 0", ph)
	}
	if len(e.FusedFrom) != 2 {
		t.Errorf("FusedFrom = %v", e.FusedFrom)
	}
}

func TestUnverifiedUsedUntilVerifiedExists(t *testing.T) {
	st, _ := NewState(UncertaintyWeighted)
	st.AddTrial(Estimate{JobID: "a", Speed: 1500, H: sampleH(1), Uncertainty: 0.1})
	if e, ok := st.EntryAt(1500); !ok || e.Verified {
		t.Fatalf("first estimate should be available unverified: %+v %v", e, ok)
	}
	st.AddTrial(Estimate{JobID: "b", Speed: 1500, H: sampleH(1), Uncertainty: 0.1, Verified: true})
	e, _ := st.EntryAt(1500)
	if !e.Verified {
		t.Fatal("with one verified estimate the fused entry must be verified")
	}
}

func TestFailedVerificationInvalidates(t *testing.T) {
	st, _ := NewState(Latest)
	st.AddTrial(Estimate{JobID: "a", Speed: 1500, H: sampleH(0.2), Uncertainty: 0.05, Verified: true})
	if _, ok := st.EntryAt(1500); !ok {
		t.Fatal("history should be available after trial")
	}
	// reuse job: the model predicts badly -> mismatch > 0.5 * original
	st.AddCheck(CheckVerdict{JobID: "reuse", Speed: 1500, MismatchRMS: 60, ResidualRMS: 50, OriginalRMS: 80})
	if _, ok := st.EntryAt(1500); ok {
		t.Fatal("history must be unavailable after a failed prediction check")
	}
}

func TestGoodCheckKeepsHistory(t *testing.T) {
	st, _ := NewState(Latest)
	st.AddTrial(Estimate{JobID: "a", Speed: 1500, H: sampleH(0.2), Uncertainty: 0.05, Verified: true})
	st.AddCheck(CheckVerdict{JobID: "reuse", Speed: 1500, MismatchRMS: 10, ResidualRMS: 8, OriginalRMS: 80})
	if _, ok := st.EntryAt(1500); !ok {
		t.Fatal("history should survive a good verification")
	}
}

func TestFreshTrialRehabilitates(t *testing.T) {
	st, _ := NewState(Latest)
	st.AddTrial(Estimate{JobID: "a", Speed: 1500, H: sampleH(0.2), Uncertainty: 0.05, Verified: true})
	st.AddCheck(CheckVerdict{JobID: "reuse", Speed: 1500, MismatchRMS: 60, ResidualRMS: 50, OriginalRMS: 80})
	if _, ok := st.EntryAt(1500); ok {
		t.Fatal("expected invalidation")
	}
	st.AddTrial(Estimate{JobID: "new", Speed: 1500, H: sampleH(0.3), Uncertainty: 0.05, Verified: true})
	if _, ok := st.EntryAt(1500); !ok {
		t.Fatal("fresh trial must rehabilitate the coefficients")
	}
}

func TestSlidingWindowDropsOldEstimates(t *testing.T) {
	st, _ := NewState(SlidingWeighted)
	for i := 0; i < Window+3; i++ {
		st.AddTrial(Estimate{JobID: "j" + string(rune('a'+i)), Speed: 1000, H: sampleH(0.1 * float64(i)), Uncertainty: 0.1, Verified: true})
	}
	e, ok := st.EntryAt(1000)
	if !ok {
		t.Fatal("unavailable")
	}
	if len(e.FusedFrom) != Window {
		t.Errorf("fused %d estimates, want window %d", len(e.FusedFrom), Window)
	}
}
