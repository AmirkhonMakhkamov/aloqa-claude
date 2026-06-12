package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/pagination"
)

func setupChannelRepoPostgresTest(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()

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
	return ctx, pool
}

// TestChannelRepoListChannelsReportsMembersCount verifies that both channel
// list queries stamp members_count from the channel_members table so the
// client can render "N members" without a per-channel round-trip.
func TestChannelRepoListChannelsReportsMembersCount(t *testing.T) {
	ctx, pool := setupChannelRepoPostgresTest(t)

	now := time.Now().UTC()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		for _, uid := range userIDs {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, uid)
		}
	})

	for _, uid := range userIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
			VALUES ($1, $2, 'Channel Test User', '', 'hash', 'active', 'en', $3, $3)`,
			uid,
			"channel-test-"+uid.String()+"@example.com",
			now,
		); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Channel Test Workspace', $2, '', $3, $4, $4)`,
		workspaceID,
		"channel-test-"+workspaceID.String(),
		userIDs[0],
		now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO channels (id, workspace_id, name, topic, type, created_by, archived, created_at, updated_at)
		VALUES ($1, $2, 'Channel Test Channel', '', 'public', $3, false, $4, $4)`,
		channelID,
		workspaceID,
		userIDs[0],
		now,
	); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	for _, uid := range userIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO channel_members (id, channel_id, user_id, role, muted_until, last_read_at, joined_at)
			VALUES ($1, $2, $3, 'member', NULL, $4, $4)`,
			uuid.New(),
			channelID,
			uid,
			now,
		); err != nil {
			t.Fatalf("insert channel member: %v", err)
		}
	}

	repo := NewChannelRepo(pool)

	byUser, err := repo.ListByUser(ctx, workspaceID, userIDs[0])
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	assertChannelMembersCount(t, byUser, channelID, 3)

	byWorkspace, err := repo.ListByWorkspace(ctx, workspaceID, pagination.Params{Limit: 100})
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	assertChannelMembersCount(t, byWorkspace, channelID, 3)
}

func assertChannelMembersCount(t *testing.T, channels []entity.Channel, channelID uuid.UUID, want int) {
	t.Helper()

	for _, ch := range channels {
		if ch.ID != channelID {
			continue
		}
		if ch.MembersCount == nil {
			t.Fatalf("expected members_count to be populated for channel %s", channelID)
		}
		if *ch.MembersCount != want {
			t.Fatalf("members_count = %d, want %d", *ch.MembersCount, want)
		}
		return
	}
	t.Fatalf("channel %s not found in list of %d", channelID, len(channels))
}
