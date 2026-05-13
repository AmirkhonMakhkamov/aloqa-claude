package middleware

import (
	"net/http"
	"testing"
)

// Regression test for the bug where copyHeaders wiped headers set by outer
// middleware (CORS, security headers) before idempotency wrapped the writer.
// Symptom in the field: cross-origin POSTs carrying an Idempotency-Key got
// 201 Created from the backend but the response had no Access-Control-Allow-*
// headers, so the browser blocked the response and the optimistic message
// rendered with a delivery-failed icon despite the server having accepted it.
func TestCopyHeaders_PreservesOuterMiddlewareHeaders(t *testing.T) {
	t.Parallel()

	dst := http.Header{}
	// Headers set by outer middleware (CORS, security) before idempotency
	// took over the writer.
	dst.Set("Access-Control-Allow-Origin", "http://localhost:3000")
	dst.Set("Access-Control-Allow-Credentials", "true")
	dst.Set("X-Content-Type-Options", "nosniff")

	// Headers written by the handler into the idempotency-buffered writer.
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("X-Custom", "from-handler")

	copyHeaders(dst, src)

	if got := dst.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("CORS allow-origin clobbered, got %q", got)
	}
	if got := dst.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("CORS allow-credentials clobbered, got %q", got)
	}
	if got := dst.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("security header clobbered, got %q", got)
	}
	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Fatalf("handler header missing, got %q", got)
	}
	if got := dst.Get("X-Custom"); got != "from-handler" {
		t.Fatalf("handler custom header missing, got %q", got)
	}
}

// Handler-set headers must still win for keys it explicitly defines, so a
// handler retains the ability to override an outer header (e.g. swap a
// Cache-Control set by a generic middleware).
func TestCopyHeaders_HandlerHeadersReplaceForOverlappingKeys(t *testing.T) {
	t.Parallel()

	dst := http.Header{}
	dst.Set("Cache-Control", "no-store")

	src := http.Header{}
	src.Set("Cache-Control", "max-age=60")

	copyHeaders(dst, src)

	if got := dst.Get("Cache-Control"); got != "max-age=60" {
		t.Fatalf("handler should override Cache-Control, got %q", got)
	}
}

// Content-Length is intentionally skipped — net/http computes it from the
// real body length on the outer writer.
func TestCopyHeaders_SkipsContentLength(t *testing.T) {
	t.Parallel()

	dst := http.Header{}
	dst.Set("Content-Length", "999")

	src := http.Header{}
	src.Set("Content-Length", "42")

	copyHeaders(dst, src)

	if got := dst.Get("Content-Length"); got != "999" {
		t.Fatalf("Content-Length must not be propagated from buffered writer, got %q", got)
	}
}
