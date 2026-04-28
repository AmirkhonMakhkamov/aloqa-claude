package recording

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/extension"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/reliability"
	"aloqa/internal/platform/storage"
	"aloqa/internal/platform/txscope"
)

type MediaRooms interface {
	GetRoom(id string) (*sfu.Room, bool)
}

type CallAccessAuthorizer interface {
	CanAccessCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) error
}

type storageTierRepository interface {
	UpdateStorageTier(ctx context.Context, recordingID uuid.UUID, tier entity.RecordingStorageTier, storageClass string, updatedAt time.Time) error
	UpdateArtifactStorageTier(ctx context.Context, recordingID, artifactID uuid.UUID, tier entity.RecordingStorageTier, storageClass string, updatedAt time.Time) error
}

// Service manages call recording lifecycle.
type Service struct {
	recordings   repository.RecordingRepository
	calls        repository.CallRepository
	workspaces   repository.WorkspaceRepository
	store        storage.Storage
	rooms        MediaRooms
	capture      *CaptureManager
	retention    time.Duration
	hooks        *extension.HookDispatcher
	audit        repository.AuditRepository
	quotaBytes   int64
	maxAttempts  int
	retryBackoff time.Duration
	spoolBaseDir string
	opTimeout    time.Duration
	signedURLTTL time.Duration
	warmAfter    time.Duration
	archiveAfter time.Duration
	callAccess   CallAccessAuthorizer
	tx           txscope.Manager
	observer     interface {
		RecordRecordingRun(workerName string, processed, failed, deleted, tiered int, duration time.Duration, err error)
		RecordRecordingHookFailure(err error)
		RecordWorkerHeartbeat(name string)
	}
}

type Config struct {
	Retention        time.Duration
	Hooks            *extension.HookDispatcher
	Audit            repository.AuditRepository
	QuotaBytes       int64
	MaxAttempts      int
	RetryBackoff     time.Duration
	SpoolBaseDir     string
	OperationTimeout time.Duration
	SignedURLTTL     time.Duration
	WarmAfter        time.Duration
	ArchiveAfter     time.Duration
}

// NewService creates a new recording service.
func NewService(
	recordings repository.RecordingRepository,
	calls repository.CallRepository,
	workspaces repository.WorkspaceRepository,
	store storage.Storage,
	rooms MediaRooms,
	capture *CaptureManager,
	cfg Config,
) *Service {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	retryBackoff := cfg.RetryBackoff
	if retryBackoff <= 0 {
		retryBackoff = 10 * time.Second
	}
	return &Service{
		recordings:   recordings,
		calls:        calls,
		workspaces:   workspaces,
		store:        store,
		rooms:        rooms,
		capture:      capture,
		retention:    cfg.Retention,
		hooks:        cfg.Hooks,
		audit:        cfg.Audit,
		quotaBytes:   cfg.QuotaBytes,
		maxAttempts:  maxAttempts,
		retryBackoff: retryBackoff,
		spoolBaseDir: cfg.SpoolBaseDir,
		opTimeout:    maxDuration(cfg.OperationTimeout, 15*time.Second),
		signedURLTTL: maxDuration(cfg.SignedURLTTL, 15*time.Minute),
		warmAfter:    cfg.WarmAfter,
		archiveAfter: cfg.ArchiveAfter,
	}
}

