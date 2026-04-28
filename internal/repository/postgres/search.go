package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/platform/reliability"
	"aloqa/internal/service/search"
)

const (
	searchJobStatusPending    = "pending"
	searchJobStatusProcessing = "processing"
	searchJobStatusFailed     = "failed"
	searchJobStatusProcessed  = "processed"
	searchJobStatusDead       = "dead"

	searchJobOperationUpsert = "upsert"
	searchJobOperationDelete = "delete"
)

var searchTextConfigPattern = regexp.MustCompile(`^[a-z_]+$`)

type SearchRepoConfig struct {
	TextConfig       string
	MaxAttempts      int
	RetryBackoff     time.Duration
	OperationTimeout time.Duration
}

type SearchRepo struct {
	pool         *pgxpool.Pool
	db           queryable
	textConfig   string
	maxAttempts  int
	retryBackoff time.Duration
	opTimeout    time.Duration
	observer     interface {
		RecordSearchBatch(processed, failed, dead int, duration time.Duration, err error)
		RecordWorkerHeartbeat(name string)
	}
}

type searchJob struct {
	ID           uuid.UUID
	WorkspaceID  uuid.UUID
	ResourceType string
	ResourceID   uuid.UUID
	Operation    string
	ChannelID    *uuid.UUID
	Title        string
	Content      string
	Metadata     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Attempts     int
	MaxAttempts  int
}

func NewSearchRepo(pool *pgxpool.Pool, cfg SearchRepoConfig) *SearchRepo {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	retryBackoff := cfg.RetryBackoff
	if retryBackoff <= 0 {
		retryBackoff = 5 * time.Second
	}

	return &SearchRepo{
		pool:         pool,
		db:           pool,
		textConfig:   normalizeSearchTextConfig(cfg.TextConfig),
		maxAttempts:  maxAttempts,
		retryBackoff: retryBackoff,
		opTimeout:    maxDuration(cfg.OperationTimeout, 5*time.Second),
	}
}

func (r *SearchRepo) withTx(tx pgx.Tx) *SearchRepo {
	if r == nil {
		return nil
	}
	return &SearchRepo{
		pool:         r.pool,
		db:           tx,
		textConfig:   r.textConfig,
		maxAttempts:  r.maxAttempts,
		retryBackoff: r.retryBackoff,
		opTimeout:    r.opTimeout,
		observer:     r.observer,
	}
}

func (r *SearchRepo) SetObserver(observer interface {
	RecordSearchBatch(processed, failed, dead int, duration time.Duration, err error)
	RecordWorkerHeartbeat(name string)
}) {
	r.observer = observer
}

