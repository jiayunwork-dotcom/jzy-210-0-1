package compute

import (
	"fmt"
	"math/cmplx"
	"time"

	"balancer/internal/domain"
	"balancer/pkg/history"
	"balancer/pkg/solve"
	"balancer/pkg/vec"
)

// EstimateSummary is what a finalized fitted job contributes to machine
// history (one per speed).
type EstimateSummary struct {
	Speed   int
	Est     history.Estimate
	TrustOK bool
}

func computeState(st *State, cfg domain.SnapshotConfig, pol history.Policy) {
	nPlanes, nPoints, nSpeeds := len(cfg.PlaneIDs), len(cfg.PointIDs), len(cfg.Speeds)

	var orig *Run
	if st.OriginalRunID != "" {
		for _, r := range st.Runs {
			if r.RunID == st.OriginalRunID {
				orig = r
			}
		}
	}

	// Runs carrying intentionally applied weights that participate in
	// fitting: trial and correction runs. Verify runs are held out.
	var fitRuns []*Run
	var verifyRun *Run
	for _, r := range st.Runs {
		switch r.Kind {
		case domain.RunTrial, domain.RunCorrection:
			fitRuns = append(fitRuns, r)
		case domain.RunVerify:
			verifyRun = r // runs are time ordered; last one wins
		}
	}

	// Fitted coefficients, one matrix per speed.
	for si, speed := range cfg.Speeds {
		fit := &Fit{Speed: speed}
		switch {
		case orig != nil && len(fitRuns) >= nPlanes:
			W := make([][]complex128, len(fitRuns))
			V := make([][]complex128, len(fitRuns))
			for r, run := range fitRuns {
				W[r] = append([]complex128(nil), run.Weights...)
				V[r] = append([]complex128(nil), run.Readings[si]...)
			}
			fr, err := solve.FitInfluence(W, V, orig.Readings[si], nil, DefaultCondLimit)
			if err != nil {
				fit.Err = err.Error()
			} else {
				fillFit(fit, cfg, fr)
			}
		case len(st.HistoryEntry) > 0:
			if e := st.HistoryEntry[speed]; e != nil && e.Status == history.StatusActive {
				fillFitFromHistory(fit, cfg, speed, e)
				st.HistoryUsed = true
			} else {
				fit.Err = "no active stored influence coefficients for this speed; a trial run is required"
			}
		}
		st.Fits[speed] = fit
	}

	if orig == nil {
		return // nothing computable until the original run exists
	}

	// Per-speed correction plans.
	for _, speed := range cfg.Speeds {
		fit := st.Fits[speed]
		plan := &SpeedPlan{Speed: speed}
		if fit.Err != "" || fit.Re == nil {
			plan.Err = missingOrErr(fit, len(fitRuns), nPlanes)
		} else {
			A := fitMatrix(fit, nPoints, nPlanes)
			si := indexOfInt(cfg.Speeds, speed)
			cor, err := solve.SolveCorrection(A, orig.Readings[si], nil, DefaultCondLimit)
			if err != nil {
				plan.Err = err.Error()
			} else {
				fillPlan(cfg, plan, A, orig.Readings[si], cor)
			}
		}
		st.Plans[speed] = plan
	}

	// Multi-speed combined plan (all point×speed conditions stacked).
	if nSpeeds > 1 && coefficientsReady(st, cfg.Speeds) {
		Aall := solve.NewCMatrix(nPoints*nSpeeds, nPlanes)
		vall := make([]complex128, nPoints*nSpeeds)
		for si, speed := range cfg.Speeds {
			fit := st.Fits[speed]
			for p := 0; p < nPoints; p++ {
				for a := 0; a < nPlanes; a++ {
					Aall.Set(si*nPoints+p, a, complex(fit.Re[p][a], fit.Im[p][a]))
				}
				vall[si*nPoints+p] = orig.Readings[si][p]
			}
		}
		plan := &SpeedPlan{Speed: 0}
		cor, err := solve.SolveCorrection(Aall, vall, nil, DefaultCondLimit)
		if err != nil {
			plan.Err = err.Error()
		} else {
			fillPlan(cfg, plan, Aall, vall, cor)
		}
		st.MultiSpeed = plan
	}

	// Trim plan: the latest weighted run shows what the rotor currently
	// carries; add the incremental weights that zero the residual.
	if latest := latestWeightedRun(st.Runs); latest != nil && coefficientsReady(st, cfg.Speeds) {
		st.Trim = buildTrim(st, cfg, latest)
	}

	// Trust check against the latest verification run.
	if verifyRun != nil {
		evaluateTrust(st, cfg, orig, verifyRun, pol)
	}
}