func (s *Service) SetCallAccessAuthorizer(authorizer CallAccessAuthorizer) {
	s.callAccess = authorizer
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

func (s *Service) SetObserver(observer interface {
	RecordRecordingRun(workerName string, processed, failed, deleted, tiered int, duration time.Duration, err error)
	RecordRecordingHookFailure(err error)
	RecordWorkerHeartbeat(name string)
}) {
	s.observer = observer
}

// StartRecording begins recording a call. Only host/co-host can start.
func (s *Service) StartRecording(ctx context.Context, workspaceID, callID, userID uuid.UUID, strategy entity.RecordingStrategy) (*entity.Recording, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("call not found")
		}
		return nil, cerrors.Internal("failed to get call", err)
	}
	if call.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("call not found")
	}

	if call.Status != entity.CallStatusActive {
		return nil, cerrors.Forbidden("can only record active calls")
	}
	if !call.Settings.Recording {
		return nil, cerrors.Forbidden("recording is disabled for this call")
	}
	if strategy == "" {
		strategy = entity.RecordingStrategyBoth
	}
	switch strategy {
	case entity.RecordingStrategyComposite, entity.RecordingStrategyPerTrack, entity.RecordingStrategyBoth:
	default:
		return nil, cerrors.InvalidInput("invalid recording strategy")
	}
	if err := s.ensureQuotaAvailable(ctx, workspaceID); err != nil {
		return nil, err
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("not a participant in this call")
		}
		return nil, cerrors.Internal("failed to get participant", err)
	}
	if participant.Role != entity.CallRoleHost && participant.Role != entity.CallRoleCoHost {
		return nil, cerrors.Forbidden("only host or co-host can start recording")
	}

	existing, err := s.recordings.ListByCall(ctx, callID)
	if err != nil {
		return nil, cerrors.Internal("failed to check existing recordings", err)
	}
	for _, r := range existing {
		if r.Status == entity.RecordingStatusRecording {
			return nil, cerrors.Conflict("call is already being recorded")
		}
	}

	room, ok := s.mediaRoom(callID)
	if !ok {
		return nil, cerrors.Unavailable("media room is not initialized")
	}
	if s.capture == nil {
		return nil, cerrors.Unavailable("recording capture is not configured")
	}

	now := time.Now().UTC()
	rec := &entity.Recording{
		ID:                    id.New(),
		CallID:                callID,
		WorkspaceID:           call.WorkspaceID,
		StartedBy:             userID,
		Strategy:              strategy,
		Format:                entity.RecordingFormatBundle,
		Status:                entity.RecordingStatusRecording,
		StorageTier:           entity.RecordingStorageTierHot,
		StorageClass:          s.defaultStorageClass(entity.RecordingStorageTierHot),
		Downloadable:          strategy != entity.RecordingStrategyPerTrack,
		ProcessingAttempts:    0,
		MaxProcessingAttempts: s.maxAttempts,
		Metadata:              map[string]any{},
		StartedAt:             now,
		CreatedAt:             now,
	}
	rec.Metadata["capture_spool_dir"] = s.capture.WorkDir(rec.ID)
	if s.retention > 0 {
		expiresAt := now.Add(s.retention)
		rec.ExpiresAt = &expiresAt
	}

	if err := s.capture.Start(ctx, *rec, room); err != nil {
		return nil, cerrors.Internal("failed to attach recording capture", err)
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Recordings() == nil {
				return cerrors.Unavailable("recording transaction scope is not configured")
			}
			if err := scope.Recordings().Create(ctx, rec); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, userID, entity.AuditActionRecordingStarted, "recording", rec.ID.String(), map[string]any{
				"call_id":   rec.CallID.String(),
				"strategy":  string(rec.Strategy),
				"format":    string(rec.Format),
				"spool_dir": s.capture.WorkDir(rec.ID),
			})
		}); err != nil {
			_ = s.capture.Stop(rec.ID)
			return nil, cerrors.Internal("failed to start recording", err)
		}
	} else {
		if err := s.recordings.Create(ctx, rec); err != nil {
			slog.ErrorContext(ctx, "failed to create recording", "call_id", callID, "error", err)
			_ = s.capture.Stop(rec.ID)
			return nil, cerrors.Internal("failed to start recording", err)
		}

		s.logAudit(ctx, workspaceID, userID, entity.AuditActionRecordingStarted, "recording", rec.ID.String(), map[string]any{
			"call_id":   rec.CallID.String(),
			"strategy":  string(rec.Strategy),
			"format":    string(rec.Format),
			"spool_dir": s.capture.WorkDir(rec.ID),
		})
	}

	recID := rec.ID
	callWS := call.WorkspaceID
	reliability.SafeGo("recording_auto_stop", func() { s.autoStopOnRoomClose(callWS, recID, room) })

	slog.InfoContext(ctx, "recording started", "recording_id", rec.ID, "call_id", callID)
	return rec, nil
}

// StopRecording stops an active recording and transitions it to processing.
func (s *Service) StopRecording(ctx context.Context, workspaceID, recordingID, userID uuid.UUID) error {
	rec, err := s.recordings.GetByID(ctx, recordingID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("recording not found")
		}
		return cerrors.Internal("failed to get recording", err)
	}
	if rec.WorkspaceID != workspaceID {
		return cerrors.NotFound("recording not found")
	}
	if rec.Status != entity.RecordingStatusRecording {
		return cerrors.Conflict("recording is not active")
	}

	participant, err := s.calls.GetParticipant(ctx, rec.CallID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("not a participant")
		}
		return cerrors.Internal("failed to get participant", err)
	}
	if participant.Role != entity.CallRoleHost && participant.Role != entity.CallRoleCoHost {
		return cerrors.Forbidden("only host or co-host can stop recording")
	}
	if s.capture != nil {
		if err := s.capture.Stop(recordingID); err != nil {
			return cerrors.Internal("failed to finalize capture", err)
		}
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Recordings() == nil {
				return cerrors.Unavailable("recording transaction scope is not configured")
			}
			if _, err := scope.Recordings().Stop(ctx, recordingID); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, userID, entity.AuditActionRecordingStopped, "recording", recordingID.String(), nil)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to stop recording transaction", "recording_id", recordingID, "error", err)
			return cerrors.Internal("failed to stop recording", err)
		}
	} else {
		if _, err := s.recordings.Stop(ctx, recordingID); err != nil {
			slog.ErrorContext(ctx, "failed to stop recording", "recording_id", recordingID, "error", err)
			return cerrors.Internal("failed to stop recording", err)
		}

		s.logAudit(ctx, workspaceID, userID, entity.AuditActionRecordingStopped, "recording", recordingID.String(), nil)
	}
	slog.InfoContext(ctx, "recording stopped", "recording_id", recordingID)
	return nil
}

