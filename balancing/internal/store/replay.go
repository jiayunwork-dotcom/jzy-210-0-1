package store

import (
	"math"
	"math/cmplx"
	"sort"
	"time"

	"balancing/internal/balance"
	"balancing/internal/split"
	"balancing/internal/vec"
)

// replay derives the complete state of one job from its event stream.
//
// Ordering rule for run-bearing events: run time first (so two technicians
// submitting concurrently are interleaved by the actual rotor-run order), then
// per-job submission sequence as a tiebreaker, with corrections landing just
// after the run they amend. The job-created event always sorts first.
func replay(events []*Event) (*JobState, error) {
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	sorted := make([]*Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Type == EvJobCreated || b.Type == EvJobCreated {
			return a.Type == EvJobCreated
		}
		if !a.RunTime.Equal(b.RunTime) {
			return a.RunTime.Before(b.RunTime)
		}
		return a.Seq < b.Seq
	})

	st := &JobState{}
	// key runs by (run time, submitting seq): two technicians can submit
	// runs carrying the same run time, and both must be retained.
	type runKey struct {
		t   time.Time
		seq int64
	}
	byKey := map[runKey]*Run{}
	var order []runKey

	for _, ev := range sorted {
		p, err := decodeEvent(ev)
		if err != nil {
			return nil, err
		}
		switch q := p.(type) {
		case *jobCreatedPayload:
			m := q.Machine
			j := q.Job
			st.Job = &j
			st.Machine = &m
		case *runRecordedPayload:
			r := runFromInput(st.Machine, q.Input, ev.Seq)
			k := runKey{r.RunTime, ev.Seq}
			byKey[k] = r
			order = append(order, k)
		case *runCorrectedPayload:
			var target *Run
			if q.Input.TargetSeq != nil {
				target = byKey[runKey{q.Input.TargetRunTime.UTC(), *q.Input.TargetSeq}]
			} else {
				// unique run at that time
				var matches []runKey
				for k := range byKey {
					if k.t.Equal(q.Input.TargetRunTime.UTC()) {
						matches = append(matches, k)
					}
				}
				switch len(matches) {
				case 1:
					target = byKey[matches[0]]
				case 0:
					return nil, ErrCorrectionMissingTarget
				default:
					return nil, ErrCorrectionAmbiguous
				}
			}
			if target == nil {
				return nil, ErrCorrectionMissingTarget
			}
			applyCorrection(st.Machine, target, q.Input)
			target.Corrected = true
		}
	}

	for _, k := range order {
		st.Runs = append(st.Runs, byKey[k])
	}
	st.Result = compute(st.Machine, st.Job, st.Runs)
	st.Events = sorted
	return st, nil
}

// runFromInput converts an external run into the internal-frame Run.
func runFromInput(m *Machine, in RunInput, seq int64) *Run {
	conv := m.Convention()
	r := &Run{
		Kind:       in.Kind,
		RunTime:    in.RunTime.UTC(),
		Operator:   in.Operator,
		Note:       in.Note,
		Readings:   map[int]map[int]complex128{},
		Weights:    map[int]complex128{},
		TrialPlane: -1,
		Seq:        seq,
	}
	for _, rd := range in.Readings {
		internal := vec.Polar{Amp: rd.Amp, Phase: conv.ToInternal(rd.Phase)}.C()
		if r.Readings[rd.Speed] == nil {
			r.Readings[rd.Speed] = map[int]complex128{}
		}
		r.Readings[rd.Speed][rd.Point] = internal
	}
	for _, w := range in.Weights {
		ang := conv.ToInternal(w.Angle)
		rad := ang * math.Pi / 180
		r.Weights[w.Plane] = complex(w.Grams*math.Cos(rad), w.Grams*math.Sin(rad))
	}
	if in.Kind == RunTrial {
		if in.TrialPlane != nil {
			r.TrialPlane = *in.TrialPlane
			r.HasTrial = true
		}
		if in.TrialWeight != nil {
			ang := conv.ToInternal(in.TrialWeight.Angle)
			rad := ang * math.Pi / 180
			r.TrialWeight = complex(in.TrialWeight.Grams*math.Cos(rad), in.TrialWeight.Grams*math.Sin(rad))
		}
	}
	return r
}