func (r *SearchRepo) QueueStats(ctx context.Context) (*entity.QueueRuntimeStats, error) {
	var stats entity.QueueRuntimeStats
	stats.Name = "search_indexer"
	stats.UpdatedAt = time.Now().UTC()
	if r.pool == nil {
		return &stats, nil
	}
	row := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending')::bigint,
			COUNT(*) FILTER (WHERE status = 'processing')::bigint,
			COUNT(*) FILTER (WHERE status = 'failed')::bigint,
			COUNT(*) FILTER (WHERE status = 'dead')::bigint,
			COALESCE((EXTRACT(EPOCH FROM NOW() - MIN(created_at)) * 1000)::bigint, 0)
		FROM search_index_jobs
		WHERE status IN ('pending', 'processing', 'failed', 'dead')
	`)
	if err := row.Scan(&stats.Pending, &stats.Processing, &stats.Failed, &stats.Dead, &stats.OldestPendingAgeMs); err != nil {
		return nil, fmt.Errorf("postgres: search queue stats: %w", err)
	}
	return &stats, nil
}

func normalizeSearchTextConfig(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "simple"
	}
	if !searchTextConfigPattern.MatchString(value) {
		return "simple"
	}
	return value
}

func (r *SearchRepo) EnqueueUpsert(ctx context.Context, doc search.Document) error {
	metadata, err := json.Marshal(doc.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal search metadata: %w", err)
	}

	query := `
		INSERT INTO search_index_jobs (
			id, workspace_id, resource_type, resource_id, operation, channel_id,
			title, content, metadata, created_at, updated_at, available_at,
			locked_at, processed_at, attempts, max_attempts, status, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NULL, NULL, 0, $12, $13, NULL)
		ON CONFLICT (workspace_id, resource_type, resource_id) DO UPDATE
		SET operation = EXCLUDED.operation,
			channel_id = EXCLUDED.channel_id,
			title = EXCLUDED.title,
			content = EXCLUDED.content,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			available_at = NOW(),
			locked_at = NULL,
			processed_at = NULL,
			attempts = 0,
			max_attempts = EXCLUDED.max_attempts,
			status = $13,
			last_error = NULL`

	if _, err := r.db.Exec(ctx, query,
		uuid.New(),
		doc.WorkspaceID,
		string(doc.Type),
		doc.ResourceID,
		searchJobOperationUpsert,
		doc.ChannelID,
		doc.Title,
		doc.Content,
		metadata,
		doc.CreatedAt.UTC(),
		doc.UpdatedAt.UTC(),
		r.maxAttempts,
		searchJobStatusPending,
	); err != nil {
		return fmt.Errorf("postgres: enqueue search upsert: %w", err)
	}
	return nil
}

func (r *SearchRepo) EnqueueDelete(ctx context.Context, workspaceID uuid.UUID, resourceType search.ResourceType, resourceID uuid.UUID) error {
	query := `
		INSERT INTO search_index_jobs (
			id, workspace_id, resource_type, resource_id, operation, title, content,
			metadata, created_at, updated_at, available_at, locked_at, processed_at,
			attempts, max_attempts, status, last_error
		)
		VALUES ($1, $2, $3, $4, $5, '', '', '{}'::jsonb, NOW(), NOW(), NOW(), NULL, NULL, 0, $6, $7, NULL)
		ON CONFLICT (workspace_id, resource_type, resource_id) DO UPDATE
		SET operation = EXCLUDED.operation,
			channel_id = NULL,
			title = '',
			content = '',
			metadata = '{}'::jsonb,
			updated_at = NOW(),
			available_at = NOW(),
			locked_at = NULL,
			processed_at = NULL,
			attempts = 0,
			max_attempts = EXCLUDED.max_attempts,
			status = $7,
			last_error = NULL`

	if _, err := r.db.Exec(ctx, query,
		uuid.New(),
		workspaceID,
		string(resourceType),
		resourceID,
		searchJobOperationDelete,
		r.maxAttempts,
		searchJobStatusPending,
	); err != nil {
		return fmt.Errorf("postgres: enqueue search delete: %w", err)
	}
	return nil
}

func (r *SearchRepo) Search(ctx context.Context, params search.Params) (*search.SearchResults, error) {
	query := `
			WITH search_query AS (
				SELECT websearch_to_tsquery($1::regconfig, $2) AS tsq
			),
		ranked AS (
			SELECT
				si.resource_type,
				si.resource_id,
				si.workspace_id,
				CASE
					WHEN si.resource_type = 'channel' THEN ch.id
					WHEN si.resource_type = 'user' THEN NULL
					ELSE c.id
				END AS channel_id,
				CASE
					WHEN si.title <> '' THEN si.title
					WHEN si.resource_type = 'message' THEN LEFT(si.content, 80)
					ELSE ''
				END AS title,
				ts_headline(
					$1::regconfig,
					COALESCE(NULLIF(TRIM(CONCAT(si.title, ' ', si.content)), ''), si.content),
					search_query.tsq,
					'StartSel=<mark>,StopSel=</mark>,MaxWords=24,MinWords=8,ShortWord=2,MaxFragments=2,FragmentDelimiter= … '
				) AS snippet,
				ts_rank_cd(si.tsv, search_query.tsq) +
					CASE
						WHEN LOWER(si.title) = LOWER($2) THEN 1.4
						WHEN si.title ILIKE '%' || $2 || '%' THEN 0.4
						ELSE 0
					END +
					CASE
						WHEN si.resource_type = 'channel' THEN 0.1
						WHEN si.resource_type = 'file' THEN 0.05
						ELSE 0
					END AS score,
				si.created_at,
				si.updated_at
			FROM search_index si
			CROSS JOIN search_query
			LEFT JOIN messages m
				ON si.resource_type = 'message'
				AND si.resource_id = m.id
			LEFT JOIN attachments a
				ON si.resource_type = 'file'
				AND si.resource_id = a.id
			LEFT JOIN messages fm
				ON a.message_id = fm.id
			LEFT JOIN channels c
				ON (
					si.resource_type = 'message'
					AND m.channel_id = c.id
				) OR (
					si.resource_type = 'file'
					AND fm.channel_id = c.id
				)
				LEFT JOIN channels ch
					ON si.resource_type = 'channel'
					AND si.resource_id = ch.id
				LEFT JOIN users u
					ON si.resource_type = 'user'
					AND si.resource_id = u.id
				LEFT JOIN workspace_members wm_user
					ON si.resource_type = 'user'
				AND wm_user.workspace_id = si.workspace_id
				AND wm_user.user_id = u.id
				WHERE si.workspace_id = $3
					AND si.tsv @@ search_query.tsq
					AND ($4::uuid IS NULL OR si.channel_id = $4)
					AND ($5 = '' OR si.resource_type = $5)
					AND ($6::timestamptz IS NULL OR si.created_at >= $6)
					AND ($7::timestamptz IS NULL OR si.created_at <= $7)
					AND (
						(
							si.resource_type = 'message'
							AND m.id IS NOT NULL
							AND m.deleted_at IS NULL
							AND c.id IS NOT NULL
							AND NOT c.archived
							AND c.id = ANY($8::uuid[])
						)
						OR (
							si.resource_type = 'file'
							AND a.id IS NOT NULL
							AND fm.id IS NOT NULL
							AND fm.deleted_at IS NULL
							AND c.id IS NOT NULL
							AND NOT c.archived
							AND c.id = ANY($8::uuid[])
						)
						OR (
							si.resource_type = 'channel'
							AND ch.id IS NOT NULL
							AND NOT ch.archived
							AND ch.id = ANY($8::uuid[])
						)
						OR (
							si.resource_type = 'user'
							AND u.id IS NOT NULL
							AND u.status = 'active'
							AND $9::boolean
							AND wm_user.user_id IS NOT NULL
						)
					)
			)
		SELECT
			resource_type,
			resource_id,
			workspace_id,
			channel_id,
			title,
			snippet,
			score,
			created_at,
			updated_at,
			COUNT(*) OVER()
		FROM ranked
			ORDER BY score DESC, updated_at DESC, created_at DESC
			LIMIT $10 OFFSET $11`

	rows, err := r.db.Query(ctx, query,
		r.textConfig,
		strings.TrimSpace(params.Query),
		params.WorkspaceID,
		params.ChannelID,
		strings.TrimSpace(params.Type),
		params.DateFrom,
		params.DateTo,
		params.AccessibleChannelIDs,
		params.AllowUserResults,
		params.Limit,
		params.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: search query: %w", err)
	}
	defer rows.Close()

	results := make([]search.Result, 0, params.Limit)
	total := 0
	for rows.Next() {
		var result search.Result
		if err := rows.Scan(
			&result.Type,
			&result.ID,
			&result.WorkspaceID,
			&result.ChannelID,
			&result.Title,
			&result.Snippet,
			&result.Score,
			&result.CreatedAt,
			&result.UpdatedAt,
			&total,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan search result: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate search results: %w", err)
	}

	searchResults := &search.SearchResults{
		Results: results,
		Total:   total,
	}
	if params.Offset+len(results) < total {
		searchResults.NextOffset = params.Offset + len(results)
	}
	return searchResults, nil
}

func (r *SearchRepo) RunWorker(ctx context.Context, interval time.Duration, batchSize int) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if batchSize <= 0 {
		batchSize = 100
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if r.observer != nil {
			r.observer.RecordWorkerHeartbeat("search_indexer")
		}

		if pressure := r.Pressure(); pressure.Saturated {
			slog.WarnContext(ctx, "search worker backpressure active", "utilization", pressure.Utilization, "queued_waiters", pressure.QueuedWaiters)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}

		startedAt := time.Now()
		processed, failed, dead, err := r.ProcessPendingJobs(ctx, batchSize)
		if r.observer != nil {
			r.observer.RecordSearchBatch(processed, failed, dead, time.Since(startedAt), err)
		}
		if err != nil {
			slog.ErrorContext(ctx, "search worker batch failed", "error", err)
		} else if processed > 0 || failed > 0 || dead > 0 {
			slog.InfoContext(ctx, "search worker processed batch", "processed", processed, "failed", failed, "dead", dead)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *SearchRepo) ProcessPendingJobs(ctx context.Context, batchSize int) (processed, failed, dead int, err error) {
	jobs, err := reliability.DoValue(ctx, r.policy(2), func(ctx context.Context) ([]searchJob, error) {
		return r.claimPendingJobs(ctx, batchSize)
	})
	if err != nil {
		return 0, 0, 0, err
	}
	for _, job := range jobs {
		if err := reliability.Do(ctx, r.policy(2), func(ctx context.Context) error {
			return r.applyJob(ctx, job)
		}); err != nil {
			status := searchJobStatusFailed
			if job.Attempts >= job.MaxAttempts {
				status = searchJobStatusDead
				dead++
			} else {
				failed++
			}
			if updateErr := reliability.Do(ctx, r.policy(2), func(ctx context.Context) error {
				return r.failJob(ctx, job, status, err)
			}); updateErr != nil {
				slog.ErrorContext(ctx, "failed to update search job failure state", "job_id", job.ID, "error", updateErr)
			}
			continue
		}
		if err := reliability.Do(ctx, r.policy(2), func(ctx context.Context) error {
			return r.completeJob(ctx, job.ID)
		}); err != nil {
			return processed, failed, dead, err
		}
		processed++
	}
	return processed, failed, dead, nil
}

func (r *SearchRepo) ReindexAll(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `SELECT id FROM workspaces ORDER BY created_at ASC`)
	if err != nil {
		return fmt.Errorf("postgres: list workspaces for search reindex: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var workspaceID uuid.UUID
		if err := rows.Scan(&workspaceID); err != nil {
			return fmt.Errorf("postgres: scan workspace for search reindex: %w", err)
		}
		if err := r.ReindexWorkspace(ctx, workspaceID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: iterate workspaces for search reindex: %w", err)
	}
	return nil
}

func (r *SearchRepo) ReindexWorkspace(ctx context.Context, workspaceID uuid.UUID) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: begin search reindex tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, `DELETE FROM search_index_jobs WHERE workspace_id = $1`, workspaceID); err != nil {
		return fmt.Errorf("postgres: clear search jobs for reindex: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM search_index WHERE workspace_id = $1`, workspaceID); err != nil {
		return fmt.Errorf("postgres: clear search index for reindex: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit search reindex reset: %w", err)
	}

	if err := r.reindexChannels(ctx, workspaceID); err != nil {
		return err
	}
	if err := r.reindexMessages(ctx, workspaceID); err != nil {
		return err
	}
	if err := r.reindexFiles(ctx, workspaceID); err != nil {
		return err
	}
	if err := r.reindexUsers(ctx, workspaceID); err != nil {
		return err
	}
	return nil
}