func missingOrErr(fit *Fit, nFitRuns, nPlanes int) string {
	if fit.Err != "" {
		return fit.Err
	}
	if nFitRuns < nPlanes {
		return fmt.Sprintf("need %d independent trial/correction run(s) for %d plane(s), have %d",
			nPlanes, nPlanes, nFitRuns)
	}
	return "no influence coefficients available"
}

func allocFit(fit *Fit, cfg domain.SnapshotConfig) {
	nPlanes, nPoints := len(cfg.PlaneIDs), len(cfg.PointIDs)
	fit.Re = make([][]float64, nPoints)
	fit.Im = make([][]float64, nPoints)
	fit.Uncert = make([][]float64, nPoints)
	fit.ExtA = make([][]vec.Polar, nPoints)
	fit.ExtSigma = make([][]float64, nPoints)
	for p := 0; p < nPoints; p++ {
		fit.Re[p] = make([]float64, nPlanes)
		fit.Im[p] = make([]float64, nPlanes)
		fit.Uncert[p] = make([]float64, nPlanes)
		fit.ExtA[p] = make([]vec.Polar, nPlanes)
		fit.ExtSigma[p] = make([]float64, nPlanes)
	}
}

func fillFit(fit *Fit, cfg domain.SnapshotConfig, fr *solve.FitResult) {
	allocFit(fit, cfg)
	nPlanes := len(cfg.PlaneIDs)
	fit.FromRun = true
	fit.FitRSS = fr.Sigma2
	fit.Log10Cond = fr.Log10Cond
	fit.Rank = fr.Rank
	for p := 0; p < fr.A.Rows; p++ {
		for a := 0; a < nPlanes; a++ {
			z := fr.A.At(p, a)
			fit.Re[p][a] = real(z)
			fit.Im[p][a] = imag(z)
			fit.Uncert[p][a] = fr.Uncert[p][a]
			fit.ExtA[p][a] = cfg.Convention.ExternalPolar(vec.FromComplex(z))
			fit.ExtSigma[p][a] = fr.Uncert[p][a]
		}
	}
}

func fillFitFromHistory(fit *Fit, cfg domain.SnapshotConfig, speed int, e *history.Entry) {
	allocFit(fit, cfg)
	nPlanes := len(cfg.PlaneIDs)
	for p := 0; p < len(cfg.PointIDs); p++ {
		for a := 0; a < nPlanes; a++ {
			k := p*nPlanes + a
			z := complex(e.Re[k], e.Im[k])
			fit.Re[p][a] = real(z)
			fit.Im[p][a] = imag(z)
			fit.Uncert[p][a] = e.Uncert[k]
			fit.ExtA[p][a] = cfg.Convention.ExternalPolar(vec.FromComplex(z))
			fit.ExtSigma[p][a] = e.Uncert[k]
		}
	}
	fit.Log10Cond = 0
	fit.Rank = nPlanes
}

func fitMatrix(fit *Fit, nPoints, nPlanes int) *solve.CMatrix {
	A := solve.NewCMatrix(nPoints, nPlanes)
	for p := 0; p < nPoints; p++ {
		for a := 0; a < nPlanes; a++ {
			A.Set(p, a, complex(fit.Re[p][a], fit.Im[p][a]))
		}
	}
	return A
}

