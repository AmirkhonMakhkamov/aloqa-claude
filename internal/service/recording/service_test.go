package recording

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/extension"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/storage"
	"aloqa/internal/service/call"
)

type fakeEgressClient struct {
	egressID   string
	startErr   error
	startCalls int
	stopCalls  int
	lastKey    string
	lastCallID uuid.UUID
}

func (c *fakeEgressClient) StartRoomCompositeFile(_ context.Context, callID uuid.UUID, key string) (string, error) {
	c.startCalls++
	c.lastKey = key
	c.lastCallID = callID
	if c.startErr != nil {
		return "", c.startErr
	}
	return c.egressID, nil
}

func (c *fakeEgressClient) StopEgress(_ context.Context, _ string) error {
	c.stopCalls++
	return nil
}

type fakeBroadcaster struct {
	started int
	updated []entity.RecordingStatus
}

func (b *fakeBroadcaster) BroadcastRecordingStarted(_ context.Context, _ *entity.Call, _ *entity.Recording) {
	b.started++
}

func (b *fakeBroadcaster) BroadcastRecordingUpdated(_ context.Context, _ *entity.Call, rec *entity.Recording) {
	b.updated = append(b.updated, rec.Status)
}

func activeCallRepo(callID, workspaceID, hostID uuid.UUID) *fakeCallRepo {
	return &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Status:      entity.CallStatusActive,
				Settings:    entity.CallSettings{Recording: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {
				ID:     uuid.New(),
				CallID: callID,
				UserID: hostID,
				Role:   entity.CallRoleHost,
				Status: entity.ParticipantStatusConnected,
			},
		},
	}
}

func TestStartRecordingCreatesRowThenEgress(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	started := time.Now().UTC()

	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{}, artifacts: map[uuid.UUID][]entity.RecordingArtifact{}}
	calls := activeCallRepo(callID, workspaceID, hostID)
	egress := &fakeEgressClient{egressID: "EG-" + uuid.NewString()}
	broadcaster := &fakeBroadcaster{}

	svc := NewService(recordings, calls, nil, &memoryStorage{}, nil, nil, Config{Retention: 24 * time.Hour})
	svc.SetEgressClient(egress)
	svc.SetBroadcaster(broadcaster)

	rec, err := svc.StartRecording(ctx, workspaceID, callID, hostID, "")
	if err != nil {
		t.Fatalf("StartRecording returned error: %v", err)
	}

	if rec.Status != entity.RecordingStatusRecording {
		t.Fatalf("Status = %s, want recording", rec.Status)
	}
	if rec.Strategy != entity.RecordingStrategyComposite {
		t.Fatalf("Strategy = %s, want composite", rec.Strategy)
	}
	wantKey := "recordings/" + callID.String() + "/" + rec.ID.String() + ".mp4"
	if rec.StoragePath != wantKey {
		t.Fatalf("StoragePath = %s, want %s", rec.StoragePath, wantKey)
	}
	if egress.startCalls != 1 || egress.lastKey != wantKey || egress.lastCallID != callID {
		t.Fatalf("egress not started with deterministic key (calls=%d key=%s)", egress.startCalls, egress.lastKey)
	}
	if rec.EgressID == nil || *rec.EgressID != egress.egressID {
		t.Fatalf("egress_id not backfilled on returned recording")
	}
	if stored := recordings.recordings[rec.ID]; stored.EgressID == nil || *stored.EgressID != egress.egressID {
		t.Fatalf("egress_id not persisted")
	}
	if broadcaster.started != 1 {
		t.Fatalf("started broadcasts = %d, want 1", broadcaster.started)
	}
	if rec.ExpiresAt == nil || rec.ExpiresAt.Before(started.Add(23*time.Hour)) {
		t.Fatalf("ExpiresAt = %v, want retention timestamp", rec.ExpiresAt)
	}

	calls.calls[callID].Settings.Recording = false
	if _, err := svc.StartRecording(ctx, workspaceID, callID, hostID, ""); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("StartRecording with disabled setting error = %v, want FORBIDDEN", err)
	}
}

func TestStartRecordingUnavailableWhenEgressMissing(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{}}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	// No SetEgressClient → egress nil.
	if _, err := svc.StartRecording(ctx, workspaceID, callID, hostID, ""); !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("StartRecording without egress error = %v, want UNAVAILABLE", err)
	}
}

func TestStartRecordingMarksRowFailedOnEgressError(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{}}
	egress := &fakeEgressClient{startErr: cerrors.Unavailable("egress down")}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	svc.SetEgressClient(egress)

	if _, err := svc.StartRecording(ctx, workspaceID, callID, hostID, ""); !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("StartRecording egress-error = %v, want UNAVAILABLE", err)
	}
	// The pre-created row must be marked failed (no orphan 'recording').
	var failed int
	for _, rec := range recordings.recordings {
		if rec.Status == entity.RecordingStatusFailed {
			failed++
		}
		if rec.Status == entity.RecordingStatusRecording {
			t.Fatalf("orphan recording row left in 'recording' after egress failure")
		}
	}
	if failed != 1 {
		t.Fatalf("failed rows = %d, want 1", failed)
	}
}

