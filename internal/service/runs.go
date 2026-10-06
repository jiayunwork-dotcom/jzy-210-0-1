package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"balancer/internal/compute"
	"balancer/internal/domain"
	"balancer/internal/store"
	"balancer/pkg/history"
)

// AddRunResult is returned after accepting (or idempotently replaying) a run.
type AddRunResult struct {
	RunID  string         `json:"runId"`
	Seq    int64          `json:"seq"`
	Dedupe bool           `json:"deduplicated"`
	State  *compute.State `json:"state"`
}

// AddRun validates, appends a run_added event, recomputes the job and, when
// the new run changes the machine's fitted history, rebuilds stored
// coefficients. The whole operation is one transaction; the job row is locked
// first so two technicians submitting concurrently serialize cleanly and
// both records are kept (ordered by runAt at replay).
func (s *Service) AddRun(ctx context.Context, jobID string, in *domain.RunInput) (*AddRunResult, error) {
	var res *AddRunResult
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		jrec, err := tx.GetJob(ctx, jobID, true)
		if err != nil {
			return mapNotFound(err, "job")
		}
		job := jobFromRecord(jrec)
		cfg := job.Snapshot()

		// Idempotency: same clientId returns the original accepted run.
		if in.ClientID != "" {
			if existing, ferr := tx.FindEventByClientID(ctx, jobID, in.ClientID); ferr == nil {
				events, _ := tx.ListEvents(ctx, jobID)
				current, _ := s.historyForReplay(ctx, tx, job)
				st, rerr := compute.Replay(job, toDomainEvents(events), current)
				if rerr != nil {
					return rerr
				}
				var runID string
				if len(existing.RunID) > 0 {
					runID = existing.RunID
				}
				res = &AddRunResult{RunID: runID, Seq: existing.Seq, Dedupe: true, State: st}
				return nil
			} else {
				var nf *store.ErrNotFound
				if !errors.As(ferr, &nf) {
					return ferr
				}
			}
		}

		if err := domain.ValidateRunInput(cfg, in); err != nil {
			return asRejection(err)
		}
		if err := domain.CheckCoverage(cfg, in.Readings); err != nil {
			return asRejection(err)
		}

		// Existing projected state is needed for cross-run checks.
		events, err := tx.ListEvents(ctx, jobID)
		if err != nil {
			return err
		}
		current, err := s.historyForReplay(ctx, tx, job)
		if err != nil {
			return err
		}
		prev, err := compute.Replay(job, toDomainEvents(events), current)
		if err != nil {
			return err
		}
		if err := s.checkRunRules(cfg, prev, in); err != nil {
			return err
		}

		runID := newID("run")
		now := s.Now()
		payload, _ := json.Marshal(domain.RunAddedEvent{
			RunID: runID, ClientID: in.ClientID, Input: *in, At: now,
		})
		rec, err := tx.AppendEvent(ctx, &store.EventRecord{
			JobID: jobID, Type: domain.EvtRunAdded, RunID: runID,
			ClientID: in.ClientID, Payload: payload, OccurredAt: now,
		})
		if err != nil {
			var dup *store.ErrDuplicateClientID
			if errors.As(err, &dup) {
				return reject(409, "run with this clientId already exists",
					domain.FieldError{Field: "clientId", Reason: dup.Error()})
			}
			return err
		}

		job.UpdatedAt = now
		if err := tx.SaveJob(ctx, jobRecord(job)); err != nil {
			return err
		}

		events, err = tx.ListEvents(ctx, jobID)
		if err != nil {
			return err
		}
		st, err := compute.Replay(job, toDomainEvents(events), current)
		if err != nil {
			return err
		}
		if err := s.rebuildMachineHistory(ctx, tx, job.MachineID); err != nil {
			return err
		}
		res = &AddRunResult{RunID: runID, Seq: rec.Seq, State: st}
		return nil
	})
	return res, err
}

