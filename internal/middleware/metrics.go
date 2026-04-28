package middleware

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// RequestMetricsCollector aggregates HTTP request counts and latency for
// Prometheus-style exposition. It is safe for concurrent use.
type RequestMetricsCollector struct {
	mu             sync.Mutex
	statusCounts   map[int]int64
	methodCounts   map[string]int64
	bucketCounts   map[float64]int64 // upper bound (seconds) → cumulative count
	latencySum     float64
	latencyCount   int64
	bucketBounds   []float64
}

// NewRequestMetricsCollector creates a collector with default histogram buckets.
func NewRequestMetricsCollector() *RequestMetricsCollector {
	bounds := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	buckets := make(map[float64]int64, len(bounds))
	for _, b := range bounds {
		buckets[b] = 0
	}
	return &RequestMetricsCollector{
		statusCounts: make(map[int]int64),
		methodCounts: make(map[string]int64),
		bucketCounts: buckets,
		bucketBounds: bounds,
	}
}

func (c *RequestMetricsCollector) record(method string, status int, d time.Duration) {
	secs := d.Seconds()
	c.mu.Lock()
	c.statusCounts[status]++
	c.methodCounts[method]++
	c.latencySum += secs
	c.latencyCount++
	for _, b := range c.bucketBounds {
		if secs <= b {
			c.bucketCounts[b]++
		}
	}
	c.mu.Unlock()
}

// PrometheusText returns the collected metrics in Prometheus text exposition format.
func (c *RequestMetricsCollector) PrometheusText(ns string) string {
	c.mu.Lock()
	// Snapshot under lock.
	statusCounts := make(map[int]int64, len(c.statusCounts))
	for k, v := range c.statusCounts {
		statusCounts[k] = v
	}
	methodCounts := make(map[string]int64, len(c.methodCounts))
	for k, v := range c.methodCounts {
		methodCounts[k] = v
	}
	bucketCounts := make(map[float64]int64, len(c.bucketCounts))
	for k, v := range c.bucketCounts {
		bucketCounts[k] = v
	}
	latencySum := c.latencySum
	latencyCount := c.latencyCount
	c.mu.Unlock()

	var b strings.Builder

	// Request count by status code.
	codes := make([]int, 0, len(statusCounts))
	for code := range statusCounts {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		fmt.Fprintf(&b, "%s_http_requests_total{status=\"%d\"} %d\n", ns, code, statusCounts[code])
	}

	// Request count by method.
	methods := make([]string, 0, len(methodCounts))
	for m := range methodCounts {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	for _, m := range methods {
		fmt.Fprintf(&b, "%s_http_requests_total{method=\"%s\"} %d\n", ns, m, methodCounts[m])
	}

	// Latency histogram.
	cumulative := int64(0)
	for _, bound := range c.bucketBounds {
		cumulative += bucketCounts[bound]
		fmt.Fprintf(&b, "%s_http_request_duration_seconds_bucket{le=\"%g\"} %d\n", ns, bound, cumulative)
	}
	fmt.Fprintf(&b, "%s_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", ns, latencyCount)
	fmt.Fprintf(&b, "%s_http_request_duration_seconds_sum %f\n", ns, latencySum)
	fmt.Fprintf(&b, "%s_http_request_duration_seconds_count %d\n", ns, latencyCount)

	return b.String()
}

// RequestMetrics returns middleware that records request status codes and latency.
func RequestMetrics(collector *RequestMetricsCollector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			collector.record(r.Method, rec.status, time.Since(start))
		})
	}
}
