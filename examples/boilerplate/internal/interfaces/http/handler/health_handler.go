package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
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

	// checkTimeout and readyTimeout bound the database ping of /healthz and
	// /readyz. They are fields only so tests can shorten them.
	checkTimeout time.Duration
	readyTimeout time.Duration

	// mu guards dbCause, the error text of the last failed ping, or "" while
	// the database answers. It is shared by every probe: logging only when
	// it changes keeps a caller hammering the public probes during an
	// outage from flooding the log with one ERROR per request.
	mu      sync.Mutex
	dbCause string
}

// NewHealthHandler builds the probe handlers. logger receives the cause of a
// failed database ping, which the response never carries, once per distinct
// cause: at Error when the database becomes unreachable or starts failing
// differently, and at Info when it answers again. nil uses slog.Default().
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

// ping checks the database within timeout and logs only changes: a failure
// whose cause differs from the previous one (including the first after a
// healthy state) at Error, the first success after a failure at Info. probe
// names the endpoint that observed the change.
//
// The ping runs on its own timeout, detached from the caller's cancellation
// and deadline, so its outcome always describes the database and is always
// recorded. Letting the caller cut it short would make the result ambiguous:
// the kubelet enforces a probe's timeoutSeconds by closing the connection,
// which the server sees as a canceled request, so a database that stalls
// under kubelet probes would never be logged if a canceled caller were
// treated as "nothing to report". Likewise a client that disconnects early
// against a healthy database cannot open an outage in the log, because the
// detached ping still succeeds. A ping abandoned by its caller costs at most
// timeout, the same bound every probe already has.
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
