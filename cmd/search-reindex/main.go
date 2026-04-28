package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"aloqa/internal/config"
	"aloqa/internal/platform/db"
	"aloqa/internal/repository/postgres"
	"github.com/google/uuid"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := run(); err != nil {
		slog.Error("search reindex failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var workspaceArg string
	flag.StringVar(&workspaceArg, "workspace", "", "workspace UUID to reindex; reindexes all workspaces when omitted")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := db.New(ctx, db.Config{
		DSN:                   cfg.DB.DSN(),
		MaxConns:              cfg.DB.MaxConns,
		MinConns:              cfg.DB.MinConns,
		ConnectTimeout:        cfg.DB.ConnectTimeout,
		QueryTimeout:          cfg.DB.QueryTimeout,
		StatementTimeout:      cfg.DB.StatementTimeout,
		LockTimeout:           cfg.DB.LockTimeout,
		IdleInTxTimeout:       cfg.DB.IdleInTxSessionTimeout,
		MaxConnLifetime:       cfg.DB.MaxConnLifetime,
		MaxConnLifetimeJitter: cfg.DB.MaxConnLifetimeJitter,
		MaxConnIdleTime:       cfg.DB.MaxConnIdleTime,
		HealthCheckPeriod:     cfg.DB.HealthCheckPeriod,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	repo := postgres.NewSearchRepo(pool, postgres.SearchRepoConfig{
		TextConfig:       cfg.Search.TextConfig,
		MaxAttempts:      cfg.Search.MaxAttempts,
		RetryBackoff:     cfg.Search.RetryBackoff,
		OperationTimeout: cfg.DB.QueryTimeout,
	})

	if workspaceArg != "" {
		workspaceID, err := uuid.Parse(workspaceArg)
		if err != nil {
			return err
		}
		slog.Info("reindexing workspace search", "workspace_id", workspaceID)
		return repo.ReindexWorkspace(ctx, workspaceID)
	}

	slog.Info("reindexing search for all workspaces")
	return repo.ReindexAll(ctx)
}