func TestStartRecordingConflictOnUniqueViolation(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	// createErr simulates the ux_recordings_active_per_call unique violation
	// (race past the fast-path ListByCall check).
	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{}, createErr: cerrors.Conflict("call is already being recorded")}
	egress := &fakeEgressClient{egressID: "EG"}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	svc.SetEgressClient(egress)

	if _, err := svc.StartRecording(ctx, workspaceID, callID, hostID, ""); !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("StartRecording concurrent = %v, want CONFLICT", err)
	}
	if egress.startCalls != 0 {
		t.Fatalf("egress started despite create conflict (calls=%d)", egress.startCalls)
	}
}

func TestStopRecordingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	egressID := "EG-" + uuid.NewString()
	recID := uuid.New()
	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{
		recID: {ID: recID, CallID: callID, WorkspaceID: workspaceID, StartedBy: hostID, Status: entity.RecordingStatusRecording, EgressID: &egressID},
	}}
	egress := &fakeEgressClient{egressID: egressID}
	broadcaster := &fakeBroadcaster{}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	svc.SetEgressClient(egress)
	svc.SetBroadcaster(broadcaster)

	if err := svc.StopRecording(ctx, workspaceID, recID, hostID); err != nil {
		t.Fatalf("first StopRecording error: %v", err)
	}
	if recordings.recordings[recID].Status != entity.RecordingStatusProcessing {
		t.Fatalf("status after stop = %s, want processing", recordings.recordings[recID].Status)
	}
	if egress.stopCalls != 1 {
		t.Fatalf("StopEgress calls = %d, want 1", egress.stopCalls)
	}
	if len(broadcaster.updated) != 1 || broadcaster.updated[0] != entity.RecordingStatusProcessing {
		t.Fatalf("updated broadcasts = %v, want [processing]", broadcaster.updated)
	}

	// Second stop on an already-processing row: no-op success, no 2nd StopEgress, no 2nd broadcast.
	if err := svc.StopRecording(ctx, workspaceID, recID, hostID); err != nil {
		t.Fatalf("repeated StopRecording error: %v, want nil (no-op)", err)
	}
	if egress.stopCalls != 1 {
		t.Fatalf("StopEgress called again on repeated stop (calls=%d)", egress.stopCalls)
	}
	if len(broadcaster.updated) != 1 {
		t.Fatalf("extra broadcast on repeated stop: %v", broadcaster.updated)
	}
}

func TestHandleEgressEventReadyRegistersCompositeArtifact(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	egressID := "EG-" + uuid.NewString()
	recID := uuid.New()
	key := "recordings/" + callID.String() + "/" + recID.String() + ".mp4"
	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recID: {ID: recID, CallID: callID, WorkspaceID: workspaceID, StartedBy: hostID, Status: entity.RecordingStatusProcessing, StoragePath: key, EgressID: &egressID},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{},
	}
	broadcaster := &fakeBroadcaster{}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	svc.SetBroadcaster(broadcaster)

	err := svc.HandleEgressEvent(ctx, call.EgressEventInfo{
		EgressID: egressID, CallID: callID, Phase: call.EgressEventEnded, Succeeded: true,
		FileSizeBytes: 4096, DurationSeconds: 12,
	})
	if err != nil {
		t.Fatalf("HandleEgressEvent error: %v", err)
	}
	rec := recordings.recordings[recID]
	if rec.Status != entity.RecordingStatusReady {
		t.Fatalf("status = %s, want ready", rec.Status)
	}
	arts := recordings.artifacts[recID]
	if len(arts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(arts))
	}
	if arts[0].Kind != entity.RecordingArtifactKindComposite || arts[0].StoragePath != key {
		t.Fatalf("artifact kind/path = %s/%s, want composite_playback/%s", arts[0].Kind, arts[0].StoragePath, key)
	}
	if len(broadcaster.updated) != 1 || broadcaster.updated[0] != entity.RecordingStatusReady {
		t.Fatalf("updated broadcasts = %v, want [ready]", broadcaster.updated)
	}
}

