package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aloqa/internal/config"
	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/extension"
	httphandler "aloqa/internal/handler/http"
	wshandler "aloqa/internal/handler/ws"
	"aloqa/internal/media/sfu"
	"aloqa/internal/middleware"
	applog "aloqa/internal/pkg/logging"
	"aloqa/internal/platform/cache"
	"aloqa/internal/platform/db"
	"aloqa/internal/platform/pubsub"
	"aloqa/internal/platform/reliability"
	"aloqa/internal/platform/storage"
	"aloqa/internal/platform/ws"
	"aloqa/internal/repository/postgres"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/guestaccess"
	"aloqa/internal/security/rbac"
	"aloqa/internal/service/admin"
	"aloqa/internal/service/auth"
	calendarsvc "aloqa/internal/service/calendar"
	"aloqa/internal/service/call"
	"aloqa/internal/service/chat"
	"aloqa/internal/service/collaboration"
	demosvc "aloqa/internal/service/demo"
	"aloqa/internal/service/file"
	"aloqa/internal/service/guest"
	"aloqa/internal/service/mediaops"
	"aloqa/internal/service/notification"
	"aloqa/internal/service/observability"
	"aloqa/internal/service/presence"
	realtimesvc "aloqa/internal/service/realtime"
	"aloqa/internal/service/recording"
	savedsvc "aloqa/internal/service/saved"
	"aloqa/internal/service/storageops"

	"aloqa/internal/security/collabaccess"
	"aloqa/internal/service/search"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/pion/webrtc/v4"
)

