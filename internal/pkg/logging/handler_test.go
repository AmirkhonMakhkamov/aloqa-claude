package logging

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger builds a logger whose records are captured in buf, wrapped by
// SuppressCanceled on top of an Info-level JSON handler (production config).
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(SuppressCanceled(base))
}

func TestSuppressCanceled_DropsErrorOnCanceledContext(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger.ErrorContext(ctx, "failed to fetch user", "user_id", "abc")

	if buf.Len() != 0 {
		t.Fatalf("expected canceled-context ERROR to be suppressed, got: %s", buf.String())
	}
}

func TestSuppressCanceled_DropsErrorWrappingContextCanceled(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	wrapped := fmt.Errorf("postgres: get user by id: %w", context.Canceled)
	// Background context (not canceled) — detection must work via the error attr.
	logger.Error("failed to fetch user", "error", wrapped)

	if buf.Len() != 0 {
		t.Fatalf("expected error wrapping context.Canceled to be suppressed, got: %s", buf.String())
	}
}

// DeadlineExceeded is a server-side timeout (slow/dead dependency), not a
// client disconnect — it must keep alerting at ERROR.
func TestSuppressCanceled_KeepsDeadlineExceeded(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	wrapped := fmt.Errorf("postgres: batch unread counts: %w", context.DeadlineExceeded)
	logger.Error("failed to batch unread counts", "error", wrapped)

	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("expected DeadlineExceeded to remain an ERROR, got: %q", buf.String())
	}
}

func TestSuppressCanceled_KeepsGenuineError(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	logger.Error("failed to check workspace membership", "error", fmt.Errorf("connection refused"))

	out := buf.String()
	if !strings.Contains(out, "failed to check workspace membership") {
		t.Fatalf("expected genuine ERROR to be logged, got: %q", out)
	}
	if !strings.Contains(out, `"level":"ERROR"`) {
		t.Fatalf("expected ERROR level preserved for genuine error, got: %q", out)
	}
}

func TestSuppressCanceled_KeepsInfoLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	logger.Info("user registered", "user_id", "abc")

	if !strings.Contains(buf.String(), "user registered") {
		t.Fatalf("expected INFO log to pass through, got: %q", buf.String())
	}
}

// WithAttrs / WithGroup must return a wrapped handler so derived loggers keep
// the suppression behaviour.
func TestSuppressCanceled_SurvivesWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf).With("component", "chat")

	wrapped := fmt.Errorf("postgres: get workspace member: %w", context.Canceled)
	logger.Error("failed to check workspace membership", "error", wrapped)

	if buf.Len() != 0 {
		t.Fatalf("expected suppression to survive With(), got: %s", buf.String())
	}
}

func TestSuppressCanceled_SurvivesWithGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf).WithGroup("db")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger.ErrorContext(ctx, "failed to check workspace membership")

	if buf.Len() != 0 {
		t.Fatalf("expected suppression to survive WithGroup(), got: %s", buf.String())
	}
}

// On a canceled request a genuine, unrelated server fault must still surface —
// a client disconnect must not blind us to real errors firing concurrently.
func TestSuppressCanceled_KeepsGenuineErrorOnCanceledContext(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger.ErrorContext(ctx, "failed to check workspace membership", "error", fmt.Errorf("connection refused"))

	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("expected genuine error on canceled context to stay ERROR, got: %q", buf.String())
	}
}

// When the base handler is verbose (DEBUG), suppressed records survive as DEBUG
// breadcrumbs rather than being dropped.
func TestSuppressCanceled_PreservedAtDebugBase(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(SuppressCanceled(base))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger.ErrorContext(ctx, "failed to fetch user")

	out := buf.String()
	if !strings.Contains(out, `"level":"DEBUG"`) {
		t.Fatalf("expected suppressed record preserved at DEBUG when base is verbose, got: %q", out)
	}
	if !strings.Contains(out, "failed to fetch user") {
		t.Fatalf("expected message preserved in DEBUG breadcrumb, got: %q", out)
	}
}

func TestSuppressCanceled_EnabledDelegatesToInner(t *testing.T) {
	var buf bytes.Buffer
	h := SuppressCanceled(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Enabled(Debug) should be false for an Info-level base handler")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Enabled(Error) should be true for an Info-level base handler")
	}
}
