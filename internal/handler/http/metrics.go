package http

import (
	"context"
	"net/http"
)

// HTTPMetricsProvider supplies Prometheus-formatted HTTP request metrics.
type HTTPMetricsProvider interface {
	PrometheusText(namespace string) string
}

type MetricsHandler struct {
	svc interface {
		Metrics(ctx context.Context) (string, error)
	}
	httpMetrics HTTPMetricsProvider
	namespace   string
}

func NewMetricsHandler(svc interface {
	Metrics(ctx context.Context) (string, error)
}, opts ...MetricsOption) *MetricsHandler {
	h := &MetricsHandler{svc: svc}
	for _, o := range opts {
		o(h)
	}
	return h
}

// MetricsOption configures the MetricsHandler.
type MetricsOption func(*MetricsHandler)

// WithHTTPMetrics attaches an HTTP request metrics provider.
func WithHTTPMetrics(p HTTPMetricsProvider, namespace string) MetricsOption {
	return func(h *MetricsHandler) {
		h.httpMetrics = p
		h.namespace = namespace
	}
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := h.svc.Metrics(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.httpMetrics != nil {
		body += h.httpMetrics.PrometheusText(h.namespace)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(body))
}