// checkRunRules enforces cross-run constraints that single-field validation
// cannot: exactly one original run first, and the trial-vs-original effect
// size screen (ill-conditioning rejection with an explicit criterion).
func (s *Service) checkRunRules(cfg domain.SnapshotConfig, prev *compute.State, in *domain.RunInput) error {
	hasOriginal := prev.OriginalRunID != ""
	switch in.Kind {
	case domain.RunOriginal:
		if hasOriginal {
			return reject(409, "job already has an original run; correct it instead of adding another",
				domain.FieldError{Field: "kind", Reason: "duplicate original run"})
		}
	case domain.RunTrial, domain.RunVerify, domain.RunCorrection:
		if !hasOriginal {
			return reject(409, "the original run must be recorded first",
				domain.FieldError{Field: "kind", Reason: "no original run in this job"})
		}
	}
	if in.Kind != domain.RunTrial {
		return nil
	}
	orig := findRun(prev, prev.OriginalRunID)
	if orig == nil {
		return nil
	}
	// Project the candidate run purely for the effect-size comparison.
	candidate := compute.ProjectForCheck(cfg, in)
	floor := cfg.ReadingChangeFloorUm
	if floor <= 0 {
		floor = DefaultFloorUm
	}
	rel := cfg.ReadingChangeRel
	if rel <= 0 {
		rel = DefaultRel
	}
	if speed, maxRel, bad := compute.TrialChangeTooSmall(orig, candidate, cfg.Speeds, floor, rel); bad {
		return reject(422,
			"trial run rejected: effect on vibration is too small to derive a reliable influence coefficient",
			domain.FieldError{
				Field:  "readings",
				Reason: fmt.Sprintf("at %d rpm no point changed by at least max(%g µm, %g%% of amplitude); largest relative change %.2f%% — use a larger/differently placed trial weight", speed, floor, rel*100, maxRel*100),
			})
	}
	return nil
}

func findRun(st *compute.State, id string) *compute.Run {
	for _, r := range st.Runs {
		if r.RunID == id {
			return r
		}
	}
	return nil
}

func mapNotFound(err error, what string) error {
	var nf *store.ErrNotFound
	if errors.As(err, &nf) {
		return reject(404, what+" not found", domain.FieldError{Field: what + "Id", Reason: nf.Error()})
	}
	return err
}

// CorrectRun appends a run_corrected event. All derived results (fit,
// recommended weights, hole split) and the machine's stored coefficients are
// rebuilt from the event stream, so the outcome is identical to having
// entered the run correctly initially.
func (s *Service) CorrectRun(ctx context.Context, jobID, runID string, fix domain.RunFix) error {
	return s.Store.WithTx(ctx, func(tx store.Tx) error {
		jrec, err := tx.GetJob(ctx, jobID, true)
		if err != nil {
			return mapNotFound(err, "job")
		}
		job := jobFromRecord(jrec)
		cfg := job.Snapshot()

		events, err := tx.ListEvents(ctx, jobID)
		if err != nil {
			return err
		}
		current, err := s.historyForReplay(ctx, tx, job)
		if err != nil {
			return err
		}
		prev, err := compute.Replay(job, toDomainEvents(events), current)
		if err != nil {
			return err
		}
		target := findRun(prev, runID)
		if target == nil {
			return reject(404, "run not found", domain.FieldError{Field: "runId", Reason: runID})
		}

		// Build the prospective corrected run and validate it the same way a
		// new submission would be validated.
		merged := mergeFix(target.Kind, target.ExtWeights, target.ExtReadings, target.RunAt, target.Note, fix)
		if err := domain.ValidateRunInput(cfg, &merged); err != nil {
			return asRejection(err)
		}
		if err := domain.CheckCoverage(cfg, merged.Readings); err != nil {
			return asRejection(err)
		}
		if target.Kind == domain.RunTrial && prev.OriginalRunID != "" {
			orig := findRun(prev, prev.OriginalRunID)
			candidate := compute.ProjectForCheck(cfg, &merged)
			floor := cfg.ReadingChangeFloorUm
			if floor <= 0 {
				floor = DefaultFloorUm
			}
			rel := cfg.ReadingChangeRel
			if rel <= 0 {
				rel = DefaultRel
			}
			if speed, maxRel, bad := compute.TrialChangeTooSmall(orig, candidate, cfg.Speeds, floor, rel); bad {
				return reject(422, "corrected trial run still ill-conditioned",
					domain.FieldError{
						Field: "readings",
						Reason: fmt.Sprintf("at %d rpm effect below max(%g µm, %g%%); largest relative change %.2f%%",
							speed, floor, rel*100, maxRel*100),
					})
			}
		}

		now := s.Now()
		payload, _ := json.Marshal(domain.RunCorrectedEvent{RunID: runID, Correction: fix, At: now})
		if _, err := tx.AppendEvent(ctx, &store.EventRecord{
			JobID: jobID, Type: domain.EvtRunCorrected, RunID: runID,
			Payload: payload, OccurredAt: now,
		}); err != nil {
			return err
		}
		job.UpdatedAt = now
		if err := tx.SaveJob(ctx, jobRecord(job)); err != nil {
			return err
		}
		return s.rebuildMachineHistory(ctx, tx, job.MachineID)
	})
}