// GetRecording returns a recording by ID if the user can view it.
func (s *Service) GetRecording(ctx context.Context, workspaceID, recordingID, userID uuid.UUID) (*entity.Recording, error) {
	rec, err := s.getVisibleRecording(ctx, workspaceID, recordingID, userID)
	if err != nil {
		return nil, err
	}
	s.logAudit(ctx, workspaceID, userID, entity.AuditActionRecordingViewed, "recording", recordingID.String(), nil)
	return rec, nil
}

// ListByCall returns all recordings for a call.
func (s *Service) ListByCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) ([]entity.Recording, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, cerrors.NotFound("call not found")
	}
	if call.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("call not found")
	}
	if err := s.requireRecordingViewAccess(ctx, workspaceID, callID, userID); err != nil {
		return nil, err
	}
	recs, err := s.recordings.ListByCall(ctx, callID)
	if err != nil {
		return nil, cerrors.Internal("failed to list recordings", err)
	}
	return recs, nil
}

func (s *Service) ListArtifacts(ctx context.Context, workspaceID, recordingID, userID uuid.UUID) ([]entity.RecordingArtifact, error) {
	rec, err := s.getVisibleRecording(ctx, workspaceID, recordingID, userID)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.recordings.ListArtifacts(ctx, rec.ID)
	if err != nil {
		return nil, cerrors.Internal("failed to list recording artifacts", err)
	}
	return artifacts, nil
}

func (s *Service) DownloadArtifact(ctx context.Context, workspaceID, recordingID, artifactID, userID uuid.UUID) (io.ReadCloser, *storage.FileInfo, *entity.RecordingArtifact, error) {
	rec, err := s.getVisibleRecording(ctx, workspaceID, recordingID, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := s.requireRecordingDownloadAccess(ctx, rec, userID); err != nil {
		return nil, nil, nil, err
	}
	artifact, err := s.recordings.GetArtifact(ctx, recordingID, artifactID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, nil, nil, cerrors.NotFound("recording artifact not found")
		}
		return nil, nil, nil, cerrors.Internal("failed to get recording artifact", err)
	}
	if !artifact.Downloadable {
		return nil, nil, nil, cerrors.Forbidden("artifact is not downloadable")
	}
	reader, info, err := s.store.Get(ctx, artifact.StoragePath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, nil, cerrors.NotFound("recording artifact not found")
		}
		return nil, nil, nil, cerrors.Internal("failed to open recording artifact", err)
	}
	info.MimeType = artifact.MimeType
	info.Size = artifact.FileSize
	info.StorageClass = artifact.StorageClass
	s.logAudit(ctx, workspaceID, userID, entity.AuditActionRecordingDownloaded, "recording_artifact", artifact.ID.String(), map[string]any{
		"recording_id": recordingID.String(),
		"kind":         string(artifact.Kind),
		"format":       artifact.Format,
	})
	return reader, info, artifact, nil
}

func (s *Service) PresignArtifactDownload(ctx context.Context, workspaceID, recordingID, artifactID, userID uuid.UUID) (string, *entity.RecordingArtifact, error) {
	signer, ok := s.store.(storage.DownloadSigner)
	if !ok || signer == nil {
		return "", nil, nil
	}
	rec, err := s.getVisibleRecording(ctx, workspaceID, recordingID, userID)
	if err != nil {
		return "", nil, err
	}
	if err := s.requireRecordingDownloadAccess(ctx, rec, userID); err != nil {
		return "", nil, err
	}
	artifact, err := s.recordings.GetArtifact(ctx, recordingID, artifactID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return "", nil, cerrors.NotFound("recording artifact not found")
		}
		return "", nil, cerrors.Internal("failed to get recording artifact", err)
	}
	if !artifact.Downloadable {
		return "", nil, cerrors.Forbidden("artifact is not downloadable")
	}
	url, err := signer.SignedDownloadURL(ctx, artifact.StoragePath, storage.SignedURLOptions{
		Filename:    artifactID.String() + "." + artifact.Format,
		ContentType: artifact.MimeType,
		ExpiresIn:   s.signedURLTTL,
		Attachment:  true,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			return "", nil, nil
		}
		return "", nil, cerrors.Internal("failed to sign recording artifact download", err)
	}
	s.logAudit(ctx, workspaceID, userID, entity.AuditActionRecordingDownloaded, "recording_artifact", artifact.ID.String(), map[string]any{
		"recording_id": recordingID.String(),
		"kind":         string(artifact.Kind),
		"format":       artifact.Format,
		"signed_url":   true,
	})
	return url, artifact, nil
}

