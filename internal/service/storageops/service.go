package storageops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/domain/entity"
	"aloqa/internal/platform/db"
	"aloqa/internal/platform/reliability"
)

type Config struct {
	MigrationsDir      string
	QueryTimeout       time.Duration
	StatementTimeout   time.Duration
	LockTimeout        time.Duration
	ProfileTopQueries  int
	RedisPoolSize      int
	RedisOpTimeout     time.Duration
	PresenceShardCount int
}

type Service struct {
	pool   *pgxpool.Pool
	rdb    *redis.Client
	config Config
}

func NewService(pool *pgxpool.Pool, rdb *redis.Client, cfg Config) *Service {
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = 5 * time.Second
	}
	if cfg.ProfileTopQueries <= 0 {
		cfg.ProfileTopQueries = 10
	}
	return &Service{
		pool:   pool,
		rdb:    rdb,
		config: cfg,
	}
}

func (s *Service) RuntimeReport(ctx context.Context) (*entity.StorageRuntimeReport, error) {
	report := &entity.StorageRuntimeReport{
		GeneratedAt: time.Now().UTC(),
	}
	if s.pool != nil {
		stat := s.pool.Stat()
		utilization := 0.0
		if stat.MaxConns() > 0 {
			utilization = (float64(stat.AcquiredConns()) / float64(stat.MaxConns())) * 100
		}
		report.Postgres = entity.PostgresRuntimeStats{
			MaxConns:                stat.MaxConns(),
			TotalConns:              stat.TotalConns(),
			AcquiredConns:           stat.AcquiredConns(),
			IdleConns:               stat.IdleConns(),
			AcquiredConnsPct:        utilization,
			AcquireCount:            stat.AcquireCount(),
			EmptyAcquireCount:       stat.EmptyAcquireCount(),
			CanceledAcquireCount:    stat.CanceledAcquireCount(),
			EmptyAcquireWaitMs:      stat.EmptyAcquireWaitTime().Milliseconds(),
			AcquireDurationMs:       stat.AcquireDuration().Milliseconds(),
			NewConnsCount:           stat.NewConnsCount(),
			MaxLifetimeDestroyCount: stat.MaxLifetimeDestroyCount(),
			MaxIdleDestroyCount:     stat.MaxIdleDestroyCount(),
			ConstructingConns:       stat.ConstructingConns(),
			Saturated:               reliability.PostgresPressure(stat).Saturated,
			QueryTimeoutMs:          s.config.QueryTimeout.Milliseconds(),
			StatementTimeoutMs:      s.config.StatementTimeout.Milliseconds(),
			LockTimeoutMs:           s.config.LockTimeout.Milliseconds(),
		}
	}
	if s.rdb != nil {
		stats := s.rdb.PoolStats()
		utilization := 0.0
		if s.config.RedisPoolSize > 0 {
			utilization = (float64(stats.TotalConns) / float64(s.config.RedisPoolSize)) * 100
		}
		report.Redis = entity.RedisRuntimeStats{
			PoolSize:           s.config.RedisPoolSize,
			TotalConns:         stats.TotalConns,
			IdleConns:          stats.IdleConns,
			StaleConns:         stats.StaleConns,
			Misses:             stats.Misses,
			Hits:               stats.Hits,
			Timeouts:           stats.Timeouts,
			UtilizationPct:     utilization,
			Saturated:          reliability.RedisPressure(stats, s.config.RedisPoolSize).Saturated,
			OperationTimeoutMs: s.config.RedisOpTimeout.Milliseconds(),
			PresenceShards:     s.config.PresenceShardCount,
		}
	}
	if audit, err := db.ValidateMigrationFiles(s.config.MigrationsDir); err == nil {
		report.Migrations = entity.StorageMigrationAudit{
			Dir:       audit.Dir,
			FileCount: len(audit.Files),
			Files:     audit.Files,
			Problems:  audit.Problems,
			Valid:     audit.Valid,
		}
	} else if audit != nil {
		report.Migrations = entity.StorageMigrationAudit{
			Dir:       audit.Dir,
			FileCount: len(audit.Files),
			Files:     audit.Files,
			Problems:  audit.Problems,
			Valid:     audit.Valid,
		}
	}
	return report, nil
}