func applyCorrection(m *Machine, r *Run, in CorrectionInput) {
	conv := m.Convention()
	if in.Operator != "" {
		r.Operator = in.Operator
	}
	if in.Note != "" {
		r.Note = in.Note
	}
	if in.Readings != nil {
		// Replace the full reading set — the correction is a restatement.
		r.Readings = map[int]map[int]complex128{}
		for _, rd := range in.Readings {
			internal := vec.Polar{Amp: rd.Amp, Phase: conv.ToInternal(rd.Phase)}.C()
			if r.Readings[rd.Speed] == nil {
				r.Readings[rd.Speed] = map[int]complex128{}
			}
			r.Readings[rd.Speed][rd.Point] = internal
		}
	}
	if in.Weights != nil {
		r.Weights = map[int]complex128{}
		for _, w := range in.Weights {
			ang := conv.ToInternal(w.Angle)
			rad := ang * math.Pi / 180
			r.Weights[w.Plane] = complex(w.Grams*math.Cos(rad), w.Grams*math.Sin(rad))
		}
	}
	if in.TrialPlane != nil {
		r.TrialPlane = *in.TrialPlane
		r.HasTrial = true
	}
	if in.TrialWeight != nil {
		ang := conv.ToInternal(in.TrialWeight.Angle)
		rad := ang * math.Pi / 180
		r.TrialWeight = complex(in.TrialWeight.Grams*math.Cos(rad), in.TrialWeight.Grams*math.Sin(rad))
	}
}

// ---- derived computation ----