// SetLegalHold places or removes a legal hold on a recording, preventing automatic deletion.
func (s *Service) SetLegalHold(ctx context.Context, recordingID uuid.UUID, hold bool) error {
	if err := s.recordings.SetLegalHold(ctx, recordingID, hold); err != nil {
		return cerrors.Internal("failed to set legal hold", err)
	}
	return nil
}

type MaintenanceStats struct {
	Processed int
	Failed    int
	Deleted   int
	Tiered    int
}

func (s *Service) ProcessPending(ctx context.Context, processor Processor, limit int) (MaintenanceStats, error) {
	if processor == nil {
		return MaintenanceStats{}, cerrors.Unavailable("recording processor is not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	if reporter, ok := s.recordings.(interface{ Pressure() reliability.Pressure }); ok {
		if pressure := reporter.Pressure(); pressure.Saturated {
			return MaintenanceStats{}, cerrors.Unavailable("recording processing backpressure is active")
		}
	}

	recs, err := reliability.DoValue(ctx, s.workerPolicy(2), func(ctx context.Context) ([]entity.Recording, error) {
		return s.recordings.ListProcessable(ctx, time.Now().UTC(), pagination.Params{Limit: limit})
	})
	if err != nil {
		return MaintenanceStats{}, cerrors.Internal("failed to list pending recordings", err)
	}

	var stats MaintenanceStats
	var firstErr error
	for _, rec := range recs {
		inProgress, err := reliability.DoValue(ctx, s.workerPolicy(2), func(ctx context.Context) (*entity.Recording, error) {
			return s.recordings.MarkProcessingAttempt(ctx, rec.ID, time.Now().UTC())
		})
		if err != nil {
			stats.Failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		artifactSet, err := processor.Process(ctx, *inProgress)
		if err != nil {
			stats.Failed++
			s.handleProcessingFailure(ctx, *inProgress, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if artifactSet == nil || artifactSet.StoragePath == "" || artifactSet.FileSize <= 0 {
			stats.Failed++
			s.handleProcessingFailure(ctx, *inProgress, cerrors.Internal("recording processor returned invalid artifact", nil))
			continue
		}
		if err := s.ensureQuotaForArtifacts(ctx, inProgress.WorkspaceID, artifactSet.Artifacts); err != nil {
			stats.Failed++
			s.deleteUploadedArtifacts(ctx, artifactSet.Artifacts)
			s.handleProcessingFailure(ctx, *inProgress, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		s.applyStorageTier(artifactSet, entity.RecordingStorageTierHot)

		inProgress.Format = artifactSet.Format
		inProgress.StoragePath = artifactSet.StoragePath
		inProgress.StorageTier = artifactSet.StorageTier
		inProgress.StorageClass = artifactSet.StorageClass
		inProgress.IntegritySHA256 = artifactSet.IntegritySHA256
		inProgress.Downloadable = artifactSet.Downloadable
		inProgress.FileSize = &artifactSet.FileSize
		inProgress.Duration = &artifactSet.DurationSeconds
		now := time.Now().UTC()
		inProgress.TierUpdatedAt = &now
		inProgress.Metadata = artifactSet.Metadata

		finalizeReady := func(ctx context.Context) error {
			if s.tx != nil {
				return s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
					if scope.Recordings() == nil {
						return cerrors.Unavailable("recording transaction scope is not configured")
					}
					if err := scope.Recordings().ReplaceArtifacts(ctx, inProgress.ID, artifactSet.Artifacts); err != nil {
						return err
					}
					return scope.Recordings().SetReady(ctx, inProgress)
				})
			}
			if err := s.recordings.ReplaceArtifacts(ctx, inProgress.ID, artifactSet.Artifacts); err != nil {
				return err
			}
			return s.recordings.SetReady(ctx, inProgress)
		}
		if err := reliability.Do(ctx, s.workerPolicy(2), finalizeReady); err != nil {
			stats.Failed++
			s.deleteUploadedArtifacts(ctx, artifactSet.Artifacts)
			if s.tx == nil {
				if rollbackErr := reliability.Do(ctx, s.workerPolicy(2), func(ctx context.Context) error {
					return s.recordings.ReplaceArtifacts(ctx, inProgress.ID, nil)
				}); rollbackErr != nil {
					slog.ErrorContext(ctx, "failed to roll back recording artifacts after ready transition failure", "recording_id", inProgress.ID, "error", rollbackErr)
				}
			}
			s.handleProcessingFailure(ctx, *inProgress, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := s.dispatchRecordingReady(ctx, *inProgress, artifactSet); err != nil {
			slog.ErrorContext(ctx, "failed to dispatch recording ready hook", "recording_id", inProgress.ID, "error", err)
		}
		if err := RemoveSpoolDir(s.spoolBaseDir, inProgress.ID); err != nil {
			slog.WarnContext(ctx, "failed to remove recording spool dir", "recording_id", inProgress.ID, "error", err)
		}
		stats.Processed++
	}
	return stats, firstErr
}

func (s *Service) CleanupExpired(ctx context.Context, now time.Time, limit int) (MaintenanceStats, error) {
	if limit <= 0 {
		limit = 50
	}
	recs, err := reliability.DoValue(ctx, s.workerPolicy(2), func(ctx context.Context) ([]entity.Recording, error) {
		return s.recordings.ListExpired(ctx, now, pagination.Params{Limit: limit})
	})
	if err != nil {
		return MaintenanceStats{}, cerrors.Internal("failed to list expired recordings", err)
	}

	var stats MaintenanceStats
	for _, rec := range recs {
		artifacts, err := reliability.DoValue(ctx, s.workerPolicy(2), func(ctx context.Context) ([]entity.RecordingArtifact, error) {
			return s.recordings.ListArtifacts(ctx, rec.ID)
		})
		if err != nil {
			return stats, cerrors.Internal("failed to list recording artifacts for deletion", err)
		}
		paths := map[string]struct{}{}
		if rec.StoragePath != "" {
			paths[rec.StoragePath] = struct{}{}
		}
		for _, artifact := range artifacts {
			if artifact.StoragePath != "" {
				paths[artifact.StoragePath] = struct{}{}
			}
		}
		for key := range paths {
			if s.store == nil || key == "" {
				continue
			}
			if err := reliability.Do(ctx, s.workerPolicy(2), func(ctx context.Context) error {
				return s.store.Delete(ctx, key)
			}); err != nil {
				return stats, cerrors.Internal("failed to delete recording object", err)
			}
		}
		if err := reliability.Do(ctx, s.workerPolicy(2), func(ctx context.Context) error {
			return s.recordings.Delete(ctx, rec.ID)
		}); err != nil {
			return stats, cerrors.Internal("failed to delete expired recording metadata", err)
		}
		if err := RemoveSpoolDir(s.spoolBaseDir, rec.ID); err != nil {
			slog.WarnContext(ctx, "failed to remove expired recording spool dir", "recording_id", rec.ID, "error", err)
		}
		stats.Deleted++
	}
	return stats, nil
}

func (s *Service) RunMaintenanceOnce(ctx context.Context, processor Processor, now time.Time, batchSize int) (MaintenanceStats, error) {
	processed, processErr := s.ProcessPending(ctx, processor, batchSize)
	cleaned, cleanupErr := s.CleanupExpired(ctx, now, batchSize)
	processed.Deleted = cleaned.Deleted
	if processErr != nil {
		return processed, processErr
	}
	return processed, cleanupErr
}

func (s *Service) RunProcessingWorker(ctx context.Context, processor Processor, interval time.Duration, batchSize int) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("recording_processing")
			}
			startedAt := time.Now()
			stats, err := s.ProcessPending(ctx, processor, batchSize)
			if s.observer != nil {
				s.observer.RecordRecordingRun("recording_processing", stats.Processed, stats.Failed, 0, 0, time.Since(startedAt), err)
			}
			if err != nil {
				slog.ErrorContext(ctx, "recording processing worker failed", "error", err)
				continue
			}
			if stats.Processed > 0 || stats.Failed > 0 {
				slog.InfoContext(ctx, "recording processing worker completed", "processed", stats.Processed, "failed", stats.Failed)
			}
		}
	}
}

func (s *Service) RunCleanupWorker(ctx context.Context, interval time.Duration, batchSize int) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("recording_cleanup")
			}
			startedAt := time.Now()
			stats, err := s.CleanupExpired(ctx, now.UTC(), batchSize)
			if s.observer != nil {
				s.observer.RecordRecordingRun("recording_cleanup", 0, stats.Failed, stats.Deleted, 0, time.Since(startedAt), err)
			}
			if err != nil {
				slog.ErrorContext(ctx, "recording cleanup worker failed", "error", err)
				continue
			}
			if stats.Deleted > 0 {
				slog.InfoContext(ctx, "recording cleanup worker completed", "deleted", stats.Deleted)
			}
		}
	}
}

