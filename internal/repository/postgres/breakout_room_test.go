package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBreakoutRoomRepoEmptyListsReturnNonNilSlices(t *testing.T) {
	dsn := os.Getenv("ALOQA_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set ALOQA_POSTGRES_TEST_DSN to a disposable migrated Postgres database to run this integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	repo := NewBreakoutRoomRepo(pool)

	rooms, err := repo.ListByCall(ctx, uuid.New())
	if err != nil {
		t.Fatalf("ListByCall returned error: %v", err)
	}
	if rooms == nil {
		t.Fatalf("ListByCall returned nil slice, want empty slice")
	}
	if len(rooms) != 0 {
		t.Fatalf("ListByCall len = %d, want 0", len(rooms))
	}

	participants, err := repo.ListParticipants(ctx, uuid.New())
	if err != nil {
		t.Fatalf("ListParticipants returned error: %v", err)
	}
	if participants == nil {
		t.Fatalf("ListParticipants returned nil slice, want empty slice")
	}
	if len(participants) != 0 {
		t.Fatalf("ListParticipants len = %d, want 0", len(participants))
	}
}