// compute evaluates every configured speed independently.
func compute(m *Machine, j *Job, runs []*Run) *JobResult {
	res := &JobResult{}
	var orig *Run
	for _, r := range runs {
		if r.Kind == RunOriginal {
			orig = r
			break
		}
	}
	for _, speed := range m.Speeds {
		sr := &SpeedResult{Speed: speed}
		if orig == nil {
			sr.Status = "insufficient_data"
			sr.Error = &FieldError{Field: "runs", Code: "missing_original", Message: "no original run recorded yet"}
			res.Speeds = append(res.Speeds, sr)
			continue
		}
		ov, ok := originalVector(m, orig, speed)
		if !ok {
			sr.Status = "insufficient_data"
			sr.Error = &FieldError{Field: "readings", Code: "incomplete_original", Message: "original run lacks readings for every point at this speed"}
			res.Speeds = append(res.Speeds, sr)
			continue
		}
		sr.Original = externalPolars(m, ov)
		sr.OriginalRMS = balance.RMS(ov)

		// runs usable for fitting / held-out verification, chronological
		var fit, verify []*Run
		reuse := j != nil && j.UseHistory
		for _, r := range runs {
			if r == orig || !runCompleteAt(m, r, speed) {
				continue
			}
			switch {
			case reuse:
				// A reuse job performs no trials: every later run checks the
				// coefficients snapshotted when the job was opened.
				verify = append(verify, r)
			case r.Kind == RunVerification:
				verify = append(verify, r)
			default:
				fit = append(fit, r)
			}
		}

		var h balance.Matrix

		if reuse {
			ext, ok := j.HistoryInfluence[speed]
			if !ok {
				sr.Status = "insufficient_data"
				sr.Error = &FieldError{Field: "history", Code: "no_history_at_speed", Message: "the job was started from history coefficients but none were snapshotted for this speed"}
				res.Speeds = append(res.Speeds, sr)
				continue
			}
			h = matrixFromExternal(m, ext)
			sr.Status = "ok"
			sr.Influence = ext
			sr.HistoryEstimate = false
		} else {
			// trial signal/noise checks (only runs explicitly trialling a plane)
			for _, r := range fit {
				if r.Kind != RunTrial || !r.HasTrial {
					continue
				}
				rv := readVector(m, r, speed)
				eff := balance.Effect(ov, rv)
				snr := balance.RMS(eff) / noiseFloor(ov)
				tc := TrialCheck{
					RunTime:   r.RunTime,
					Plane:     r.TrialPlane,
					EffectRMS: balance.RMS(eff),
					SNR:       snr,
					OK:        snr >= balance.MinSignalToNoise,
				}
				sr.TrialChecks = append(sr.TrialChecks, tc)
			}

			// Need at least one fit run per plane to identify H.
			covered := map[int]bool{}
			for _, r := range fit {
				if r.Kind == RunTrial && r.HasTrial {
					covered[r.TrialPlane] = true
				}
				// correction runs usually touch all planes
				for p := 0; p < m.PlaneCount; p++ {
					if cmplx.Abs(r.Weights[p]) > 0 {
						covered[p] = true
					}
				}
			}
			enough := len(fit) >= m.PlaneCount
			for p := 0; p < m.PlaneCount; p++ {
				if !covered[p] {
					enough = false
				}
			}
			if !enough {
				sr.Status = "insufficient_data"
				sr.Error = &FieldError{Field: "runs", Code: "need_trial_runs", Message: "record one trial run with adequate effect for every correction plane"}
				res.Speeds = append(res.Speeds, sr)
				continue
			}

			// weak trial effects: reject rather than return a garbage coefficient
			var weak *TrialCheck
			for i := range sr.TrialChecks {
				if !sr.TrialChecks[i].OK {
					weak = &sr.TrialChecks[i]
				}
			}
			if weak != nil {
				sr.Status = "ill_conditioned"
				sr.Error = &FieldError{
					Field:   "runs",
					Code:    "small_trial_effect",
					Message: "trial-run vibration change is below 3x the noise floor; trial weight too small or readings/phase entered incorrectly",
				}
				res.Speeds = append(res.Speeds, sr)
				continue
			}

			loaded := make([]balance.LoadedRun, 0, len(fit))
			for _, r := range fit {
				ws := make([]complex128, m.PlaneCount)
				for p := 0; p < m.PlaneCount; p++ {
					ws[p] = r.Weights[p]
				}
				loaded = append(loaded, balance.LoadedRun{
					Planes: m.PlaneCount,
					W:      ws,
					V:      readVector(m, r, speed),
				})
			}
			hh, rcond, err := balance.EstimateInfluence(ov, loaded, m.PointWeights)
			if err != nil || rcond < balance.MinRcond {
				sr.Status = "ill_conditioned"
				sr.Error = &FieldError{Field: "runs", Code: "ill_conditioned", Message: "influence matrix is singular/collinear: trial weights mounted in nearly identical directions"}
				sr.Rcond = rcond
				res.Speeds = append(res.Speeds, sr)
				continue
			}
			h = hh
			sr.Status = "ok"
			sr.Rcond = rcond
			sr.Influence = matrixToExternal(m, h)
			sr.Uncertainty = estimateUncertainty(h, ov, loaded)
			sr.HistoryEstimate = trialledEveryPlane(m, fit)
		}

		w, residual, _, cerr := balance.Correct(h, ov, m.PointWeights)
		if cerr == nil {
			sr.Correction = weightsToExternal(m, w)
			sr.PredictedResidual = externalPolars(m, residual)
			sr.ResidualRMS = balance.RMS(residual)
			plans, perr := split.Plans(m.Convention(), m.HoleLayout, w)
			if perr == nil {
				sr.HolePlans = plans
			} else {
				sr.Error = &FieldError{Field: "hole_layout", Code: "split_failed", Message: perr.Error()}
			}
		}

		// held-out verification: predict each verification run from the
		// pre-verification model h, using the weights actually mounted.
		if len(verify) > 0 {
			var misSq, resSq float64
			n := 0
			for _, r := range verify {
				rv := readVector(m, r, speed)
				ws := make([]complex128, m.PlaneCount)
				for p := 0; p < m.PlaneCount; p++ {
					ws[p] = r.Weights[p]
				}
				pred := balance.Predict(h, ov, ws)
				mis := balance.Effect(pred, rv)
				misSq += dot(mis, mis)
				resSq += dot(rv, rv)
				n += len(rv)
			}
			resRMS := math.Sqrt(resSq / float64(n))
			misRMS := math.Sqrt(misSq / float64(n))
			red := 0.0
			if sr.OriginalRMS > 0 {
				red = 1 - resRMS/sr.OriginalRMS
			}
			sr.Verification = &Verification{
				MismatchRMS:   misRMS,
				ResidualRMS:   resRMS,
				OriginalRMS:   sr.OriginalRMS,
				Reduction:     red,
				MismatchRatio: ratio(misRMS, sr.OriginalRMS),
				ModelAccepted: misRMS <= historyFailRatio*sr.OriginalRMS,
			}
		}

		res.Speeds = append(res.Speeds, sr)
	}
	return res
}

