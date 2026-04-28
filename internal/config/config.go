package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server        ServerConfig
	DB            DBConfig
	Redis         RedisConfig
	NATS          NATSConfig
	JWT           JWTConfig
	Media         MediaConfig
	WebRTC        WebRTCConfig
	Search        SearchConfig
	Realtime      RealtimeConfig
	Observability ObservabilityConfig
	CORS          CORSConfig
}

type ObservabilityConfig struct {
	EventLagWarn                time.Duration
	EventLagCritical            time.Duration
	DeadLetterWarn              int64
	DeadLetterCritical          int64
	DBUtilizationWarnPct        float64
	DBUtilizationCriticalPct    float64
	RedisUtilizationWarnPct     float64
	RedisUtilizationCriticalPct float64
	WorkerStallAfter            time.Duration
	CallDegradedWarnRatio       float64
	CallDegradedCriticalRatio   float64
	ReplaySuccessTargetPct      float64
	RecordingSuccessTargetPct   float64
	ConsumerLagWarn             int64
	ConsumerLagCritical         int64
}

type SearchConfig struct {
	TextConfig     string
	WorkerInterval time.Duration
	BatchSize      int
	MaxAttempts    int
	RetryBackoff   time.Duration
}

type RealtimeConfig struct {
	WorkerInterval     time.Duration
	BatchSize          int
	MaxAttempts        int
	RetryBackoff       time.Duration
	ReplayLimit        int
	WSStateTTL         time.Duration
	IdempotencyTTL     time.Duration
	IdempotencyMaxBody int64
}

// WebRTCConfig holds ICE/TURN/STUN server configuration.
type WebRTCConfig struct {
	STUNServers                     []string // e.g. "stun:stun.l.google.com:19302"
	TURNServer                      string   // e.g. "turn:turn.example.com:3478"
	TURNUsername                    string
	TURNPassword                    string
	NodeID                          string
	Region                          string
	ControlURL                      string
	MediaURL                        string
	PortMin                         uint16 // UDP port range for media
	PortMax                         uint16
	MaxRoomsPerNode                 int
	MaxPresentersPerCall            int
	MaxViewersPerCall               int
	MaxScreenSharesPerCall          int
	MaxTracksPerPresenter           int
	EnableExperimentalLyra          bool
	MediaTokenTTL                   time.Duration
	NodeHeartbeatInterval           time.Duration
	NodeTTL                         time.Duration
	ServerTelemetryInterval         time.Duration
	OneToOneParticipantCap          int
	GroupParticipantCap             int
	MeetingParticipantCap           int
	WebinarParticipantCap           int
	SelectorParticipantCap          int
	WebinarPresenterCap             int
	SelectorPresenterCap            int
	WebinarFanoutThreshold          int
	TURNStrategy                    string
	DefaultQualityMode              string
	AlertPacketLossPct              float64
	AlertJitterMs                   float64
	AlertRoundTripTimeMs            float64
	CorrelationTolerancePct         float64
	CorrelationToleranceMs          float64
	ServerDrivenEnabled             bool
	ServerDrivenMinInterval         time.Duration
	AdaptiveLowLayerMinKbps         int
	AdaptiveMediumLayerMinKbps      int
	AdaptiveHighLayerMinKbps        int
	AdaptiveCriticalLayerKbps       int
	AdaptiveMinUpswitchInterval     time.Duration
	AdaptiveMinDownswitchInterval   time.Duration
	AdaptiveGoodSamplesForUpgrade   int
	AdaptivePoorSamplesForDowngrade int
	AdaptiveEWMAAlpha               float64
}

// CORSConfig holds CORS allowed origins.
type CORSConfig struct {
	AllowedOrigins []string
}

type ServerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

type DBConfig struct {
	Host                      string
	Port                      int
	User                      string
	Password                  string
	Name                      string
	SSLMode                   string
	MaxConns                  int32
	MinConns                  int32
	ConnectTimeout            time.Duration
	QueryTimeout              time.Duration
	StatementTimeout          time.Duration
	LockTimeout               time.Duration
	IdleInTxSessionTimeout    time.Duration
	MaxConnLifetime           time.Duration
	MaxConnLifetimeJitter     time.Duration
	MaxConnIdleTime           time.Duration
	HealthCheckPeriod         time.Duration
	ProfileTopQueries         int
	SlowQueryThreshold        time.Duration
	MigrationsDir             string
	ValidateMigrationsOnStart bool
}

