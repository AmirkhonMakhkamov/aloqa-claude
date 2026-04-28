package http

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// ReadinessChecker provides dependency health checks for the readiness probe.
type ReadinessChecker interface {
	Ping(ctx context.Context) error
}

// readyFlag is set to 1 once the server signals that all dependencies are
// initialised and ready to serve traffic.
var readyFlag atomic.Int32

// SetReady marks the server as ready to receive traffic. Call this from main
// after all dependency initialisation succeeds.
func SetReady() { readyFlag.Store(1) }

func healthCheck(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]string{"status": "ok"})
}

// ReadinessCheck returns 200 only when the server has flagged itself as ready.
// Kubernetes/load balancers should use this instead of /healthz.
func ReadinessCheck(checkers ...ReadinessChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if readyFlag.Load() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		for _, c := range checkers {
			if err := c.Ping(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"status": "degraded",
					"error":  "dependency check failed",
				})
				return
			}
		}

		writeOK(w, map[string]string{"status": "ready"})
	}
}