func (r *SearchRepo) claimPendingJobs(ctx context.Context, batchSize int) ([]searchJob, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin search job claim tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	query := `
		SELECT id, workspace_id, resource_type, resource_id, operation, channel_id, title, content,
			metadata, created_at, updated_at, attempts, max_attempts
		FROM search_index_jobs
		WHERE available_at <= NOW()
			AND (
				status IN ('pending', 'failed')
				OR (status = 'processing' AND locked_at < NOW() - INTERVAL '5 minutes')
			)
		ORDER BY available_at ASC, created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(ctx, query, batchSize)
	if err != nil {
		return nil, fmt.Errorf("postgres: query pending search jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]searchJob, 0, batchSize)
	for rows.Next() {
		var (
			job          searchJob
			metadataJSON []byte
		)
		if err := rows.Scan(
			&job.ID,
			&job.WorkspaceID,
			&job.ResourceType,
			&job.ResourceID,
			&job.Operation,
			&job.ChannelID,
			&job.Title,
			&job.Content,
			&metadataJSON,
			&job.CreatedAt,
			&job.UpdatedAt,
			&job.Attempts,
			&job.MaxAttempts,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan search job: %w", err)
		}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &job.Metadata); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal search job metadata: %w", err)
			}
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate search jobs: %w", err)
	}

	for i := range jobs {
		if _, err := tx.Exec(ctx, `
			UPDATE search_index_jobs
			SET status = $2,
				locked_at = NOW(),
				processed_at = NULL,
				updated_at = NOW(),
				attempts = attempts + 1
			WHERE id = $1`,
			jobs[i].ID,
			searchJobStatusProcessing,
		); err != nil {
			return nil, fmt.Errorf("postgres: claim search job: %w", err)
		}
		jobs[i].Attempts++
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: commit search job claim tx: %w", err)
	}
	return jobs, nil
}

func (r *SearchRepo) applyJob(ctx context.Context, job searchJob) error {
	switch job.Operation {
	case searchJobOperationUpsert:
		return r.upsertDocument(ctx, job)
	case searchJobOperationDelete:
		_, err := r.pool.Exec(ctx, `
			DELETE FROM search_index
			WHERE workspace_id = $1 AND resource_type = $2 AND resource_id = $3`,
			job.WorkspaceID,
			job.ResourceType,
			job.ResourceID,
		)
		if err != nil {
			return fmt.Errorf("postgres: delete search document: %w", err)
		}
		return nil
	default:
		return cerrors.InvalidInput("unsupported search job operation")
	}
}

func (r *SearchRepo) upsertDocument(ctx context.Context, job searchJob) error {
	metadata, err := json.Marshal(job.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal search document metadata: %w", err)
	}

	// $6 and $7 appear twice — once as the title/content columns and again
	// inside CONCAT() for the tsvector. pgx's prepare step can't infer a type
	// through variadic CONCAT, so Postgres errors with "could not determine
	// data type of parameter $6" (SQLSTATE 42P08). Explicit ::text casts on
	// the CONCAT site pin the types so prepare succeeds. Without this every
	// search index job was dying at attempt 8 and the search_index stayed
	// empty — search returned zero results for every query.
	query := `
		INSERT INTO search_index (
			id, workspace_id, resource_type, resource_id, channel_id,
			title, content, metadata, tsv, created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			to_tsvector($9::regconfig, TRIM(CONCAT($6::text, ' ', $7::text))),
			$10, $11
		)
		ON CONFLICT (workspace_id, resource_type, resource_id) DO UPDATE
		SET channel_id = EXCLUDED.channel_id,
			title = EXCLUDED.title,
			content = EXCLUDED.content,
			metadata = EXCLUDED.metadata,
			tsv = EXCLUDED.tsv,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at`

	if _, err := r.pool.Exec(ctx, query,
		uuid.New(),
		job.WorkspaceID,
		job.ResourceType,
		job.ResourceID,
		job.ChannelID,
		job.Title,
		job.Content,
		metadata,
		r.textConfig,
		job.CreatedAt.UTC(),
		job.UpdatedAt.UTC(),
	); err != nil {
		return fmt.Errorf("postgres: upsert search document: %w", err)
	}
	return nil
}

func (r *SearchRepo) completeJob(ctx context.Context, jobID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE search_index_jobs
		SET status = $2,
			processed_at = NOW(),
			locked_at = NULL,
			updated_at = NOW(),
			last_error = NULL
		WHERE id = $1`,
		jobID,
		searchJobStatusProcessed,
	); err != nil {
		return fmt.Errorf("postgres: mark search job processed: %w", err)
	}
	return nil
}

