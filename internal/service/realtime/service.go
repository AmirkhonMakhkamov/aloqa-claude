package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/event"
	"aloqa/internal/platform/reliability"
)

type Transport interface {
	PublishWithID(ctx context.Context, subject string, data []byte, msgID string) error
}

type Store interface {
	Enqueue(ctx context.Context, evt event.Event, body []byte, maxAttempts int) error
	ClaimPending(ctx context.Context, batchSize int) ([]event.QueuedEvent, error)
	MarkPublished(ctx context.Context, eventID uuid.UUID) error
	MarkFailed(ctx context.Context, eventID uuid.UUID, lastError string, nextRetryAt *time.Time, dead bool) error
	ReplayRoom(ctx context.Context, room string, afterSequence int64, limit int) ([]event.Event, error)
	UpdateConsumerCursor(ctx context.Context, consumerName, streamName string, evt *event.Event, success bool, status, lastError string) error
	// ResetStuckJobs clears locked_at on any processing-status rows whose
	// locked_at is older than stuckAfter, returning them to the pending queue.
	// Call once at worker startup to recover jobs abandoned by a previous crash.
	ResetStuckJobs(ctx context.Context, stuckAfter time.Duration) (int64, error)
}

type Config struct {
	BatchSize        int
	MaxAttempts      int
	RetryBackoff     time.Duration
	ReplayLimit      int
	ConsumerName     string
	ConsumerStream   string
	OperationTimeout time.Duration
}

type Service struct {
	store          Store
	transport      Transport
	batchSize      int
	maxAttempts    int
	retryBackoff   time.Duration
	replayLimit    int
	consumerName   string
	consumerStream string
	opTimeout      time.Duration
	observer       interface {
		RecordRealtimeBatch(processed, failed, dead int, duration time.Duration, err error)
		RecordWorkerHeartbeat(name string)
	}
}

func NewService(store Store, transport Transport, cfg Config) *Service {
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	retryBackoff := cfg.RetryBackoff
	if retryBackoff <= 0 {
		retryBackoff = 3 * time.Second
	}
	replayLimit := cfg.ReplayLimit
	if replayLimit <= 0 {
		replayLimit = 200
	}
	consumerName := cfg.ConsumerName
	if consumerName == "" {
		consumerName = "realtime-outbox"
	}
	consumerStream := cfg.ConsumerStream
	if consumerStream == "" {
		consumerStream = "realtime_events"
	}
	opTimeout := cfg.OperationTimeout
	if opTimeout <= 0 {
		opTimeout = 5 * time.Second
	}

	return &Service{
		store:          store,
		transport:      transport,
		batchSize:      batchSize,
		maxAttempts:    maxAttempts,
		retryBackoff:   retryBackoff,
		replayLimit:    replayLimit,
		consumerName:   consumerName,
		consumerStream: consumerStream,
		opTimeout:      opTimeout,
	}
}

func (s *Service) SetObserver(observer interface {
	RecordRealtimeBatch(processed, failed, dead int, duration time.Duration, err error)
	RecordWorkerHeartbeat(name string)
}) {
	s.observer = observer
}

func (s *Service) Publish(ctx context.Context, subject string, data []byte) error {
	if s.transport == nil && s.store == nil {
		return nil
	}

	evt, body, durable, err := s.normalize(subject, data)
	if err != nil {
		if s.transport == nil {
			return err
		}
		return s.transport.PublishWithID(ctx, subject, data, "")
	}

	if durable {
		if s.store == nil {
			return fmt.Errorf("realtime durable store is not configured")
		}
		return s.store.Enqueue(ctx, *evt, body, s.maxAttempts)
	}
	if s.transport == nil {
		return nil
	}
	return s.transport.PublishWithID(ctx, subject, body, evt.ID.String())
}

func (s *Service) ReplayRoom(ctx context.Context, room string, afterSequence int64, limit int) ([]event.Event, error) {
	if s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = s.replayLimit
	}
	return s.store.ReplayRoom(ctx, room, afterSequence, limit)
}