func TestHandleEgressEventRaceFallbackByCall(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	egressID := "EG-" + uuid.NewString()
	recID := uuid.New()
	key := "recordings/" + callID.String() + "/" + recID.String() + ".mp4"
	// egress_ended races the egress_id backfill: row has NO egress_id yet.
	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recID: {ID: recID, CallID: callID, WorkspaceID: workspaceID, StartedBy: hostID, Status: entity.RecordingStatusRecording, StoragePath: key},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{},
	}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	svc.SetBroadcaster(&fakeBroadcaster{})

	if err := svc.HandleEgressEvent(ctx, call.EgressEventInfo{EgressID: egressID, CallID: callID, Phase: call.EgressEventEnded, Succeeded: true, FileSizeBytes: 10, DurationSeconds: 1}); err != nil {
		t.Fatalf("HandleEgressEvent (race) error: %v", err)
	}
	rec := recordings.recordings[recID]
	if rec.Status != entity.RecordingStatusReady {
		t.Fatalf("status = %s, want ready (correlated via GetActiveByCall)", rec.Status)
	}
	if rec.EgressID == nil || *rec.EgressID != egressID {
		t.Fatalf("egress_id not backfilled during race fallback")
	}
}

func TestHandleEgressEventFailed(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	egressID := "EG-" + uuid.NewString()
	recID := uuid.New()
	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{
		recID: {ID: recID, CallID: callID, WorkspaceID: workspaceID, StartedBy: hostID, Status: entity.RecordingStatusProcessing, EgressID: &egressID},
	}}
	broadcaster := &fakeBroadcaster{}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})
	svc.SetBroadcaster(broadcaster)

	if err := svc.HandleEgressEvent(ctx, call.EgressEventInfo{EgressID: egressID, CallID: callID, Phase: call.EgressEventEnded, Succeeded: false}); err != nil {
		t.Fatalf("HandleEgressEvent (failed) error: %v", err)
	}
	if recordings.recordings[recID].Status != entity.RecordingStatusFailed {
		t.Fatalf("status = %s, want failed", recordings.recordings[recID].Status)
	}
	if len(broadcaster.updated) != 1 || broadcaster.updated[0] != entity.RecordingStatusFailed {
		t.Fatalf("updated broadcasts = %v, want [failed]", broadcaster.updated)
	}
}

func TestHandleEgressEventUncorrelatedTerminalIsRetryable(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID := uuid.New(), uuid.New(), uuid.New()
	recordings := &fakeRecordingRepo{recordings: map[uuid.UUID]*entity.Recording{}}
	svc := NewService(recordings, activeCallRepo(callID, workspaceID, hostID), nil, &memoryStorage{}, nil, nil, Config{})

	// No recording exists for this egress/call: a terminal event must error so
	// the webhook stays unprocessed and LiveKit can redeliver.
	if err := svc.HandleEgressEvent(ctx, call.EgressEventInfo{EgressID: "EG-unknown", CallID: callID, Phase: call.EgressEventEnded, Succeeded: true}); err == nil {
		t.Fatalf("HandleEgressEvent uncorrelated terminal error = nil, want non-nil")
	}
	// A non-terminal phase with no correlation is benign (no error).
	if err := svc.HandleEgressEvent(ctx, call.EgressEventInfo{EgressID: "EG-unknown", CallID: callID, Phase: call.EgressEventStarted}); err != nil {
		t.Fatalf("HandleEgressEvent uncorrelated started error = %v, want nil", err)
	}
}

func TestGetRecordingRequiresCallParticipant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	recordingID := uuid.New()
	userID := uuid.New()

	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recordingID: {ID: recordingID, CallID: callID, WorkspaceID: workspaceID, Status: entity.RecordingStatusReady},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{},
	}
	calls := &fakeCallRepo{
		calls:        map[uuid.UUID]*entity.Call{callID: {ID: callID, WorkspaceID: workspaceID}},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	svc := NewService(recordings, calls, nil, nil, nil, nil, Config{})

	if _, err := svc.GetRecording(ctx, workspaceID, recordingID, userID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("GetRecording nonparticipant error = %v, want FORBIDDEN", err)
	}
}

func TestRecordingAccessCanFollowCallAuthorizer(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	recordingID := uuid.New()
	artifactID := uuid.New()
	userID := uuid.New()
	storageKey := "recordings/test.zip"

	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recordingID: {
				ID:           recordingID,
				CallID:       callID,
				WorkspaceID:  workspaceID,
				Status:       entity.RecordingStatusReady,
				StartedBy:    uuid.New(),
				Downloadable: true,
			},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{
			recordingID: {{
				ID:           artifactID,
				RecordingID:  recordingID,
				WorkspaceID:  workspaceID,
				Kind:         entity.RecordingArtifactKindSessionBundle,
				StoragePath:  storageKey,
				FileSize:     4,
				MimeType:     "application/zip",
				Downloadable: true,
				CreatedAt:    time.Now().UTC(),
			}},
		},
	}
	calls := &fakeCallRepo{
		calls:        map[uuid.UUID]*entity.Call{callID: {ID: callID, WorkspaceID: workspaceID}},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	store := &memoryStorage{objects: map[string][]byte{storageKey: []byte("test")}}

	svc := NewService(recordings, calls, nil, store, nil, nil, Config{})
	svc.SetCallAccessAuthorizer(fakeCallAccessAuthorizer{})

	if _, err := svc.GetRecording(ctx, workspaceID, recordingID, userID); err != nil {
		t.Fatalf("GetRecording returned error: %v", err)
	}
	reader, info, artifact, err := svc.DownloadArtifact(ctx, workspaceID, recordingID, artifactID, userID)
	if err != nil {
		t.Fatalf("DownloadArtifact returned error: %v", err)
	}
	defer reader.Close()
	if info.Size != 4 || artifact.ID != artifactID {
		t.Fatalf("unexpected artifact download info: info=%+v artifact=%+v", info, artifact)
	}
}

