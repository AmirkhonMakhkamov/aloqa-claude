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

// TestGuestAccessCallIDRoundTrip verifies that a call-scoped guest access grant
// persists and reads back its call_id (migration 049). Requires a disposable
// migrated Postgres database via ALOQA_POSTGRES_TEST_DSN; skipped otherwise.
func TestGuestAccessCallIDRoundTrip(t *testing.T) {
	ctx, pool := setupGuestAccessRepoPostgresTest(t)
	env := setupGuestAccessRepoTestEnv(t, ctx, pool)
	repo := NewGuestAccessRepo(pool)

	now := time.Now().UTC()
	grant := &entity.GuestAccessGrant{
		ID:          uuid.New(),
		InviteID:    env.inviteID,
		WorkspaceID: env.workspaceID,
		UserID:      env.userID,
		ChannelIDs:  nil, // call-scoped: no channel access
		CallID:      &env.callID,
		ExpiresAt:   now.Add(time.Hour),
		CreatedAt:   now,
	}
	if err := repo.CreateGrant(ctx, grant); err != nil {
		t.Fatalf("create grant: %v", err)
	}

	grants, err := repo.ListActiveByUserWorkspace(ctx, env.userID, env.workspaceID, now)
	if err != nil {
		t.Fatalf("list active grants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("active grants = %d, want 1", len(grants))
	}
	got := grants[0]
	if got.CallID == nil {
		t.Fatalf("call_id did not round-trip (nil)")
	}
	if *got.CallID != env.callID {
		t.Fatalf("call_id = %s, want %s", *got.CallID, env.callID)
	}
	if len(got.ChannelIDs) != 0 {
		t.Fatalf("channel_ids = %v, want empty for a call-scoped grant", got.ChannelIDs)
	}
}

type guestAccessRepoTestEnv struct {
	workspaceID uuid.UUID
	userID      uuid.UUID
	callID      uuid.UUID
	inviteID    uuid.UUID
}

func setupGuestAccessRepoPostgresTest(t *testing.T) (context.Context, *pgxpool.Pool) {
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

func setupGuestAccessRepoTestEnv(t *testing.T, ctx context.Context, pool *pgxpool.Pool) guestAccessRepoTestEnv {
	t.Helper()

	now := time.Now().UTC()
	env := guestAccessRepoTestEnv{
		workspaceID: uuid.New(),
		userID:      uuid.New(),
		callID:      uuid.New(),
		inviteID:    uuid.New(),
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, env.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, env.userID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
		VALUES ($1, $2, 'Guest Access Test User', '', 'hash', 'active', 'en', $3, $3)`,
		env.userID,
		"guest-access-test-"+env.userID.String()+"@example.com",
		now,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Guest Access Test Workspace', $2, '', $3, $4, $4)`,
		env.workspaceID,
		"guest-access-test-"+env.workspaceID.String(),
		env.userID,
		now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO calls (id, workspace_id, type, status, title, created_by, settings, created_at)
		VALUES ($1, $2, 'group', 'active', 'Guest Access Test Call', $3, $4, $5)`,
		env.callID,
		env.workspaceID,
		env.userID,
		`{"chat": true}`,
		now,
	); err != nil {
		t.Fatalf("insert call: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO guest_invites (id, workspace_id, created_by, token, channel_ids, call_id, max_uses, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, '{}', $5, 100, 'active', $6, $7)`,
		env.inviteID,
		env.workspaceID,
		env.userID,
		"guest-access-test-token-"+env.inviteID.String(),
		env.callID,
		now.Add(time.Hour),
		now,
	); err != nil {
		t.Fatalf("insert guest invite: %v", err)
	}

	return env
}