func (s *Service) RunOutboxWorker(ctx context.Context, interval time.Duration) {
	if s.store == nil || s.transport == nil {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}

	// On startup, release any jobs that were left in "processing" state by a
	// previous worker that crashed before it could finish.
	if n, err := s.store.ResetStuckJobs(ctx, 5*time.Minute); err != nil {
		slog.WarnContext(ctx, "realtime outbox: failed to reset stuck jobs on startup", "error", err)
	} else if n > 0 {
		slog.InfoContext(ctx, "realtime outbox: reset stuck jobs", "count", n)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if s.observer != nil {
			s.observer.RecordWorkerHeartbeat("realtime_outbox")
		}

		if reporter, ok := s.store.(interface{ Pressure() reliability.Pressure }); ok {
			if pressure := reporter.Pressure(); pressure.Saturated {
				slog.WarnContext(ctx, "realtime outbox backpressure active", "utilization", pressure.Utilization, "queued_waiters", pressure.QueuedWaiters)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			}
		}

		startedAt := time.Now()
		processed, failed, dead, err := s.ProcessPending(ctx)
		if s.observer != nil {
			s.observer.RecordRealtimeBatch(processed, failed, dead, time.Since(startedAt), err)
		}
		if err != nil {
			slog.ErrorContext(ctx, "realtime outbox batch failed", "error", err)
		} else if processed > 0 || failed > 0 || dead > 0 {
			slog.InfoContext(ctx, "realtime outbox processed batch", "processed", processed, "failed", failed, "dead", dead)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) ProcessPending(ctx context.Context) (processed, failed, dead int, err error) {
	queued, err := reliability.DoValue(ctx, s.policy(2), func(ctx context.Context) ([]event.QueuedEvent, error) {
		return s.store.ClaimPending(ctx, s.batchSize)
	})
	if err != nil {
		return 0, 0, 0, err
	}
	for _, job := range queued {
		publishErr := s.transport.PublishWithID(ctx, job.Event.Subject, job.Body, job.Event.ID.String())
		if publishErr != nil {
			isDead := job.Attempts >= job.MaxAttempts
			if failErr := reliability.Do(ctx, s.policy(2), func(ctx context.Context) error {
				return s.store.MarkFailed(ctx, job.Event.ID, publishErr.Error(), s.nextRetryAt(job.Attempts, job.MaxAttempts, isDead), isDead)
			}); failErr != nil {
				slog.ErrorContext(ctx, "failed to update realtime event failure state", "event_id", job.Event.ID, "error", failErr)
			}
			_ = s.store.UpdateConsumerCursor(ctx, s.consumerName, s.consumerStream, &job.Event, false, failureStatus(isDead), publishErr.Error())
			if isDead {
				dead++
			} else {
				failed++
			}
			continue
		}
		if err := reliability.Do(ctx, s.policy(2), func(ctx context.Context) error {
			return s.store.MarkPublished(ctx, job.Event.ID)
		}); err != nil {
			return processed, failed, dead, err
		}
		_ = s.store.UpdateConsumerCursor(ctx, s.consumerName, s.consumerStream, &job.Event, true, "active", "")
		processed++
	}
	return processed, failed, dead, nil
}

func (s *Service) RecordConsumerSuccess(ctx context.Context, consumerName, streamName string, evt *event.Event) {
	if s.store == nil {
		return
	}
	if err := s.store.UpdateConsumerCursor(ctx, consumerName, streamName, evt, true, "active", ""); err != nil {
		slog.ErrorContext(ctx, "failed to record realtime consumer success", "consumer", consumerName, "error", err)
	}
}

func (s *Service) RecordConsumerFailure(ctx context.Context, consumerName, streamName string, evt *event.Event, cause error) {
	if s.store == nil {
		return
	}
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	if err := s.store.UpdateConsumerCursor(ctx, consumerName, streamName, evt, false, "degraded", lastError); err != nil {
		slog.ErrorContext(ctx, "failed to record realtime consumer failure", "consumer", consumerName, "error", err)
	}
}

func (s *Service) normalize(subject string, data []byte) (*event.Event, []byte, bool, error) {
	var evt event.Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil, nil, false, err
	}
	normalized, body, durable, err := event.Prepare(subject, evt)
	if err != nil {
		return nil, nil, false, err
	}
	return &normalized, body, durable, nil
}

func (s *Service) nextRetryAt(attempts, maxAttempts int, dead bool) *time.Time {
	if dead || attempts >= maxAttempts {
		return nil
	}
	delay := s.retryBackoff
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay > time.Minute {
			delay = time.Minute
			break
		}
	}
	next := time.Now().UTC().Add(delay)
	return &next
}

func failureStatus(dead bool) string {
	if dead {
		return "failed"
	}
	return "degraded"
}

func (s *Service) policy(maxAttempts int) reliability.Policy {
	return reliability.Policy{
		Timeout:      s.opTimeout,
		MaxAttempts:  maxAttempts,
		RetryBackoff: s.retryBackoff,
		MaxBackoff:   5 * time.Second,
	}
}
