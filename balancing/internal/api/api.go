// Package api wires the store to an Echo HTTP server.
package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"balancing/internal/history"
	"balancing/internal/split"
	"balancing/internal/store"
	"balancing/internal/vec"
)

// Server holds HTTP dependencies.
type Server struct {
	store store.Store
	echo  *echo.Echo
}

// NewServer builds the router.
func NewServer(s store.Store) *Server {
	e := echo.New()
	e.HideBanner = true
	e.Validator = &noopValidator{}
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())
	e.HTTPErrorHandler = errorHandler

	srv := &Server{store: s, echo: e}
	r := e.Group("/api/v1")

	r.GET("/health", srv.health)
	r.POST("/machines", srv.createMachine)
	r.GET("/machines", srv.listMachines)
	r.GET("/machines/:id", srv.getMachine)
	r.GET("/machines/:id/history", srv.machineHistory)

	r.POST("/machines/:id/jobs", srv.createJob)
	r.GET("/machines/:id/jobs", srv.listJobs)
	r.GET("/jobs/:id", srv.getJob)
	r.POST("/jobs/:id/runs", srv.addRun)
	r.POST("/jobs/:id/corrections", srv.correctRun)
	r.GET("/jobs/:id/events", srv.listEvents)

	return srv
}

// Handler returns the Echo instance (used by tests and main).
func (s *Server) Handler() http.Handler { return s.echo }

func (s *Server) health(c echo.Context) error {
	if err := s.store.Ping(c.Request().Context()); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ---- DTOs ----

type holeDTO struct {
	Count        int     `json:"count"`
	FirstHoleDeg float64 `json:"first_hole_deg"`
}

type machineReq struct {
	Name           string           `json:"name" validate:"required"`
	PlaneCount     int              `json:"plane_count" validate:"min=1,max=2"`
	PointCount     int              `json:"point_count" validate:"min=1,max=4"`
	Speeds         []int            `json:"speeds" validate:"required,min=1"`
	ReferenceDeg   float64          `json:"reference_deg"`
	PhaseDirection vec.Direction    `json:"phase_direction" validate:"required,oneof=against_rotation with_rotation"`
	HoleLayout     []*holeDTO       `json:"hole_layout"`
	FusionStrategy history.Strategy `json:"fusion_strategy"`
	PointWeights   []float64        `json:"point_weights"`
}

type machineResp struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	PlaneCount     int              `json:"plane_count"`
	PointCount     int              `json:"point_count"`
	Speeds         []int            `json:"speeds"`
	ReferenceDeg   float64          `json:"reference_deg"`
	PhaseDirection vec.Direction    `json:"phase_direction"`
	HoleLayout     []*holeDTO       `json:"hole_layout"`
	FusionStrategy history.Strategy `json:"fusion_strategy"`
	PointWeights   []float64        `json:"point_weights"`
	CreatedAt      time.Time        `json:"created_at"`
}

func toMachineResp(m *store.Machine) machineResp {
	holes := make([]*holeDTO, len(m.HoleLayout))
	for i, h := range m.HoleLayout {
		if h != nil {
			holes[i] = &holeDTO{Count: h.Count, FirstHoleDeg: h.FirstHoleDeg}
		}
	}
	return machineResp{
		ID: m.ID, Name: m.Name, PlaneCount: m.PlaneCount, PointCount: m.PointCount,
		Speeds: m.Speeds, ReferenceDeg: m.ReferenceDeg, PhaseDirection: m.Direction,
		HoleLayout: holes, FusionStrategy: m.FusionStrategy, PointWeights: m.PointWeights,
		CreatedAt: m.CreatedAt,
	}
}

type jobReq struct {
	Operator   string `json:"operator" validate:"required"`
	Note       string `json:"note"`
	UseHistory bool   `json:"use_history"`
}

type readingReq struct {
	Speed int     `json:"speed" validate:"required"`
	Point int     `json:"point" validate:"gte=0"`
	Amp   float64 `json:"amp_um"`
	Phase float64 `json:"phase_deg"`
}

