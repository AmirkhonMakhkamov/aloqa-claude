package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestMetricsCollectorConcurrent(t *testing.T) {
	c := NewRequestMetricsCollector()

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 1000

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			methods := []string{"GET", "POST", "PUT", "DELETE"}
			statuses := []int{200, 201, 400, 404, 500}
			for i := 0; i < iterations; i++ {
				c.record(methods[i%len(methods)], statuses[i%len(statuses)], time.Duration(i)*time.Microsecond)
			}
		}(g)
	}
	wg.Wait()

	text := c.PrometheusText("test")
	if text == "" {
		t.Fatal("expected non-empty prometheus text")
	}
	if !strings.Contains(text, "test_http_requests_total{status=\"200\"}") {
		t.Errorf("missing status counter in output:\n%s", text)
	}
	if !strings.Contains(text, "test_http_request_duration_seconds_count") {
		t.Errorf("missing histogram count in output:\n%s", text)
	}
	if !strings.Contains(text, "test_http_request_duration_seconds_sum") {
		t.Errorf("missing histogram sum in output:\n%s", text)
	}
}

func TestRequestMetricsMiddleware(t *testing.T) {
	c := NewRequestMetricsCollector()
	handler := RequestMetrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest("POST", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	text := c.PrometheusText("app")
	if !strings.Contains(text, `app_http_requests_total{status="201"} 1`) {
		t.Errorf("expected status 201 counter of 1, got:\n%s", text)
	}
	if !strings.Contains(text, `app_http_requests_total{method="POST"} 1`) {
		t.Errorf("expected POST counter of 1, got:\n%s", text)
	}
}

func TestRequestMetricsCollectorHistogramBucketsAreMonotonic(t *testing.T) {
	c := NewRequestMetricsCollector()
	c.record("GET", http.StatusOK, 4*time.Millisecond)
	c.record("GET", http.StatusOK, 2*time.Second)
	c.record("GET", http.StatusOK, 6*time.Second)

	text := c.PrometheusText("app")
	var previous float64
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, `app_http_request_duration_seconds_bucket`) {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("expected metric and value, got %q", line)
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			t.Fatalf("parse bucket value %q: %v", parts[1], err)
		}
		if value < previous {
			t.Fatalf("histogram buckets must be monotonic, got %f after %f in:\n%s", value, previous, text)
		}
		previous = value
	}

	if !strings.Contains(text, `app_http_request_duration_seconds_bucket{le="0.005"} 1`) {
		t.Fatalf("expected 5ms bucket to contain only the fastest request, got:\n%s", text)
	}
	if !strings.Contains(text, `app_http_request_duration_seconds_bucket{le="+Inf"} 3`) {
		t.Fatalf("expected +Inf bucket to match total request count, got:\n%s", text)
	}
}

func BenchmarkRequestMetricsRecord(b *testing.B) {
	c := NewRequestMetricsCollector()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.record("GET", 200, 5*time.Millisecond)
		}
	})
}

func BenchmarkRequestMetricsPrometheusText(b *testing.B) {
	c := NewRequestMetricsCollector()
	// Seed with realistic data.
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	statuses := []int{200, 201, 204, 301, 400, 401, 403, 404, 500}
	for i := 0; i < 10000; i++ {
		c.record(methods[i%len(methods)], statuses[i%len(statuses)], time.Duration(i)*time.Microsecond)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.PrometheusText("aloqa")
	}
}
