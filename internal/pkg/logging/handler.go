// Package logging provides slog handler decorators shared across the service
// entrypoints (server, recording-worker, search-reindex).
package logging

import (
	"context"
	"errors"
	"log/slog"
)

// SuppressCanceled wraps a slog.Handler so that WARN/ERROR records caused by
// request-context cancellation are demoted to DEBUG.
//
// When a browser client disconnects mid-request (navigation, tab close, reload)
// the request context is canceled and any in-flight Postgres queries return
// context.Canceled. Services log that at ERROR (via slog.ErrorContext) and the
// request finishes as a 500, both of which a downstream log shipper forwards to
// alerting. Client cancellation is normal behaviour — not a server fault — so
// demoting these records keeps a breadcrumb available under verbose (DEBUG)
// logging while dropping them from production ERROR/WARN sinks.
//
// A record is treated as cancellation noise when either the log context is
// already canceled, or any attribute carries an error that unwraps to
// context.Canceled. context.DeadlineExceeded is deliberately left alone: it
// signals a server-side timeout (slow/dead dependency) worth alerting on.
func SuppressCanceled(h slog.Handler) slog.Handler {
	return &canceledHandler{inner: h}
}

type canceledHandler struct {
	inner slog.Handler
}

func (h *canceledHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *canceledHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && isCancellation(ctx, r) {
		r.Level = slog.LevelDebug
		if !h.inner.Enabled(ctx, slog.LevelDebug) {
			return nil
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *canceledHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &canceledHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *canceledHandler) WithGroup(name string) slog.Handler {
	return &canceledHandler{inner: h.inner.WithGroup(name)}
}

func isCancellation(ctx context.Context, r slog.Record) bool {
	if isCanceled(ctx.Err()) {
		return true
	}

	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() != slog.KindAny {
			return true
		}
		if err, ok := a.Value.Any().(error); ok && isCanceled(err) {
			found = true
			return false // stop iterating
		}
		return true
	})
	return found
}

func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}
