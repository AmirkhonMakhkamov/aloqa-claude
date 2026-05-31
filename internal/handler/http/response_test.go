package http

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"aloqa/internal/pkg/cerrors"
)

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