func TestPresignArtifactDownloadUsesSignerWhenAvailable(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	recordingID := uuid.New()
	artifactID := uuid.New()
	userID := uuid.New()
	storageKey := "recordings/test.zip"

	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recordingID: {
				ID:           recordingID,
				CallID:       callID,
				WorkspaceID:  workspaceID,
				Status:       entity.RecordingStatusReady,
				StartedBy:    userID,
				Downloadable: true,
			},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{
			recordingID: {{
				ID:           artifactID,
				RecordingID:  recordingID,
				WorkspaceID:  workspaceID,
				Kind:         entity.RecordingArtifactKindSessionBundle,
				StoragePath:  storageKey,
				FileSize:     4,
				MimeType:     "application/zip",
				Format:       "zip",
				Downloadable: true,
				CreatedAt:    time.Now().UTC(),
			}},
		},
	}
	store := &memoryStorage{objects: map[string][]byte{storageKey: []byte("test")}, signedURL: "https://objects.example.com/presigned/recordings/test.zip"}
	svc := NewService(recordings, &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{callID: {ID: callID, WorkspaceID: workspaceID}}}, nil, store, nil, nil, Config{SignedURLTTL: time.Minute})
	svc.SetCallAccessAuthorizer(fakeCallAccessAuthorizer{})

	url, artifact, err := svc.PresignArtifactDownload(ctx, workspaceID, recordingID, artifactID, userID)
	if err != nil {
		t.Fatalf("PresignArtifactDownload returned error: %v", err)
	}
	if url != store.signedURL {
		t.Fatalf("signed URL = %q, want %q", url, store.signedURL)
	}
	if artifact == nil || artifact.ID != artifactID {
		t.Fatalf("artifact = %+v, want %s", artifact, artifactID)
	}
}

func TestRunMaintenanceProcessesPendingAndDeletesExpired(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	pendingID := uuid.New()
	workspaceID := uuid.New()
	expiredID := uuid.New()
	expiredAt := now.Add(-time.Hour)
	oldKey := "recordings/old.zip"

	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			pendingID: {ID: pendingID, CallID: uuid.New(), WorkspaceID: workspaceID, Status: entity.RecordingStatusProcessing, MaxProcessingAttempts: 5},
			expiredID: {ID: expiredID, CallID: uuid.New(), WorkspaceID: workspaceID, Status: entity.RecordingStatusReady, StoragePath: oldKey, ExpiresAt: &expiredAt},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{
			expiredID: {{
				ID:          uuid.New(),
				RecordingID: expiredID,
				WorkspaceID: workspaceID,
				Kind:        entity.RecordingArtifactKindSessionBundle,
				StoragePath: oldKey,
				FileSize:    int64(len([]byte("old"))),
				CreatedAt:   now,
			}},
		},
	}

	hooks := extension.NewHookDispatcher()
	var readyEvent *extension.HookEvent
	hooks.Register(extension.HookRecordingReady, extension.HookHandlerFunc(func(_ context.Context, event extension.HookEvent) error {
		readyEvent = &event
		return nil
	}))

	store := &memoryStorage{objects: map[string][]byte{oldKey: []byte("old")}}
	svc := NewService(recordings, &fakeCallRepo{}, nil, store, nil, nil, Config{
		Hooks:        hooks,
		RetryBackoff: time.Second,
		SpoolBaseDir: t.TempDir(),
	})

	stats, err := svc.RunMaintenanceOnce(ctx, fixedProcessor{artifact: &ProcessedRecording{
		Format:          entity.RecordingFormatBundle,
		StoragePath:     "recordings/new.zip",
		FileSize:        128,
		DurationSeconds: 42,
		IntegritySHA256: "abc123",
		Downloadable:    true,
		Artifacts: []entity.RecordingArtifact{{
			ID:              uuid.New(),
			RecordingID:     pendingID,
			WorkspaceID:     workspaceID,
			Kind:            entity.RecordingArtifactKindSessionBundle,
			Format:          "zip",
			MimeType:        "application/zip",
			StoragePath:     "recordings/new.zip",
			FileSize:        128,
			IntegritySHA256: "abc123",
			Downloadable:    true,
			CreatedAt:       now,
		}},
	}}, now, 10)
	if err != nil {
		t.Fatalf("RunMaintenanceOnce returned error: %v", err)
	}
	if stats.Processed != 1 || stats.Deleted != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want processed=1 deleted=1 failed=0", stats)
	}

	processed := recordings.recordings[pendingID]
	if processed.Status != entity.RecordingStatusReady || processed.StoragePath != "recordings/new.zip" {
		t.Fatalf("processed recording = %+v, want ready artifact", processed)
	}
	if processed.FileSize == nil || *processed.FileSize != 128 {
		t.Fatalf("processed file size = %v, want 128", processed.FileSize)
	}
	if processed.IntegritySHA256 != "abc123" {
		t.Fatalf("integrity sha = %q, want abc123", processed.IntegritySHA256)
	}
	if readyEvent == nil || readyEvent.Type != extension.HookRecordingReady || readyEvent.ResourceID != pendingID {
		t.Fatalf("ready event = %+v, want recording.ready for pending recording", readyEvent)
	}
	if _, exists := recordings.recordings[expiredID]; exists {
		t.Fatalf("expired recording metadata was not deleted")
	}
	if _, exists := store.objects[oldKey]; exists {
		t.Fatalf("expired recording object was not deleted")
	}
}

