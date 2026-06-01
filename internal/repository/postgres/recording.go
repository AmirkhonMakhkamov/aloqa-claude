package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/reliability"
)

// pgUniqueViolation is the Postgres SQLSTATE for a unique_violation.
const pgUniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// RecordingRepo implements repository.RecordingRepository using PostgreSQL.
type RecordingRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewRecordingRepo creates a new RecordingRepo.
func NewRecordingRepo(pool *pgxpool.Pool) *RecordingRepo {
	return &RecordingRepo{pool: pool, db: pool}
}

func (r *RecordingRepo) withTx(tx pgx.Tx) *RecordingRepo {
	if r == nil {
		return nil
	}
	return &RecordingRepo{pool: r.pool, db: tx}
}

func (r *RecordingRepo) Pressure() reliability.Pressure {
	if r == nil || r.pool == nil {
		return reliability.Pressure{}
	}
	return reliability.PostgresPressure(r.pool.Stat())
}

func (r *RecordingRepo) Create(ctx context.Context, rec *entity.Recording) error {
	metadata, err := json.Marshal(rec.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal recording metadata: %w", err)
	}

	query := `
		INSERT INTO recordings (
			id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27
		)`

	_, err = r.db.Exec(ctx, query,
		rec.ID, rec.CallID, rec.WorkspaceID, rec.StartedBy, rec.Strategy, rec.Format, rec.Status, rec.Duration, rec.FileSize,
		rec.StoragePath, rec.StorageTier, rec.StorageClass, rec.IntegritySHA256, rec.Downloadable, rec.ProcessingAttempts, rec.MaxProcessingAttempts,
		rec.LastError, metadata, rec.LegalHold, rec.StartedAt, rec.StoppedAt, rec.ReadyAt, rec.NextRetryAt,
		rec.TierUpdatedAt, rec.ExpiresAt, rec.CreatedAt, rec.EgressID,
	)
	if err != nil {
		// ux_recordings_active_per_call enforces one in-flight recording per call;
		// a concurrent start trips this (ALK-701) → surface as a conflict.
		if isUniqueViolation(err) {
			return cerrors.Conflict("call is already being recorded")
		}
		return fmt.Errorf("postgres: create recording: %w", err)
	}
	return nil
}

