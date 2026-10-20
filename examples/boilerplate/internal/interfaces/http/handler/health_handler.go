package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kafeiih/vogel/httpx/response"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthHandler struct {
	db     Pinger
	logger *slog.Logger

	checkTimeout time.Duration
	readyTimeout time.Duration

	mu      sync.Mutex
	dbCause string
}

func NewHealthHandler(db Pinger, logger *slog.Logger) *HealthHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthHandler{db: db, logger: logger, checkTimeout: 5 * time.Second, readyTimeout: 3 * time.Second}
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
	dbStatus := "up"
	dbCheck := map[string]any{"status": dbStatus}

	err := h.ping(r.Context(), h.checkTimeout, "health check")
	if err != nil {
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
	err := h.ping(r.Context(), h.readyTimeout, "readiness check")
	if err != nil {
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

func (h *HealthHandler) ping(caller context.Context, timeout time.Duration, probe string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(caller), timeout)
	defer cancel()

	err := h.db.Ping(ctx)

	cause := ""
	if err != nil {
		cause = err.Error()
	}

	h.mu.Lock()
	prev := h.dbCause
	h.dbCause = cause
	h.mu.Unlock()

	switch {
	case cause == prev:
	case err != nil:
		h.logger.ErrorContext(ctx, probe+": database ping failed", "error", err)
	default:
		h.logger.InfoContext(ctx, probe+": database reachable again")
	}
	return err
}