type RedisConfig struct {
	Addr                 string
	Password             string
	DB                   int
	DialTimeout          time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	PoolTimeout          time.Duration
	ConnMaxIdleTime      time.Duration
	ConnMaxLifetime      time.Duration
	PoolSize             int
	MinIdleConns         int
	MaxRetries           int
	OperationTimeout     time.Duration
	PresenceShardCount   int
	ProfileCommandSample int
}

type NATSConfig struct {
	URL string
}

type JWTConfig struct {
	Secret          string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

type MediaConfig struct {
	MaxFileSize                    int64
	AllowedTypes                   []string
	StorageBackend                 string
	StoragePath                    string
	ObjectStorageEndpoint          string
	ObjectStorageRegion            string
	ObjectStorageBucket            string
	ObjectStorageAccessKey         string
	ObjectStorageSecretKey         string
	ObjectStorageUseSSL            bool
	ObjectStorageForcePathStyle    bool
	ObjectStoragePrefix            string
	ObjectStorageHotClass          string
	ObjectStorageWarmClass         string
	ObjectStorageArchiveClass      string
	SignedURLTTL                   time.Duration
	RecordingRetention             time.Duration
	RecordingCleanupInterval       time.Duration
	RecordingProcessingInterval    time.Duration
	RecordingLifecycleInterval     time.Duration
	RecordingWorkDir               string
	RecordingWorkspaceQuota        int64
	RecordingMaxAttempts           int
	RecordingRetryBackoff          time.Duration
	RecordingWarmAfter             time.Duration
	RecordingArchiveAfter          time.Duration
	RecordingCompositeBinary       string
	RecordingCompositeFormat       string
	RecordingCompositeWidth        int
	RecordingCompositeHeight       int
	RecordingCompositeVideoBitrate string
	RecordingCompositeAudioBitrate string
	RecordingTranscriptSampleRate  int
}

func (db DBConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		db.User, db.Password, db.Host, db.Port, db.Name, db.SSLMode,
	)
}