func (s *Service) TransitionStorageLifecycle(ctx context.Context, now time.Time, limit int) (MaintenanceStats, error) {
	if limit <= 0 {
		limit = 50
	}
	tieredStore, ok := s.store.(storage.TieredStorage)
	if !ok || tieredStore == nil {
		return MaintenanceStats{}, nil
	}
	recs, err := reliability.DoValue(ctx, s.workerPolicy(2), func(ctx context.Context) ([]entity.Recording, error) {
		return s.recordings.ListByStatus(ctx, entity.RecordingStatusReady, pagination.Params{Limit: limit})
	})
	if err != nil {
		return MaintenanceStats{}, cerrors.Internal("failed to list ready recordings for lifecycle transition", err)
	}
	var stats MaintenanceStats
	for _, rec := range recs {
		if rec.LegalHold {
			continue
		}
		if rec.ExpiresAt != nil && !rec.ExpiresAt.After(now) {
			continue
		}
		target := s.targetStorageTier(now, rec)
		if target == "" || target == rec.StorageTier {
			continue
		}
		if err := s.transitionRecordingTier(ctx, tieredStore, &rec, target, now.UTC()); err != nil {
			stats.Failed++
			slog.WarnContext(ctx, "failed to transition recording storage tier", "recording_id", rec.ID, "target_tier", target, "error", err)
			continue
		}
		stats.Tiered++
	}
	return stats, nil
}

