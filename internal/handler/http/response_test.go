package http

import (
	"net/http/httptest"
	"strings"
	"testing"

	"aloqa/internal/pkg/cerrors"
)

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
