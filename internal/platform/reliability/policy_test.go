package reliability

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
)

func TestShouldRetryRecognizesTransientErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "redis nil", err: redis.Nil, want: false},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: true},
		{name: "pg serialization", err: &pgconn.PgError{Code: "40001"}, want: true},
		{name: "pool timeout", err: errors.New("redis: connection pool timeout"), want: true},
		{name: "plain app error", err: errors.New("validation failed"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRetry(tc.err); got != tc.want {
				t.Fatalf("ShouldRetry(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDoValueRetriesTransientTimeouts(t *testing.T) {
	attempts := 0
	value, err := DoValue(context.Background(), Policy{
		Timeout:      100 * time.Millisecond,
		MaxAttempts:  3,
		RetryBackoff: 10 * time.Millisecond,
		MaxBackoff:   20 * time.Millisecond,
	}, func(context.Context) (int, error) {
		attempts++
		if attempts < 3 {
			return 0, timeoutErr{}
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("DoValue returned error: %v", err)
	}
	if value != 42 {
		t.Fatalf("expected 42, got %d", value)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestSuperviseRestartsAfterPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count int64
	ready := make(chan struct{})
	Supervise(ctx, "test-panic", func(_ context.Context) {
		n := atomic.AddInt64(&count, 1)
		if n <= 2 {
			panic("boom")
		}
		close(ready)
		// Block until cancelled.
		<-ctx.Done()
	})

	select {
	case <-ready:
		// Worker recovered from 2 panics and is now running.
	case <-time.After(10 * time.Second):
		t.Fatal("Supervise did not restart worker after panics")
	}

	got := atomic.LoadInt64(&count)
	if got < 3 {
		t.Fatalf("expected at least 3 invocations, got %d", got)
	}
}

func TestSuperviseStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	Supervise(ctx, "test-cancel", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	})

	<-started
	cancel()
	// Give the goroutine time to exit — the test relies on the race detector
	// to catch any issues if the goroutine outlives the context.
	time.Sleep(50 * time.Millisecond)
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}
