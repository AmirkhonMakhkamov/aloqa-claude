package postgres

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/pagination"
)

type callMessageRepoTestEnv struct {
	workspaceID uuid.UUID
	userID      uuid.UUID
	otherUserID uuid.UUID
	callID      uuid.UUID
	otherCallID uuid.UUID
}

func TestCallMessageRepoCreateGetRoundTrip(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	env := setupCallMessageRepoTestEnv(t, ctx, pool)
	repo := NewCallMessageRepo(pool)

	msg := newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(1), "hello call")
	if err := repo.Create(ctx, msg); err != nil {
		t.Fatalf("create call message: %v", err)
	}

	got, err := repo.GetByID(ctx, msg.ID)
	if err != nil {
		t.Fatalf("get call message: %v", err)
	}
	if got.ID != msg.ID || got.CallID != env.callID || got.SenderID != env.userID || got.Body != "hello call" {
		t.Fatalf("message = %+v, want round-tripped %+v", got, msg)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("created_at was not populated")
	}
}

func TestCallMessageRepoListByCallNewestFirstExcludesSoftDeleted(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	env := setupCallMessageRepoTestEnv(t, ctx, pool)
	repo := NewCallMessageRepo(pool)

	for i, body := range []string{"first", "second", "third"} {
		if err := repo.Create(ctx, newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(i+1), body)); err != nil {
			t.Fatalf("create call message %d: %v", i+1, err)
		}
	}
	if err := repo.Create(ctx, newCallMessageRepoTestMessage(env.otherCallID, env.userID, callMessageRepoTestUUID(4), "other call")); err != nil {
		t.Fatalf("create other call message: %v", err)
	}
	if err := repo.SoftDelete(ctx, callMessageRepoTestUUID(2), env.callID); err != nil {
		t.Fatalf("soft delete call message: %v", err)
	}

	got, err := repo.ListByCall(ctx, env.callID, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("list call messages: %v", err)
	}
	wantIDs := []uuid.UUID{callMessageRepoTestUUID(3), callMessageRepoTestUUID(1)}
	if len(got) != len(wantIDs) {
		t.Fatalf("listed %d messages, want %d: %+v", len(got), len(wantIDs), got)
	}
	for i, wantID := range wantIDs {
		if got[i].ID != wantID {
			t.Fatalf("message[%d].ID = %s, want %s", i, got[i].ID, wantID)
		}
	}
}

func TestCallMessageRepoListByCallCursorPagination(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	env := setupCallMessageRepoTestEnv(t, ctx, pool)
	repo := NewCallMessageRepo(pool)

	for i := 1; i <= 5; i++ {
		if err := repo.Create(ctx, newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(i), "message")); err != nil {
			t.Fatalf("create call message %d: %v", i, err)
		}
	}

	first, err := repo.ListByCall(ctx, env.callID, pagination.Params{Limit: 3})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("first page len = %d, want limit+1", len(first))
	}
	for i, wantID := range []uuid.UUID{callMessageRepoTestUUID(5), callMessageRepoTestUUID(4), callMessageRepoTestUUID(3), callMessageRepoTestUUID(2)} {
		if first[i].ID != wantID {
			t.Fatalf("first[%d].ID = %s, want %s", i, first[i].ID, wantID)
		}
	}

	second, err := repo.ListByCall(ctx, env.callID, pagination.Params{Cursor: first[2].ID, Limit: 3})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second page len = %d, want 2", len(second))
	}
	for i, wantID := range []uuid.UUID{callMessageRepoTestUUID(2), callMessageRepoTestUUID(1)} {
		if second[i].ID != wantID {
			t.Fatalf("second[%d].ID = %s, want %s", i, second[i].ID, wantID)
		}
	}
}

