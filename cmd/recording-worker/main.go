package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"aloqa/internal/config"
	"aloqa/internal/domain/entity"
	"aloqa/internal/extension"
	"aloqa/internal/platform/db"
	"aloqa/internal/platform/reliability"
	"aloqa/internal/platform/storage"
	"aloqa/internal/repository/postgres"
	"aloqa/internal/service/recording"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := run(); err != nil {
		slog.Error("recording worker failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
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

	var fileStore storage.Storage
	switch strings.ToLower(cfg.Media.StorageBackend) {
	case "", "local":
		fileStore, err = storage.NewLocalStorage(cfg.Media.StoragePath)
	case "s3", "minio":
		fileStore, err = storage.NewS3Storage(storage.S3Config{
			Endpoint:            cfg.Media.ObjectStorageEndpoint,
			Region:              cfg.Media.ObjectStorageRegion,
			Bucket:              cfg.Media.ObjectStorageBucket,
			AccessKey:           cfg.Media.ObjectStorageAccessKey,
			SecretKey:           cfg.Media.ObjectStorageSecretKey,
			UseSSL:              cfg.Media.ObjectStorageUseSSL,
			ForcePathStyle:      cfg.Media.ObjectStorageForcePathStyle,
			Prefix:              cfg.Media.ObjectStoragePrefix,
			HotStorageClass:     cfg.Media.ObjectStorageHotClass,
			WarmStorageClass:    cfg.Media.ObjectStorageWarmClass,
			ArchiveStorageClass: cfg.Media.ObjectStorageArchiveClass,
		})
	default:
		return fmt.Errorf("unsupported media storage backend %q", cfg.Media.StorageBackend)
	}
	if err != nil {
		return err
	}

	recordingRepo := postgres.NewRecordingRepo(pool)
	callRepo := postgres.NewCallRepo(pool)
	workspaceRepo := postgres.NewWorkspaceRepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)
	hooks := extension.NewHookDispatcher()
	service := recording.NewService(recordingRepo, callRepo, workspaceRepo, fileStore, nil, nil, recording.Config{
		Retention:        cfg.Media.RecordingRetention,
		Hooks:            hooks,
		Audit:            auditRepo,
		QuotaBytes:       cfg.Media.RecordingWorkspaceQuota,
		MaxAttempts:      cfg.Media.RecordingMaxAttempts,
		RetryBackoff:     cfg.Media.RecordingRetryBackoff,
		SpoolBaseDir:     cfg.Media.RecordingWorkDir,
		OperationTimeout: cfg.DB.QueryTimeout,
		SignedURLTTL:     cfg.Media.SignedURLTTL,
		WarmAfter:        cfg.Media.RecordingWarmAfter,
		ArchiveAfter:     cfg.Media.RecordingArchiveAfter,
	})
	processor := recording.NewSpoolProcessor(cfg.Media.RecordingWorkDir, fileStore, recording.SpoolProcessorConfig{
		Calls:                 callRepo,
		Users:                 nil,
		FFmpegBinary:          cfg.Media.RecordingCompositeBinary,
		CompositeFormat:       entity.RecordingFormat(strings.ToLower(cfg.Media.RecordingCompositeFormat)),
		CompositeWidth:        cfg.Media.RecordingCompositeWidth,
		CompositeHeight:       cfg.Media.RecordingCompositeHeight,
		CompositeVideoBitrate: cfg.Media.RecordingCompositeVideoBitrate,
		CompositeAudioBitrate: cfg.Media.RecordingCompositeAudioBitrate,
		TranscriptSampleRate:  cfg.Media.RecordingTranscriptSampleRate,
	})

	if err := processor.Validate(); err != nil {
		return fmt.Errorf("recording processor: %w", err)
	}

	slog.Info("recording worker started")
	reliability.Supervise(ctx, "recording_cleanup_worker", func(ctx context.Context) {
		service.RunCleanupWorker(ctx, cfg.Media.RecordingCleanupInterval, 20)
	})
	reliability.Supervise(ctx, "recording_lifecycle_worker", func(ctx context.Context) {
		service.RunLifecycleWorker(ctx, cfg.Media.RecordingLifecycleInterval, 20)
	})
	service.RunProcessingWorker(ctx, processor, cfg.Media.RecordingProcessingInterval, 20)
	return nil
}
