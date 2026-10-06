// Package api exposes the balancing service over HTTP with Echo.
package api

import (
	"net/http"

	"balancer/internal/service"

	"github.com/labstack/echo/v4"
)

// Server wires the service to Echo routes.
type Server struct {
	svc *service.Service
}

// NewServer builds the Echo instance with all routes registered.
func NewServer(svc *service.Service) *echo.Echo {
	s := &Server{svc: svc}
	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = errorHandler

	e.GET("/healthz", s.health)

	api := e.Group("/api/v1")

	api.POST("/machines", s.createMachine)
	api.GET("/machines", s.listMachines)
	api.GET("/machines/:id", s.getMachine)
	api.GET("/machines/:id/coefficients", s.listCoefficients)

	api.POST("/jobs", s.createJob)
	api.GET("/jobs/:id", s.getJob)
	api.POST("/jobs/:id/runs", s.addRun)
	api.POST("/jobs/:id/runs/:runId/corrections", s.correctRun)
	api.POST("/jobs/:id/finalize", s.finalize)

	return e
}

func (s *Server) health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	if rj, ok := service.IsRejection(err); ok {
		_ = c.JSON(rj.StatusCode, echo.Map{
			"error":  rj.Message,
			"fields": rj.Fields,
		})
		return
	}
	if he, ok := err.(*echo.HTTPError); ok {
		msg := http.StatusText(he.Code)
		if m, ok := he.Message.(string); ok {
			msg = m
		}
		_ = c.JSON(he.Code, echo.Map{"error": msg})
		return
	}
	_ = c.JSON(http.StatusInternalServerError, echo.Map{"error": err.Error()})
}