// Build-time variables set via -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	// Structured logging. SuppressCanceled demotes client-disconnect noise
	// (context.Canceled / DeadlineExceeded) out of the ERROR/WARN sinks.
	slog.SetDefault(slog.New(applog.SuppressCanceled(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))))

	slog.Info("starting aloqa", "version", version, "commit", commit, "build_time", buildTime)

	if err := run(); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.Info("configuration loaded")

	if cfg.DB.ValidateMigrationsOnStart {
		if audit, err := db.ValidateMigrationFiles(cfg.DB.MigrationsDir); err != nil {
			return err
		} else {
			slog.Info("migration files validated", "count", len(audit.Files), "dir", audit.Dir)
		}
	}

	// Connect to PostgreSQL.
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
	slog.Info("postgresql connected")

	// Connect to Redis.
	rdb, err := cache.New(cache.Config{
		Addr:            cfg.Redis.Addr,
		Password:        cfg.Redis.Password,
		DB:              cfg.Redis.DB,
		DialTimeout:     cfg.Redis.DialTimeout,
		ReadTimeout:     cfg.Redis.ReadTimeout,
		WriteTimeout:    cfg.Redis.WriteTimeout,
		PoolTimeout:     cfg.Redis.PoolTimeout,
		ConnMaxIdleTime: cfg.Redis.ConnMaxIdleTime,
		ConnMaxLifetime: cfg.Redis.ConnMaxLifetime,
		PoolSize:        cfg.Redis.PoolSize,
		MinIdleConns:    cfg.Redis.MinIdleConns,
		MaxRetries:      cfg.Redis.MaxRetries,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			slog.Error("redis close failed", "error", err)
		}
	}()
	slog.Info("redis connected")

	// Connect to NATS.
	ps, err := pubsub.New(cfg.NATS.URL)
	if err != nil {
		return err
	}
	defer ps.Close()
	slog.Info("nats connected")

	// SFU (Selective Forwarding Unit) for WebRTC media.
	iceServers := []webrtc.ICEServer{
		{URLs: cfg.WebRTC.STUNServers},
	}
	if cfg.WebRTC.TURNServer != "" {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:           []string{cfg.WebRTC.TURNServer},
			Username:       cfg.WebRTC.TURNUsername,
			Credential:     cfg.WebRTC.TURNPassword,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
		slog.Info("TURN server configured", "server", cfg.WebRTC.TURNServer)
	}
	sfuServer, err := sfu.NewSFU(sfu.Config{
		ICEServers:             iceServers,
		PortMin:                cfg.WebRTC.PortMin,
		PortMax:                cfg.WebRTC.PortMax,
		MaxRoomsPerNode:        cfg.WebRTC.MaxRoomsPerNode,
		EnableExperimentalLyra: cfg.WebRTC.EnableExperimentalLyra,
	})
	if err != nil {
		return err
	}
	defer sfuServer.Close()
	slog.Info("sfu initialized")

	// Repositories.
	userRepo := postgres.NewUserRepo(pool)
	workspaceRepo := postgres.NewWorkspaceRepo(pool)
	channelRepo := postgres.NewChannelRepo(pool)
	messageRepo := postgres.NewMessageRepo(pool)
	callRepo := postgres.NewCallRepo(pool)
	callMessageRepo := postgres.NewCallMessageRepo(pool)
	breakoutRoomRepo := postgres.NewBreakoutRoomRepo(pool)
	recordingRepo := postgres.NewRecordingRepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)
	guestInviteRepo := postgres.NewGuestInviteRepo(pool)
	guestAccessRepo := postgres.NewGuestAccessRepo(pool)
	workspaceRoleRepo := postgres.NewWorkspaceRoleRepo(pool)
	channelAccessGrantRepo := postgres.NewChannelAccessGrantRepo(pool)
	channelAccessStateRepo := postgres.NewChannelAccessStateRepo(pool)
	workspaceCollaborationRepo := postgres.NewWorkspaceCollaborationRepo(pool)
	realtimeRepo := postgres.NewRealtimeRepo(pool)
	savedRepo := postgres.NewSavedRepo(pool)
	mediaRepo := postgres.NewMediaRepo(pool)
	calendarRepo := postgres.NewCalendarRepo(pool)

	// File/object storage.
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
	slog.Info("file storage initialized", "path", cfg.Media.StoragePath)
	recordingCapture, err := recording.NewCaptureManager(cfg.Media.RecordingWorkDir)
	if err != nil {
		return err
	}
	recordingProcessor := recording.NewSpoolProcessor(cfg.Media.RecordingWorkDir, fileStore, recording.SpoolProcessorConfig{
		Calls:                 callRepo,
		Users:                 userRepo,
		FFmpegBinary:          cfg.Media.RecordingCompositeBinary,
		CompositeFormat:       entity.RecordingFormat(strings.ToLower(cfg.Media.RecordingCompositeFormat)),
		CompositeWidth:        cfg.Media.RecordingCompositeWidth,
		CompositeHeight:       cfg.Media.RecordingCompositeHeight,
		CompositeVideoBitrate: cfg.Media.RecordingCompositeVideoBitrate,
		CompositeAudioBitrate: cfg.Media.RecordingCompositeAudioBitrate,
		TranscriptSampleRate:  cfg.Media.RecordingTranscriptSampleRate,
	})
	if err := recordingProcessor.Validate(); err != nil {
		return fmt.Errorf("recording processor: %w", err)
	}
	wsStateStore := ws.NewStateStore(rdb, cfg.Realtime.WSStateTTL)

	// Services.
	extensionRegistry := extension.NewRegistry()
	permissionChecker := rbac.NewChecker(workspaceRepo, workspaceRoleRepo)
	guestAccessChecker := guestaccess.NewChecker(guestAccessRepo)
	collaborationSvc := collaboration.NewService(workspaceRepo, workspaceCollaborationRepo, permissionChecker)
	collaborationAccessChecker := collabaccess.NewChecker(channelAccessGrantRepo, collaborationSvc)
	channelAccessPolicy := accesspolicy.NewChecker(workspaceRepo, channelRepo, guestAccessChecker, collaborationAccessChecker)
	searchRepo := postgres.NewSearchRepo(pool, postgres.SearchRepoConfig{
		TextConfig:       cfg.Search.TextConfig,
		MaxAttempts:      cfg.Search.MaxAttempts,
		RetryBackoff:     cfg.Search.RetryBackoff,
		OperationTimeout: cfg.DB.QueryTimeout,
	})
	txManager := postgres.NewTxManager(pool, postgres.TxManagerConfig{
		Users:               userRepo,
		Workspaces:          workspaceRepo,
		Messages:            messageRepo,
		Channels:            channelRepo,
		ChannelGrants:       channelAccessGrantRepo,
		Calls:               callRepo,
		CallMessages:        callMessageRepo,
		Calendars:           calendarRepo,
		Recordings:          recordingRepo,
		Invites:             guestInviteRepo,
		GuestGrants:         guestAccessRepo,
		Roles:               workspaceRoleRepo,
		Search:              searchRepo,
		Realtime:            realtimeRepo,
		Audit:               auditRepo,
		RealtimeMaxAttempts: cfg.Realtime.MaxAttempts,
	})
	realtimePublisher := realtimesvc.NewService(realtimeRepo, ps, realtimesvc.Config{
		BatchSize:        cfg.Realtime.BatchSize,
		MaxAttempts:      cfg.Realtime.MaxAttempts,
		RetryBackoff:     cfg.Realtime.RetryBackoff,
		ReplayLimit:      cfg.Realtime.ReplayLimit,
		ConsumerName:     "realtime-outbox",
		ConsumerStream:   "realtime_events",
		OperationTimeout: cfg.DB.QueryTimeout,
	})
	mediaOpsSvc := mediaops.NewService(mediaRepo, rdb, sfuServer, mediaops.Config{
		NodeID:                  cfg.WebRTC.NodeID,
		Region:                  cfg.WebRTC.Region,
		ControlURL:              cfg.WebRTC.ControlURL,
		MediaURL:                cfg.WebRTC.MediaURL,
		HeartbeatInterval:       cfg.WebRTC.NodeHeartbeatInterval,
		NodeTTL:                 cfg.WebRTC.NodeTTL,
		TelemetryInterval:       cfg.WebRTC.ServerTelemetryInterval,
		OneToOneParticipantCap:  cfg.WebRTC.OneToOneParticipantCap,
		GroupParticipantCap:     cfg.WebRTC.GroupParticipantCap,
		MeetingParticipantCap:   cfg.WebRTC.MeetingParticipantCap,
		WebinarParticipantCap:   cfg.WebRTC.WebinarParticipantCap,
		SelectorParticipantCap:  cfg.WebRTC.SelectorParticipantCap,
		WebinarPresenterCap:     cfg.WebRTC.WebinarPresenterCap,
		SelectorPresenterCap:    cfg.WebRTC.SelectorPresenterCap,
		WebinarFanoutThreshold:  cfg.WebRTC.WebinarFanoutThreshold,
		MaxRoomsPerNode:         cfg.WebRTC.MaxRoomsPerNode,
		TURNStrategy:            cfg.WebRTC.TURNStrategy,
		DefaultQualityMode:      entity.MediaQualityPolicyMode(cfg.WebRTC.DefaultQualityMode),
		AlertPacketLossPct:      cfg.WebRTC.AlertPacketLossPct,
		AlertJitterMs:           cfg.WebRTC.AlertJitterMs,
		AlertRoundTripTimeMs:    cfg.WebRTC.AlertRoundTripTimeMs,
		CorrelationTolerancePct: cfg.WebRTC.CorrelationTolerancePct,
		CorrelationToleranceMs:  cfg.WebRTC.CorrelationToleranceMs,
		ServerDrivenEnabled:     cfg.WebRTC.ServerDrivenEnabled,
		ServerDrivenMinInterval: cfg.WebRTC.ServerDrivenMinInterval,
		OperationTimeout:        cfg.DB.QueryTimeout,
	})
	storageOpsSvc := storageops.NewService(pool, rdb, storageops.Config{
		MigrationsDir:      cfg.DB.MigrationsDir,
		QueryTimeout:       cfg.DB.QueryTimeout,
		StatementTimeout:   cfg.DB.StatementTimeout,
		LockTimeout:        cfg.DB.LockTimeout,
		ProfileTopQueries:  cfg.DB.ProfileTopQueries,
		RedisPoolSize:      cfg.Redis.PoolSize,
		RedisOpTimeout:     cfg.Redis.OperationTimeout,
		PresenceShardCount: cfg.Redis.PresenceShardCount,
	})
	observabilitySvc := observability.NewService(observability.Config{
		EventLagWarn:                cfg.Observability.EventLagWarn,
		EventLagCritical:            cfg.Observability.EventLagCritical,
		DeadLetterWarn:              cfg.Observability.DeadLetterWarn,
		DeadLetterCritical:          cfg.Observability.DeadLetterCritical,
		DBUtilizationWarnPct:        cfg.Observability.DBUtilizationWarnPct,
		DBUtilizationCriticalPct:    cfg.Observability.DBUtilizationCriticalPct,
		RedisUtilizationWarnPct:     cfg.Observability.RedisUtilizationWarnPct,
		RedisUtilizationCriticalPct: cfg.Observability.RedisUtilizationCriticalPct,
		WorkerStallAfter:            cfg.Observability.WorkerStallAfter,
		CallDegradedWarnRatio:       cfg.Observability.CallDegradedWarnRatio,
		CallDegradedCriticalRatio:   cfg.Observability.CallDegradedCriticalRatio,
		ReplaySuccessTargetPct:      cfg.Observability.ReplaySuccessTargetPct,
		RecordingSuccessTargetPct:   cfg.Observability.RecordingSuccessTargetPct,
		ConsumerLagWarn:             cfg.Observability.ConsumerLagWarn,
		ConsumerLagCritical:         cfg.Observability.ConsumerLagCritical,
	})
	observabilitySvc.SetStorageProvider(storageOpsSvc)
	observabilitySvc.SetRealtimeProvider(realtimeRepo, realtimeRepo)
	observabilitySvc.SetSearchProvider(searchRepo)
	observabilitySvc.SetActiveCallProvider(callRepo)
	searchRepo.SetObserver(observabilitySvc)
	realtimePublisher.SetObserver(observabilitySvc)
	mediaOpsSvc.SetObserver(observabilitySvc)
	searchSvc := search.NewService(searchRepo, searchRepo, workspaceRepo, channelRepo, auditRepo)
	authSvc := auth.NewService(userRepo, workspaceRepo, rdb, []byte(cfg.JWT.Secret), cfg.JWT.AccessTokenTTL, cfg.JWT.RefreshTokenTTL, searchSvc)
	authSvc.SetSessionNotifier(auth.NewPubSubSessionNotifier(ps))
	authSvc.SetSessionOperationTimeout(cfg.Redis.OperationTimeout)
	chatSvc := chat.NewService(channelRepo, messageRepo, workspaceRepo, channelAccessGrantRepo, realtimePublisher, guestAccessChecker, collaborationAccessChecker, searchSvc, collaborationSvc)
	chatSvc.SetTransactionManager(txManager)
	savedSvc := savedsvc.NewService(userRepo, channelRepo, messageRepo, savedRepo, channelAccessPolicy, realtimePublisher)
	callSvc := call.NewService(callRepo, breakoutRoomRepo, channelRepo, workspaceRepo, realtimePublisher, sfuServer, call.MediaConfig{
		TokenSecret:              []byte(cfg.JWT.Secret),
		TokenTTL:                 cfg.WebRTC.MediaTokenTTL,
		STUNURLs:                 cfg.WebRTC.STUNServers,
		TURNURLs:                 turnURLsFromConfig(cfg.WebRTC.TURNServer),
		TURNUsername:             cfg.WebRTC.TURNUsername,
		TURNCredential:           cfg.WebRTC.TURNPassword,
		TURNSecret:               cfg.WebRTC.TURNSecret,
		STUNServers:              cfg.WebRTC.STUNServers,
		TURNCredentialsTTL:       cfg.WebRTC.MediaTokenTTL,
		MaxPresentersPerCall:     cfg.WebRTC.MaxPresentersPerCall,
		MaxViewersPerCall:        cfg.WebRTC.MaxViewersPerCall,
		MaxScreenSharesPerCall:   cfg.WebRTC.MaxScreenSharesPerCall,
		MaxTracksPerPresenter:    cfg.WebRTC.MaxTracksPerPresenter,
		DefaultWebinarPresenters: cfg.WebRTC.MaxPresentersPerCall,
		Adaptive: sfu.AdaptiveOptions{
			LowLayerMinKbps:         cfg.WebRTC.AdaptiveLowLayerMinKbps,
			MediumLayerMinKbps:      cfg.WebRTC.AdaptiveMediumLayerMinKbps,
			HighLayerMinKbps:        cfg.WebRTC.AdaptiveHighLayerMinKbps,
			CriticalLayerKbps:       cfg.WebRTC.AdaptiveCriticalLayerKbps,
			MinUpswitchInterval:     cfg.WebRTC.AdaptiveMinUpswitchInterval,
			MinDownswitchInterval:   cfg.WebRTC.AdaptiveMinDownswitchInterval,
			GoodSamplesForUpgrade:   cfg.WebRTC.AdaptiveGoodSamplesForUpgrade,
			PoorSamplesForDowngrade: cfg.WebRTC.AdaptivePoorSamplesForDowngrade,
			EWMAAlpha:               cfg.WebRTC.AdaptiveEWMAAlpha,
		},
	}, guestAccessChecker, collaborationAccessChecker)
	callSvc.SetMediaControlPlane(mediaOpsSvc)
	callSvc.SetCallMessageRepo(callMessageRepo)
	callSvc.SetMessageRepo(messageRepo)
	callSvc.SetTransactionManager(txManager)
	callSvc.SetLiveKit(call.LiveKitSettings{
		URL:       cfg.LiveKit.URL,
		APIKey:    cfg.LiveKit.APIKey,
		APISecret: cfg.LiveKit.APISecret,
		TokenTTL:  cfg.LiveKit.TokenTTL,
	})
	calendarSvc := calendarsvc.NewService(calendarRepo, workspaceRepo, callSvc, realtimePublisher)
	calendarSvc.SetTransactionManager(txManager)
	authSvc.SetNewUserSeeder(demosvc.NewService(userRepo, workspaceRepo, channelRepo, calendarSvc))
	authSvc.SetCommonChannelsLister(channelRepo)
	mediaOpsSvc.SetEventPublisher(realtimePublisher)
	mediaOpsSvc.SetRelayTransport(ps)
	presenceSvc := presence.NewService(rdb,
		presence.WithOperationTimeout(cfg.Redis.OperationTimeout),
		presence.WithOnlineShardCount(cfg.Redis.PresenceShardCount),
		presence.WithEventPublisher(realtimePublisher),
	)
	fileSvc := file.NewService(fileStore, messageRepo, channelRepo, workspaceRepo, nil, searchSvc, file.Config{
		MaxFileSize:  cfg.Media.MaxFileSize,
		AllowedTypes: cfg.Media.AllowedTypes,
		SignedURLTTL: cfg.Media.SignedURLTTL,
	}, guestAccessChecker)
	fileSvc.SetTransactionManager(txManager)
	recordingSvc := recording.NewService(recordingRepo, callRepo, workspaceRepo, fileStore, sfuServer, recordingCapture, recording.Config{
		Retention:        cfg.Media.RecordingRetention,
		Hooks:            extensionRegistry.Hooks(),
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
	recordingSvc.SetObserver(observabilitySvc)
	recordingSvc.SetTransactionManager(txManager)
	notificationStore := postgres.NewNotificationRepo(pool)
	notificationSvc := notification.NewService(notificationStore)
	adminSvc := admin.NewService(userRepo, workspaceRepo, workspaceRoleRepo, channelRepo, recordingRepo, auditRepo, permissionChecker, searchSvc)
	adminSvc.SetTransactionManager(txManager)
	adminSvc.SetMediaObserver(mediaOpsSvc)
	adminSvc.SetStorageObserver(storageOpsSvc)
	adminSvc.SetObservabilityObserver(observabilitySvc)
	guestSvc := guest.NewService(guestInviteRepo, guestAccessRepo, userRepo, workspaceRepo, channelRepo, &authTokenIssuer{authSvc})
	guestSvc.SetTransactionManager(txManager)
	guestSvc.SetCallLookup(callRepo) // ALK-700: reject call-scoped guest links once the call has ended
	chatSvc.SetAccessPolicy(channelAccessPolicy)
	chatSvc.SetChannelAccessStates(channelAccessStateRepo)
	fileSvc.SetAccessPolicy(channelAccessPolicy)
	searchSvc.SetAccessPolicy(channelAccessPolicy)
	recordingSvc.SetCallAccessAuthorizer(callSvc)

	// ALK-701: wire LiveKit Egress recording. egressClient is nil when egress is
	// not configured, in which case StartRecording returns 503 and new calls
	// advertise settings.recording=false (the FE control stays hidden).
	egressOutput := call.EgressSettings{
		Enabled:  cfg.LiveKit.EgressEnabled,
		FileRoot: cfg.LiveKit.EgressFileRoot,
	}
	if cfg.Media.StorageBackend == "s3" || cfg.Media.StorageBackend == "minio" {
		egressOutput.S3 = &call.EgressS3Settings{
			AccessKey:      cfg.Media.ObjectStorageAccessKey,
			Secret:         cfg.Media.ObjectStorageSecretKey,
			Region:         cfg.Media.ObjectStorageRegion,
			Endpoint:       cfg.Media.ObjectStorageEndpoint,
			Bucket:         cfg.Media.ObjectStorageBucket,
			ForcePathStyle: cfg.Media.ObjectStorageForcePathStyle,
		}
	}
	egressClient := call.NewEgressClient(call.LiveKitSettings{
		URL:       cfg.LiveKit.URL,
		APIKey:    cfg.LiveKit.APIKey,
		APISecret: cfg.LiveKit.APISecret,
		TokenTTL:  cfg.LiveKit.TokenTTL,
	}, egressOutput)
	recordingSvc.SetEgressClient(egressClient)
	recordingSvc.SetBroadcaster(callSvc)
	callSvc.SetEgressSink(recordingSvc)
	callSvc.SetRecordingEnabled(egressClient != nil)

	reliability.Supervise(ctx, "session_touch", func(c context.Context) {
		authSvc.RunSessionTouchWorker(c, 30*time.Second)
	})
	reliability.Supervise(ctx, "search_indexer", func(c context.Context) {
		searchRepo.RunWorker(c, cfg.Search.WorkerInterval, cfg.Search.BatchSize)
	})
	reliability.Supervise(ctx, "realtime_outbox", func(c context.Context) {
		realtimePublisher.RunOutboxWorker(c, cfg.Realtime.WorkerInterval)
	})
	reliability.Supervise(ctx, "media_heartbeat", func(c context.Context) {
		mediaOpsSvc.RunNodeHeartbeat(c)
	})
	reliability.Supervise(ctx, "media_telemetry", func(c context.Context) {
		mediaOpsSvc.RunTelemetryCollector(c)
	})
	reliability.Supervise(ctx, "media_relay", func(c context.Context) {
		mediaOpsSvc.RunRelayFabric(c)
	})
	reliability.Supervise(ctx, "call_stale_cleanup", func(c context.Context) {
		callSvc.RunStaleCallCleanupWorker(c, cfg.WebRTC.CallCleanupInterval, cfg.WebRTC.CallEmptyGrace, cfg.WebRTC.CallCleanupBatchSize)
	})
	reliability.Supervise(ctx, "recording_processor", func(c context.Context) {
		recordingSvc.RunProcessingWorker(c, recordingProcessor, cfg.Media.RecordingProcessingInterval, 20)
	})
	reliability.Supervise(ctx, "recording_cleanup", func(c context.Context) {
		recordingSvc.RunCleanupWorker(c, cfg.Media.RecordingCleanupInterval, 20)
	})
	reliability.Supervise(ctx, "recording_lifecycle", func(c context.Context) {
		recordingSvc.RunLifecycleWorker(c, cfg.Media.RecordingLifecycleInterval, 20)
	})
	reliability.Supervise(ctx, "calendar_reminders", func(c context.Context) {
		runCalendarReminderWorker(c, calendarSvc, 30*time.Second)
	})
	reliability.Supervise(ctx, "calendar_reminder_outbox", func(c context.Context) {
		runCalendarReminderOutboxWorker(c, calendarSvc, 2*time.Second)
	})

	// WebSocket hub.
	hub := ws.NewHub(wsStateStore)
	hub.SetObserver(observabilitySvc)
	reliability.Supervise(ctx, "ws_hub_run", func(ctx context.Context) { hub.Run(ctx) })

	// Subscribe to NATS events and forward to WebSocket clients.
	reliability.Supervise(ctx, "nats_forward_events", func(ctx context.Context) {
		forwardEventsToWS(ctx, ps, hub, realtimePublisher)
	})
	reliability.Supervise(ctx, "nats_forward_session_evictions", func(ctx context.Context) {
		forwardSessionEvictions(ctx, ps, hub, presenceSvc)
	})

	// HTTP handlers.
	authHandler := httphandler.NewAuthHandler(authSvc)
	accountHandler := httphandler.NewAccountHandler(authSvc)
	channelHandler := httphandler.NewChannelHandler(chatSvc)
	savedHandler := httphandler.NewSavedHandler(savedSvc)
	messageHandler := httphandler.NewMessageHandler(chatSvc)
	callHandler := httphandler.NewCallHandler(callSvc, guestSvc)
	livekitWebhookHandler := httphandler.NewLiveKitWebhookHandler(
		callSvc,
		cfg.LiveKit.APIKey,
		cfg.LiveKit.APISecret,
		httphandler.WithLiveKitWebhookPath(cfg.LiveKit.WebhookPath),
		httphandler.WithPreviousLiveKitWebhookKey(cfg.LiveKit.WebhookPreviousAPIKey, cfg.LiveKit.WebhookPreviousAPISecret),
	)
	calendarHandler := httphandler.NewCalendarHandler(calendarSvc, callSvc)
	breakoutHandler := httphandler.NewBreakoutHandler(callSvc)
	fileHandler := httphandler.NewFileHandler(fileSvc, cfg.Media.MaxFileSize)
	presenceHandler := httphandler.NewPresenceHandler(presenceSvc)
	recordingHandler := httphandler.NewRecordingHandler(recordingSvc)
	notificationHandler := httphandler.NewNotificationHandler(notificationSvc)
	searchHandler := httphandler.NewSearchHandler(searchSvc)
	adminHandler := httphandler.NewAdminHandler(adminSvc)
	requestMetrics := middleware.NewRequestMetricsCollector()
	metricsHandler := httphandler.NewMetricsHandler(observabilitySvc, httphandler.WithHTTPMetrics(requestMetrics, "aloqa"))
	guestHandler := httphandler.NewGuestHandler(guestSvc)
	wsHandler := wshandler.NewHandler(hub, chatSvc, callSvc, wsStateStore, realtimePublisher, cfg.Realtime.ReplayLimit, cfg.CORS.AllowedOrigins...)
	wsHandler.SetObserver(observabilitySvc)

	router := httphandler.NewRouter(httphandler.RouterDeps{
		Auth:             authHandler,
		Account:          accountHandler,
		Channels:         channelHandler,
		Saved:            savedHandler,
		Messages:         messageHandler,
		Calls:            callHandler,
		LiveKit:          livekitWebhookHandler,
		Calendar:         calendarHandler,
		Breakout:         breakoutHandler,
		Files:            fileHandler,
		Presence:         presenceHandler,
		Recordings:       recordingHandler,
		Notifications:    notificationHandler,
		Search:           searchHandler,
		Admin:            adminHandler,
		Metrics:          metricsHandler,
		Guests:           guestHandler,
		WS:               wsHandler,
		Validator:        authSvc,
		PersonalResolver: authSvc,
		SessionResolver:  authSvc,
		Idempotency: middleware.Idempotency(rdb, middleware.IdempotencyConfig{
			TTL:          cfg.Realtime.IdempotencyTTL,
			MaxBodyBytes: cfg.Realtime.IdempotencyMaxBody,
		}),
		CORSOrigins:    cfg.CORS.AllowedOrigins,
		RequestMetrics: requestMetrics,
	})

	// HTTP server.
	srv := &http.Server{
		Addr:         cfg.Server.Addr(),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Graceful shutdown.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	// Mark server as ready for traffic now that all dependencies are initialised.
	httphandler.SetReady()

	serverErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				serverErr <- fmt.Errorf("http server panicked: %v", r)
			}
		}()
		slog.Info("server starting", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	case sig := <-shutdown:
		slog.Info("shutdown signal received", "signal", sig)

		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 30*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed, forcing", "error", err)
			if closeErr := srv.Close(); closeErr != nil {
				slog.Error("forced server close failed", "error", closeErr)
			}
		}

		slog.Info("server stopped gracefully")
	}

	return nil
}

