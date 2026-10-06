package api

import (
	"net/http"
	"time"

	"balancer/internal/compute"
	"balancer/internal/domain"
	"balancer/internal/service"
	"balancer/pkg/vec"

	"github.com/labstack/echo/v4"
)

type createJobRequest struct {
	MachineID  string `json:"machineId"`
	Name       string `json:"name"`
	UseHistory bool   `json:"useHistory"`
}

type weightDTO struct {
	PlaneID string    `json:"planeId"`
	Polar   polarDTO2 `json:"polar"`
}

type polarDTO2 struct {
	Amp   float64 `json:"amp"`
	Phase float64 `json:"phase"`
}

type readingDTO struct {
	PointID string    `json:"pointId"`
	Speed   int       `json:"speedRpm"`
	Polar   polarDTO2 `json:"polar"`
}

type addRunRequest struct {
	ClientID string       `json:"clientId,omitempty"`
	Kind     string       `json:"kind"`
	RunAt    time.Time    `json:"runAt"`
	Note     string       `json:"note,omitempty"`
	Weights  []weightDTO  `json:"weights"`
	Readings []readingDTO `json:"readings"`
}

type correctRunRequest struct {
	Weights  *[]weightDTO  `json:"weights,omitempty"`
	Readings *[]readingDTO `json:"readings,omitempty"`
	RunAt    *time.Time    `json:"runAt,omitempty"`
	Note     *string       `json:"note,omitempty"`
}

func toRunInput(req *addRunRequest) *domain.RunInput {
	in := &domain.RunInput{
		ClientID: req.ClientID, Kind: domain.RunKind(req.Kind),
		RunAt: req.RunAt, Note: req.Note,
	}
	for _, w := range req.Weights {
		in.Weights = append(in.Weights, domain.Weight{
			PlaneID: w.PlaneID, Polar: vec.Polar{Amp: w.Polar.Amp, Phase: w.Polar.Phase},
		})
	}
	for _, r := range req.Readings {
		in.Readings = append(in.Readings, domain.Reading{
			PointID: r.PointID, Speed: r.Speed,
			Polar: vec.Polar{Amp: r.Polar.Amp, Phase: r.Polar.Phase},
		})
	}
	return in
}