func TestRunMaintenanceKeepsRecordingReadyWhenHookFails(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	recordingID := uuid.New()
	workspaceID := uuid.New()

	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recordingID: {ID: recordingID, CallID: uuid.New(), WorkspaceID: workspaceID, Status: entity.RecordingStatusProcessing, MaxProcessingAttempts: 5},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{},
	}
	hooks := extension.NewHookDispatcher()
	hooks.Register(extension.HookRecordingReady, extension.HookHandlerFunc(func(context.Context, extension.HookEvent) error {
		return errors.New("hook sink unavailable")
	}))

	svc := NewService(recordings, &fakeCallRepo{}, nil, &memoryStorage{}, nil, nil, Config{
		Hooks:        hooks,
		RetryBackoff: time.Second,
	})

	stats, err := svc.RunMaintenanceOnce(ctx, fixedProcessor{artifact: &ProcessedRecording{
		Format:          entity.RecordingFormatBundle,
		StoragePath:     "recordings/final.zip",
		FileSize:        64,
		DurationSeconds: 10,
		IntegritySHA256: "ready",
		Downloadable:    true,
		Artifacts: []entity.RecordingArtifact{{
			ID:              uuid.New(),
			RecordingID:     recordingID,
			WorkspaceID:     workspaceID,
			Kind:            entity.RecordingArtifactKindSessionBundle,
			StoragePath:     "recordings/final.zip",
			FileSize:        64,
			IntegritySHA256: "ready",
			Downloadable:    true,
			CreatedAt:       now,
		}},
	}}, now, 10)
	if err != nil {
		t.Fatalf("RunMaintenanceOnce returned error: %v", err)
	}
	if stats.Processed != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want processed=1 failed=0", stats)
	}
	if recordings.recordings[recordingID].Status != entity.RecordingStatusReady {
		t.Fatalf("recording status = %s, want ready", recordings.recordings[recordingID].Status)
	}
}

func TestTransitionStorageLifecycleMovesReadyArtifactsToWarmTier(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	recordingID := uuid.New()
	artifactID := uuid.New()
	readyAt := time.Now().UTC().Add(-48 * time.Hour)
	key := "recordings/warm.zip"

	recordings := &fakeRecordingRepo{
		recordings: map[uuid.UUID]*entity.Recording{
			recordingID: {
				ID:          recordingID,
				CallID:      uuid.New(),
				WorkspaceID: workspaceID,
				Status:      entity.RecordingStatusReady,
				StoragePath: key,
				StorageTier: entity.RecordingStorageTierHot,
				ReadyAt:     &readyAt,
				CreatedAt:   readyAt,
			},
		},
		artifacts: map[uuid.UUID][]entity.RecordingArtifact{
			recordingID: {{
				ID:          artifactID,
				RecordingID: recordingID,
				WorkspaceID: workspaceID,
				StoragePath: key,
				StorageTier: entity.RecordingStorageTierHot,
				FileSize:    32,
				CreatedAt:   readyAt,
			}},
		},
	}
	store := &memoryStorage{objects: map[string][]byte{key: []byte("warm")}}
	svc := NewService(recordings, &fakeCallRepo{}, nil, store, nil, nil, Config{
		WarmAfter:    24 * time.Hour,
		ArchiveAfter: 30 * 24 * time.Hour,
	})

	stats, err := svc.TransitionStorageLifecycle(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("TransitionStorageLifecycle returned error: %v", err)
	}
	if stats.Tiered != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want tiered=1 failed=0", stats)
	}
	if recordings.recordings[recordingID].StorageTier != entity.RecordingStorageTierWarm {
		t.Fatalf("recording tier = %s, want warm", recordings.recordings[recordingID].StorageTier)
	}
	if recordings.artifacts[recordingID][0].StorageTier != entity.RecordingStorageTierWarm {
		t.Fatalf("artifact tier = %s, want warm", recordings.artifacts[recordingID][0].StorageTier)
	}
	if len(store.transitions) != 1 || store.transitions[0].key != key {
		t.Fatalf("transitions = %+v, want one transition for %s", store.transitions, key)
	}
}

