package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
)

type messageRepoTestEnv struct {
	workspaceID uuid.UUID
	channelID   uuid.UUID
	userID      uuid.UUID
}

func TestMessageRepoSoftDeleteClearsForwardedFromAndQuoteFields(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	repo := NewMessageRepo(pool)
	msg := newMessageRepoTestMessage(env.channelID, env.userID, messageRepoTestUUID(1), "forward")
	msg.ForwardedFrom = json.RawMessage(`{"message_id":"source","snapshot":{"content":"secret"}}`)
	msg.ProfileShare = &entity.ProfileShare{
		UserID:      env.userID,
		WorkspaceID: env.workspaceID,
		Snapshot: entity.ProfileShareSnapshot{
			DisplayName: "Secret User",
			Role:        entity.WorkspaceRoleMember,
		},
	}
	quotedMessageID := messageRepoTestUUID(2)
	parentMessageID := messageRepoTestUUID(3)
	msg.QuotedMessageID = &quotedMessageID
	msg.QuotedSnapshot = &entity.QuotedSnapshot{
		UserID:          env.userID,
		ContentExcerpt:  "quoted secret",
		CreatedAt:       time.Now().UTC(),
		ParentMessageID: &parentMessageID,
	}

	if err := repo.Create(ctx, msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if err := repo.SoftDelete(ctx, msg.ID); err != nil {
		t.Fatalf("soft delete message: %v", err)
	}
	got, err := repo.GetByID(ctx, msg.ID)
	if err != nil {
		t.Fatalf("get message after soft delete: %v", err)
	}
	if got.Content != "" {
		t.Fatalf("content = %q, want empty after soft delete", got.Content)
	}
	if got.ForwardedFrom != nil {
		t.Fatalf("forwarded_from = %s, want nil after soft delete", got.ForwardedFrom)
	}
	if got.QuotedMessageID != nil {
		t.Fatalf("quoted_message_id = %s, want nil after soft delete", got.QuotedMessageID)
	}
	if got.QuotedSnapshot != nil {
		t.Fatalf("quoted_snapshot = %+v, want nil after soft delete", got.QuotedSnapshot)
	}
	if got.ProfileShare != nil {
		t.Fatalf("profile_share = %+v, want nil after soft delete", got.ProfileShare)
	}
}

func TestMessageRepoSoftDeleteWithCascadeMarksQuotedSnapshotsDeleted(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	repo := NewMessageRepo(pool)
	source := newMessageRepoTestMessage(env.channelID, env.userID, messageRepoTestUUID(10), "source")
	if err := repo.Create(ctx, source); err != nil {
		t.Fatalf("create source message: %v", err)
	}

	quotedIDs := []uuid.UUID{
		messageRepoTestUUID(11),
		messageRepoTestUUID(12),
		messageRepoTestUUID(13),
	}
	for _, quotedID := range quotedIDs {
		msg := newMessageRepoTestMessage(env.channelID, env.userID, quotedID, "quote")
		msg.QuotedMessageID = &source.ID
		msg.QuotedSnapshot = &entity.QuotedSnapshot{
			UserID:         env.userID,
			ContentExcerpt: "source",
			CreatedAt:      source.CreatedAt,
		}
		if err := repo.Create(ctx, msg); err != nil {
			t.Fatalf("create quoted message %s: %v", quotedID, err)
		}
	}

	affected, err := repo.SoftDeleteWithCascade(ctx, source.ID)
	if err != nil {
		t.Fatalf("soft delete with cascade: %v", err)
	}
	if len(affected) != len(quotedIDs) {
		t.Fatalf("affected IDs = %v, want %d IDs", affected, len(quotedIDs))
	}
	affectedSet := map[uuid.UUID]bool{}
	for _, id := range affected {
		affectedSet[id] = true
	}
	for _, id := range quotedIDs {
		if !affectedSet[id] {
			t.Fatalf("affected IDs = %v, missing %s", affected, id)
		}
		got, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("get quoted message %s: %v", id, err)
		}
		if got.QuotedSnapshot == nil || got.QuotedSnapshot.Deleted == nil || !*got.QuotedSnapshot.Deleted {
			t.Fatalf("quoted_snapshot.deleted for %s = %+v, want true", id, got.QuotedSnapshot)
		}
	}
}