func (r *SearchRepo) failJob(ctx context.Context, job searchJob, status string, cause error) error {
	if status != searchJobStatusFailed && status != searchJobStatusDead {
		return cerrors.InvalidInput("invalid search failure status")
	}
	delay := time.Duration(0)
	if status == searchJobStatusFailed {
		delay = r.retryDelay(job.Attempts)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE search_index_jobs
		SET status = $2,
			available_at = NOW() + ($3 * INTERVAL '1 millisecond'),
			locked_at = NULL,
			processed_at = NULL,
			updated_at = NOW(),
			last_error = $4
		WHERE id = $1`,
		job.ID,
		status,
		delay.Milliseconds(),
		truncateText(cause.Error(), 2048),
	); err != nil {
		return fmt.Errorf("postgres: update failed search job: %w", err)
	}
	return nil
}

func (r *SearchRepo) Pressure() reliability.Pressure {
	if r == nil || r.pool == nil {
		return reliability.Pressure{}
	}
	return reliability.PostgresPressure(r.pool.Stat())
}

func (r *SearchRepo) policy(maxAttempts int) reliability.Policy {
	return reliability.Policy{
		Timeout:      r.opTimeout,
		MaxAttempts:  maxAttempts,
		RetryBackoff: r.retryBackoff,
		MaxBackoff:   5 * time.Second,
	}
}

func maxDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func (r *SearchRepo) retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return r.retryBackoff
	}
	delay := r.retryBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > time.Hour {
			return time.Hour
		}
	}
	return delay
}

func (r *SearchRepo) reindexChannels(ctx context.Context, workspaceID uuid.UUID) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, topic, created_at, updated_at
		FROM channels
		WHERE workspace_id = $1 AND archived = false`,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("postgres: list channels for search reindex: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			channelID uuid.UUID
			name      string
			topic     string
			createdAt time.Time
			updatedAt time.Time
		)
		if err := rows.Scan(&channelID, &name, &topic, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("postgres: scan channel for search reindex: %w", err)
		}
		if err := r.upsertDocument(ctx, searchJob{
			WorkspaceID:  workspaceID,
			ResourceType: string(search.ResourceTypeChannel),
			ResourceID:   channelID,
			ChannelID:    &channelID,
			Title:        name,
			Content:      strings.TrimSpace(name + " " + topic),
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: iterate channels for search reindex: %w", err)
	}
	return nil
}

func (r *SearchRepo) reindexMessages(ctx context.Context, workspaceID uuid.UUID) error {
	rows, err := r.pool.Query(ctx, `
		SELECT m.id, m.channel_id, m.content, m.created_at, m.updated_at
		FROM messages m
		INNER JOIN channels c ON c.id = m.channel_id
		WHERE c.workspace_id = $1 AND c.archived = false AND m.deleted_at IS NULL`,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("postgres: list messages for search reindex: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			messageID uuid.UUID
			channelID uuid.UUID
			content   string
			createdAt time.Time
			updatedAt time.Time
		)
		if err := rows.Scan(&messageID, &channelID, &content, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("postgres: scan message for search reindex: %w", err)
		}
		if err := r.upsertDocument(ctx, searchJob{
			WorkspaceID:  workspaceID,
			ResourceType: string(search.ResourceTypeMessage),
			ResourceID:   messageID,
			ChannelID:    &channelID,
			Content:      content,
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: iterate messages for search reindex: %w", err)
	}
	return nil
}

func (r *SearchRepo) reindexFiles(ctx context.Context, workspaceID uuid.UUID) error {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, m.channel_id, a.message_id, a.file_name, a.mime_type, a.created_at
		FROM attachments a
		INNER JOIN messages m ON m.id = a.message_id
		INNER JOIN channels c ON c.id = m.channel_id
		WHERE c.workspace_id = $1 AND c.archived = false AND m.deleted_at IS NULL`,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("postgres: list files for search reindex: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			attachmentID uuid.UUID
			channelID    uuid.UUID
			messageID    uuid.UUID
			fileName     string
			mimeType     string
			createdAt    time.Time
		)
		if err := rows.Scan(&attachmentID, &channelID, &messageID, &fileName, &mimeType, &createdAt); err != nil {
			return fmt.Errorf("postgres: scan file for search reindex: %w", err)
		}
		if err := r.upsertDocument(ctx, searchJob{
			WorkspaceID:  workspaceID,
			ResourceType: string(search.ResourceTypeFile),
			ResourceID:   attachmentID,
			ChannelID:    &channelID,
			Title:        fileName,
			Content:      strings.TrimSpace(fileName + " " + mimeType),
			Metadata: map[string]any{
				"message_id": messageID.String(),
				"mime_type":  mimeType,
			},
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: iterate files for search reindex: %w", err)
	}
	return nil
}

func (r *SearchRepo) reindexUsers(ctx context.Context, workspaceID uuid.UUID) error {
	rows, err := r.pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, wm.joined_at, u.updated_at
		FROM workspace_members wm
		INNER JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = $1 AND u.status = 'active'`,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("postgres: list users for search reindex: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			userID      uuid.UUID
			displayName string
			email       string
			createdAt   time.Time
			updatedAt   time.Time
		)
		if err := rows.Scan(&userID, &displayName, &email, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("postgres: scan user for search reindex: %w", err)
		}
		if err := r.upsertDocument(ctx, searchJob{
			WorkspaceID:  workspaceID,
			ResourceType: string(search.ResourceTypeUser),
			ResourceID:   userID,
			Title:        displayName,
			Content:      strings.TrimSpace(displayName + " " + email),
			Metadata: map[string]any{
				"email": email,
			},
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: iterate users for search reindex: %w", err)
	}
	return nil
}

func truncateText(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
