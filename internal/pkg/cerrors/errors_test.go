package cerrors

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestCallEnded_ProducesCodeAndMessage(t *testing.T) {
	err := CallEnded("call has already ended")
	if err == nil {
		t.Fatal("CallEnded returned nil")
	}
	if err.Code != CodeCallEnded {
		t.Errorf("code = %q, want %q", err.Code, CodeCallEnded)
	}
	if err.Message != "call has already ended" {
		t.Errorf("message = %q, want %q", err.Message, "call has already ended")
	}
}

func TestCallEnded_HTTPStatusGone(t *testing.T) {
	err := CallEnded("call has already ended")
	if got, want := err.HTTPStatus(), http.StatusGone; got != want {
		t.Errorf("HTTPStatus = %d, want %d (StatusGone)", got, want)
	}
}

func TestCodeCallEnded_StringValue(t *testing.T) {
	if CodeCallEnded != Code("CALL_ENDED") {
		t.Errorf("CodeCallEnded = %q, want CALL_ENDED", CodeCallEnded)
	}
}

func TestCanceled_HTTPStatusClientClosed(t *testing.T) {
	err := Canceled("request canceled")
	if got, want := err.HTTPStatus(), StatusClientClosedRequest; got != want {
		t.Errorf("HTTPStatus = %d, want %d (StatusClientClosedRequest)", got, want)
	}
	if err.Code != CodeCanceled {
		t.Errorf("code = %q, want %q", err.Code, CodeCanceled)
	}
}

func TestStatusClientClosedRequest_Is499(t *testing.T) {
	if StatusClientClosedRequest != 499 {
		t.Errorf("StatusClientClosedRequest = %d, want 499", StatusClientClosedRequest)
	}
}

// Internal errors that wrap a context cancellation must remain discoverable via
// errors.Is so the HTTP layer can map them to 499 instead of 500.
func TestInternal_UnwrapsContextCanceled(t *testing.T) {
	err := Internal("failed to fetch user", fmt.Errorf("postgres: get user by id: %w", context.Canceled))
	if !IsContextCanceled(err) {
		t.Fatalf("expected wrapped context.Canceled to be discoverable via errors.Is")
	}
	if IsContextCanceled(Internal("real failure", fmt.Errorf("connection refused"))) {
		t.Fatalf("genuine error must not be classified as a context cancellation")
	}
	if IsContextCanceled(fmt.Errorf("query: %w", context.DeadlineExceeded)) {
		t.Fatalf("DeadlineExceeded is a server-side timeout, not a client cancellation")
	}
}
