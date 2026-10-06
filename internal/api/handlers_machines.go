package api

import (
	"net/http"
	"time"

	"balancer/internal/domain"
	"balancer/internal/service"
	"balancer/internal/store"
	"balancer/pkg/vec"

	"github.com/labstack/echo/v4"
)

type planeDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	RadiusMm  float64 `json:"radiusMm,omitempty"`
	HoleCount int     `json:"holeCount"`
}

type pointDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type conventionDTO struct {
	ZeroOffsetDeg float64 `json:"zeroOffsetDeg"`
	Direction     int     `json:"direction"`
}

type createMachineRequest struct {
	Name                 string        `json:"name"`
	Planes               []planeDTO    `json:"planes"`
	Points               []pointDTO    `json:"points"`
	SpeedsRpm            []int         `json:"speedsRpm"`
	Convention           conventionDTO `json:"convention"`
	ReadingChangeFloorUm float64       `json:"readingChangeFloorUm,omitempty"`
}

func (s *Server) createMachine(c echo.Context) error {
	var req createMachineRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	in := &service.CreateMachineInput{
		Name:                 req.Name,
		Speeds:               req.SpeedsRpm,
		Convention:           vec.Convention{ZeroOffsetDeg: req.Convention.ZeroOffsetDeg, Direction: req.Convention.Direction},
		ReadingChangeFloorUm: req.ReadingChangeFloorUm,
	}
	for _, p := range req.Planes {
		in.Planes = append(in.Planes, domain.Plane{ID: p.ID, Name: p.Name, Radius: p.RadiusMm, HoleCount: p.HoleCount})
	}
	for _, p := range req.Points {
		in.Points = append(in.Points, domain.Point{ID: p.ID, Name: p.Name})
	}
	m, err := s.svc.CreateMachine(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, machineResponse(m))
}

func machineResponse(m *domain.Machine) echo.Map {
	planes := make([]echo.Map, len(m.Planes))
	for i, p := range m.Planes {
		planes[i] = echo.Map{"id": p.ID, "name": p.Name, "radiusMm": p.Radius, "holeCount": p.HoleCount}
	}
	points := make([]echo.Map, len(m.Points))
	for i, p := range m.Points {
		points[i] = echo.Map{"id": p.ID, "name": p.Name}
	}
	return echo.Map{
		"id": m.ID, "name": m.Name, "planes": planes, "points": points,
		"speedsRpm": m.Speeds,
		"convention": echo.Map{
			"zeroOffsetDeg": m.Convention.ZeroOffsetDeg, "direction": m.Convention.Direction,
		},
		"readingChangeFloorUm": m.ReadingChangeFloorUm,
		"createdAt":            m.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updatedAt":            m.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (s *Server) listMachines(c echo.Context) error {
	ms, err := s.svc.ListMachines(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]echo.Map, len(ms))
	for i, m := range ms {
		out[i] = machineResponse(m)
	}
	return c.JSON(http.StatusOK, echo.Map{"machines": out})
}

func (s *Server) getMachine(c echo.Context) error {
	m, err := s.svc.GetMachine(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, machineResponse(m))
}

func (s *Server) listCoefficients(c echo.Context) error {
	recs, err := s.svc.ListCoefficients(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	out := make([]echo.Map, 0, len(recs))
	for _, r := range recs {
		out = append(out, coefficientResponse(r))
	}
	return c.JSON(http.StatusOK, echo.Map{"machineId": c.Param("id"), "coefficients": out})
}

func coefficientResponse(r store.CurrentCoeffRecord) echo.Map {
	// Flatten the complex matrix into external polar entries [point][plane].
	nPlanes := len(r.PlaneIDs)
	nPoints := len(r.PointIDs)
	cells := make([][]echo.Map, nPoints)
	for p := 0; p < nPoints; p++ {
		cells[p] = make([]echo.Map, nPlanes)
		for a := 0; a < nPlanes; a++ {
			k := p*nPlanes + a
			cells[p][a] = echo.Map{
				"planeId": r.PlaneIDs[a],
				"re":      r.Re[k],
				"im":      r.Im[k],
				"uncert":  r.Uncert[k],
			}
		}
	}
	return echo.Map{
		"speedRpm":  r.Speed,
		"status":    r.Status,
		"planeIds":  r.PlaneIDs,
		"pointIds":  r.PointIDs,
		"entries":   cells,
		"nContrib":  r.NContrib,
		"updatedAt": r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}
