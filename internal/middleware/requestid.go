package middleware

import (
	"context"
	"net/http"
	"regexp"

	"aloqa/internal/pkg/id"
)

const (
	requestIDKey    contextKey = "request_id"
	maxRequestIDLen int        = 128
)

// validRequestID allows only printable ASCII without control characters.
// This prevents log injection via newlines, null bytes, or unicode tricks.
var validRequestID = regexp.MustCompile(`^[a-zA-Z0-9\-_.~:]+$`)

// RequestID injects a unique request ID into the context and response header.
// Client-supplied IDs are accepted only if they are short, printable, and safe
// for logging; otherwise a fresh UUIDv7 is generated.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" || len(reqID) > maxRequestIDLen || !validRequestID.MatchString(reqID) {
			reqID = id.New().String()
		}

		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request ID from context.
func RequestIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}
