package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
)

func TestCallRepoClaimLiveKitWebhookEventIsDurable(t *testing.T) {
	dsn := os.Getenv("ALOQA_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set ALOQA_POSTGRES_TEST_DSN to a disposable migrated Postgres database to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewCallRepo(pool)
	eventID := "test-livekit-" + uuid.New().String()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM call_livekit_webhook_events WHERE event_id = $1`, eventID)
	})
	event := &entity.LiveKitWebhookEvent{
		EventID:    eventID,
		CallID:     uuid.New(),
		EventType:  "participant_left",
		ReceivedAt: time.Now().UTC(),
	}

	claimed, err := repo.ClaimLiveKitWebhookEvent(ctx, event)
	if err != nil {
		t.Fatalf("first ClaimLiveKitWebhookEvent returned error: %v", err)
	}
	if !claimed {
		t.Fatalf("first ClaimLiveKitWebhookEvent claimed = false, want true")
	}

	secondRepo := NewCallRepo(pool)
	claimed, err = secondRepo.ClaimLiveKitWebhookEvent(ctx, event)
	if err != nil {
		t.Fatalf("second ClaimLiveKitWebhookEvent returned error: %v", err)
	}
	if claimed {
		t.Fatalf("second ClaimLiveKitWebhookEvent claimed = true, want false")
	}

	if err := repo.ReleaseLiveKitWebhookEvent(ctx, eventID); err != nil {
		t.Fatalf("ReleaseLiveKitWebhookEvent returned error: %v", err)
	}
	claimed, err = secondRepo.ClaimLiveKitWebhookEvent(ctx, event)
	if err != nil {
		t.Fatalf("claim after release returned error: %v", err)
	}
	if !claimed {
		t.Fatalf("claim after release = false, want true")
	}
}
