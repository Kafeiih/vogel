package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kafeiih/vogel/httpx/response"
)

// Pinger abstracts the database ping operation for health checks.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler serves the public, unauthenticated probe endpoints.
type HealthHandler struct {
	db     Pinger
	logger *slog.Logger
}

// NewHealthHandler builds the probe handlers. logger receives the cause of a
// failed database ping, which the response never carries; nil uses
// slog.Default().
func NewHealthHandler(db Pinger, logger *slog.Logger) *HealthHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthHandler{db: db, logger: logger}
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
		// The endpoint is public: the driver's error names hosts, ports,
		// roles and TLS details, so it is logged here and the caller only
		// learns that the database is down.
		h.logger.ErrorContext(ctx, "health check: database ping failed", "error", err)
		dbStatus = "down"
		dbCheck["status"] = dbStatus
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
		h.logger.ErrorContext(ctx, "readiness check: database ping failed", "error", err)
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
