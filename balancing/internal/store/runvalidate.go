package store

import "math"

// ValidateRunInput checks every field of a submitted run independently of the
// other events in the job (cross-run checks live in the replay engine).
func ValidateRunInput(in RunInput, planes, points int, speedSet map[int]bool) *ValidationError {
	var fes []FieldError
	if !in.Kind.Valid() {
		fes = append(fes, FieldError{"kind", "invalid", "run kind must be original, trial, correction or verification"})
	}
	if in.RunTime.IsZero() {
		fes = append(fes, FieldError{"run_time", "required", "run_time is required and is the concurrent-submission ordering key"})
	}
	if len(in.Readings) == 0 {
		fes = append(fes, FieldError{"readings", "required", "at least one vibration reading is required"})
	}
	for i, r := range in.Readings {
		if math.IsNaN(r.Amp) || math.IsInf(r.Amp, 0) {
			fes = append(fes, FieldError{fieldName("readings", i) + ".amp_um", "invalid", "amplitude must be finite"})
		} else if r.Amp < 0 {
			fes = append(fes, FieldError{fieldName("readings", i) + ".amp_um", "negative_amplitude", "vibration amplitude must be non-negative"})
		}
		if r.Phase < 0 || r.Phase >= 360 {
			fes = append(fes, FieldError{fieldName("readings", i) + ".phase_deg", "phase_out_of_range", "phase must be in [0,360) degrees; check for a 180° flip or inverted convention"})
		}
		if r.Point < 0 || r.Point >= points {
			fes = append(fes, FieldError{fieldName("readings", i) + ".point", "out_of_range", "point index outside configured measurement points"})
		}
		if !speedSet[r.Speed] {
			fes = append(fes, FieldError{fieldName("readings", i) + ".speed", "unknown_speed", "speed is not configured for this machine"})
		}
	}
	seen := map[[2]int]bool{}
	for i, r := range in.Readings {
		k := [2]int{r.Speed, r.Point}
		if seen[k] {
			fes = append(fes, FieldError{fieldName("readings", i), "duplicate", "duplicate reading for same speed and point"})
		}
		seen[k] = true
	}
	for i, w := range in.Weights {
		if math.IsNaN(w.Grams) || math.IsInf(w.Grams, 0) {
			fes = append(fes, FieldError{fieldName("weights", i) + ".grams", "invalid", "weight must be finite"})
		} else if w.Grams <= 0 {
			fes = append(fes, FieldError{fieldName("weights", i) + ".grams", "non_positive_weight", "attached weight must be greater than zero grams"})
		}
		if w.Angle < 0 || w.Angle >= 360 {
			fes = append(fes, FieldError{fieldName("weights", i) + ".angle_deg", "phase_out_of_range", "weight angle must be in [0,360) degrees"})
		}
		if w.Plane < 0 || w.Plane >= planes {
			fes = append(fes, FieldError{fieldName("weights", i) + ".plane", "out_of_range", "plane index outside configured correction planes"})
		}
	}
	seenPlane := map[int]bool{}
	for i, w := range in.Weights {
		if seenPlane[w.Plane] {
			fes = append(fes, FieldError{fieldName("weights", i) + ".plane", "duplicate", "give exactly one total weight entry per plane"})
		}
		seenPlane[w.Plane] = true
	}
	if in.Kind == RunTrial {
		if in.TrialPlane == nil {
			fes = append(fes, FieldError{"trial_plane", "required", "a trial run must name the plane carrying the trial weight"})
		} else if *in.TrialPlane < 0 || *in.TrialPlane >= planes {
			fes = append(fes, FieldError{"trial_plane", "out_of_range", "trial plane outside configured correction planes"})
		}
		if in.TrialWeight == nil {
			fes = append(fes, FieldError{"trial_weight", "required", "a trial run must record the trial weight vector"})
		} else {
			tw := *in.TrialWeight
			if tw.Grams <= 0 {
				fes = append(fes, FieldError{"trial_weight.grams", "zero_trial_weight", "trial weight must be greater than zero grams, otherwise no influence coefficient can be derived"})
			}
			if tw.Angle < 0 || tw.Angle >= 360 {
				fes = append(fes, FieldError{"trial_weight.angle_deg", "phase_out_of_range", "trial-weight angle must be in [0,360) degrees"})
			}
		}
	}
	if len(fes) > 0 {
		return &ValidationError{Fields: fes}
	}
	return nil
}

// ValidateCorrectionInput checks a correction event's own fields.
func ValidateCorrectionInput(in CorrectionInput, planes, points int, speedSet map[int]bool) *ValidationError {
	if in.TargetRunTime.IsZero() {
		return &ValidationError{Fields: []FieldError{{"target_run_time", "required", "must identify the run being corrected"}}}
	}
	var fes []FieldError
	seen := map[[2]int]bool{}
	for i, r := range in.Readings {
		if r.Amp < 0 {
			fes = append(fes, FieldError{fieldName("readings", i) + ".amp_um", "negative_amplitude", "vibration amplitude must be non-negative"})
		}
		if r.Phase < 0 || r.Phase >= 360 {
			fes = append(fes, FieldError{fieldName("readings", i) + ".phase_deg", "phase_out_of_range", "phase must be in [0,360) degrees"})
		}
		if r.Point < 0 || r.Point >= points || !speedSet[r.Speed] {
			fes = append(fes, FieldError{fieldName("readings", i), "out_of_range", "reading references unknown point or speed"})
		}
		k := [2]int{r.Speed, r.Point}
		if seen[k] {
			fes = append(fes, FieldError{fieldName("readings", i), "duplicate", "duplicate reading for same speed and point"})
		}
		seen[k] = true
	}
	for i, w := range in.Weights {
		if w.Grams <= 0 {
			fes = append(fes, FieldError{fieldName("weights", i) + ".grams", "non_positive_weight", "weight must be greater than zero grams"})
		}
		if w.Angle < 0 || w.Angle >= 360 {
			fes = append(fes, FieldError{fieldName("weights", i) + ".angle_deg", "phase_out_of_range", "weight angle must be in [0,360) degrees"})
		}
		if w.Plane < 0 || w.Plane >= planes {
			fes = append(fes, FieldError{fieldName("weights", i) + ".plane", "out_of_range", "plane outside configured correction planes"})
		}
	}
	if in.TrialPlane != nil && (*in.TrialPlane < 0 || *in.TrialPlane >= planes) {
		fes = append(fes, FieldError{"trial_plane", "out_of_range", "trial plane outside configured correction planes"})
	}
	if in.TrialWeight != nil && in.TrialWeight.Grams <= 0 {
		fes = append(fes, FieldError{"trial_weight.grams", "zero_trial_weight", "trial weight must be greater than zero grams"})
	}
	if len(fes) > 0 {
		return &ValidationError{Fields: fes}
	}
	return nil
}
