package cerrors

import (
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