func (s *Service) Audit(ctx context.Context) (*entity.StorageAuditReport, error) {
	report := &entity.StorageAuditReport{
		GeneratedAt:       time.Now().UTC(),
		PaginationReview:  defaultPaginationReview(),
		ArchivingStrategy: defaultArchivingStrategy(),
	}
	if s.pool == nil {
		return report, nil
	}

	queryProfiles, err := s.queryProfiles(ctx)
	if err != nil {
		return nil, err
	}
	tableStats, err := s.tableStats(ctx)
	if err != nil {
		return nil, err
	}
	indexes, err := s.indexAudit(ctx)
	if err != nil {
		return nil, err
	}
	report.QueryProfiles = queryProfiles
	report.TableStats = tableStats
	report.Indexes = indexes
	return report, nil
}

func (s *Service) queryProfiles(ctx context.Context) ([]entity.StorageQueryProfile, error) {
	opCtx, cancel := reliability.WithTimeout(ctx, s.config.QueryTimeout)
	defer cancel()

	rows, err := s.pool.Query(opCtx, `
		SELECT
			queryid::text,
			calls,
			rows,
			total_exec_time,
			mean_exec_time,
			shared_blks_hit,
			shared_blks_read,
			LEFT(REGEXP_REPLACE(query, '\s+', ' ', 'g'), 240)
		FROM pg_stat_statements
		ORDER BY total_exec_time DESC
		LIMIT $1
	`, s.config.ProfileTopQueries)
	if err != nil {
		if ignoreMissingStatsView(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("storageops: query profiles: %w", err)
	}
	defer rows.Close()

	profiles := make([]entity.StorageQueryProfile, 0, s.config.ProfileTopQueries)
	for rows.Next() {
		var item entity.StorageQueryProfile
		if err := rows.Scan(&item.QueryID, &item.Calls, &item.Rows, &item.TotalExecMs, &item.MeanExecMs, &item.SharedHit, &item.SharedRead, &item.Query); err != nil {
			return nil, fmt.Errorf("storageops: scan query profile: %w", err)
		}
		profiles = append(profiles, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storageops: iterate query profiles: %w", err)
	}
	return profiles, nil
}

func (s *Service) tableStats(ctx context.Context) ([]entity.StorageTableStat, error) {
	opCtx, cancel := reliability.WithTimeout(ctx, s.config.QueryTimeout)
	defer cancel()

	rows, err := s.pool.Query(opCtx, `
		SELECT
			schemaname || '.' || relname AS table_name,
			reltuples::bigint AS estimated_rows,
			n_live_tup,
			n_dead_tup,
			seq_scan,
			idx_scan,
			pg_total_relation_size(relid),
			pg_relation_size(relid),
			pg_indexes_size(relid)
		FROM pg_stat_user_tables
		ORDER BY pg_total_relation_size(relid) DESC, relname ASC
		LIMIT 25
	`)
	if err != nil {
		return nil, fmt.Errorf("storageops: query table stats: %w", err)
	}
	defer rows.Close()

	stats := make([]entity.StorageTableStat, 0, 25)
	for rows.Next() {
		var item entity.StorageTableStat
		if err := rows.Scan(
			&item.Table,
			&item.EstimatedRows,
			&item.LiveTuples,
			&item.DeadTuples,
			&item.SeqScans,
			&item.IndexScans,
			&item.TotalSizeBytes,
			&item.TableSizeBytes,
			&item.IndexSizeBytes,
		); err != nil {
			return nil, fmt.Errorf("storageops: scan table stat: %w", err)
		}
		item.RecommendedAction = recommendTableAction(item)
		stats = append(stats, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storageops: iterate table stats: %w", err)
	}
	return stats, nil
}

func (s *Service) indexAudit(ctx context.Context) ([]entity.StorageIndexAudit, error) {
	opCtx, cancel := reliability.WithTimeout(ctx, s.config.QueryTimeout)
	defer cancel()

	rows, err := s.pool.Query(opCtx, `
		SELECT
			ns.nspname || '.' || tbl.relname AS table_name,
			idx.relname AS index_name,
			pg_get_indexdef(ix.indexrelid),
			pg_relation_size(ix.indexrelid),
			COALESCE(psui.idx_scan, 0),
			COALESCE(psui.idx_tup_read, 0),
			COALESCE(psui.idx_tup_fetch, 0),
			ix.indisvalid,
			ix.indisready
		FROM pg_index ix
		JOIN pg_class idx ON idx.oid = ix.indexrelid
		JOIN pg_class tbl ON tbl.oid = ix.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		LEFT JOIN pg_stat_user_indexes psui ON psui.indexrelid = ix.indexrelid
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		ORDER BY pg_relation_size(ix.indexrelid) DESC, idx.relname ASC
		LIMIT 50
	`)
	if err != nil {
		return nil, fmt.Errorf("storageops: query index audit: %w", err)
	}
	defer rows.Close()

	audits := make([]entity.StorageIndexAudit, 0, 50)
	for rows.Next() {
		var item entity.StorageIndexAudit
		if err := rows.Scan(
			&item.Table,
			&item.Name,
			&item.Definition,
			&item.SizeBytes,
			&item.Scans,
			&item.TuplesRead,
			&item.TuplesFetched,
			&item.Valid,
			&item.Ready,
		); err != nil {
			return nil, fmt.Errorf("storageops: scan index audit: %w", err)
		}
		item.RecommendedUse = recommendIndexAction(item)
		audits = append(audits, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storageops: iterate index audit: %w", err)
	}
	return audits, nil
}

func ignoreMissingStatsView(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01" || pgErr.Code == "42501"
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "pg_stat_statements") && (strings.Contains(msg, "does not exist") || strings.Contains(msg, "permission denied"))
}

func recommendTableAction(item entity.StorageTableStat) string {
	switch {
	case item.DeadTuples > 100000:
		return "high dead tuple count; review vacuum/autovacuum thresholds"
	case strings.Contains(item.Table, "realtime_events"):
		return "archive published/dead events by time window to keep replay table bounded"
	case strings.Contains(item.Table, "media_qos_samples"):
		return "partition or archive QoS samples by day/week for long-running deployments"
	case strings.Contains(item.Table, "audit_log"):
		return "archive audit records by retention policy and legal/compliance requirements"
	case strings.Contains(item.Table, "search_index_jobs"):
		return "purge processed/dead search jobs on a rolling retention window"
	default:
		return "healthy"
	}
}

func recommendIndexAction(item entity.StorageIndexAudit) string {
	switch {
	case !item.Valid || !item.Ready:
		return "repair or rebuild invalid index before relying on it operationally"
	case item.Scans == 0 && item.SizeBytes > 64*1024*1024:
		return "low-usage large index; review whether it still matches production query paths"
	default:
		return "healthy"
	}
}

func defaultPaginationReview() []string {
	return []string{
		"Hot chat timelines already use cursor pagination and should stay keyset-based.",
		"Administrative listings still rely on offset pagination; move very large audit/search/recording feeds to cursor pagination before multi-million row scale.",
		"Stable ordering should always include a monotonic tie-breaker, not just created_at.",
	}
}

func defaultArchivingStrategy() []entity.StorageArchivingRule {
	return []entity.StorageArchivingRule{
		{Table: "realtime_events", Retention: "7-30 days", Strategy: "time-window delete or move to cold storage after replay horizon", Rationale: "replayable outbox data grows quickly and is not primary history forever"},
		{Table: "search_index_jobs", Retention: "3-7 days", Strategy: "purge processed/dead jobs after operator review window", Rationale: "queue table should remain operational, not historical"},
		{Table: "media_qos_samples", Retention: "7-30 days hot, longer cold aggregate", Strategy: "partition by time and downsample older samples", Rationale: "high-ingest telemetry tables become the largest tables fast"},
		{Table: "audit_log", Retention: "policy-driven", Strategy: "archive by compliance tier and legal hold", Rationale: "durable but expensive long-tail access pattern"},
		{Table: "notifications", Retention: "30-90 days", Strategy: "delete or archive read notifications", Rationale: "user inbox history is operationally hot only for a limited time"},
	}
}
