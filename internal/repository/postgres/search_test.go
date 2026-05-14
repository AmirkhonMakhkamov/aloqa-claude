package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	searchsvc "aloqa/internal/service/search"
)

func TestSearchRepoSearchFindsIndexedWorkspaceUser(t *testing.T) {
	dsn := os.Getenv("ALOQA_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set ALOQA_POSTGRES_TEST_DSN to a disposable migrated Postgres database to run this integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	repo := NewSearchRepo(pool, SearchRepoConfig{TextConfig: "simple", RetryBackoff: time.Millisecond})
	searchService := searchsvc.NewService(repo, repo, nil, nil, nil)

	now := time.Now().UTC()
	workspaceID := uuid.New()
	aliceID := uuid.New()
	bobID := uuid.New()

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, aliceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, bobID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
		VALUES
			($1, 'alice.search@example.com', 'Alice Search', '', 'hash', 'active', 'en', $3, $3),
			($2, 'bob.search@example.com', 'Bob Builder', '', 'hash', 'active', 'en', $3, $3)`,
		aliceID,
		bobID,
		now,
	); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Search Test Workspace', $2, '', $3, $4, $4)`,
		workspaceID,
		"search-test-"+workspaceID.String(),
		aliceID,
		now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (id, workspace_id, user_id, role, joined_at)
		VALUES
			($1, $3, $4, 'member', $6),
			($2, $3, $5, 'member', $6)`,
		uuid.New(),
		uuid.New(),
		workspaceID,
		aliceID,
		bobID,
		now,
	); err != nil {
		t.Fatalf("insert workspace members: %v", err)
	}

	if err := searchService.IndexUser(ctx, workspaceID, aliceID, "Alice Search", "alice.search@example.com", now, now); err != nil {
		t.Fatalf("enqueue alice user index: %v", err)
	}
	if err := searchService.IndexUser(ctx, workspaceID, bobID, "Bob Builder", "bob.search@example.com", now, now); err != nil {
		t.Fatalf("enqueue bob user index: %v", err)
	}

	processed, failed, dead, err := repo.ProcessPendingJobs(ctx, 10)
	if err != nil {
		t.Fatalf("process search jobs: %v", err)
	}
	if failed != 0 || dead != 0 {
		t.Fatalf("search job failures: processed=%d failed=%d dead=%d", processed, failed, dead)
	}
	if processed < 2 {
		t.Fatalf("processed search jobs = %d, want at least 2", processed)
	}

	results, err := repo.Search(ctx, searchsvc.Params{
		Query:            "Bob",
		WorkspaceID:      workspaceID,
		Type:             string(searchsvc.ResourceTypeUser),
		Limit:            5,
		AllowUserResults: true,
	})
	if err != nil {
		t.Fatalf("search users: %v", err)
	}

	for _, result := range results.Results {
		if result.ID == bobID {
			if result.User == nil {
				t.Fatalf("bob result has nil user")
			}
			if result.User.DisplayName != "Bob Builder" || result.User.Email != "bob.search@example.com" {
				t.Fatalf("bob user = %+v, want hydrated Bob", result.User)
			}
			return
		}
	}
	t.Fatalf("Bob user result not found in %+v", results.Results)
}