func TestMessageRepoPersistsProfileShare(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	repo := NewMessageRepo(pool)
	msg := newMessageRepoTestMessage(env.channelID, env.userID, messageRepoTestUUID(30), "")
	position := "Product Manager"
	department := "Product"
	msg.ProfileShare = &entity.ProfileShare{
		UserID:      env.userID,
		WorkspaceID: env.workspaceID,
		Snapshot: entity.ProfileShareSnapshot{
			DisplayName: "Madina Karimova",
			AvatarURL:   "https://cdn.test/madina.png",
			Role:        entity.WorkspaceRoleAdmin,
			Position:    &position,
			Department:  &department,
		},
	}

	if err := repo.Create(ctx, msg); err != nil {
		t.Fatalf("create profile-share message: %v", err)
	}
	got, err := repo.GetByID(ctx, msg.ID)
	if err != nil {
		t.Fatalf("get profile-share message: %v", err)
	}
	if got.ProfileShare == nil {
		t.Fatalf("profile_share = nil, want value")
	}
	if got.ProfileShare.UserID != env.userID || got.ProfileShare.WorkspaceID != env.workspaceID {
		t.Fatalf("profile_share ids = %s/%s, want %s/%s", got.ProfileShare.UserID, got.ProfileShare.WorkspaceID, env.userID, env.workspaceID)
	}
	if got.ProfileShare.Snapshot.DisplayName != "Madina Karimova" || got.ProfileShare.Snapshot.Role != entity.WorkspaceRoleAdmin {
		t.Fatalf("profile_share snapshot = %+v, want stored snapshot", got.ProfileShare.Snapshot)
	}
	if got.ProfileShare.Snapshot.Position == nil || *got.ProfileShare.Snapshot.Position != position {
		t.Fatalf("profile_share position = %v, want %q", got.ProfileShare.Snapshot.Position, position)
	}
	if got.ProfileShare.Snapshot.Department == nil || *got.ProfileShare.Snapshot.Department != department {
		t.Fatalf("profile_share department = %v, want %q", got.ProfileShare.Snapshot.Department, department)
	}
}

