package service

import (
	"context"
	"sort"
	"time"

	"balancer/internal/compute"
	"balancer/internal/store"
	"balancer/pkg/history"
)

// rebuildMachineHistory reconstructs every stored current coefficient of one
// machine from the machine's complete job history. It is called inside the
// writer transaction after any run addition or correction, which is what
// makes a correction to an old run propagate into machine history exactly as
// if the run had been entered correctly originally.
//
// Reconstruction order and rules (see DESIGN.md for rationale):
//   - jobs are processed chronologically by UpdatedAt (the time of their
//     newest event), so a correction re-positions the job's contribution in
//     the same place a correct original entry would have occupied;
//   - a job contributes its fitted coefficients only when it actually
//     contains trial/correction runs (fitted-from-runs), and only when its
//     verification (if present) passed;
//   - inverse-variance fusion with exponential age weighting is applied by
//     pkg/history; a failed verification first marks the then-current entry
//     suspect, after which later good jobs start a fresh entry.
func (s *Service) rebuildMachineHistory(ctx context.Context, tx store.Tx, machineID string) error {
	jrecs, err := tx.ListJobsByMachine(ctx, machineID)
	if err != nil {
		return err
	}
	sort.Slice(jrecs, func(i, j int) bool { return jrecs[i].UpdatedAt.Before(jrecs[j].UpdatedAt) })

	// Reset the machine's current coefficients: everything derives from
	// immutable job contributions + the event streams.
	existing, err := tx.ListCurrentCoeffs(ctx, machineID)
	if err != nil {
		return err
	}
	current := map[int]*history.Entry{}
	createdAt := map[int]time.Time{}
	for _, r := range existing {
		e := entryFromRecord(r)
		current[r.Speed] = &history.Entry{}
		*current[r.Speed] = *e
		createdAt[r.Speed] = r.CreatedAt
	}
	// Rebuild from scratch rather than incrementally patching: start empty so
	// a corrected/removed contribution cannot leave stale residue.
	current = map[int]*history.Entry{}
	createdAt = map[int]time.Time{}

	for _, jrec := range jrecs {
		job := jobFromRecord(jrec)
		events, err := tx.ListEvents(ctx, job.ID)
		if err != nil {
			return err
		}
		// This job's contributions are re-derived like everything else: drop
		// its prior rows, then write the current ones.
		if err := tx.DeleteEstimatesForJob(ctx, job.ID); err != nil {
			return err
		}
		// Replay without current coefficients: each job derives its own fits;
		// trust evaluation for a history-based job needs the coefficient that
		// was active *when the job ran*, which the chronological rebuild has
		// just produced — pass it in.
		hist := map[int]*history.Entry{}
		if job.UseHistory {
			for _, sp := range job.Speeds {
				if e, ok := current[sp]; ok {
					hist[sp] = e
				}
			}
		}
		st, err := compute.Replay(job, toDomainEvents(events), hist)
		if err != nil {
			return err
		}

		// A history-based job's verification verdict acts on the active entry.
		if job.UseHistory && st.TrustChecked && !st.TrustOK {
			for _, sp := range job.Speeds {
				if e := current[sp]; e != nil && e.Status == history.StatusActive {
					e.InflateAfterFailure()
				}
			}
			continue
		}

		summaries := st.Estimates(jrec.UpdatedAt)
		for _, sum := range summaries {
			est := sum.Est
			// Keep an auditable per-job contribution, but only trust-passing
			// fitted estimates feed the fused current coefficient.
			if sum.TrustOK {
				if err := tx.UpsertEstimate(ctx, &store.EstimateRecord{
					JobID: job.ID, MachineID: machineID, Speed: sum.Speed,
					PlaneIDs: append([]string(nil), est.PlaneIDs...),
					PointIDs: append([]string(nil), est.PointIDs...),
					Re:       append([]float64(nil), est.Re...),
					Im:       append([]float64(nil), est.Im...),
					Uncert:   append([]float64(nil), est.Uncert...),
					TrustOK:  true, UpdatedAt: jrec.UpdatedAt,
				}); err != nil {
					return err
				}
				fused := history.Fuse(current[sum.Speed], &est, s.Policy, jrec.UpdatedAt)
				if _, existed := current[sum.Speed]; !existed {
					createdAt[sum.Speed] = jrec.CreatedAt
				}
				current[sum.Speed] = fused
			} else {
				if err := tx.UpsertEstimate(ctx, &store.EstimateRecord{
					JobID: job.ID, MachineID: machineID, Speed: sum.Speed,
					PlaneIDs: append([]string(nil), est.PlaneIDs...),
					PointIDs: append([]string(nil), est.PointIDs...),
					Re:       append([]float64(nil), est.Re...),
					Im:       append([]float64(nil), est.Im...),
					Uncert:   append([]float64(nil), est.Uncert...),
					TrustOK:  false, UpdatedAt: jrec.UpdatedAt,
				}); err != nil {
					return err
				}
			}
		}
	}

	keepSpeeds := make([]int, 0, len(current))
	for speed, e := range current {
		keepSpeeds = append(keepSpeeds, speed)
		cat, ok := createdAt[speed]
		if !ok {
			cat = e.CreatedAt
		}
		rec := &store.CurrentCoeffRecord{
			MachineID: machineID, Speed: speed,
			PlaneIDs: append([]string(nil), e.PlaneIDs...),
			PointIDs: append([]string(nil), e.PointIDs...),
			Re:       append([]float64(nil), e.Re...),
			Im:       append([]float64(nil), e.Im...),
			Uncert:   append([]float64(nil), e.Uncert...),
			NContrib: e.NContrib, Status: e.Status,
			CreatedAt: cat, UpdatedAt: e.UpdatedAt,
		}
		if err := tx.PutCurrentCoeff(ctx, rec); err != nil {
			return err
		}
	}
	return tx.DeleteCurrentCoeffsNotIn(ctx, machineID, keepSpeeds)
}

// ListCoefficients returns the machine's current stored coefficients per
// speed (including suspect ones, flagged so the UI can explain why a trial
// run is required).
func (s *Service) ListCoefficients(ctx context.Context, machineID string) ([]store.CurrentCoeffRecord, error) {
	var out []store.CurrentCoeffRecord
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		recs, err := tx.ListCurrentCoeffs(ctx, machineID)
		if err != nil {
			return err
		}
		out = recs
		sort.Slice(out, func(i, j int) bool { return out[i].Speed < out[j].Speed })
		return nil
	})
	return out, err
}
