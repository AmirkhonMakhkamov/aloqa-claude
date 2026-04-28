package entity

import "time"

type StorageRuntimeReport struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Postgres    PostgresRuntimeStats  `json:"postgres"`
	Redis       RedisRuntimeStats     `json:"redis"`
	Migrations  StorageMigrationAudit `json:"migrations"`
}

type PostgresRuntimeStats struct {
	MaxConns                int32   `json:"max_conns"`
	TotalConns              int32   `json:"total_conns"`
	AcquiredConns           int32   `json:"acquired_conns"`
	IdleConns               int32   `json:"idle_conns"`
	AcquiredConnsPct        float64 `json:"acquired_conns_pct"`
	AcquireCount            int64   `json:"acquire_count"`
	EmptyAcquireCount       int64   `json:"empty_acquire_count"`
	CanceledAcquireCount    int64   `json:"canceled_acquire_count"`
	EmptyAcquireWaitMs      int64   `json:"empty_acquire_wait_ms"`
	AcquireDurationMs       int64   `json:"acquire_duration_ms"`
	NewConnsCount           int64   `json:"new_conns_count"`
	MaxLifetimeDestroyCount int64   `json:"max_lifetime_destroy_count"`
	MaxIdleDestroyCount     int64   `json:"max_idle_destroy_count"`
	ConstructingConns       int32   `json:"constructing_conns"`
	Saturated               bool    `json:"saturated"`
	QueryTimeoutMs          int64   `json:"query_timeout_ms"`
	StatementTimeoutMs      int64   `json:"statement_timeout_ms"`
	LockTimeoutMs           int64   `json:"lock_timeout_ms"`
}

type RedisRuntimeStats struct {
	PoolSize           int     `json:"pool_size"`
	TotalConns         uint32  `json:"total_conns"`
	IdleConns          uint32  `json:"idle_conns"`
	StaleConns         uint32  `json:"stale_conns"`
	Misses             uint32  `json:"misses"`
	Hits               uint32  `json:"hits"`
	Timeouts           uint32  `json:"timeouts"`
	UtilizationPct     float64 `json:"utilization_pct"`
	Saturated          bool    `json:"saturated"`
	OperationTimeoutMs int64   `json:"operation_timeout_ms"`
	PresenceShards     int     `json:"presence_shards"`
}

type StorageMigrationAudit struct {
	Dir       string   `json:"dir"`
	FileCount int      `json:"file_count"`
	Files     []string `json:"files"`
	Problems  []string `json:"problems"`
	Valid     bool     `json:"valid"`
}

type StorageAuditReport struct {
	GeneratedAt       time.Time              `json:"generated_at"`
	QueryProfiles     []StorageQueryProfile  `json:"query_profiles"`
	TableStats        []StorageTableStat     `json:"table_stats"`
	Indexes           []StorageIndexAudit    `json:"indexes"`
	PaginationReview  []string               `json:"pagination_review"`
	ArchivingStrategy []StorageArchivingRule `json:"archiving_strategy"`
}

type StorageQueryProfile struct {
	QueryID     string  `json:"query_id"`
	Calls       int64   `json:"calls"`
	Rows        int64   `json:"rows"`
	TotalExecMs float64 `json:"total_exec_ms"`
	MeanExecMs  float64 `json:"mean_exec_ms"`
	SharedHit   int64   `json:"shared_hit"`
	SharedRead  int64   `json:"shared_read"`
	Query       string  `json:"query"`
}

type StorageTableStat struct {
	Table             string `json:"table"`
	EstimatedRows     int64  `json:"estimated_rows"`
	LiveTuples        int64  `json:"live_tuples"`
	DeadTuples        int64  `json:"dead_tuples"`
	SeqScans          int64  `json:"seq_scans"`
	IndexScans        int64  `json:"index_scans"`
	TotalSizeBytes    int64  `json:"total_size_bytes"`
	TableSizeBytes    int64  `json:"table_size_bytes"`
	IndexSizeBytes    int64  `json:"index_size_bytes"`
	RecommendedAction string `json:"recommended_action"`
}

type StorageIndexAudit struct {
	Table          string `json:"table"`
	Name           string `json:"name"`
	Definition     string `json:"definition"`
	SizeBytes      int64  `json:"size_bytes"`
	Scans          int64  `json:"scans"`
	TuplesRead     int64  `json:"tuples_read"`
	TuplesFetched  int64  `json:"tuples_fetched"`
	Valid          bool   `json:"valid"`
	Ready          bool   `json:"ready"`
	RecommendedUse string `json:"recommended_use"`
}

type StorageArchivingRule struct {
	Table     string `json:"table"`
	Retention string `json:"retention"`
	Strategy  string `json:"strategy"`
	Rationale string `json:"rationale"`
}