func TestMessageRepoSoftDeleteWithCascadeFailureRollsBackPrimaryInOuterTx(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	repo := NewMessageRepo(pool)
	source := newMessageRepoTestMessage(env.channelID, env.userID, messageRepoTestUUID(20), "source")
	if err := repo.Create(ctx, source); err != nil {
		t.Fatalf("create source message: %v", err)
	}
	quoted := newMessageRepoTestMessage(env.channelID, env.userID, messageRepoTestUUID(21), "quote")
	quoted.QuotedMessageID = &source.ID
	quoted.QuotedSnapshot = &entity.QuotedSnapshot{
		UserID:         env.userID,
		ContentExcerpt: "source",
		CreatedAt:      source.CreatedAt,
	}
	if err := repo.Create(ctx, quoted); err != nil {
		t.Fatalf("create quoted message: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	const constraintName = "message_repo_quote_deleted_block"
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE messages
		ADD CONSTRAINT %s CHECK ((quoted_snapshot ->> 'deleted') IS DISTINCT FROM 'true')`, constraintName)); err != nil {
		t.Fatalf("add cascade failure constraint: %v", err)
	}

	txRepo := repo.withTx(tx)
	if _, err := txRepo.SoftDeleteWithCascade(ctx, source.ID); err == nil {
		t.Fatalf("soft delete with cascade unexpectedly succeeded")
	}
	_ = tx.Rollback(context.Background())

	got, err := repo.GetByID(ctx, source.ID)
	if err != nil {
		t.Fatalf("get source after rollback: %v", err)
	}
	if got.DeletedAt != nil {
		t.Fatalf("source deleted_at = %v, want nil after cascade rollback", got.DeletedAt)
	}
	if got.Content != source.Content {
		t.Fatalf("source content = %q, want %q after cascade rollback", got.Content, source.Content)
	}
}

func TestMessageForwardedFromMigrationUpDownIdempotent(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration test tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})

	up := readRepoRootFile(t, "migrations/035_messages_forward_from.sql")
	down := readRepoRootFile(t, "migrations/down/035_messages_forward_from.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("reset migration down: %v", err)
	}
	if messageRepoColumnExists(ctx, t, tx, "forwarded_from") {
		t.Fatalf("forwarded_from column exists before migration up")
	}

	existingMessageID := messageRepoTestUUID(2)
	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, channel_id, user_id, content, type, created_at, updated_at)
		VALUES ($1, $2, $3, 'existing message', 'text', $4, $4)`,
		existingMessageID,
		env.channelID,
		env.userID,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("insert existing pre-migration message: %v", err)
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("apply migration up: %v", err)
	}
	if !messageRepoColumnExists(ctx, t, tx, "forwarded_from") {
		t.Fatalf("forwarded_from column does not exist after up migration")
	}
	var (
		content       string
		messageType   entity.MessageType
		forwardedFrom json.RawMessage
	)
	if err := tx.QueryRow(ctx, `
		SELECT content, type, forwarded_from
		FROM messages
		WHERE id = $1`,
		existingMessageID,
	).Scan(&content, &messageType, &forwardedFrom); err != nil {
		t.Fatalf("query existing message after migration up: %v", err)
	}
	if content != "existing message" || messageType != entity.MessageTypeText {
		t.Fatalf("existing message after up = content %q type %q, want original values", content, messageType)
	}
	if forwardedFrom != nil {
		t.Fatalf("forwarded_from for existing row = %s, want NULL", forwardedFrom)
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("apply migration down: %v", err)
	}
	if messageRepoColumnExists(ctx, t, tx, "forwarded_from") {
		t.Fatalf("forwarded_from column still exists after down migration")
	}
	if err := tx.QueryRow(ctx, `
		SELECT content, type
		FROM messages
		WHERE id = $1`,
		existingMessageID,
	).Scan(&content, &messageType); err != nil {
		t.Fatalf("query existing message after migration down: %v", err)
	}
	if content != "existing message" || messageType != entity.MessageTypeText {
		t.Fatalf("existing message after down = content %q type %q, want original values", content, messageType)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply migration up: %v", err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply migration up second time: %v", err)
	}
	if !messageRepoColumnExists(ctx, t, tx, "forwarded_from") {
		t.Fatalf("forwarded_from column does not exist after idempotent up migration")
	}
}