func mergeFix(kind domain.RunKind, ws []domain.Weight, rs []domain.Reading, runAt time.Time, note string, fix domain.RunFix) domain.RunInput {
	in := domain.RunInput{Kind: kind, RunAt: runAt, Note: note, Weights: ws, Readings: rs}
	if fix.Weights != nil {
		in.Weights = *fix.Weights
	}
	if fix.Readings != nil {
		in.Readings = *fix.Readings
	}
	if fix.RunAt != nil {
		in.RunAt = *fix.RunAt
	}
	if fix.Note != nil {
		in.Note = *fix.Note
	}
	return in
}

// GetJobState returns the job and its fully derived state.
func (s *Service) GetJobState(ctx context.Context, jobID string) (*JobView, error) {
	var view *JobView
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		jrec, err := tx.GetJob(ctx, jobID, false)
		if err != nil {
			return mapNotFound(err, "job")
		}
		job := jobFromRecord(jrec)
		events, err := tx.ListEvents(ctx, jobID)
		if err != nil {
			return err
		}
		current, err := s.historyForReplay(ctx, tx, job)
		if err != nil {
			return err
		}
		st, err := compute.Replay(job, toDomainEvents(events), current)
		if err != nil {
			return err
		}
		view = &JobView{Job: job, State: st}
		return nil
	})
	return view, err
}

// Finalize marks a job closed and appends job_finalized. Coefficient
// contributions are maintained continuously as runs arrive; finalization is
// the point at which the verification verdict is required. A job whose
// verification failed cannot be finalized until a passing run exists.
func (s *Service) Finalize(ctx context.Context, jobID string) (*compute.State, error) {
	var st *compute.State
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		jrec, err := tx.GetJob(ctx, jobID, true)
		if err != nil {
			return mapNotFound(err, "job")
		}
		job := jobFromRecord(jrec)
		events, err := tx.ListEvents(ctx, jobID)
		if err != nil {
			return err
		}
		current, err := s.historyForReplay(ctx, tx, job)
		if err != nil {
			return err
		}
		st, err = compute.Replay(job, toDomainEvents(events), current)
		if err != nil {
			return err
		}
		if st.OriginalRunID == "" {
			return reject(409, "cannot finalize without an original run",
				domain.FieldError{Field: "runs", Reason: "missing original run"})
		}
		if st.TrustChecked && !st.TrustOK {
			return reject(409, "verification run failed: stored coefficients are suspect; add a passing verification or new trial runs",
				domain.FieldError{Field: "runs", Reason: "trust check failed"})
		}
		now := s.Now()
		payload, _ := json.Marshal(domain.FinalizedEvent{
			At: now, Fused: len(st.Estimates(now)) > 0,
			VerificationOK: !st.TrustChecked || st.TrustOK,
		})
		if _, err := tx.AppendEvent(ctx, &store.EventRecord{
			JobID: jobID, Type: domain.EvtFinalized, Payload: payload, OccurredAt: now,
		}); err != nil {
			return err
		}
		job.UpdatedAt = now
		return tx.SaveJob(ctx, jobRecord(job))
	})
	return st, err
}

// historyForReplay supplies active stored coefficients for a job. For jobs
// started from scratch it returns nil (they fit their own); for history
// jobs it returns the current entries for every speed.
func (s *Service) historyForReplay(ctx context.Context, tx store.Tx, job *domain.Job) (map[int]*history.Entry, error) {
	recs, err := tx.ListCurrentCoeffs(ctx, job.MachineID)
	if err != nil {
		return nil, err
	}
	out := map[int]*history.Entry{}
	for _, r := range recs {
		out[r.Speed] = entryFromRecord(r)
	}
	// Jobs started from scratch fit their own coefficients; mid-job they must
	// not silently borrow stored coefficients (and failed verifications of a
	// scratch job must not invalidate stored coefficients).
	if !job.UseHistory || len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