type fixedProcessor struct {
	artifact *ProcessedRecording
	err      error
}

func (p fixedProcessor) Process(context.Context, entity.Recording) (*ProcessedRecording, error) {
	return p.artifact, p.err
}

type fakeMediaRooms struct {
	rooms map[string]*sfu.Room
}

func (f fakeMediaRooms) GetRoom(id string) (*sfu.Room, bool) {
	room, ok := f.rooms[id]
	return room, ok
}

func newTestRoom(t *testing.T, roomID string) *sfu.Room {
	t.Helper()

	server, err := sfu.NewSFU(sfu.Config{})
	if err != nil {
		t.Fatalf("NewSFU returned error: %v", err)
	}
	t.Cleanup(server.Close)

	room, err := server.CreateRoom(roomID, sfu.RoomOptions{})
	if err != nil {
		t.Fatalf("CreateRoom returned error: %v", err)
	}
	return room
}

type fakeRecordingRepo struct {
	recordings map[uuid.UUID]*entity.Recording
	artifacts  map[uuid.UUID][]entity.RecordingArtifact
	createErr  error // injected to simulate the active-per-call unique violation
}

func (r *fakeRecordingRepo) Create(_ context.Context, rec *entity.Recording) error {
	if r.createErr != nil {
		return r.createErr
	}
	if r.recordings == nil {
		r.recordings = map[uuid.UUID]*entity.Recording{}
	}
	r.recordings[rec.ID] = cloneRecording(rec)
	return nil
}

func (r *fakeRecordingRepo) GetByEgressID(_ context.Context, egressID string) (*entity.Recording, error) {
	for _, rec := range r.recordings {
		if rec.EgressID != nil && *rec.EgressID == egressID {
			return cloneRecording(rec), nil
		}
	}
	return nil, cerrors.NotFound("recording not found")
}

func (r *fakeRecordingRepo) GetActiveByCall(_ context.Context, callID uuid.UUID) (*entity.Recording, error) {
	for _, rec := range r.recordings {
		if rec.CallID == callID &&
			(rec.Status == entity.RecordingStatusRecording || rec.Status == entity.RecordingStatusProcessing) {
			return cloneRecording(rec), nil
		}
	}
	return nil, cerrors.NotFound("recording not found")
}

func (r *fakeRecordingRepo) SetEgressID(_ context.Context, id uuid.UUID, egressID string) error {
	rec, ok := r.recordings[id]
	if !ok {
		return cerrors.NotFound("recording not found")
	}
	ev := egressID
	rec.EgressID = &ev
	return nil
}

func (r *fakeRecordingRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Recording, error) {
	rec := r.recordings[id]
	if rec == nil {
		return nil, cerrors.NotFound("recording not found")
	}
	return cloneRecording(rec), nil
}

func (r *fakeRecordingRepo) ListByCall(_ context.Context, callID uuid.UUID) ([]entity.Recording, error) {
	var out []entity.Recording
	for _, rec := range r.recordings {
		if rec.CallID == callID {
			out = append(out, *cloneRecording(rec))
		}
	}
	return out, nil
}

func (r *fakeRecordingRepo) ListByWorkspace(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.Recording, error) {
	var out []entity.Recording
	for _, rec := range r.recordings {
		if rec.WorkspaceID == workspaceID {
			out = append(out, *cloneRecording(rec))
		}
	}
	return out, nil
}

func (r *fakeRecordingRepo) ListByStatus(_ context.Context, status entity.RecordingStatus, _ pagination.Params) ([]entity.Recording, error) {
	var out []entity.Recording
	for _, rec := range r.recordings {
		if rec.Status == status {
			out = append(out, *cloneRecording(rec))
		}
	}
	return out, nil
}

func (r *fakeRecordingRepo) ListProcessable(_ context.Context, now time.Time, _ pagination.Params) ([]entity.Recording, error) {
	var out []entity.Recording
	for _, rec := range r.recordings {
		if (rec.Status == entity.RecordingStatusProcessing || rec.Status == entity.RecordingStatusFailed) &&
			(rec.NextRetryAt == nil || !rec.NextRetryAt.After(now)) &&
			(rec.Status != entity.RecordingStatusFailed || rec.ProcessingAttempts < rec.MaxProcessingAttempts) {
			out = append(out, *cloneRecording(rec))
		}
	}
	return out, nil
}