func TestMessageQuoteFieldsMigrationUpDownIdempotent(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration test tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})

	up := readRepoRootFile(t, "migrations/036_messages_quote_fields.sql")
	down := readRepoRootFile(t, "migrations/down/036_messages_quote_fields.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("reset migration down: %v", err)
	}
	if messageRepoColumnExists(ctx, t, tx, "quoted_message_id") {
		t.Fatalf("quoted_message_id column exists before migration up")
	}
	if messageRepoColumnExists(ctx, t, tx, "quoted_snapshot") {
		t.Fatalf("quoted_snapshot column exists before migration up")
	}
	if messageRepoIndexExists(ctx, t, tx, "idx_messages_quoted_message_id") {
		t.Fatalf("quoted_message_id index exists before migration up")
	}

	existingMessageID := messageRepoTestUUID(30)
	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, channel_id, user_id, content, type, created_at, updated_at)
		VALUES ($1, $2, $3, 'existing message', 'text', $4, $4)`,
		existingMessageID,
		env.channelID,
		env.userID,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("insert existing pre-migration message: %v", err)
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("apply migration up: %v", err)
	}
	if !messageRepoColumnExists(ctx, t, tx, "quoted_message_id") {
		t.Fatalf("quoted_message_id column does not exist after up migration")
	}
	if !messageRepoColumnExists(ctx, t, tx, "quoted_snapshot") {
		t.Fatalf("quoted_snapshot column does not exist after up migration")
	}
	if !messageRepoIndexExists(ctx, t, tx, "idx_messages_quoted_message_id") {
		t.Fatalf("quoted_message_id index does not exist after up migration")
	}
	var (
		content         string
		messageType     entity.MessageType
		quotedMessageID *uuid.UUID
		quotedSnapshot  json.RawMessage
	)
	if err := tx.QueryRow(ctx, `
		SELECT content, type, quoted_message_id, quoted_snapshot
		FROM messages
		WHERE id = $1`,
		existingMessageID,
	).Scan(&content, &messageType, &quotedMessageID, &quotedSnapshot); err != nil {
		t.Fatalf("query existing message after migration up: %v", err)
	}
	if content != "existing message" || messageType != entity.MessageTypeText {
		t.Fatalf("existing message after up = content %q type %q, want original values", content, messageType)
	}
	if quotedMessageID != nil {
		t.Fatalf("quoted_message_id for existing row = %s, want NULL", *quotedMessageID)
	}
	if quotedSnapshot != nil {
		t.Fatalf("quoted_snapshot for existing row = %s, want NULL", quotedSnapshot)
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("apply migration down: %v", err)
	}
	if messageRepoColumnExists(ctx, t, tx, "quoted_message_id") {
		t.Fatalf("quoted_message_id column still exists after down migration")
	}
	if messageRepoColumnExists(ctx, t, tx, "quoted_snapshot") {
		t.Fatalf("quoted_snapshot column still exists after down migration")
	}
	if messageRepoIndexExists(ctx, t, tx, "idx_messages_quoted_message_id") {
		t.Fatalf("quoted_message_id index still exists after down migration")
	}
	if err := tx.QueryRow(ctx, `
		SELECT content, type
		FROM messages
		WHERE id = $1`,
		existingMessageID,
	).Scan(&content, &messageType); err != nil {
		t.Fatalf("query existing message after migration down: %v", err)
	}
	if content != "existing message" || messageType != entity.MessageTypeText {
		t.Fatalf("existing message after down = content %q type %q, want original values", content, messageType)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply migration up: %v", err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply migration up second time: %v", err)
	}
	if !messageRepoColumnExists(ctx, t, tx, "quoted_message_id") || !messageRepoColumnExists(ctx, t, tx, "quoted_snapshot") {
		t.Fatalf("quote columns do not exist after idempotent up migration")
	}
	if !messageRepoIndexExists(ctx, t, tx, "idx_messages_quoted_message_id") {
		t.Fatalf("quoted_message_id index does not exist after idempotent up migration")
	}
}

func TestMessageProfileShareMigrationUpDownIdempotent(t *testing.T) {
	ctx, pool := setupMessageRepoPostgresTest(t)
	env := setupMessageRepoTestEnv(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration test tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})

	up := readRepoRootFile(t, "migrations/044_messages_profile_share.sql")
	down := readRepoRootFile(t, "migrations/down/044_messages_profile_share.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("reset migration down: %v", err)
	}
	if messageRepoColumnExists(ctx, t, tx, "profile_share") {
		t.Fatalf("profile_share column exists before migration up")
	}
	if messageRepoIndexExists(ctx, t, tx, "idx_messages_profile_share_user_id") {
		t.Fatalf("profile_share index exists before migration up")
	}

	existingMessageID := messageRepoTestUUID(44)
	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, channel_id, user_id, content, type, created_at, updated_at)
		VALUES ($1, $2, $3, 'existing message', 'text', $4, $4)`,
		existingMessageID,
		env.channelID,
		env.userID,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("insert existing pre-migration message: %v", err)
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("apply migration up: %v", err)
	}
	if !messageRepoColumnExists(ctx, t, tx, "profile_share") {
		t.Fatalf("profile_share column does not exist after up migration")
	}
	if !messageRepoIndexExists(ctx, t, tx, "idx_messages_profile_share_user_id") {
		t.Fatalf("profile_share index does not exist after up migration")
	}
	var profileShare json.RawMessage
	if err := tx.QueryRow(ctx, `
		SELECT profile_share
		FROM messages
		WHERE id = $1`,
		existingMessageID,
	).Scan(&profileShare); err != nil {
		t.Fatalf("query existing message after migration up: %v", err)
	}
	if profileShare != nil {
		t.Fatalf("profile_share for existing row = %s, want NULL", profileShare)
	}

	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("apply migration down: %v", err)
	}
	if messageRepoColumnExists(ctx, t, tx, "profile_share") {
		t.Fatalf("profile_share column still exists after down migration")
	}
	if messageRepoIndexExists(ctx, t, tx, "idx_messages_profile_share_user_id") {
		t.Fatalf("profile_share index still exists after down migration")
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply migration up: %v", err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("reapply migration up second time: %v", err)
	}
	if !messageRepoColumnExists(ctx, t, tx, "profile_share") {
		t.Fatalf("profile_share column does not exist after idempotent up migration")
	}
	if !messageRepoIndexExists(ctx, t, tx, "idx_messages_profile_share_user_id") {
		t.Fatalf("profile_share index does not exist after idempotent up migration")
	}
}

