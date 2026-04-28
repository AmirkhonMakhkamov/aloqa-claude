package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	DSN                   string
	MaxConns              int32
	MinConns              int32
	ConnectTimeout        time.Duration
	QueryTimeout          time.Duration
	StatementTimeout      time.Duration
	LockTimeout           time.Duration
	IdleInTxTimeout       time.Duration
	MaxConnLifetime       time.Duration
	MaxConnLifetimeJitter time.Duration
	MaxConnIdleTime       time.Duration
	HealthCheckPeriod     time.Duration
}

type MigrationAudit struct {
	Dir      string   `json:"dir"`
	Files    []string `json:"files"`
	Problems []string `json:"problems"`
	Valid    bool     `json:"valid"`
}

var migrationFilePattern = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

// New creates a PostgreSQL connection pool with the given DSN and pool settings.
func New(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 20
	}
	if cfg.MinConns < 0 {
		cfg.MinConns = 0
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.MaxConnLifetime <= 0 {
		cfg.MaxConnLifetime = time.Hour
	}
	if cfg.MaxConnLifetimeJitter < 0 {
		cfg.MaxConnLifetimeJitter = 0
	}
	if cfg.MaxConnIdleTime <= 0 {
		cfg.MaxConnIdleTime = 30 * time.Minute
	}
	if cfg.HealthCheckPeriod <= 0 {
		cfg.HealthCheckPeriod = 30 * time.Second
	}

	parsed, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parsing postgres dsn: %w", err)
	}

	parsed.MaxConns = cfg.MaxConns
	parsed.MinConns = cfg.MinConns
	parsed.MaxConnLifetime = cfg.MaxConnLifetime
	parsed.MaxConnLifetimeJitter = cfg.MaxConnLifetimeJitter
	parsed.MaxConnIdleTime = cfg.MaxConnIdleTime
	parsed.HealthCheckPeriod = cfg.HealthCheckPeriod
	parsed.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	if parsed.ConnConfig.RuntimeParams == nil {
		parsed.ConnConfig.RuntimeParams = make(map[string]string)
	}
	parsed.ConnConfig.RuntimeParams["application_name"] = "aloqa-api"
	if cfg.StatementTimeout > 0 {
		parsed.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	}
	if cfg.LockTimeout > 0 {
		parsed.ConnConfig.RuntimeParams["lock_timeout"] = strconv.FormatInt(cfg.LockTimeout.Milliseconds(), 10)
	}
	if cfg.IdleInTxTimeout > 0 {
		parsed.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(cfg.IdleInTxTimeout.Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}

	slog.Info("postgres connected",
		slog.String("host", parsed.ConnConfig.Host),
		slog.Uint64("port", uint64(parsed.ConnConfig.Port)),
		slog.String("database", parsed.ConnConfig.Database),
		slog.Int("max_conns", int(cfg.MaxConns)),
		slog.Int("min_conns", int(cfg.MinConns)),
		slog.Int64("statement_timeout_ms", cfg.StatementTimeout.Milliseconds()),
		slog.Int64("lock_timeout_ms", cfg.LockTimeout.Milliseconds()),
		slog.Int64("query_timeout_ms", cfg.QueryTimeout.Milliseconds()),
	)

	return pool, nil
}

func ValidateMigrationFiles(dir string) (*MigrationAudit, error) {
	report := &MigrationAudit{
		Dir:   dir,
		Valid: true,
	}
	if dir == "" {
		report.Valid = false
		report.Problems = append(report.Problems, "migration directory is empty")
		return report, fmt.Errorf("migration directory is empty")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		report.Valid = false
		report.Problems = append(report.Problems, err.Error())
		return report, fmt.Errorf("read migration directory: %w", err)
	}

	seen := map[int]string{}
	var numbers []int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".sql" {
			continue
		}
		report.Files = append(report.Files, name)
		match := migrationFilePattern.FindStringSubmatch(name)
		if match == nil {
			report.Valid = false
			report.Problems = append(report.Problems, fmt.Sprintf("invalid migration filename: %s", name))
			continue
		}
		number, convErr := strconv.Atoi(match[1])
		if convErr != nil {
			report.Valid = false
			report.Problems = append(report.Problems, fmt.Sprintf("invalid migration number: %s", name))
			continue
		}
		if existing, ok := seen[number]; ok {
			report.Valid = false
			report.Problems = append(report.Problems, fmt.Sprintf("duplicate migration number %03d: %s and %s", number, existing, name))
			continue
		}
		seen[number] = name
		numbers = append(numbers, number)
	}

	sort.Ints(numbers)
	for i, number := range numbers {
		expected := i + 1
		if number != expected {
			report.Valid = false
			report.Problems = append(report.Problems, fmt.Sprintf("missing migration number %03d before %03d", expected, number))
			break
		}
	}
	if len(numbers) == 0 {
		report.Valid = false
		report.Problems = append(report.Problems, "no .sql migration files found")
	}
	if !report.Valid {
		return report, fmt.Errorf("migration validation failed")
	}
	return report, nil
}