func (s *Service) RunWorker(ctx context.Context, processor Processor, interval time.Duration, batchSize int) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("recording_maintenance")
			}
			startedAt := time.Now()
			stats, err := s.RunMaintenanceOnce(ctx, processor, now.UTC(), batchSize)
			if s.observer != nil {
				s.observer.RecordRecordingRun("recording_maintenance", stats.Processed, stats.Failed, stats.Deleted, 0, time.Since(startedAt), err)
			}
			if err != nil {
				slog.ErrorContext(ctx, "recording maintenance failed", "error", err)
				continue
			}
			if stats.Processed > 0 || stats.Failed > 0 || stats.Deleted > 0 {
				slog.InfoContext(ctx, "recording maintenance completed", "processed", stats.Processed, "failed", stats.Failed, "deleted", stats.Deleted)
			}
		}
	}
}

func (s *Service) RunLifecycleWorker(ctx context.Context, interval time.Duration, batchSize int) {
	if interval <= 0 {
		interval = 12 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("recording_lifecycle")
			}
			startedAt := time.Now()
			stats, err := s.TransitionStorageLifecycle(ctx, now.UTC(), batchSize)
			if s.observer != nil {
				s.observer.RecordRecordingRun("recording_lifecycle", 0, stats.Failed, 0, stats.Tiered, time.Since(startedAt), err)
			}
			if err != nil {
				slog.ErrorContext(ctx, "recording lifecycle worker failed", "error", err)
				continue
			}
			if stats.Tiered > 0 || stats.Failed > 0 {
				slog.InfoContext(ctx, "recording lifecycle worker completed", "tiered", stats.Tiered, "failed", stats.Failed)
			}
		}
	}
}

func (s *Service) dispatchRecordingReady(ctx context.Context, rec entity.Recording, artifact *ProcessedRecording) error {
	if s.hooks == nil {
		return nil
	}
	payload := map[string]any{
		"recording_id":     rec.ID,
		"call_id":          rec.CallID,
		"storage_path":     artifact.StoragePath,
		"file_size":        artifact.FileSize,
		"duration_seconds": artifact.DurationSeconds,
		"format":           artifact.Format,
		"integrity_sha256": artifact.IntegritySHA256,
	}
	for key, value := range artifact.Metadata {
		payload[key] = value
	}
	err := s.hooks.Dispatch(ctx, extension.HookEvent{
		Type:           extension.HookRecordingReady,
		WorkspaceID:    rec.WorkspaceID,
		ActorID:        rec.StartedBy,
		ResourceID:     rec.ID,
		IdempotencyKey: "recording.ready:" + rec.ID.String(),
		Payload:        payload,
	})
	if err != nil && s.observer != nil {
		s.observer.RecordRecordingHookFailure(err)
	}
	return err
}

func (s *Service) defaultStorageClass(tier entity.RecordingStorageTier) string {
	tieredStore, ok := s.store.(storage.TieredStorage)
	if !ok || tieredStore == nil {
		return ""
	}
	return tieredStore.DefaultStorageClass(storage.ObjectTier(tier))
}