func setupMessageRepoPostgresTest(t *testing.T) (context.Context, *pgxpool.Pool) {
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

func setupMessageRepoTestEnv(t *testing.T, ctx context.Context, pool *pgxpool.Pool) messageRepoTestEnv {
	t.Helper()

	now := time.Now().UTC()
	env := messageRepoTestEnv{
		workspaceID: uuid.New(),
		channelID:   uuid.New(),
		userID:      uuid.New(),
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, env.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, env.userID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
		VALUES ($1, $2, 'Message Test User', '', 'hash', 'active', 'en', $3, $3)`,
		env.userID,
		"message-test-"+env.userID.String()+"@example.com",
		now,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Message Test Workspace', $2, '', $3, $4, $4)`,
		env.workspaceID,
		"message-test-"+env.workspaceID.String(),
		env.userID,
		now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO channels (id, workspace_id, name, topic, type, created_by, archived, created_at, updated_at)
		VALUES ($1, $2, 'Message Test Channel', '', 'public', $3, false, $4, $4)`,
		env.channelID,
		env.workspaceID,
		env.userID,
		now,
	); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	return env
}

func newMessageRepoTestMessage(channelID, userID, messageID uuid.UUID, content string) *entity.Message {
	now := time.Now().UTC()
	return &entity.Message{
		ID:        messageID,
		ChannelID: channelID,
		UserID:    userID,
		Content:   content,
		Type:      entity.MessageTypeText,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func messageRepoTestUUID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", n))
}

func readRepoRootFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

type messageRepoColumnQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func messageRepoColumnExists(ctx context.Context, t *testing.T, q messageRepoColumnQueryer, column string) bool {
	t.Helper()
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
				AND table_name = 'messages'
				AND column_name = $1
		)`, column).Scan(&exists); err != nil {
		t.Fatalf("check column %s: %v", column, err)
	}
	return exists
}

func messageRepoIndexExists(ctx context.Context, t *testing.T, q messageRepoColumnQueryer, index string) bool {
	t.Helper()
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = 'public'
				AND tablename = 'messages'
				AND indexname = $1
		)`, index).Scan(&exists); err != nil {
		t.Fatalf("check index %s: %v", index, err)
	}
	return exists
}