func (r *fakeRecordingRepo) ListExpired(_ context.Context, now time.Time, _ pagination.Params) ([]entity.Recording, error) {
	var out []entity.Recording
	for _, rec := range r.recordings {
		if rec.Status == entity.RecordingStatusReady && rec.ExpiresAt != nil && !rec.ExpiresAt.After(now) && !rec.LegalHold {
			out = append(out, *cloneRecording(rec))
		}
	}
	return out, nil
}

func (r *fakeRecordingRepo) UpdateStatus(_ context.Context, id uuid.UUID, status entity.RecordingStatus) error {
	rec, ok := r.recordings[id]
	if !ok {
		return cerrors.NotFound("recording not found")
	}
	rec.Status = status
	return nil
}

func (r *fakeRecordingRepo) SetReady(_ context.Context, rec *entity.Recording) error {
	stored, ok := r.recordings[rec.ID]
	if !ok {
		return cerrors.NotFound("recording not found")
	}
	now := time.Now().UTC()
	stored.Status = entity.RecordingStatusReady
	stored.Format = rec.Format
	stored.StoragePath = rec.StoragePath
	stored.StorageTier = rec.StorageTier
	stored.StorageClass = rec.StorageClass
	stored.IntegritySHA256 = rec.IntegritySHA256
	stored.Downloadable = rec.Downloadable
	stored.FileSize = rec.FileSize
	stored.Duration = rec.Duration
	stored.Metadata = cloneMetadata(rec.Metadata)
	stored.ReadyAt = &now
	stored.TierUpdatedAt = rec.TierUpdatedAt
	stored.NextRetryAt = nil
	stored.LastError = ""
	return nil
}

func (r *fakeRecordingRepo) MarkProcessingAttempt(_ context.Context, id uuid.UUID, nextRetryAt time.Time) (*entity.Recording, error) {
	rec, ok := r.recordings[id]
	if !ok {
		return nil, cerrors.NotFound("recording not found")
	}
	rec.Status = entity.RecordingStatusProcessing
	rec.ProcessingAttempts++
	rec.NextRetryAt = &nextRetryAt
	return cloneRecording(rec), nil
}

func (r *fakeRecordingRepo) MarkFailed(_ context.Context, id uuid.UUID, lastError string, nextRetryAt *time.Time) error {
	rec, ok := r.recordings[id]
	if !ok {
		return cerrors.NotFound("recording not found")
	}
	rec.Status = entity.RecordingStatusFailed
	rec.LastError = lastError
	rec.NextRetryAt = nextRetryAt
	return nil
}

func (r *fakeRecordingRepo) SetLegalHold(_ context.Context, id uuid.UUID, hold bool) error {
	rec, ok := r.recordings[id]
	if !ok {
		return cerrors.NotFound("recording not found")
	}
	rec.LegalHold = hold
	return nil
}

func (r *fakeRecordingRepo) Stop(_ context.Context, id uuid.UUID) (*entity.Recording, error) {
	rec, ok := r.recordings[id]
	if !ok {
		return nil, cerrors.NotFound("recording not found")
	}
	now := time.Now().UTC()
	rec.Status = entity.RecordingStatusProcessing
	rec.StoppedAt = &now
	rec.NextRetryAt = &now
	return cloneRecording(rec), nil
}

func (r *fakeRecordingRepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := r.recordings[id]; !ok {
		return cerrors.NotFound("recording not found")
	}
	delete(r.recordings, id)
	delete(r.artifacts, id)
	return nil
}

func (r *fakeRecordingRepo) ReplaceArtifacts(_ context.Context, recordingID uuid.UUID, artifacts []entity.RecordingArtifact) error {
	if r.artifacts == nil {
		r.artifacts = map[uuid.UUID][]entity.RecordingArtifact{}
	}
	replaced := make([]entity.RecordingArtifact, len(artifacts))
	copy(replaced, artifacts)
	r.artifacts[recordingID] = replaced
	return nil
}

func (r *fakeRecordingRepo) ListArtifacts(_ context.Context, recordingID uuid.UUID) ([]entity.RecordingArtifact, error) {
	artifacts := r.artifacts[recordingID]
	out := make([]entity.RecordingArtifact, len(artifacts))
	copy(out, artifacts)
	return out, nil
}

func (r *fakeRecordingRepo) GetArtifact(_ context.Context, recordingID, artifactID uuid.UUID) (*entity.RecordingArtifact, error) {
	for _, artifact := range r.artifacts[recordingID] {
		if artifact.ID == artifactID {
			copy := artifact
			return &copy, nil
		}
	}
	return nil, cerrors.NotFound("recording artifact not found")
}

func (r *fakeRecordingRepo) WorkspaceStorageUsage(_ context.Context, workspaceID uuid.UUID) (int64, error) {
	var usage int64
	for _, artifacts := range r.artifacts {
		for _, artifact := range artifacts {
			if artifact.WorkspaceID == workspaceID {
				usage += artifact.FileSize
			}
		}
	}
	return usage, nil
}