type weightReq struct {
	Plane int     `json:"plane" validate:"gte=0"`
	Grams float64 `json:"grams"`
	Angle float64 `json:"angle_deg"`
}

type runReq struct {
	Kind        store.RunKind `json:"kind" validate:"required,oneof=original trial correction verification"`
	RunTime     time.Time     `json:"run_time" validate:"required"`
	Operator    string        `json:"operator"`
	Note        string        `json:"note"`
	Readings    []readingReq  `json:"readings" validate:"required,min=1,dive"`
	Weights     []weightReq   `json:"weights"`
	TrialPlane  *int          `json:"trial_plane"`
	TrialWeight *weightReq    `json:"trial_weight"`
}

type correctionReq struct {
	TargetRunTime time.Time    `json:"target_run_time" validate:"required"`
	TargetSeq     *int64       `json:"target_seq"`
	Operator      string       `json:"operator"`
	Note          string       `json:"note"`
	Readings      []readingReq `json:"readings"`
	Weights       []weightReq  `json:"weights"`
	TrialPlane    *int         `json:"trial_plane"`
	TrialWeight   *weightReq   `json:"trial_weight"`
}

func readingsToStore(in []readingReq) []store.ReadingInput {
	out := make([]store.ReadingInput, len(in))
	for i, r := range in {
		out[i] = store.ReadingInput{Speed: r.Speed, Point: r.Point, Amp: r.Amp, Phase: r.Phase}
	}
	return out
}

func weightsToStore(in []weightReq) []store.WeightInput {
	out := make([]store.WeightInput, len(in))
	for i, w := range in {
		out[i] = store.WeightInput{Plane: w.Plane, Grams: w.Grams, Angle: w.Angle}
	}
	return out
}

// ---- handlers ----

func (s *Server) createMachine(c echo.Context) error {
	var req machineReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}
	if req.FusionStrategy == "" {
		req.FusionStrategy = history.UncertaintyWeighted
	}
	holesIn := make([]*store.HoleLayoutIn, 0, len(req.HoleLayout))
	for _, h := range req.HoleLayout {
		if h != nil {
			holesIn = append(holesIn, store.NewHoleLayoutIn(h.Count, h.FirstHoleDeg))
		} else {
			holesIn = append(holesIn, nil)
		}
	}
	if ver := store.ValidateMachine(req.Name, req.PlaneCount, req.PointCount, req.Speeds, holesIn, string(req.PhaseDirection), req.ReferenceDeg, req.PointWeights, string(req.FusionStrategy)); ver != nil {
		return ver
	}
	holes := make([]*split.Plane, len(req.HoleLayout))
	for i, h := range req.HoleLayout {
		if h != nil {
			holes[i] = &split.Plane{Count: h.Count, FirstHoleDeg: h.FirstHoleDeg}
		}
	}
	m := &store.Machine{
		Name: req.Name, PlaneCount: req.PlaneCount, PointCount: req.PointCount,
		Speeds: req.Speeds, ReferenceDeg: req.ReferenceDeg, Direction: req.PhaseDirection,
		HoleLayout: holes, FusionStrategy: req.FusionStrategy, PointWeights: req.PointWeights,
	}
	if err := s.store.CreateMachine(c.Request().Context(), m); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toMachineResp(m))
}

func (s *Server) listMachines(c echo.Context) error {
	ms, err := s.store.ListMachines(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]machineResp, len(ms))
	for i, m := range ms {
		out[i] = toMachineResp(m)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) getMachine(c echo.Context) error {
	m, err := s.store.GetMachine(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toMachineResp(m))
}

type historyResp struct {
	MachineID string        `json:"machine_id"`
	Strategy  string        `json:"strategy"`
	Speeds    []historyView `json:"speeds"`
}