func (s *Service) applyStorageTier(processed *ProcessedRecording, tier entity.RecordingStorageTier) {
	if processed == nil {
		return
	}
	storageClass := s.defaultStorageClass(tier)
	processed.StorageTier = tier
	processed.StorageClass = storageClass
	for i := range processed.Artifacts {
		processed.Artifacts[i].StorageTier = tier
		processed.Artifacts[i].StorageClass = storageClass
		now := time.Now().UTC()
		processed.Artifacts[i].TierUpdatedAt = &now
	}
}

func (s *Service) targetStorageTier(now time.Time, rec entity.Recording) entity.RecordingStorageTier {
	base := rec.CreatedAt
	if rec.ReadyAt != nil {
		base = *rec.ReadyAt
	} else if rec.StoppedAt != nil {
		base = *rec.StoppedAt
	}
	age := now.Sub(base)
	if s.archiveAfter > 0 && age >= s.archiveAfter {
		return entity.RecordingStorageTierArchive
	}
	if s.warmAfter > 0 && age >= s.warmAfter {
		return entity.RecordingStorageTierWarm
	}
	return entity.RecordingStorageTierHot
}

func (s *Service) transitionRecordingTier(ctx context.Context, tieredStore storage.TieredStorage, rec *entity.Recording, target entity.RecordingStorageTier, now time.Time) error {
	if rec == nil {
		return nil
	}
	targetClass := tieredStore.DefaultStorageClass(storage.ObjectTier(target))
	artifacts, err := reliability.DoValue(ctx, s.workerPolicy(2), func(ctx context.Context) ([]entity.RecordingArtifact, error) {
		return s.recordings.ListArtifacts(ctx, rec.ID)
	})
	if err != nil {
		return err
	}
	transitionedPaths := map[string]struct{}{}
	transitionObject := func(key string) error {
		if key == "" {
			return nil
		}
		if _, seen := transitionedPaths[key]; seen {
			return nil
		}
		if err := reliability.Do(ctx, s.workerPolicy(2), func(ctx context.Context) error {
			return tieredStore.TransitionObject(ctx, key, storage.ObjectTier(target), targetClass)
		}); err != nil {
			return err
		}
		transitionedPaths[key] = struct{}{}
		return nil
	}
	if err := transitionObject(rec.StoragePath); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := transitionObject(artifact.StoragePath); err != nil {
			return err
		}
	}
	repo, ok := s.recordings.(storageTierRepository)
	if !ok {
		return nil
	}
	if err := repo.UpdateStorageTier(ctx, rec.ID, target, targetClass, now); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := repo.UpdateArtifactStorageTier(ctx, rec.ID, artifact.ID, target, targetClass, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) getVisibleRecording(ctx context.Context, workspaceID, recordingID, userID uuid.UUID) (*entity.Recording, error) {
	rec, err := s.recordings.GetByID(ctx, recordingID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("recording not found")
		}
		return nil, cerrors.Internal("failed to get recording", err)
	}
	if rec.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("recording not found")
	}
	if err := s.requireRecordingViewAccess(ctx, workspaceID, rec.CallID, userID); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) requireRecordingViewAccess(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	if s.callAccess != nil {
		return s.callAccess.CanAccessCall(ctx, workspaceID, callID, userID)
	}
	if s.isWorkspaceAdmin(ctx, workspaceID, userID) {
		return nil
	}
	if _, err := s.calls.GetParticipant(ctx, callID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("not authorized to view recording")
		}
		return cerrors.Internal("failed to verify recording access", err)
	}
	return nil
}

func (s *Service) requireRecordingDownloadAccess(ctx context.Context, rec *entity.Recording, userID uuid.UUID) error {
	if rec == nil {
		return cerrors.NotFound("recording not found")
	}
	if s.callAccess != nil {
		return s.callAccess.CanAccessCall(ctx, rec.WorkspaceID, rec.CallID, userID)
	}
	if s.isWorkspaceAdmin(ctx, rec.WorkspaceID, userID) || rec.StartedBy == userID {
		return nil
	}
	return s.requireRecordingViewAccess(ctx, rec.WorkspaceID, rec.CallID, userID)
}

func (s *Service) isWorkspaceAdmin(ctx context.Context, workspaceID, userID uuid.UUID) bool {
	if s.workspaces == nil {
		return false
	}
	member, err := s.workspaces.GetMember(ctx, workspaceID, userID)
	if err != nil {
		return false
	}
	return member.Role == entity.WorkspaceRoleOwner || member.Role == entity.WorkspaceRoleAdmin
}

func (s *Service) ensureQuotaAvailable(ctx context.Context, workspaceID uuid.UUID) error {
	if s.quotaBytes <= 0 {
		return nil
	}
	usage, err := s.recordings.WorkspaceStorageUsage(ctx, workspaceID)
	if err != nil {
		return cerrors.Internal("failed to determine workspace recording usage", err)
	}
	if usage >= s.quotaBytes {
		return cerrors.Forbidden("workspace recording storage quota exceeded")
	}
	return nil
}

