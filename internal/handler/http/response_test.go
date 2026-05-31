package http

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"aloqa/internal/pkg/cerrors"
)

// brokenPipeWriter fails every Write with a broken-pipe error, simulating a
// client that disconnected mid-response.
type brokenPipeWriter struct {
	header http.Header
}

func (b *brokenPipeWriter) Header() http.Header {
	if b.header == nil {
		b.header = make(http.Header)
	}
	return b.header
}
func (b *brokenPipeWriter) WriteHeader(int) {}
func (b *brokenPipeWriter) Write([]byte) (int, error) {
	return 0, &net.OpError{Op: "write", Err: syscall.EPIPE}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestWriteJSON_ClientDisconnectNotLoggedAsError(t *testing.T) {
	buf := captureLogs(t)
	writeJSON(&brokenPipeWriter{}, http.StatusOK, map[string]string{"hello": "world"})

	if strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("client disconnect during encode must not log ERROR, got: %q", buf.String())
	}
}

func TestWriteJSON_GenuineEncodeErrorLogsError(t *testing.T) {
	buf := captureLogs(t)
	// A channel cannot be JSON-encoded: a real programmer error, must stay loud.
	writeJSON(httptest.NewRecorder(), http.StatusOK, make(chan int))

	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("genuine encode failure must log ERROR, got: %q", buf.String())
	}
}

func TestWriteErr_ContextCanceledMapsTo499(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, fmt.Errorf("postgres: get user by id: %w", context.Canceled))

	if rec.Code != cerrors.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d (client closed request)", rec.Code, cerrors.StatusClientClosedRequest)
	}
}

// Services wrap context cancellation in cerrors.Internal; the HTTP layer must
// still resolve it to 499, not 500, so it does not inflate the 5xx error rate.
func TestWriteErr_InternalWrappingCanceledMapsTo499(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, cerrors.Internal("failed to fetch user", context.Canceled))

	if rec.Code != cerrors.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d (client closed request)", rec.Code, cerrors.StatusClientClosedRequest)
	}
}

// A server-side deadline is a real fault (slow/dead dependency), so it must
// stay 5xx, not be downgraded to 499.
func TestWriteErr_DeadlineExceededStays500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, cerrors.Internal("query timed out", context.DeadlineExceeded))

	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 for server-side deadline", rec.Code)
	}
}

func TestWriteErr_GenuineInternalStays500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, cerrors.Internal("boom", fmt.Errorf("disk full")))

	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 for genuine internal error", rec.Code)
	}
}

func TestDecodeJSONRejectsEmptyBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	var body struct {
		Name string `json:"name"`
	}

	err := decodeJSON(req, &body)
	if err == nil {
		t.Fatalf("expected an error for empty body")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok || appErr.Code != cerrors.CodeInvalidInput {
		t.Fatalf("expected invalid input error, got %v", err)
	}
}

func TestDecodeJSONRejectsTrailingJSONValue(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"first"}{"name":"second"}`))
	var body struct {
		Name string `json:"name"`
	}

	err := decodeJSON(req, &body)
	if err == nil {
		t.Fatalf("expected an error for multiple JSON values")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok || appErr.Code != cerrors.CodeInvalidInput {
		t.Fatalf("expected invalid input error, got %v", err)
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"first","unknown":true}`))
	var body struct {
		Name string `json:"name"`
	}

	err := decodeJSON(req, &body)
	if err == nil {
		t.Fatalf("expected an error for unknown field")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok || appErr.Code != cerrors.CodeInvalidInput {
		t.Fatalf("expected invalid input error, got %v", err)
	}
}