func TestCallMessageRepoSoftDeleteIdempotent(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	env := setupCallMessageRepoTestEnv(t, ctx, pool)
	repo := NewCallMessageRepo(pool)

	msg := newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(1), "delete me")
	if err := repo.Create(ctx, msg); err != nil {
		t.Fatalf("create call message: %v", err)
	}
	if err := repo.SoftDelete(ctx, msg.ID, env.callID); err != nil {
		t.Fatalf("soft delete call message first time: %v", err)
	}
	if err := repo.SoftDelete(ctx, msg.ID, env.callID); err != nil {
		t.Fatalf("soft delete call message second time: %v", err)
	}

	got, err := repo.ListByCall(ctx, env.callID, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("list call messages: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("listed %d messages after delete, want 0: %+v", len(got), got)
	}
}

func TestCallMessageRepoCallDeleteCascadesMessages(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	env := setupCallMessageRepoTestEnv(t, ctx, pool)
	repo := NewCallMessageRepo(pool)

	msg := newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(1), "cascade")
	if err := repo.Create(ctx, msg); err != nil {
		t.Fatalf("create call message: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM calls WHERE id = $1`, env.callID); err != nil {
		t.Fatalf("delete call: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM call_messages WHERE id = $1`, msg.ID).Scan(&count); err != nil {
		t.Fatalf("count call messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("call message count after call delete = %d, want 0", count)
	}
}

func TestCallMessageRepoBodyLengthCheck(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	env := setupCallMessageRepoTestEnv(t, ctx, pool)
	repo := NewCallMessageRepo(pool)

	if err := repo.Create(ctx, newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(1), "   ")); err == nil {
		t.Fatalf("create blank body succeeded, want CHECK violation")
	}
	if err := repo.Create(ctx, newCallMessageRepoTestMessage(env.callID, env.userID, callMessageRepoTestUUID(2), strings.Repeat("a", 2001))); err == nil {
		t.Fatalf("create oversize body succeeded, want CHECK violation")
	}
}

func setupCallMessageRepoPostgresTest(t *testing.T) (context.Context, *pgxpool.Pool) {
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

func setupCallMessageRepoTestEnv(t *testing.T, ctx context.Context, pool *pgxpool.Pool) callMessageRepoTestEnv {
	t.Helper()

	now := time.Now().UTC()
	env := callMessageRepoTestEnv{
		workspaceID: uuid.New(),
		userID:      uuid.New(),
		otherUserID: uuid.New(),
		callID:      uuid.New(),
		otherCallID: uuid.New(),
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, env.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, env.userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, env.otherUserID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
		VALUES
			($1, $3, 'Call Message Test User', '', 'hash', 'active', 'en', $5, $5),
			($2, $4, 'Call Message Other User', '', 'hash', 'active', 'en', $5, $5)`,
		env.userID,
		env.otherUserID,
		"call-message-test-"+env.userID.String()+"@example.com",
		"call-message-test-"+env.otherUserID.String()+"@example.com",
		now,
	); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Call Message Test Workspace', $2, '', $3, $4, $4)`,
		env.workspaceID,
		"call-message-test-"+env.workspaceID.String(),
		env.userID,
		now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO calls (id, workspace_id, type, status, title, created_by, settings, created_at)
		VALUES
			($1, $3, 'group', 'active', 'Call Message Test Call', $4, $5, $6),
			($2, $3, 'group', 'active', 'Call Message Other Test Call', $4, $5, $6)`,
		env.callID,
		env.otherCallID,
		env.workspaceID,
		env.userID,
		`{"chat": true}`,
		now,
	); err != nil {
		t.Fatalf("insert calls: %v", err)
	}

	return env
}

func newCallMessageRepoTestMessage(callID, senderID, messageID uuid.UUID, body string) *entity.CallMessage {
	return &entity.CallMessage{
		ID:       messageID,
		CallID:   callID,
		SenderID: senderID,
		Body:     body,
	}
}

func callMessageRepoTestUUID(n int) uuid.UUID {
	return uuid.MustParse("00000000-0000-0000-0000-" + strings.Repeat("0", 11-len(strconv.Itoa(n))) + strconv.Itoa(n))
}