// forwardEventsToWS subscribes to NATS workspace events and forwards them to WebSocket clients.
// Blocks until ctx is cancelled, then unsubscribes cleanly so Supervise restarts do not leak
// NATS subscriptions. Subscription errors are fatal for this function (return) so the supervisor
// can retry the whole setup with backoff.
func forwardEventsToWS(ctx context.Context, ps *pubsub.PubSub, hub *ws.Hub, realtime *realtimesvc.Service) {
	subs := make([]*nats.Subscription, 0, 3)
	defer func() {
		for _, sub := range subs {
			if sub == nil {
				continue
			}
			if err := sub.Unsubscribe(); err != nil {
				slog.Warn("failed to unsubscribe nats fanout", "subject", sub.Subject, "error", err)
			}
		}
	}()

	sub, err := ps.Subscribe("aloqa.ws.>", func(data []byte, subject string) {
		hub.BroadcastToRoom(subject, data)
		if realtime != nil {
			evt := parseRealtimeEvent(data)
			realtime.RecordConsumerSuccess(ctx, "ws-fanout", "aloqa.ws", evt)
		}
	})
	if err != nil {
		slog.Error("failed to subscribe to ws events", "error", err)
		return
	}
	subs = append(subs, sub)

	sub, err = ps.Subscribe("aloqa.signal.>", func(data []byte, subject string) {
		hub.BroadcastToRoom(subject, data)
		if realtime != nil {
			evt := parseRealtimeEvent(data)
			realtime.RecordConsumerSuccess(ctx, "ws-fanout", "aloqa.signal", evt)
		}
	})
	if err != nil {
		slog.Error("failed to subscribe to signal events", "error", err)
		return
	}
	subs = append(subs, sub)

	sub, err = ps.Subscribe("aloqa.chat.>", func(data []byte, subject string) {
		hub.BroadcastToRoom(subject, data)
		hub.BroadcastToRoom("channel:"+subject[len("aloqa.chat."):], data)
		if realtime != nil {
			evt := parseRealtimeEvent(data)
			realtime.RecordConsumerSuccess(ctx, "ws-fanout", "aloqa.chat", evt)
		}
	})
	if err != nil {
		slog.Error("failed to subscribe to chat events", "error", err)
		return
	}
	subs = append(subs, sub)

	<-ctx.Done()
}