func fillPlan(cfg domain.SnapshotConfig, plan *SpeedPlan, A *solve.CMatrix, v0 []complex128, cor *solve.Correction) {
	nPlanes, nPoints := A.Cols, A.Rows
	plan.Weights = make([]vec.Polar, nPlanes)
	plan.Residual = make([]vec.Polar, nPoints)
	plan.ResidualAmp = make([]float64, nPoints)
	plan.Holes = make([][]HoleSplit, nPlanes)
	for a := 0; a < nPlanes; a++ {
		wp := cfg.Convention.ExternalPolar(vec.FromComplex(cor.Weights[a]))
		plan.Weights[a] = wp
		if a < len(cfg.PlaneHoles) && cfg.PlaneHoles[a] > 0 {
			plan.Holes[a] = splitWeight(wp, cfg.PlaneHoles[a])
		}
	}
	for p := 0; p < nPoints; p++ {
		rp := cfg.Convention.ExternalPolar(vec.FromComplex(cor.Residual[p]))
		plan.Residual[p] = rp
		plan.ResidualAmp[p] = cmplx.Abs(cor.Residual[p])
	}
}

func latestWeightedRun(runs []*Run) *Run {
	var latest *Run
	for _, r := range runs {
		if r.Kind == domain.RunTrial || r.Kind == domain.RunCorrection || r.Kind == domain.RunVerify {
			latest = r // runs already ordered by RunAt
		}
	}
	return latest
}

func coefficientsReady(st *State, speeds []int) bool {
	for _, s := range speeds {
		f := st.Fits[s]
		if f == nil || f.Re == nil || f.Err != "" {
			return false
		}
	}
	return true
}

func buildTrim(st *State, cfg domain.SnapshotConfig, latest *Run) *TrimPlan {
	nPlanes, nPoints, nSpeeds := len(cfg.PlaneIDs), len(cfg.PointIDs), len(cfg.Speeds)
	tp := &TrimPlan{BasedOnRun: latest.RunID, Kind: latest.Kind}

	rows := nPoints * nSpeeds
	Aall := solve.NewCMatrix(rows, nPlanes)
	ball := make([]complex128, rows)
	orig := map[int][]complex128{}
	for _, r := range st.Runs {
		if r.RunID == st.OriginalRunID {
			for si := 0; si < nSpeeds; si++ {
				orig[si] = r.Readings[si]
			}
		}
	}
	for si, speed := range cfg.Speeds {
		fit := st.Fits[speed]
		for p := 0; p < nPoints; p++ {
			for a := 0; a < nPlanes; a++ {
				Aall.Set(si*nPoints+p, a, complex(fit.Re[p][a], fit.Im[p][a]))
			}
			vcur := orig[si][p]
			for a := 0; a < nPlanes; a++ {
				vcur += Aall.At(si*nPoints+p, a) * latest.Weights[a]
			}
			ball[si*nPoints+p] = vcur
		}
	}
	neg := solve.NewCMatrix(rows, 1)
	for i := range ball {
		neg.Set(i, 0, -ball[i])
	}
	ls, err := solve.WeightedLeastSquares(Aall, neg, nil, DefaultCondLimit)
	if err != nil {
		tp.Err = err.Error()
		return tp
	}
	add := make([]complex128, nPlanes)
	for a := 0; a < nPlanes; a++ {
		add[a] = ls.X.At(a, 0)
	}
	tp.AddWeights = make([]vec.Polar, nPlanes)
	tp.Installed = make([]vec.Polar, nPlanes)
	tp.Residual = make([]vec.Polar, rows)
	tp.Holes = make([][]HoleSplit, nPlanes)
	for a := 0; a < nPlanes; a++ {
		ap := cfg.Convention.ExternalPolar(vec.FromComplex(add[a]))
		tp.AddWeights[a] = ap
		tp.Installed[a] = cfg.Convention.ExternalPolar(vec.FromComplex(latest.Weights[a] + add[a]))
		if a < len(cfg.PlaneHoles) && cfg.PlaneHoles[a] > 0 {
			tp.Holes[a] = splitWeight(tp.AddWeights[a], cfg.PlaneHoles[a])
		}
	}
	for i := 0; i < rows; i++ {
		tp.Residual[i] = cfg.Convention.ExternalPolar(vec.FromComplex(ls.Residual.At(i, 0)))
	}
	return tp
}