func (s *Service) ensureQuotaForArtifacts(ctx context.Context, workspaceID uuid.UUID, artifacts []entity.RecordingArtifact) error {
	if s.quotaBytes <= 0 {
		return nil
	}
	usage, err := s.recordings.WorkspaceStorageUsage(ctx, workspaceID)
	if err != nil {
		return cerrors.Internal("failed to determine workspace recording usage", err)
	}
	var additional int64
	for _, artifact := range artifacts {
		additional += artifact.FileSize
	}
	if usage+additional > s.quotaBytes {
		return cerrors.Forbidden("workspace recording storage quota would be exceeded")
	}
	return nil
}

func (s *Service) deleteUploadedArtifacts(ctx context.Context, artifacts []entity.RecordingArtifact) {
	if s.store == nil {
		return
	}
	seen := map[string]struct{}{}
	for _, artifact := range artifacts {
		if artifact.StoragePath == "" {
			continue
		}
		if _, ok := seen[artifact.StoragePath]; ok {
			continue
		}
		seen[artifact.StoragePath] = struct{}{}
		if err := reliability.Do(ctx, s.workerPolicy(2), func(ctx context.Context) error {
			return s.store.Delete(ctx, artifact.StoragePath)
		}); err != nil {
			slog.WarnContext(ctx, "failed to delete uploaded recording artifact after failure", "storage_path", artifact.StoragePath, "error", err)
		}
	}
}

func (s *Service) handleProcessingFailure(ctx context.Context, rec entity.Recording, cause error) {
	attempts := rec.ProcessingAttempts
	var nextRetryAt *time.Time
	if attempts < rec.MaxProcessingAttempts {
		delay := s.retryBackoff
		for i := 1; i < attempts; i++ {
			delay *= 2
			if delay > time.Hour {
				delay = time.Hour
				break
			}
		}
		retry := time.Now().UTC().Add(delay)
		nextRetryAt = &retry
	}
	if err := reliability.Do(ctx, s.workerPolicy(2), func(ctx context.Context) error {
		return s.recordings.MarkFailed(ctx, rec.ID, cause.Error(), nextRetryAt)
	}); err != nil {
		slog.ErrorContext(ctx, "failed to mark recording processing failure", "recording_id", rec.ID, "error", err)
	}
	slog.ErrorContext(ctx, "recording processing failed", "recording_id", rec.ID, "attempts", attempts, "error", cause)
}

func (s *Service) autoStopOnRoomClose(workspaceID, recordingID uuid.UUID, room *sfu.Room) {
	if s.capture == nil {
		return
	}
	if room == nil {
		return
	}
	<-room.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec, err := s.recordings.GetByID(ctx, recordingID)
	if err != nil || rec.Status != entity.RecordingStatusRecording {
		return
	}
	if err := s.capture.Stop(recordingID); err != nil {
		slog.WarnContext(ctx, "failed to auto-stop recording capture on room close", "recording_id", recordingID, "error", err)
		return
	}
	if _, err := s.recordings.Stop(ctx, recordingID); err != nil {
		slog.WarnContext(ctx, "failed to auto-transition recording after room close", "recording_id", recordingID, "error", err)
		return
	}
	slog.InfoContext(ctx, "recording auto-stopped after room close", "recording_id", recordingID, "workspace_id", workspaceID)
}

func (s *Service) mediaRoom(callID uuid.UUID) (*sfu.Room, bool) {
	if s.rooms == nil {
		return nil, false
	}
	return s.rooms.GetRoom(callID.String())
}

func (s *Service) workerPolicy(maxAttempts int) reliability.Policy {
	return reliability.Policy{
		Timeout:      s.opTimeout,
		MaxAttempts:  maxAttempts,
		RetryBackoff: s.retryBackoff,
		MaxBackoff:   5 * time.Second,
	}
}

func maxDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func (s *Service) createAuditTx(ctx context.Context, scope txscope.Scope, workspaceID, actorID uuid.UUID, action entity.AuditAction, targetType, targetID string, metadata map[string]any) error {
	if scope == nil || scope.Audit() == nil {
		return nil
	}
	entry := &entity.AuditEntry{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		ActorID:     actorID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    metadata,
		CreatedAt:   time.Now().UTC(),
	}
	return scope.Audit().Create(ctx, entry)
}

func (s *Service) logAudit(ctx context.Context, workspaceID, actorID uuid.UUID, action entity.AuditAction, targetType, targetID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	entry := &entity.AuditEntry{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		ActorID:     actorID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    metadata,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.audit.Create(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "failed to create recording audit entry", "workspace_id", workspaceID, "actor_id", actorID, "action", action, "error", err)
	}
}