func (r *fakeRecordingRepo) UpdateStorageTier(_ context.Context, recordingID uuid.UUID, tier entity.RecordingStorageTier, storageClass string, updatedAt time.Time) error {
	rec, ok := r.recordings[recordingID]
	if !ok {
		return cerrors.NotFound("recording not found")
	}
	rec.StorageTier = tier
	rec.StorageClass = storageClass
	rec.TierUpdatedAt = &updatedAt
	return nil
}

func (r *fakeRecordingRepo) UpdateArtifactStorageTier(_ context.Context, recordingID, artifactID uuid.UUID, tier entity.RecordingStorageTier, storageClass string, updatedAt time.Time) error {
	artifacts := r.artifacts[recordingID]
	for i := range artifacts {
		if artifacts[i].ID == artifactID {
			artifacts[i].StorageTier = tier
			artifacts[i].StorageClass = storageClass
			artifacts[i].TierUpdatedAt = &updatedAt
			r.artifacts[recordingID] = artifacts
			return nil
		}
	}
	return cerrors.NotFound("recording artifact not found")
}

type fakeCallRepo struct {
	calls        map[uuid.UUID]*entity.Call
	participants map[[2]uuid.UUID]*entity.CallParticipant
}

func (r *fakeCallRepo) Create(context.Context, *entity.Call) error { return nil }

func (r *fakeCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	if call := r.calls[id]; call != nil {
		return call, nil
	}
	return nil, cerrors.NotFound("call not found")
}

func (r *fakeCallRepo) ListActiveByWorkspace(context.Context, uuid.UUID) ([]entity.Call, error) {
	return nil, nil
}

func (r *fakeCallRepo) UpdateStatus(context.Context, uuid.UUID, entity.CallStatus) error {
	return nil
}

func (r *fakeCallRepo) End(context.Context, uuid.UUID) error { return nil }

func (r *fakeCallRepo) AddParticipant(context.Context, *entity.CallParticipant) error {
	return nil
}
func (r *fakeCallRepo) AddParticipantIfCapacity(context.Context, *entity.CallParticipant, int) error {
	return nil
}

func (r *fakeCallRepo) GetParticipant(_ context.Context, callID, userID uuid.UUID) (*entity.CallParticipant, error) {
	if p := r.participants[[2]uuid.UUID{callID, userID}]; p != nil {
		return p, nil
	}
	return nil, cerrors.NotFound("call participant not found")
}

func (r *fakeCallRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}

func (r *fakeCallRepo) UpdateParticipantStatus(context.Context, uuid.UUID, entity.ParticipantStatus) error {
	return nil
}

func (r *fakeCallRepo) UpdateParticipantRole(context.Context, uuid.UUID, entity.CallRole) error {
	return nil
}

func (r *fakeCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}

func (r *fakeCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (r *fakeCallRepo) TransferHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

type fakeCallAccessAuthorizer struct{}

func (fakeCallAccessAuthorizer) CanAccessCall(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}

type memoryStorage struct {
	objects     map[string][]byte
	signedURL   string
	transitions []tierTransition
}

func (s *memoryStorage) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}

func (s *memoryStorage) Get(_ context.Context, key string) (io.ReadCloser, *storage.FileInfo, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, nil, errors.Join(storage.ErrNotFound, cerrors.NotFound("file not found"))
	}
	return io.NopCloser(bytes.NewReader(data)), &storage.FileInfo{Key: key, Size: int64(len(data))}, nil
}

func (s *memoryStorage) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func (s *memoryStorage) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.objects[key]
	return ok, nil
}

func (s *memoryStorage) SignedDownloadURL(_ context.Context, key string, _ storage.SignedURLOptions) (string, error) {
	if s.signedURL == "" {
		return "", storage.ErrNotSupported
	}
	return s.signedURL, nil
}

func (s *memoryStorage) TransitionObject(_ context.Context, key string, tier storage.ObjectTier, storageClass string) error {
	s.transitions = append(s.transitions, tierTransition{key: key, tier: tier, storageClass: storageClass})
	return nil
}

func (s *memoryStorage) DefaultStorageClass(tier storage.ObjectTier) string {
	switch tier {
	case storage.ObjectTierWarm:
		return "STANDARD_IA"
	case storage.ObjectTierArchive:
		return "GLACIER_IR"
	default:
		return "STANDARD"
	}
}

type tierTransition struct {
	key          string
	tier         storage.ObjectTier
	storageClass string
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

func cloneRecording(rec *entity.Recording) *entity.Recording {
	if rec == nil {
		return nil
	}
	copy := *rec
	copy.Metadata = cloneMetadata(rec.Metadata)
	return &copy
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