type historyView struct {
	Speed       int           `json:"speed"`
	Available   bool          `json:"available"`
	Verified    bool          `json:"verified"`
	Uncertainty float64       `json:"uncertainty"`
	FusedFrom   []string      `json:"fused_from"`
	Influence   [][]vec.Polar `json:"influence"`
}

func (s *Server) machineHistory(c echo.Context) error {
	st, m, err := s.store.MachineHistory(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	out := historyResp{MachineID: m.ID, Strategy: string(m.FusionStrategy)}
	for _, speed := range st.Speeds() {
		e, ok := st.EntryAt(speed)
		hv := historyView{Speed: speed, Available: ok}
		if ok {
			hv.Verified = e.Verified
			hv.Uncertainty = e.Uncertainty
			hv.FusedFrom = e.FusedFrom
			hv.Influence = matrixView(m, e.H)
		}
		out.Speeds = append(out.Speeds, hv)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) createJob(c echo.Context) error {
	var req jobReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}
	j, err := s.store.CreateJob(c.Request().Context(), c.Param("id"), req.Operator, req.Note, req.UseHistory, time.Time{})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, j)
}

func (s *Server) listJobs(c echo.Context) error {
	js, err := s.store.ListJobs(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	out := make([]*store.JobView, len(js))
	for i, st := range js {
		out[i] = store.BuildView(st)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) getJob(c echo.Context) error {
	js, err := s.store.GetJob(c.Request().Context(), c.Param("id"), true)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, store.BuildView(js))
}

func (s *Server) addRun(c echo.Context) error {
	var req runReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}
	in := store.RunInput{
		Kind: req.Kind, RunTime: req.RunTime, Operator: req.Operator, Note: req.Note,
		Readings: readingsToStore(req.Readings), Weights: weightsToStore(req.Weights),
		TrialPlane: req.TrialPlane,
	}
	if req.TrialWeight != nil {
		tw := store.WeightInput{Plane: -1, Grams: req.TrialWeight.Grams, Angle: req.TrialWeight.Angle}
		in.TrialWeight = &tw
	}
	r, err := s.store.AddRun(c.Request().Context(), c.Param("id"), in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{"accepted": true, "run": summarizeRun(r)})
}

func (s *Server) correctRun(c echo.Context) error {
	var req correctionReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}
	in := store.CorrectionInput{
		TargetRunTime: req.TargetRunTime, TargetSeq: req.TargetSeq,
		Operator: req.Operator, Note: req.Note, TrialPlane: req.TrialPlane,
	}
	if req.Readings != nil {
		in.Readings = readingsToStore(req.Readings)
	}
	if req.Weights != nil {
		in.Weights = weightsToStore(req.Weights)
	}
	if req.TrialWeight != nil {
		tw := store.WeightInput{Grams: req.TrialWeight.Grams, Angle: req.TrialWeight.Angle}
		in.TrialWeight = &tw
	}
	r, err := s.store.CorrectRun(c.Request().Context(), c.Param("id"), in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{"corrected": true, "run": summarizeRun(r)})
}

func (s *Server) listEvents(c echo.Context) error {
	evs, err := s.store.ListEvents(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, store.BuildEventViews(evs))
}

type runSummary struct {
	Kind      store.RunKind `json:"kind"`
	RunTime   time.Time     `json:"run_time"`
	Corrected bool          `json:"corrected"`
}

func summarizeRun(r *store.Run) runSummary {
	if r == nil {
		return runSummary{}
	}
	return runSummary{Kind: r.Kind, RunTime: r.RunTime, Corrected: r.Corrected}
}

func errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		_ = c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error":  "validation_failed",
			"fields": ve.Fields,
		})
		return
	}
	var he *echo.HTTPError
	if errors.As(err, &he) {
		_ = c.JSON(he.Code, map[string]any{"error": he.Message})
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		_ = c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, store.ErrHistoryUnavailable):
		_ = c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrCorrectionMissingTarget):
		_ = c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrCorrectionAmbiguous):
		_ = c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		_ = c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
