package compute

import (
	"balancer/pkg/split"
	"balancer/pkg/vec"
)

// splitWeight maps an external-frame correction polar to the machine's fixed
// holes. Hole angles in pkg/split live in the same frame as the input; since
// the API speaks the machine convention, hole 0 is at the machine's field 0°
// and angles increase in field-positive direction, which is exactly the
// convention the field technicians read.
func splitWeight(w vec.Polar, holeCount int) []HoleSplit {
	r, err := split.Split(w.Phase, w.Amp, holeCount)
	if err != nil {
		return nil
	}
	return []HoleSplit{{
		First:          Hole{Index: r.First.Index, Angle: r.First.Angle, Mass: r.First.Mass},
		Second:         Hole{Index: r.Second.Index, Angle: r.Second.Angle, Mass: r.Second.Mass},
		ResultantMass:  r.ResultantMass,
		ResultantAngle: r.ResultantAngle,
	}}
}

// TrialChangeTooSmall applies the ill-conditioning rejection for a single
// trial run against the original run.
//
// Criterion: for every speed, the trial must change at least one measurement
// point's vibration by max(floorUm, relFloor · amplitude), i.e. the measured
// effect of the trial weight must be distinguishable from reading noise.
// Returns the offending speed, the maximum observed relative change for
// diagnostics, and bad=true when rejection applies. A run passing this
// per-run screen can still fail the matrix condition-number test once all
// planes have trial runs.
func TrialChangeTooSmall(orig, trial *Run, rpms []int, floorUm, relFloor float64) (speed int, maxRel float64, bad bool) {
	points := len(orig.Readings[0])
	for si, rpm := range rpms {
		anyOK := false
		for p := 0; p < points; p++ {
			d := vec.FromComplex(trial.Readings[si][p] - orig.Readings[si][p]).Amp
			scale := vec.FromComplex(orig.Readings[si][p]).Amp
			thr := floorUm
			if r := relFloor * scale; r > thr {
				thr = r
			}
			if d >= thr {
				anyOK = true
			}
			if scale > 0 && d/scale > maxRel {
				maxRel = d / scale
			}
		}
		if !anyOK {
			return rpm, maxRel, true
		}
	}
	return 0, maxRel, false
}
