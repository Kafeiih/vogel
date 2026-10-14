package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
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

	// dbDown is the database state the last ping observed, shared by every
	// probe. Logging only when it flips keeps a caller hammering the public
	// probes during an outage from flooding the log with one ERROR per
	// request.
	dbDown atomic.Bool
}

// NewHealthHandler builds the probe handlers. logger receives the cause of a
// failed database ping, which the response never carries, once per outage:
// at Error when the database becomes unreachable and at Info when it answers
// again. nil uses slog.Default().
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

	if err := h.ping(ctx, "health check"); err != nil {
		// The endpoint is public: the driver's error names hosts, ports,
		// roles and TLS details, so ping logs it and the caller only learns
		// that the database is down.
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

	if err := h.ping(ctx, "readiness check"); err != nil {
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

// ping checks the database and logs only state transitions: the first
// failure after a healthy state at Error (with the cause), the first success
// after a failure at Info. probe names the endpoint that observed the change.
func (h *HealthHandler) ping(ctx context.Context, probe string) error {
	err := h.db.Ping(ctx)
	if err != nil {
		if h.dbDown.CompareAndSwap(false, true) {
			h.logger.ErrorContext(ctx, probe+": database ping failed", "error", err)
		}
		return err
	}
	if h.dbDown.CompareAndSwap(true, false) {
		h.logger.InfoContext(ctx, probe+": database reachable again")
	}
	return nil
}