func (s *Server) createJob(c echo.Context) error {
	var req createJobRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	view, err := s.svc.CreateJob(c.Request().Context(), &service.CreateJobInput{
		MachineID: req.MachineID, Name: req.Name, UseHistory: req.UseHistory,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, jobViewResponse(view))
}

func (s *Server) getJob(c echo.Context) error {
	view, err := s.svc.GetJobState(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, jobViewResponse(view))
}

func (s *Server) addRun(c echo.Context) error {
	var req addRunRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	res, err := s.svc.AddRun(c.Request().Context(), c.Param("id"), toRunInput(&req))
	if err != nil {
		return err
	}
	code := http.StatusCreated
	if res.Dedupe {
		code = http.StatusOK
	}
	return c.JSON(code, echo.Map{
		"runId":        res.RunID,
		"seq":          res.Seq,
		"deduplicated": res.Dedupe,
		"state":        stateResponse(res.State),
	})
}

func (s *Server) correctRun(c echo.Context) error {
	var req correctRunRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	fix := domain.RunFix{}
	if req.Weights != nil {
		ws := make([]domain.Weight, 0, len(*req.Weights))
		for _, w := range *req.Weights {
			ws = append(ws, domain.Weight{PlaneID: w.PlaneID, Polar: vec.Polar{Amp: w.Polar.Amp, Phase: w.Polar.Phase}})
		}
		fix.Weights = &ws
	}
	if req.Readings != nil {
		rs := make([]domain.Reading, 0, len(*req.Readings))
		for _, r := range *req.Readings {
			rs = append(rs, domain.Reading{PointID: r.PointID, Speed: r.Speed, Polar: vec.Polar{Amp: r.Polar.Amp, Phase: r.Polar.Phase}})
		}
		fix.Readings = &rs
	}
	fix.RunAt = req.RunAt
	fix.Note = req.Note
	if err := s.svc.CorrectRun(c.Request().Context(), c.Param("id"), c.Param("runId"), fix); err != nil {
		return err
	}
	view, err := s.svc.GetJobState(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, jobViewResponse(view))
}

func (s *Server) finalize(c echo.Context) error {
	st, err := s.svc.Finalize(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, echo.Map{"state": stateResponse(st)})
}

func jobViewResponse(v *service.JobView) echo.Map {
	return echo.Map{
		"job": echo.Map{
			"id":          v.Job.ID,
			"machineId":   v.Job.MachineID,
			"name":        v.Job.Name,
			"planeIds":    v.Job.PlaneIDs,
			"pointIds":    v.Job.PointIDs,
			"speedsRpm":   v.Job.Speeds,
			"usedHistory": v.Job.UseHistory,
			"createdAt":   v.Job.CreatedAt.UTC().Format(time.RFC3339Nano),
			"updatedAt":   v.Job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		"state": stateResponse(v.State),
	}
}

func stateResponse(st *compute.State) echo.Map {
	runs := make([]echo.Map, 0, len(st.Runs))
	for _, r := range st.Runs {
		runs = append(runs, echo.Map{
			"runId":    r.RunID,
			"kind":     string(r.Kind),
			"runAt":    r.RunAt.UTC().Format(time.RFC3339Nano),
			"seq":      r.Seq,
			"note":     r.Note,
			"weights":  weightsResponse(r.ExtWeights),
			"readings": readingsResponse(r.ExtReadings),
		})
	}
	fits := make([]echo.Map, 0, len(st.Fits))
	for speed := range st.Fits {
		f := st.Fits[speed]
		fits = append(fits, fitResponse(f))
	}
	plans := make([]echo.Map, 0, len(st.Plans))
	for speed := range st.Plans {
		plans = append(plans, planResponse(st.Plans[speed]))
	}
	out := echo.Map{
		"originalRunId": st.OriginalRunID,
		"runs":          runs,
		"fits":          fits,
		"plans":         plans,
		"usedHistory":   st.HistoryUsed,
	}
	if st.MultiSpeed != nil {
		out["multiSpeedPlan"] = planResponse(st.MultiSpeed)
	}
	if st.Trim != nil {
		out["trimPlan"] = trimResponse(st.Trim)
	}
	if st.Trust != nil {
		out["trust"] = trustResponse(st)
	}
	if st.Finalized != nil {
		out["finalized"] = echo.Map{
			"at":             st.Finalized.At.UTC().Format(time.RFC3339Nano),
			"fused":          st.Finalized.Fused,
			"verificationOk": st.Finalized.VerificationOK,
		}
	}
	return out
}

func weightsResponse(ws []domain.Weight) []echo.Map {
	out := make([]echo.Map, 0, len(ws))
	for _, w := range ws {
		out = append(out, echo.Map{
			"planeId": w.PlaneID,
			"polar":   echo.Map{"amp": w.Polar.Amp, "phase": w.Polar.Phase},
		})
	}
	return out
}

func readingsResponse(rs []domain.Reading) []echo.Map {
	out := make([]echo.Map, 0, len(rs))
	for _, r := range rs {
		out = append(out, echo.Map{
			"pointId":  r.PointID,
			"speedRpm": r.Speed,
			"polar":    echo.Map{"amp": r.Polar.Amp, "phase": r.Polar.Phase},
		})
	}
	return out
}

func polarResponse(p vec.Polar) echo.Map {
	return echo.Map{"amp": p.Amp, "phase": p.Phase}
}

func fitResponse(f *compute.Fit) echo.Map {
	rows := make([][]echo.Map, len(f.ExtA))
	for p := range f.ExtA {
		rows[p] = make([]echo.Map, len(f.ExtA[p]))
		for a := range f.ExtA[p] {
			rows[p][a] = echo.Map{
				"polar":       polarResponse(f.ExtA[p][a]),
				"uncertainty": f.ExtSigma[p][a],
			}
		}
	}
	m := echo.Map{
		"speedRpm":  f.Speed,
		"fromRuns":  f.FromRun,
		"influence": rows,
		"log10Cond": f.Log10Cond,
		"rank":      f.Rank,
	}
	if f.Err != "" {
		m["error"] = f.Err
	}
	return m
}

func planResponse(p *compute.SpeedPlan) echo.Map {
	weights := make([]echo.Map, len(p.Weights))
	for i, w := range p.Weights {
		entry := echo.Map{"polar": polarResponse(w)}
		if i < len(p.Holes) && len(p.Holes[i]) > 0 {
			hs := make([]echo.Map, len(p.Holes[i]))
			for j, h := range p.Holes[i] {
				hs[j] = echo.Map{
					"first":          echo.Map{"index": h.First.Index, "angle": h.First.Angle, "mass": h.First.Mass},
					"second":         echo.Map{"index": h.Second.Index, "angle": h.Second.Angle, "mass": h.Second.Mass},
					"resultantMass":  h.ResultantMass,
					"resultantAngle": h.ResultantAngle,
				}
			}
			entry["holes"] = hs
		}
		weights[i] = entry
	}
	residual := make([]echo.Map, len(p.Residual))
	for i, r := range p.Residual {
		residual[i] = echo.Map{"polar": polarResponse(r), "ampUm": p.ResidualAmp[i]}
	}
	m := echo.Map{
		"speedRpm":          p.Speed,
		"weights":           weights,
		"predictedResidual": residual,
	}
	if p.Err != "" {
		m["error"] = p.Err
	}
	return m
}

func trimResponse(t *compute.TrimPlan) echo.Map {
	add := make([]echo.Map, len(t.AddWeights))
	inst := make([]echo.Map, len(t.Installed))
	for i := range add {
		add[i] = echo.Map{"polar": polarResponse(t.AddWeights[i])}
		inst[i] = echo.Map{"polar": polarResponse(t.Installed[i])}
	}
	m := echo.Map{
		"basedOnRunId":     t.BasedOnRun,
		"addWeights":       add,
		"installedWeights": inst,
	}
	if t.Err != "" {
		m["error"] = t.Err
	}
	return m
}

func trustResponse(st *compute.State) echo.Map {
	pts := make([]echo.Map, 0, len(st.Trust.PerPoint))
	for _, p := range st.Trust.PerPoint {
		pts = append(pts, echo.Map{
			"pointId":       p.PointID,
			"originalAmp":   p.Original,
			"measuredAmp":   p.Measured,
			"predictedAmp":  p.Predicted,
			"relModelError": p.RelError,
			"reduction":     p.Reduction,
			"passed":        p.Passed,
			"reason":        p.Reason,
		})
	}
	return echo.Map{"ok": st.TrustOK, "perPoint": pts}
}