// SetEgressID backfills the LiveKit Egress job id used to correlate egress_*
// webhooks to the recording row (ALK-701).
func (r *RecordingRepo) SetEgressID(ctx context.Context, id uuid.UUID, egressID string) error {
	tag, err := r.db.Exec(ctx, `UPDATE recordings SET egress_id = $2 WHERE id = $1`, id, egressID)
	if err != nil {
		return fmt.Errorf("postgres: set recording egress id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Recording, error) {
	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings WHERE id = $1`
	return r.queryRecording(ctx, query, id)
}

// GetByEgressID returns the recording whose LiveKit Egress job matches egressID.
// Used by the egress_* webhook bridge to finalize the recording (ALK-701).
func (r *RecordingRepo) GetByEgressID(ctx context.Context, egressID string) (*entity.Recording, error) {
	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings WHERE egress_id = $1`
	return r.queryRecording(ctx, query, egressID)
}

// GetActiveByCall returns the single in-flight (recording|processing) recording for a call.
// At most one exists because StartRecording rejects a concurrent recording with 409. This is the
// webhook correlation fallback when an egress event races the egress_id backfill (ALK-701).
func (r *RecordingRepo) GetActiveByCall(ctx context.Context, callID uuid.UUID) (*entity.Recording, error) {
	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings
		WHERE call_id = $1 AND status IN ('recording', 'processing')
		ORDER BY started_at DESC
		LIMIT 1`
	return r.queryRecording(ctx, query, callID)
}

func (r *RecordingRepo) ListByCall(ctx context.Context, callID uuid.UUID) ([]entity.Recording, error) {
	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings WHERE call_id = $1 ORDER BY started_at`
	return r.queryRecordings(ctx, query, callID)
}

func (r *RecordingRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.Recording, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	return r.queryRecordings(ctx, query, workspaceID, limit, p.Offset)
}

func (r *RecordingRepo) ListByStatus(ctx context.Context, status entity.RecordingStatus, p pagination.Params) ([]entity.Recording, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings
		WHERE status = $1
		ORDER BY stopped_at NULLS FIRST, created_at
		LIMIT $2 OFFSET $3`
	return r.queryRecordings(ctx, query, status, limit, p.Offset)
}

func (r *RecordingRepo) ListProcessable(ctx context.Context, now time.Time, p pagination.Params) ([]entity.Recording, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings
		WHERE status IN ('processing', 'failed')
		  AND (next_retry_at IS NULL OR next_retry_at <= $1)
		  AND (status = 'processing' OR processing_attempts < max_processing_attempts)
		ORDER BY COALESCE(next_retry_at, stopped_at, created_at), created_at
		LIMIT $2 OFFSET $3`
	return r.queryRecordings(ctx, query, now.UTC(), limit, p.Offset)
}

func (r *RecordingRepo) ListExpired(ctx context.Context, now time.Time, p pagination.Params) ([]entity.Recording, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id
		FROM recordings
		WHERE expires_at IS NOT NULL
		  AND expires_at <= $1
		  AND status = 'ready'
		  AND legal_hold = false
		ORDER BY expires_at
		LIMIT $2 OFFSET $3`
	return r.queryRecordings(ctx, query, now.UTC(), limit, p.Offset)
}

func (r *RecordingRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status entity.RecordingStatus) error {
	tag, err := r.db.Exec(ctx, `UPDATE recordings SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("postgres: update recording status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) SetReady(ctx context.Context, rec *entity.Recording) error {
	metadata, err := json.Marshal(rec.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal ready recording metadata: %w", err)
	}

	tag, err := r.db.Exec(ctx, `
		UPDATE recordings
		SET status = 'ready',
			format = $2,
			duration = $3,
			file_size = $4,
			storage_path = $5,
			storage_tier = $6,
			storage_class = $7,
			integrity_sha256 = $8,
			downloadable = $9,
			last_error = '',
			metadata = $10,
			ready_at = $11,
			tier_updated_at = $12,
			next_retry_at = NULL
		WHERE id = $1`,
		rec.ID,
		rec.Format,
		rec.Duration,
		rec.FileSize,
		rec.StoragePath,
		rec.StorageTier,
		rec.StorageClass,
		rec.IntegritySHA256,
		rec.Downloadable,
		metadata,
		time.Now().UTC(),
		rec.TierUpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: set recording ready: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) MarkProcessingAttempt(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) (*entity.Recording, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE recordings
		SET status = 'processing',
			processing_attempts = processing_attempts + 1,
			next_retry_at = $2
		WHERE id = $1
		RETURNING id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id`,
		id,
		nextRetryAt.UTC(),
	)
	return scanRecording(row)
}

func (r *RecordingRepo) MarkFailed(ctx context.Context, id uuid.UUID, lastError string, nextRetryAt *time.Time) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE recordings
		SET status = 'failed',
			last_error = $2,
			next_retry_at = $3
		WHERE id = $1`,
		id,
		lastError,
		nextRetryAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: mark recording failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) SetLegalHold(ctx context.Context, id uuid.UUID, hold bool) error {
	tag, err := r.db.Exec(ctx, `UPDATE recordings SET legal_hold = $2 WHERE id = $1`, id, hold)
	if err != nil {
		return fmt.Errorf("postgres: set legal hold: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) UpdateStorageTier(ctx context.Context, recordingID uuid.UUID, tier entity.RecordingStorageTier, storageClass string, updatedAt time.Time) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE recordings
		SET storage_tier = $2,
			storage_class = $3,
			tier_updated_at = $4
		WHERE id = $1`,
		recordingID,
		tier,
		storageClass,
		updatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: update recording storage tier: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) UpdateArtifactStorageTier(ctx context.Context, recordingID, artifactID uuid.UUID, tier entity.RecordingStorageTier, storageClass string, updatedAt time.Time) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE recording_artifacts
		SET storage_tier = $3,
			storage_class = $4,
			tier_updated_at = $5
		WHERE recording_id = $1 AND id = $2`,
		recordingID,
		artifactID,
		tier,
		storageClass,
		updatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: update recording artifact storage tier: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording artifact not found")
	}
	return nil
}

func (r *RecordingRepo) Stop(ctx context.Context, id uuid.UUID) (*entity.Recording, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE recordings
		SET status = 'processing',
			stopped_at = $2,
			next_retry_at = $2
		WHERE id = $1 AND status = 'recording'
		RETURNING id, call_id, workspace_id, started_by, strategy, format, status, duration, file_size,
			storage_path, storage_tier, storage_class, integrity_sha256, downloadable, processing_attempts, max_processing_attempts,
			last_error, metadata, legal_hold, started_at, stopped_at, ready_at, next_retry_at,
			tier_updated_at, expires_at, created_at, egress_id`,
		id,
		time.Now().UTC(),
	)
	return scanRecording(row)
}

func (r *RecordingRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM recordings WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete recording: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("recording not found")
	}
	return nil
}

func (r *RecordingRepo) ReplaceArtifacts(ctx context.Context, recordingID uuid.UUID, artifacts []entity.RecordingArtifact) error {
	if tx, ok := r.db.(pgx.Tx); ok {
		return r.replaceArtifactsTx(ctx, tx, recordingID, artifacts)
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: begin replace recording artifacts tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if err := r.replaceArtifactsTx(ctx, tx, recordingID, artifacts); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit replace recording artifacts tx: %w", err)
	}
	return nil
}

func (r *RecordingRepo) replaceArtifactsTx(ctx context.Context, tx pgx.Tx, recordingID uuid.UUID, artifacts []entity.RecordingArtifact) error {
	if _, err := tx.Exec(ctx, `DELETE FROM recording_artifacts WHERE recording_id = $1`, recordingID); err != nil {
		return fmt.Errorf("postgres: clear recording artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		metadata, err := json.Marshal(artifact.Metadata)
		if err != nil {
			return fmt.Errorf("postgres: marshal recording artifact metadata: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO recording_artifacts (
				id, recording_id, workspace_id, kind, source_user_id, track_id, stream_id, layer,
				codec, mime_type, format, storage_path, storage_tier, storage_class, file_size, integrity_sha256, packet_count,
				duration, downloadable, metadata, tier_updated_at, created_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				$9, $10, $11, $12, $13, $14, $15, $16,
				$17, $18, $19, $20, $21
			)`,
			artifact.ID, artifact.RecordingID, artifact.WorkspaceID, artifact.Kind, artifact.SourceUserID, artifact.TrackID, artifact.StreamID, artifact.Layer,
			artifact.Codec, artifact.MimeType, artifact.Format, artifact.StoragePath, artifact.StorageTier, artifact.StorageClass, artifact.FileSize, artifact.IntegritySHA256, artifact.PacketCount,
			artifact.Duration, artifact.Downloadable, metadata, artifact.TierUpdatedAt, artifact.CreatedAt,
		); err != nil {
			return fmt.Errorf("postgres: insert recording artifact: %w", err)
		}
	}
	return nil
}

func (r *RecordingRepo) ListArtifacts(ctx context.Context, recordingID uuid.UUID) ([]entity.RecordingArtifact, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, recording_id, workspace_id, kind, source_user_id, track_id, stream_id, layer,
			codec, mime_type, format, storage_path, storage_tier, storage_class, file_size, integrity_sha256, packet_count,
			duration, downloadable, metadata, tier_updated_at, created_at
		FROM recording_artifacts
		WHERE recording_id = $1
		ORDER BY created_at, id`,
		recordingID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list recording artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []entity.RecordingArtifact
	for rows.Next() {
		artifact, err := scanRecordingArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, *artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate recording artifacts: %w", err)
	}
	return artifacts, nil
}

func (r *RecordingRepo) GetArtifact(ctx context.Context, recordingID, artifactID uuid.UUID) (*entity.RecordingArtifact, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, recording_id, workspace_id, kind, source_user_id, track_id, stream_id, layer,
			codec, mime_type, format, storage_path, storage_tier, storage_class, file_size, integrity_sha256, packet_count,
			duration, downloadable, metadata, tier_updated_at, created_at
		FROM recording_artifacts
		WHERE recording_id = $1 AND id = $2`,
		recordingID,
		artifactID,
	)
	artifact, err := scanRecordingArtifact(row)
	if err != nil {
		return nil, err
	}
	return artifact, nil
}

func (r *RecordingRepo) WorkspaceStorageUsage(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	var usage int64
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(file_size), 0)
		FROM recording_artifacts
		WHERE workspace_id = $1`,
		workspaceID,
	).Scan(&usage)
	if err != nil {
		return 0, fmt.Errorf("postgres: sum workspace recording usage: %w", err)
	}
	return usage, nil
}

func (r *RecordingRepo) queryRecording(ctx context.Context, query string, args ...any) (*entity.Recording, error) {
	row := r.db.QueryRow(ctx, query, args...)
	return scanRecording(row)
}

func (r *RecordingRepo) queryRecordings(ctx context.Context, query string, args ...any) ([]entity.Recording, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query recordings: %w", err)
	}
	defer rows.Close()

	var recordings []entity.Recording
	for rows.Next() {
		rec, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		recordings = append(recordings, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate recordings: %w", err)
	}
	return recordings, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecording(row rowScanner) (*entity.Recording, error) {
	rec := &entity.Recording{}
	var metadataJSON []byte
	err := row.Scan(
		&rec.ID, &rec.CallID, &rec.WorkspaceID, &rec.StartedBy, &rec.Strategy, &rec.Format, &rec.Status, &rec.Duration, &rec.FileSize,
		&rec.StoragePath, &rec.StorageTier, &rec.StorageClass, &rec.IntegritySHA256, &rec.Downloadable, &rec.ProcessingAttempts, &rec.MaxProcessingAttempts,
		&rec.LastError, &metadataJSON, &rec.LegalHold, &rec.StartedAt, &rec.StoppedAt, &rec.ReadyAt, &rec.NextRetryAt,
		&rec.TierUpdatedAt, &rec.ExpiresAt, &rec.CreatedAt, &rec.EgressID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("recording not found")
		}
		return nil, fmt.Errorf("postgres: scan recording: %w", err)
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &rec.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal recording metadata: %w", err)
		}
	}
	return rec, nil
}

func scanRecordingArtifact(row rowScanner) (*entity.RecordingArtifact, error) {
	artifact := &entity.RecordingArtifact{}
	var metadataJSON []byte
	err := row.Scan(
		&artifact.ID, &artifact.RecordingID, &artifact.WorkspaceID, &artifact.Kind, &artifact.SourceUserID, &artifact.TrackID, &artifact.StreamID, &artifact.Layer,
		&artifact.Codec, &artifact.MimeType, &artifact.Format, &artifact.StoragePath, &artifact.StorageTier, &artifact.StorageClass, &artifact.FileSize, &artifact.IntegritySHA256, &artifact.PacketCount,
		&artifact.Duration, &artifact.Downloadable, &metadataJSON, &artifact.TierUpdatedAt, &artifact.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("recording artifact not found")
		}
		return nil, fmt.Errorf("postgres: scan recording artifact: %w", err)
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &artifact.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal recording artifact metadata: %w", err)
		}
	}
	return artifact, nil
}