func (s ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Host:         env("SERVER_HOST", "0.0.0.0"),
			Port:         envInt("SERVER_PORT", 8080),
			ReadTimeout:  envDuration("SERVER_READ_TIMEOUT", 15*time.Second),
			WriteTimeout: envDuration("SERVER_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:  envDuration("SERVER_IDLE_TIMEOUT", 60*time.Second),
		},
		DB: DBConfig{
			Host:                      env("DB_HOST", "localhost"),
			Port:                      envInt("DB_PORT", 5432),
			User:                      envRequired("DB_USER"),
			Password:                  envRequired("DB_PASSWORD"),
			Name:                      envRequired("DB_NAME"),
			SSLMode:                   env("DB_SSLMODE", "disable"),
			MaxConns:                  int32(envInt("DB_MAX_CONNS", 20)),
			MinConns:                  int32(envInt("DB_MIN_CONNS", 5)),
			ConnectTimeout:            envDuration("DB_CONNECT_TIMEOUT", 10*time.Second),
			QueryTimeout:              envDuration("DB_QUERY_TIMEOUT", 5*time.Second),
			StatementTimeout:          envDuration("DB_STATEMENT_TIMEOUT", 5*time.Second),
			LockTimeout:               envDuration("DB_LOCK_TIMEOUT", 2*time.Second),
			IdleInTxSessionTimeout:    envDuration("DB_IDLE_IN_TX_TIMEOUT", 15*time.Second),
			MaxConnLifetime:           envDuration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnLifetimeJitter:     envDuration("DB_MAX_CONN_LIFETIME_JITTER", 5*time.Minute),
			MaxConnIdleTime:           envDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
			HealthCheckPeriod:         envDuration("DB_HEALTHCHECK_PERIOD", 30*time.Second),
			ProfileTopQueries:         envInt("DB_PROFILE_TOP_QUERIES", 10),
			SlowQueryThreshold:        envDuration("DB_SLOW_QUERY_THRESHOLD", 500*time.Millisecond),
			MigrationsDir:             env("DB_MIGRATIONS_DIR", "migrations"),
			ValidateMigrationsOnStart: envBool("DB_VALIDATE_MIGRATIONS_ON_START", true),
		},
		Redis: RedisConfig{
			Addr:                 env("REDIS_ADDR", "localhost:6379"),
			Password:             env("REDIS_PASSWORD", ""),
			DB:                   envInt("REDIS_DB", 0),
			DialTimeout:          envDuration("REDIS_DIAL_TIMEOUT", 5*time.Second),
			ReadTimeout:          envDuration("REDIS_READ_TIMEOUT", 3*time.Second),
			WriteTimeout:         envDuration("REDIS_WRITE_TIMEOUT", 3*time.Second),
			PoolTimeout:          envDuration("REDIS_POOL_TIMEOUT", 4*time.Second),
			ConnMaxIdleTime:      envDuration("REDIS_CONN_MAX_IDLE_TIME", 5*time.Minute),
			ConnMaxLifetime:      envDuration("REDIS_CONN_MAX_LIFETIME", time.Hour),
			PoolSize:             envInt("REDIS_POOL_SIZE", 32),
			MinIdleConns:         envInt("REDIS_MIN_IDLE_CONNS", 8),
			MaxRetries:           envInt("REDIS_MAX_RETRIES", 2),
			OperationTimeout:     envDuration("REDIS_OPERATION_TIMEOUT", 3*time.Second),
			PresenceShardCount:   envInt("REDIS_PRESENCE_SHARD_COUNT", 32),
			ProfileCommandSample: envInt("REDIS_PROFILE_COMMAND_SAMPLE", 20),
		},
		NATS: NATSConfig{
			URL: env("NATS_URL", "nats://localhost:4222"),
		},
		JWT: JWTConfig{
			Secret:          env("JWT_SECRET", ""),
			AccessTokenTTL:  envDuration("JWT_ACCESS_TTL", 15*time.Minute),
			RefreshTokenTTL: envDuration("JWT_REFRESH_TTL", 7*24*time.Hour),
		},
		Media: MediaConfig{
			MaxFileSize:                    int64(envInt("MEDIA_MAX_FILE_SIZE", 100*1024*1024)),
			StorageBackend:                 env("MEDIA_STORAGE_BACKEND", "local"),
			StoragePath:                    env("MEDIA_STORAGE_PATH", "./storage"),
			ObjectStorageEndpoint:          env("MEDIA_OBJECT_STORAGE_ENDPOINT", ""),
			ObjectStorageRegion:            env("MEDIA_OBJECT_STORAGE_REGION", "us-east-1"),
			ObjectStorageBucket:            env("MEDIA_OBJECT_STORAGE_BUCKET", ""),
			ObjectStorageAccessKey:         env("MEDIA_OBJECT_STORAGE_ACCESS_KEY", ""),
			ObjectStorageSecretKey:         env("MEDIA_OBJECT_STORAGE_SECRET_KEY", ""),
			ObjectStorageUseSSL:            envBool("MEDIA_OBJECT_STORAGE_USE_SSL", true),
			ObjectStorageForcePathStyle:    envBool("MEDIA_OBJECT_STORAGE_FORCE_PATH_STYLE", true),
			ObjectStoragePrefix:            env("MEDIA_OBJECT_STORAGE_PREFIX", ""),
			ObjectStorageHotClass:          env("MEDIA_OBJECT_STORAGE_HOT_CLASS", "STANDARD"),
			ObjectStorageWarmClass:         env("MEDIA_OBJECT_STORAGE_WARM_CLASS", "STANDARD_IA"),
			ObjectStorageArchiveClass:      env("MEDIA_OBJECT_STORAGE_ARCHIVE_CLASS", "GLACIER_IR"),
			SignedURLTTL:                   envDuration("MEDIA_SIGNED_URL_TTL", 15*time.Minute),
			RecordingRetention:             envDuration("MEDIA_RECORDING_RETENTION", 90*24*time.Hour),
			RecordingCleanupInterval:       envDuration("MEDIA_RECORDING_CLEANUP_INTERVAL", time.Hour),
			RecordingProcessingInterval:    envDuration("MEDIA_RECORDING_PROCESSING_INTERVAL", 15*time.Second),
			RecordingLifecycleInterval:     envDuration("MEDIA_RECORDING_LIFECYCLE_INTERVAL", 12*time.Hour),
			RecordingWorkDir:               env("MEDIA_RECORDING_WORK_DIR", "./recording-work"),
			RecordingWorkspaceQuota:        envInt64("MEDIA_RECORDING_WORKSPACE_QUOTA_BYTES", 0),
			RecordingMaxAttempts:           envInt("MEDIA_RECORDING_MAX_ATTEMPTS", 5),
			RecordingRetryBackoff:          envDuration("MEDIA_RECORDING_RETRY_BACKOFF", 10*time.Second),
			RecordingWarmAfter:             envDuration("MEDIA_RECORDING_WARM_AFTER", 30*24*time.Hour),
			RecordingArchiveAfter:          envDuration("MEDIA_RECORDING_ARCHIVE_AFTER", 90*24*time.Hour),
			RecordingCompositeBinary:       env("MEDIA_RECORDING_COMPOSITE_BINARY", "ffmpeg"),
			RecordingCompositeFormat:       env("MEDIA_RECORDING_COMPOSITE_FORMAT", "mp4"),
			RecordingCompositeWidth:        envInt("MEDIA_RECORDING_COMPOSITE_WIDTH", 1280),
			RecordingCompositeHeight:       envInt("MEDIA_RECORDING_COMPOSITE_HEIGHT", 720),
			RecordingCompositeVideoBitrate: env("MEDIA_RECORDING_COMPOSITE_VIDEO_BITRATE", "3500k"),
			RecordingCompositeAudioBitrate: env("MEDIA_RECORDING_COMPOSITE_AUDIO_BITRATE", "128k"),
			RecordingTranscriptSampleRate:  envInt("MEDIA_RECORDING_TRANSCRIPT_SAMPLE_RATE", 16000),
		},
		WebRTC: WebRTCConfig{
			STUNServers:                     envList("WEBRTC_STUN_SERVERS", "stun:stun.l.google.com:19302"),
			TURNServer:                      env("WEBRTC_TURN_SERVER", ""),
			TURNUsername:                    env("WEBRTC_TURN_USERNAME", ""),
			TURNPassword:                    env("WEBRTC_TURN_PASSWORD", ""),
			NodeID:                          env("WEBRTC_NODE_ID", ""),
			Region:                          env("WEBRTC_REGION", "global"),
			ControlURL:                      env("WEBRTC_CONTROL_URL", ""),
			MediaURL:                        env("WEBRTC_MEDIA_URL", ""),
			PortMin:                         uint16(envInt("WEBRTC_PORT_MIN", 0)),
			PortMax:                         uint16(envInt("WEBRTC_PORT_MAX", 0)),
			MaxRoomsPerNode:                 envInt("WEBRTC_MAX_ROOMS_PER_NODE", 500),
			MaxPresentersPerCall:            envInt("WEBRTC_MAX_PRESENTERS_PER_CALL", 50),
			MaxViewersPerCall:               envInt("WEBRTC_MAX_VIEWERS_PER_CALL", 10000),
			MaxScreenSharesPerCall:          envInt("WEBRTC_MAX_SCREEN_SHARES_PER_CALL", 1),
			MaxTracksPerPresenter:           envInt("WEBRTC_MAX_TRACKS_PER_PRESENTER", 8),
			EnableExperimentalLyra:          envBool("WEBRTC_ENABLE_EXPERIMENTAL_LYRA", false),
			MediaTokenTTL:                   envDuration("WEBRTC_MEDIA_TOKEN_TTL", 5*time.Minute),
			NodeHeartbeatInterval:           envDuration("WEBRTC_NODE_HEARTBEAT_INTERVAL", 5*time.Second),
			NodeTTL:                         envDuration("WEBRTC_NODE_TTL", 20*time.Second),
			ServerTelemetryInterval:         envDuration("WEBRTC_SERVER_TELEMETRY_INTERVAL", 5*time.Second),
			OneToOneParticipantCap:          envInt("WEBRTC_ONE_TO_ONE_PARTICIPANT_CAP", 2),
			GroupParticipantCap:             envInt("WEBRTC_GROUP_PARTICIPANT_CAP", 32),
			MeetingParticipantCap:           envInt("WEBRTC_MEETING_PARTICIPANT_CAP", 500),
			WebinarParticipantCap:           envInt("WEBRTC_WEBINAR_PARTICIPANT_CAP", 10000),
			SelectorParticipantCap:          envInt("WEBRTC_SELECTOR_PARTICIPANT_CAP", 10000),
			WebinarPresenterCap:             envInt("WEBRTC_WEBINAR_PRESENTER_CAP", 50),
			SelectorPresenterCap:            envInt("WEBRTC_SELECTOR_PRESENTER_CAP", 12),
			WebinarFanoutThreshold:          envInt("WEBRTC_WEBINAR_FANOUT_THRESHOLD", 1500),
			TURNStrategy:                    env("WEBRTC_TURN_STRATEGY", "regional_turn_pool"),
			DefaultQualityMode:              env("WEBRTC_QUALITY_POLICY_MODE", "auto"),
			AlertPacketLossPct:              envFloat("WEBRTC_QUALITY_ALERT_PACKET_LOSS_PCT", 5),
			AlertJitterMs:                   envFloat("WEBRTC_QUALITY_ALERT_JITTER_MS", 70),
			AlertRoundTripTimeMs:            envFloat("WEBRTC_QUALITY_ALERT_RTT_MS", 400),
			CorrelationTolerancePct:         envFloat("WEBRTC_QUALITY_CORRELATION_TOLERANCE_PCT", 8),
			CorrelationToleranceMs:          envFloat("WEBRTC_QUALITY_CORRELATION_TOLERANCE_MS", 80),
			ServerDrivenEnabled:             envBool("WEBRTC_SERVER_DRIVEN_ADAPTATION_ENABLED", true),
			ServerDrivenMinInterval:         envDuration("WEBRTC_SERVER_DRIVEN_ADAPTATION_MIN_INTERVAL", 4*time.Second),
			AdaptiveLowLayerMinKbps:         envInt("WEBRTC_ADAPTIVE_LOW_LAYER_MIN_KBPS", 180),
			AdaptiveMediumLayerMinKbps:      envInt("WEBRTC_ADAPTIVE_MEDIUM_LAYER_MIN_KBPS", 650),
			AdaptiveHighLayerMinKbps:        envInt("WEBRTC_ADAPTIVE_HIGH_LAYER_MIN_KBPS", 1600),
			AdaptiveCriticalLayerKbps:       envInt("WEBRTC_ADAPTIVE_CRITICAL_LAYER_KBPS", 120),
			AdaptiveMinUpswitchInterval:     envDuration("WEBRTC_ADAPTIVE_MIN_UPSWITCH_INTERVAL", 8*time.Second),
			AdaptiveMinDownswitchInterval:   envDuration("WEBRTC_ADAPTIVE_MIN_DOWNSWITCH_INTERVAL", 1500*time.Millisecond),
			AdaptiveGoodSamplesForUpgrade:   envInt("WEBRTC_ADAPTIVE_GOOD_SAMPLES_FOR_UPGRADE", 3),
			AdaptivePoorSamplesForDowngrade: envInt("WEBRTC_ADAPTIVE_POOR_SAMPLES_FOR_DOWNGRADE", 1),
			AdaptiveEWMAAlpha:               envFloat("WEBRTC_ADAPTIVE_EWMA_ALPHA", 0.35),
		},
		Search: SearchConfig{
			TextConfig:     env("SEARCH_TEXT_CONFIG", "simple"),
			WorkerInterval: envDuration("SEARCH_WORKER_INTERVAL", 2*time.Second),
			BatchSize:      envInt("SEARCH_BATCH_SIZE", 100),
			MaxAttempts:    envInt("SEARCH_MAX_ATTEMPTS", 8),
			RetryBackoff:   envDuration("SEARCH_RETRY_BACKOFF", 5*time.Second),
		},
		Realtime: RealtimeConfig{
			WorkerInterval:     envDuration("REALTIME_WORKER_INTERVAL", 2*time.Second),
			BatchSize:          envInt("REALTIME_BATCH_SIZE", 200),
			MaxAttempts:        envInt("REALTIME_MAX_ATTEMPTS", 8),
			RetryBackoff:       envDuration("REALTIME_RETRY_BACKOFF", 3*time.Second),
			ReplayLimit:        envInt("REALTIME_REPLAY_LIMIT", 200),
			WSStateTTL:         envDuration("REALTIME_WS_STATE_TTL", 24*time.Hour),
			IdempotencyTTL:     envDuration("REALTIME_IDEMPOTENCY_TTL", 24*time.Hour),
			IdempotencyMaxBody: envInt64("REALTIME_IDEMPOTENCY_MAX_BODY_BYTES", 1<<20),
		},
		Observability: ObservabilityConfig{
			EventLagWarn:                envDuration("OBS_EVENT_LAG_WARN", 15*time.Second),
			EventLagCritical:            envDuration("OBS_EVENT_LAG_CRITICAL", time.Minute),
			DeadLetterWarn:              int64(envInt("OBS_DEAD_LETTER_WARN", 10)),
			DeadLetterCritical:          int64(envInt("OBS_DEAD_LETTER_CRITICAL", 50)),
			DBUtilizationWarnPct:        envFloat("OBS_DB_UTIL_WARN_PCT", 75),
			DBUtilizationCriticalPct:    envFloat("OBS_DB_UTIL_CRITICAL_PCT", 90),
			RedisUtilizationWarnPct:     envFloat("OBS_REDIS_UTIL_WARN_PCT", 75),
			RedisUtilizationCriticalPct: envFloat("OBS_REDIS_UTIL_CRITICAL_PCT", 90),
			WorkerStallAfter:            envDuration("OBS_WORKER_STALL_AFTER", 2*time.Minute),
			CallDegradedWarnRatio:       envFloat("OBS_CALL_DEGRADED_WARN_RATIO", 5),
			CallDegradedCriticalRatio:   envFloat("OBS_CALL_DEGRADED_CRITICAL_RATIO", 15),
			ReplaySuccessTargetPct:      envFloat("OBS_REPLAY_SUCCESS_TARGET_PCT", 99),
			RecordingSuccessTargetPct:   envFloat("OBS_RECORDING_SUCCESS_TARGET_PCT", 99),
			ConsumerLagWarn:             int64(envInt("OBS_CONSUMER_LAG_WARN", 250)),
			ConsumerLagCritical:         int64(envInt("OBS_CONSUMER_LAG_CRITICAL", 1000)),
		},
		CORS: CORSConfig{
			AllowedOrigins: envList("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:5173"),
		},
	}

	// Validate required secrets.
	var missing []string
	if cfg.DB.User == "" {
		missing = append(missing, "DB_USER")
	}
	if cfg.DB.Password == "" {
		missing = append(missing, "DB_PASSWORD")
	}
	if cfg.DB.Name == "" {
		missing = append(missing, "DB_NAME")
	}
	if cfg.JWT.Secret == "" {
		missing = append(missing, "JWT_SECRET")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}
	if len(cfg.JWT.Secret) < 64 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 64 characters for HS256")
	}

	// Validate S3 credentials when S3 backend is selected.
	if cfg.Media.StorageBackend == "s3" {
		if cfg.Media.ObjectStorageAccessKey == "" || cfg.Media.ObjectStorageSecretKey == "" {
			return nil, fmt.Errorf("MEDIA_OBJECT_STORAGE_ACCESS_KEY and MEDIA_OBJECT_STORAGE_SECRET_KEY are required when using s3 storage backend")
		}
		if cfg.Media.ObjectStorageBucket == "" {
			return nil, fmt.Errorf("MEDIA_OBJECT_STORAGE_BUCKET is required when using s3 storage backend")
		}
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envRequired returns the environment variable value or empty string.
// Callers must validate required values after Load() builds the config.
func envRequired(key string) string {
	return os.Getenv(key)
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			slog.Warn("invalid integer env var, using default", "key", key, "value", v, "default", fallback)
			return fallback
		}
		return n
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			slog.Warn("invalid int64 env var, using default", "key", key, "value", v, "default", fallback)
			return fallback
		}
		return n
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseBool(v)
		if err != nil {
			return fallback
		}
		return n
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			slog.Warn("invalid float env var, using default", "key", key, "value", v, "default", fallback)
			return fallback
		}
		return n
	}
	return fallback
}

func envList(key, fallback string) []string {
	v := env(key, fallback)
	parts := strings.Split(v, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			slog.Warn("invalid duration env var, using default", "key", key, "value", v, "default", fallback)
			return fallback
		}
		return d
	}
	return fallback
}
