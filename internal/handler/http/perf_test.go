package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aloqa/internal/service/perf"
)

type fakePerfService struct {
	labRuns   []perf.LabRun
	rumBatch  perf.RumBatch
	rumCalled bool
	labErr    error
	rumErr    error
}

func (f *fakePerfService) IngestLab(_ context.Context, runs []perf.LabRun) error {
	if f.labErr != nil {
		return f.labErr
	}
	f.labRuns = append(f.labRuns, runs...)
	return nil
}

func (f *fakePerfService) IngestRum(_ context.Context, batch perf.RumBatch) (int, int, error) {
	if f.rumErr != nil {
		return 0, 0, f.rumErr
	}
	f.rumCalled = true
	f.rumBatch = batch
	return len(batch.Events), 0, nil
}

func doPerfReq(handler http.HandlerFunc, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/perf/x", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

const validLabBody = `{"commit":"abc","branch":"develop","profile":"desktop","environment":"mocked","scenario":"common.load","ts":"2026-06-08T12:00:00.500Z","metrics":[{"key":"web-vital.lcp","routeTemplate":"/login","case":"default","value":900,"unit":"ms"}]}`

func TestPerfHandlerIngestLab(t *testing.T) {
	const token = "lab-secret"

	t.Run("unconfigured token returns 503", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, "", "")
		rec := doPerfReq(h.IngestLab, token, validLabBody)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d", rec.Code)
		}
	})

	t.Run("wrong token returns 401", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, token, "")
		rec := doPerfReq(h.IngestLab, "nope", validLabBody)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})

	t.Run("missing token returns 401", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, token, "")
		rec := doPerfReq(h.IngestLab, "", validLabBody)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})

	t.Run("happy single run returns 204", func(t *testing.T) {
		svc := &fakePerfService{}
		h := NewPerfHandler(svc, token, "")
		rec := doPerfReq(h.IngestLab, token, validLabBody)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d (%s)", rec.Code, rec.Body.String())
		}
		if len(svc.labRuns) != 1 {
			t.Fatalf("want 1 run captured, got %d", len(svc.labRuns))
		}
		if svc.labRuns[0].Commit != "abc" || len(svc.labRuns[0].Metrics) != 1 {
			t.Fatalf("run not mapped correctly: %+v", svc.labRuns[0])
		}
	})

	t.Run("happy array of runs returns 204", func(t *testing.T) {
		svc := &fakePerfService{}
		h := NewPerfHandler(svc, token, "")
		rec := doPerfReq(h.IngestLab, token, "["+validLabBody+"]")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d", rec.Code)
		}
		if len(svc.labRuns) != 1 {
			t.Fatalf("want 1 run captured, got %d", len(svc.labRuns))
		}
	})

	t.Run("bad enum returns 400", func(t *testing.T) {
		svc := &fakePerfService{}
		h := NewPerfHandler(svc, token, "")
		bad := strings.Replace(validLabBody, `"profile":"desktop"`, `"profile":"phone"`, 1)
		rec := doPerfReq(h.IngestLab, token, bad)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
		if len(svc.labRuns) != 0 {
			t.Fatalf("expected no service call on invalid input")
		}
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, token, "")
		rec := doPerfReq(h.IngestLab, token, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})
}

const validRumBody = `{"session":"s1","release":"v0.25.0","deviceClass":"desktop","connection":"4g","events":[{"kind":"web-vital","name":"web-vital.lcp","value":1200,"rating":"good","routeTemplate":"/w/:ws"}]}`

func TestPerfHandlerIngestRUM(t *testing.T) {
	const token = "rum-secret"

	t.Run("unconfigured token returns 503", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, "", "")
		rec := doPerfReq(h.IngestRUM, token, validRumBody)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d", rec.Code)
		}
	})

	t.Run("wrong token returns 401", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, "", token)
		rec := doPerfReq(h.IngestRUM, "nope", validRumBody)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})

	t.Run("happy batch returns 204", func(t *testing.T) {
		svc := &fakePerfService{}
		h := NewPerfHandler(svc, "", token)
		rec := doPerfReq(h.IngestRUM, token, validRumBody)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d (%s)", rec.Code, rec.Body.String())
		}
		if !svc.rumCalled || svc.rumBatch.Session != "s1" || len(svc.rumBatch.Events) != 1 {
			t.Fatalf("batch not mapped correctly: %+v", svc.rumBatch)
		}
	})

	t.Run("missing session returns 400", func(t *testing.T) {
		svc := &fakePerfService{}
		h := NewPerfHandler(svc, "", token)
		rec := doPerfReq(h.IngestRUM, token, `{"events":[]}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
		if svc.rumCalled {
			t.Fatalf("expected no service call on invalid input")
		}
	})

	t.Run("invalid json returns 400", func(t *testing.T) {
		h := NewPerfHandler(&fakePerfService{}, "", token)
		rec := doPerfReq(h.IngestRUM, token, "{not json")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("persist error still returns 204 (fire-and-forget)", func(t *testing.T) {
		svc := &fakePerfService{rumErr: errors.New("db down")}
		h := NewPerfHandler(svc, "", token)
		rec := doPerfReq(h.IngestRUM, token, validRumBody)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204 despite persist error, got %d", rec.Code)
		}
	})
}
