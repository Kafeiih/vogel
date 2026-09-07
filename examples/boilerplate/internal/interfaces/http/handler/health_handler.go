package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/kafeiih/vogel/httpx/response"
)

// Pinger abstracts the database ping operation for health checks.
type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthHandler struct {
	db Pinger
}

func NewHealthHandler(db Pinger) *HealthHandler {
	return &HealthHandler{db: db}
}

// healthcheckHandler godoc
//
//	@Summary		Deep health check
//	@Description	Verifies service health including database connectivity. Returns 503 if any dependency is unhealthy.
//	@Tags			ops
//	@Produce		json
//	@Success		200	{object}	map[string]any	"All dependencies healthy"
//	@Failure		503	{object}	map[string]any	"One or more dependencies unhealthy"
//	@Router			/healthz [get]
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	dbStatus := "up"
	dbCheck := map[string]any{"status": dbStatus}

	if err := h.db.Ping(ctx); err != nil {
		dbStatus = "down"
		dbCheck["status"] = dbStatus
		dbCheck["error"] = err.Error()
	}

	result := map[string]any{
		"status": "ok",
		"checks": map[string]any{
			"database": dbCheck,
		},
	}

	if dbStatus == "down" {
		result["status"] = "degraded"
		response.JSON(w, http.StatusServiceUnavailable, result)
		return
	}

	response.JSON(w, http.StatusOK, result)
}

// readyzHandler godoc
//
//	@Summary		Readiness check
//	@Description	Indicates whether the service is ready to accept traffic. Used by Kubernetes readiness probes.
//	@Tags			ops
//	@Produce		json
//	@Success		200	{object}	map[string]any	"Service is ready"
//	@Failure		503	{object}	map[string]any	"Service is not ready"
//	@Router			/readyz [get]
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		result := map[string]any{
			"status": "not_ready",
			"reason": "database unavailable",
		}
		response.JSON(w, http.StatusServiceUnavailable, result)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// livezHandler godoc
//
//	@Summary		Liveness check
//	@Description	Indicates whether the process is alive. Used by Kubernetes liveness probes.
//	@Tags			ops
//	@Produce		json
//	@Success		200	{object}	map[string]string	"Process is alive"
//	@Router			/livez [get]
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
}
