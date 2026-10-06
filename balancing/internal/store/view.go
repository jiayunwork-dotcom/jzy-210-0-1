package store

import (
	"sort"
	"time"

	"balancing/internal/vec"
)

// RunReadingView is one reading in the machine's external convention.
type RunReadingView struct {
	Speed int       `json:"speed"`
	Point int       `json:"point"`
	V     vec.Polar `json:"vibration"`
}

// RunWeightView is one attached weight in the external convention.
type RunWeightView struct {
	Plane int       `json:"plane"`
	W     vec.Polar `json:"weight"`
}

// RunView is the serializable form of a replayed Run.
type RunView struct {
	Kind        RunKind          `json:"kind"`
	RunTime     time.Time        `json:"run_time"`
	Operator    string           `json:"operator"`
	Note        string           `json:"note"`
	Seq         int64            `json:"seq"`
	TrialPlane  int              `json:"trial_plane"`
	HasTrial    bool             `json:"has_trial"`
	Corrected   bool             `json:"corrected"`
	Readings    []RunReadingView `json:"readings"`
	Weights     []RunWeightView  `json:"weights"`
	TrialWeight *vec.Polar       `json:"trial_weight,omitempty"`
}

// JobView is the serializable form of a JobState.
type JobView struct {
	Job     *Job       `json:"job"`
	Machine *Machine   `json:"machine"`
	Runs    []*RunView `json:"runs"`
	Result  *JobResult `json:"result"`
}

// BuildView converts a replayed job state into external-convention JSON form.
func BuildView(js *JobState) *JobView {
	conv := js.Machine.Convention()
	v := &JobView{Job: js.Job, Machine: js.Machine, Result: js.Result}
	for _, r := range js.Runs {
		rv := &RunView{
			Kind: r.Kind, RunTime: r.RunTime,
			Operator: r.Operator, Note: r.Note, Seq: r.Seq,
			TrialPlane: r.TrialPlane, HasTrial: r.HasTrial, Corrected: r.Corrected,
		}
		speeds := make([]int, 0, len(r.Readings))
		for sp := range r.Readings {
			speeds = append(speeds, sp)
		}
		sort.Ints(speeds)
		for _, sp := range speeds {
			points := make([]int, 0, len(r.Readings[sp]))
			for pt := range r.Readings[sp] {
				points = append(points, pt)
			}
			sort.Ints(points)
			for _, pt := range points {
				p := vec.FromC(r.Readings[sp][pt])
				p.Phase = conv.ToExternal(p.Phase)
				rv.Readings = append(rv.Readings, RunReadingView{Speed: sp, Point: pt, V: p})
			}
		}
		planes := make([]int, 0, len(r.Weights))
		for pl := range r.Weights {
			planes = append(planes, pl)
		}
		sort.Ints(planes)
		for _, pl := range planes {
			p := vec.FromC(r.Weights[pl])
			p.Phase = conv.ToExternal(p.Phase)
			rv.Weights = append(rv.Weights, RunWeightView{Plane: pl, W: p})
		}
		if r.HasTrial {
			p := vec.FromC(r.TrialWeight)
			p.Phase = conv.ToExternal(p.Phase)
			pp := p
			rv.TrialWeight = &pp
		}
		v.Runs = append(v.Runs, rv)
	}
	return v
}