func evaluateTrust(st *State, cfg domain.SnapshotConfig, orig, verify *Run, pol history.Policy) {
	nPoints := len(cfg.PointIDs)
	nPlanes := len(cfg.PlaneIDs)
	st.TrustBySpeed = map[int]*history.TrustReport{}
	overallOK := true
	for si, speed := range cfg.Speeds {
		fit := st.Fits[speed]
		if fit == nil || fit.Re == nil {
			overallOK = false
			continue
		}
		v0 := make([]complex128, nPoints)
		vm := make([]complex128, nPoints)
		vp := make([]complex128, nPoints)
		for p := 0; p < nPoints; p++ {
			v0[p] = orig.Readings[si][p]
			vm[p] = verify.Readings[si][p]
			pred := v0[p]
			for a := 0; a < nPlanes; a++ {
				pred += complex(fit.Re[p][a], fit.Im[p][a]) * verify.Weights[a]
			}
			vp[p] = pred
		}
		rep := history.CheckTrust(append([]string(nil), cfg.PointIDs...), v0, vm, vp, pol)
		st.TrustBySpeed[speed] = &rep
		if !rep.OK {
			overallOK = false
		}
	}
	primary := st.TrustBySpeed[cfg.Speeds[0]]
	st.Trust = primary
	st.TrustOK = overallOK
	st.TrustChecked = true
	// A failed history-based verification invalidates the stored coefficients:
	// they become suspect and must be re-established by trial runs.
	if !overallOK && st.HistoryUsed {
		for _, e := range st.HistoryEntry {
			if e != nil && e.Status == history.StatusActive {
				e.InflateAfterFailure()
			}
		}
	}
}

// Estimates returns the per-speed coefficient estimates a finalized fitted
// job contributes. History-only jobs contribute nothing.
func (st *State) Estimates(at time.Time) []EstimateSummary {
	if !anyFittedFromRuns(st) {
		return nil
	}
	var out []EstimateSummary
	nPlanes := len(st.Job.PlaneIDs)
	nPoints := len(st.Job.PointIDs)
	for _, speed := range st.Job.Speeds {
		fit := st.Fits[speed]
		if fit == nil || !fit.FromRun {
			continue
		}
		est := history.Estimate{
			MachineID: st.Job.MachineID, Speed: speed,
			PlaneIDs: append([]string(nil), st.Job.PlaneIDs...),
			PointIDs: append([]string(nil), st.Job.PointIDs...),
			Re:       make([]float64, nPlanes*nPoints),
			Im:       make([]float64, nPlanes*nPoints),
			Uncert:   make([]float64, nPlanes*nPoints),
			At:       at,
		}
		for p := 0; p < nPoints; p++ {
			for a := 0; a < nPlanes; a++ {
				k := p*nPlanes + a
				est.Re[k] = fit.Re[p][a]
				est.Im[k] = fit.Im[p][a]
				est.Uncert[k] = fit.Uncert[p][a]
			}
		}
		out = append(out, EstimateSummary{Speed: speed, Est: est, TrustOK: !st.TrustChecked || st.TrustOK})
	}
	return out
}

func anyFittedFromRuns(st *State) bool {
	for _, f := range st.Fits {
		if f.FromRun {
			return true
		}
	}
	return false
}
