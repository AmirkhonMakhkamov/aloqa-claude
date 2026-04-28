package reliability

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
)

type Policy struct {
	Timeout      time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
	MaxBackoff   time.Duration
}

type Pressure struct {
	Utilization   float64 `json:"utilization"`
	Saturated     bool    `json:"saturated"`
	QueuedWaiters int64   `json:"queued_waiters"`
}

func (p Policy) normalized() Policy {
	if p.Timeout <= 0 {
		p.Timeout = 5 * time.Second
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	if p.RetryBackoff <= 0 {
		p.RetryBackoff = 200 * time.Millisecond
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 5 * time.Second
	}
	return p
}

func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func Do(ctx context.Context, policy Policy, fn func(context.Context) error) error {
	_, err := DoValue[struct{}](ctx, policy, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}

func DoValue[T any](ctx context.Context, policy Policy, fn func(context.Context) (T, error)) (T, error) {
	policy = policy.normalized()
	var zero T
	var lastErr error

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptCtx, cancel := WithTimeout(ctx, policy.Timeout)
		value, err := fn(attemptCtx)
		cancel()
		if err == nil {
			return value, nil
		}
		lastErr = err
		if !ShouldRetry(err) || attempt == policy.MaxAttempts || ctx.Err() != nil {
			break
		}
		if sleepErr := Sleep(ctx, backoffForAttempt(policy, attempt)); sleepErr != nil {
			lastErr = errors.Join(err, sleepErr)
			break
		}
	}

	return zero, lastErr
}

func Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ShouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03", "53300", "57P03":
			return true
		}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"):
		return true
	case strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "too many clients"),
		strings.Contains(msg, "pool timeout"),
		strings.Contains(msg, "readonly"),
		strings.Contains(msg, "tryagain"),
		strings.Contains(msg, "loading"):
		return true
	default:
		return false
	}
}

func backoffForAttempt(policy Policy, attempt int) time.Duration {
	delay := policy.RetryBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= policy.MaxBackoff {
			return policy.MaxBackoff
		}
	}
	return delay
}

// SafeGo spawns fn in a goroutine that recovers from panics and logs them.
// Use for fire-and-forget goroutines where a restart is not appropriate
// (per-connection pumps, per-track forwarders, dispatch fan-outs). For
// long-running workers that should restart on failure, use Supervise.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panicked", "name", name, "panic", r)
			}
		}()
		fn()
	}()
}

// Supervise runs fn in a goroutine and automatically restarts it if it panics
// or returns, using exponential backoff between restarts. It respects context
// cancellation and logs all recovery events.
func Supervise(ctx context.Context, name string, fn func(ctx context.Context)) {
	go func() {
		const (
			baseDelay = time.Second
			maxDelay  = 30 * time.Second
		)
		attempt := 0
		for {
			if ctx.Err() != nil {
				return
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("supervised worker panicked, restarting",
							"worker", name, "panic", r, "attempt", attempt)
					}
				}()
				fn(ctx)
			}()

			// fn returned normally or panicked — restart unless context is done.
			if ctx.Err() != nil {
				return
			}
			attempt++
			delay := baseDelay
			for i := 1; i < attempt && delay < maxDelay; i++ {
				delay *= 2
			}
			if delay > maxDelay {
				delay = maxDelay
			}
			slog.Warn("supervised worker exited, restarting after backoff",
				"worker", name, "attempt", attempt, "backoff", delay)

			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
}