const historyFailRatio = 0.5

func dot(a, b []complex128) float64 {
	s := 0.0
	for i := range a {
		s += real(a[i])*real(b[i]) + imag(a[i])*imag(b[i])
	}
	return s
}

func ratio(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// noiseFloor returns the assumed measurement scatter: at least the configured
// constant, but never below 2% of the original signal itself.
func noiseFloor(ov []complex128) float64 {
	return math.Max(balance.NoiseFloorUM, 0.02*balance.RMS(ov))
}

// estimateUncertainty returns a relative standard error for H: the residual of
// the least-squares fit (in μm) divided by the RMS weight-excited response
// (also μm), floored at 5%. With exact synthetic data this is ~0; with field
// data it reflects how linear/repeatable the machine was.
func estimateUncertainty(h balance.Matrix, ov []complex128, runs []balance.LoadedRun) float64 {
	var resSq, sigSq float64
	for _, r := range runs {
		pred := balance.Predict(h, ov, r.W)
		for i := range pred {
			res := r.V[i] - pred[i]
			resSq += real(res)*real(res) + imag(res)*imag(res)
			d := r.V[i] - ov[i]
			sigSq += real(d)*real(d) + imag(d)*imag(d)
		}
	}
	if sigSq == 0 {
		return 1
	}
	return math.Max(0.05, math.Sqrt(resSq/sigSq))
}

func trialledEveryPlane(m *Machine, fit []*Run) bool {
	seen := map[int]bool{}
	for _, r := range fit {
		if r.Kind == RunTrial && r.HasTrial {
			seen[r.TrialPlane] = true
		}
	}
	for p := 0; p < m.PlaneCount; p++ {
		if !seen[p] {
			return false
		}
	}
	return true
}

func originalVector(m *Machine, r *Run, speed int) ([]complex128, bool) {
	rd, ok := r.Readings[speed]
	if !ok {
		return nil, false
	}
	out := make([]complex128, m.PointCount)
	for p := 0; p < m.PointCount; p++ {
		v, ok := rd[p]
		if !ok {
			return nil, false
		}
		out[p] = v
	}
	return out, true
}

func readVector(m *Machine, r *Run, speed int) []complex128 {
	out := make([]complex128, m.PointCount)
	if rd := r.Readings[speed]; rd != nil {
		for p := 0; p < m.PointCount; p++ {
			out[p] = rd[p]
		}
	}
	return out
}

func runCompleteAt(m *Machine, r *Run, speed int) bool {
	rd, ok := r.Readings[speed]
	if !ok {
		return false
	}
	for p := 0; p < m.PointCount; p++ {
		if _, ok := rd[p]; !ok {
			return false
		}
	}
	return true
}

func externalPolars(m *Machine, v []complex128) []vec.Polar {
	out := make([]vec.Polar, len(v))
	for i, z := range v {
		p := vec.FromC(z)
		p.Phase = m.Convention().ToExternal(p.Phase)
		out[i] = p
	}
	return out
}

func weightsToExternal(m *Machine, w []complex128) []vec.Polar {
	out := make([]vec.Polar, len(w))
	for i, z := range w {
		p := vec.FromC(z)
		p.Phase = m.Convention().ToExternal(p.Phase)
		out[i] = p
	}
	return out
}

func matrixToExternal(m *Machine, h balance.Matrix) [][]vec.Polar {
	// An influence coefficient transforms a weight vector into a vibration
	// change. A keyphasor reference offset cancels between the two frames;
	// only the angle growth direction matters. With external angles growing
	// with rotation the complex representation is mirrored, i.e. the
	// coefficient phase is negated.
	out := make([][]vec.Polar, h.R)
	for i := 0; i < h.R; i++ {
		out[i] = make([]vec.Polar, h.C)
		for j := 0; j < h.C; j++ {
			p := vec.FromC(h.At(i, j))
			if m.Direction == vec.WithRotation {
				p.Phase = vec.Norm(-p.Phase)
			}
			out[i][j] = p
		}
	}
	return out
}