func parseRealtimeEvent(data []byte) *event.Event {
	var evt event.Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil
	}
	return &evt
}

func runCalendarReminderWorker(ctx context.Context, svc *calendarsvc.Service, interval time.Duration) {
	if svc == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := svc.ListAndDispatchReminders(ctx, time.Now().UTC()); err != nil {
			slog.ErrorContext(ctx, "calendar reminder dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runCalendarReminderOutboxWorker(ctx context.Context, svc *calendarsvc.Service, interval time.Duration) {
	if svc == nil {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		processed, failed, dead, err := svc.PublishPendingReminderOutbox(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "calendar reminder outbox publish failed", "error", err)
		} else if processed > 0 || failed > 0 || dead > 0 {
			slog.InfoContext(ctx, "calendar reminder outbox processed", "processed", processed, "failed", failed, "dead", dead)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func turnURLsFromConfig(server string) []string {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil
	}
	return []string{server}
}

func forwardSessionEvictions(ctx context.Context, ps *pubsub.PubSub, hub *ws.Hub, presenceSvc *presence.Service) {
	sub, err := ps.Subscribe(auth.SessionRevokedSubject, func(data []byte, _ string) {
		var evt auth.SessionRevokedEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			slog.Warn("failed to decode session eviction event", "error", err)
			return
		}
		hub.EvictSession(evt.SessionID)
		if presenceSvc != nil {
			// Detach from ctx cancellation so shutdown doesn't abort the clear,
			// but keep a timeout so a dead Redis cannot hang this callback forever.
			clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := presenceSvc.ClearSession(clearCtx, evt.SessionID); err != nil {
				slog.Warn("failed to clear session presence", "session_id", evt.SessionID, "user_id", evt.UserID, "error", err)
			}
		}
	})
	if err != nil {
		slog.Error("failed to subscribe to session eviction events", "error", err)
		return
	}
	defer func() {
		if err := sub.Unsubscribe(); err != nil {
			slog.Warn("failed to unsubscribe session eviction", "error", err)
		}
	}()

	<-ctx.Done()
}

// authTokenIssuer adapts auth.Service to guest.TokenIssuer.
type authTokenIssuer struct {
	auth *auth.Service
}

func (a *authTokenIssuer) CreateSessionForUser(ctx context.Context, userID uuid.UUID, deviceInfo, ipAddress string) (*guest.TokenResult, error) {
	result, err := a.auth.CreateSessionForUser(ctx, userID, deviceInfo, ipAddress)
	if err != nil {
		return nil, err
	}
	return &guest.TokenResult{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		SessionID:    result.SessionID,
		ExpiresIn:    result.ExpiresIn,
	}, nil
}
