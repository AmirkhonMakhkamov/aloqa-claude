package recording

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

func TestSpoolProcessorProcessBuildsCompositeTranscriptAndAIManifest(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	recordingID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	viewerID := uuid.New()
	startedAt := time.Now().UTC().Add(-2 * time.Minute)
	stoppedAt := startedAt.Add(90 * time.Second)

	state := &SpoolState{
		RecordingID: recordingID,
		WorkspaceID: workspaceID,
		CallID:      callID,
		StartedBy:   hostID,
		Strategy:    entity.RecordingStrategyBoth,
		StartedAt:   startedAt,
		StoppedAt:   &stoppedAt,
		Tracks: []SpoolTrackState{
			makeTestTrack(t, baseDir, recordingID, hostID, entity.RecordingArtifactKindScreenTrack, "screen-main.ivf", 1200, "screen", "screen-share", "video/vp9"),
			makeTestTrack(t, baseDir, recordingID, hostID, entity.RecordingArtifactKindAudioTrack, "host-audio.ogg", 900, "audio", "host-mic", "audio/opus"),
			makeTestTrack(t, baseDir, recordingID, hostID, entity.RecordingArtifactKindVideoTrack, "host-video.ivf", 800, "cam", "host-cam", "video/vp8"),
			makeTestTrack(t, baseDir, recordingID, viewerID, entity.RecordingArtifactKindAudioTrack, "viewer-audio.ogg", 300, "audio", "viewer-mic", "audio/opus"),
			makeTestTrack(t, baseDir, recordingID, viewerID, entity.RecordingArtifactKindVideoTrack, "viewer-video.ivf", 250, "cam", "viewer-cam", "video/vp8"),
		},
	}
	writeSpoolState(t, baseDir, recordingID, state)

	renderer := &fakeCompositeRenderer{}
	store := &memoryStorage{}
	processor := NewSpoolProcessor(baseDir, store, SpoolProcessorConfig{
		Calls: &fakeCallRepo{
			calls: map[uuid.UUID]*entity.Call{
				callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeWebinar, Title: "Quarterly Webinar"},
			},
			participants: map[[2]uuid.UUID]*entity.CallParticipant{
				{callID, hostID}:   {UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
				{callID, viewerID}: {UserID: viewerID, Role: entity.CallRoleViewer, Status: entity.ParticipantStatusConnected},
			},
		},
		Users: &fakeProcessorUserRepo{users: map[uuid.UUID]*entity.User{
			hostID:   {ID: hostID, DisplayName: "Host User"},
			viewerID: {ID: viewerID, DisplayName: "Viewer User"},
		}},
		Renderer:             renderer,
		CompositeFormat:      entity.RecordingFormatMP4,
		TranscriptSampleRate: 16000,
	})

	rec := entity.Recording{
		ID:          recordingID,
		WorkspaceID: workspaceID,
		CallID:      callID,
		StartedBy:   hostID,
		Strategy:    entity.RecordingStrategyBoth,
		StartedAt:   startedAt,
	}
	result, err := processor.Process(ctx, rec)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if renderer.lastComposite == nil || renderer.lastComposite.Layout != CompositeLayoutScreenShare {
		t.Fatalf("composite layout = %+v, want screen_share_first", renderer.lastComposite)
	}
	if result.Format != entity.RecordingFormatMP4 {
		t.Fatalf("result format = %s, want mp4", result.Format)
	}
	if got := storagePathByKind(result.Artifacts, entity.RecordingArtifactKindComposite); got == "" {
		t.Fatalf("expected composite artifact in %+v", result.Artifacts)
	}
	if got := storagePathByKind(result.Artifacts, entity.RecordingArtifactKindTranscript); got == "" {
		t.Fatalf("expected transcript artifact in %+v", result.Artifacts)
	}
	if got := storagePathByKind(result.Artifacts, entity.RecordingArtifactKindAIManifest); got == "" {
		t.Fatalf("expected ai manifest artifact in %+v", result.Artifacts)
	}
	if got := storagePathByKind(result.Artifacts, entity.RecordingArtifactKindSessionBundle); got == "" {
		t.Fatalf("expected bundle artifact in %+v", result.Artifacts)
	}

	aiManifestKey := storagePathByKind(result.Artifacts, entity.RecordingArtifactKindAIManifest)
	aiData := store.objects[aiManifestKey]
	if len(aiData) == 0 {
		t.Fatalf("ai manifest not uploaded to storage")
	}
	var aiManifest map[string]any
	if err := json.Unmarshal(aiData, &aiManifest); err != nil {
		t.Fatalf("unmarshal ai manifest: %v", err)
	}
	transcriptAudio := aiManifest["transcript_audio"].(map[string]any)
	if transcriptAudio["storage_path"] == "" {
		t.Fatalf("ai manifest transcript audio missing: %+v", aiManifest)
	}
	composite := aiManifest["composite"].(map[string]any)
	if composite["layout"] != string(CompositeLayoutScreenShare) {
		t.Fatalf("ai manifest layout = %v, want %s", composite["layout"], CompositeLayoutScreenShare)
	}
}

func TestSpoolProcessorUsesWebinarStageLayoutWithoutScreenShare(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	recordingID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	presenterID := uuid.New()
	startedAt := time.Now().UTC().Add(-time.Minute)
	stoppedAt := startedAt.Add(40 * time.Second)

	state := &SpoolState{
		RecordingID: recordingID,
		WorkspaceID: workspaceID,
		CallID:      callID,
		StartedBy:   hostID,
		Strategy:    entity.RecordingStrategyComposite,
		StartedAt:   startedAt,
		StoppedAt:   &stoppedAt,
		Tracks: []SpoolTrackState{
			makeTestTrack(t, baseDir, recordingID, hostID, entity.RecordingArtifactKindAudioTrack, "host-audio.ogg", 600, "audio", "host", "audio/opus"),
			makeTestTrack(t, baseDir, recordingID, hostID, entity.RecordingArtifactKindVideoTrack, "host-video.ivf", 700, "cam", "host", "video/vp8"),
			makeTestTrack(t, baseDir, recordingID, presenterID, entity.RecordingArtifactKindAudioTrack, "presenter-audio.ogg", 500, "audio", "presenter", "audio/opus"),
			makeTestTrack(t, baseDir, recordingID, presenterID, entity.RecordingArtifactKindVideoTrack, "presenter-video.ivf", 650, "cam", "presenter", "video/vp8"),
		},
	}
	writeSpoolState(t, baseDir, recordingID, state)

	renderer := &fakeCompositeRenderer{}
	processor := NewSpoolProcessor(baseDir, &memoryStorage{}, SpoolProcessorConfig{
		Calls: &fakeCallRepo{
			calls: map[uuid.UUID]*entity.Call{
				callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeWebinar},
			},
			participants: map[[2]uuid.UUID]*entity.CallParticipant{
				{callID, hostID}:      {UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
				{callID, presenterID}: {UserID: presenterID, Role: entity.CallRolePresenter, Status: entity.ParticipantStatusConnected},
			},
		},
		Renderer:        renderer,
		CompositeFormat: entity.RecordingFormatMP4,
	})

	_, err := processor.Process(ctx, entity.Recording{
		ID:          recordingID,
		WorkspaceID: workspaceID,
		CallID:      callID,
		StartedBy:   hostID,
		Strategy:    entity.RecordingStrategyComposite,
		StartedAt:   startedAt,
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if renderer.lastComposite == nil || renderer.lastComposite.Layout != CompositeLayoutWebinarStage {
		t.Fatalf("composite layout = %+v, want webinar_stage", renderer.lastComposite)
	}
}

func makeTestTrack(t *testing.T, baseDir string, recordingID, userID uuid.UUID, kind entity.RecordingArtifactKind, filename string, packetCount int64, trackID, streamID, mimeType string) SpoolTrackState {
	t.Helper()
	localPath := filepath.Join(baseDir, recordingID.String(), "tracks", filename)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		t.Fatalf("mkdir track dir: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(filename), 0o640); err != nil {
		t.Fatalf("write track file: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Minute)
	stoppedAt := startedAt.Add(30 * time.Second)
	return SpoolTrackState{
		Kind:         kind,
		SourceUserID: &userID,
		TrackID:      trackID,
		StreamID:     streamID,
		Codec:        mimeType,
		MimeType:     mimeType,
		Format:       formatForTestKind(kind),
		LocalPath:    localPath,
		FileName:     filename,
		PacketCount:  packetCount,
		StartedAt:    startedAt,
		StoppedAt:    &stoppedAt,
	}
}

func formatForTestKind(kind entity.RecordingArtifactKind) string {
	switch kind {
	case entity.RecordingArtifactKindAudioTrack:
		return "ogg"
	default:
		return "ivf"
	}
}

func writeSpoolState(t *testing.T, baseDir string, recordingID uuid.UUID, state *SpoolState) {
	t.Helper()
	path := filepath.Join(baseDir, recordingID.String(), "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir spool dir: %v", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal spool state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatalf("write spool state: %v", err)
	}
}

type fakeCompositeRenderer struct {
	lastComposite  *CompositeRenderSpec
	lastTranscript *TranscriptAudioSpec
}

func (f *fakeCompositeRenderer) RenderComposite(_ context.Context, spec CompositeRenderSpec) (*RenderedAsset, error) {
	copy := spec
	f.lastComposite = &copy
	path := filepath.Join(spec.WorkDir, "composite.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("composite"), 0o640); err != nil {
		return nil, err
	}
	return &RenderedAsset{
		LocalPath: path,
		FileName:  filepath.Base(path),
		Format:    "mp4",
		MimeType:  "video/mp4",
		Metadata: map[string]any{
			"layout": string(spec.Layout),
		},
	}, nil
}

func (f *fakeCompositeRenderer) ExtractTranscriptAudio(_ context.Context, spec TranscriptAudioSpec) (*RenderedAsset, error) {
	copy := spec
	f.lastTranscript = &copy
	path := filepath.Join(spec.WorkDir, "transcript_audio.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("transcript"), 0o640); err != nil {
		return nil, err
	}
	return &RenderedAsset{
		LocalPath: path,
		FileName:  filepath.Base(path),
		Format:    "wav",
		MimeType:  "audio/wav",
		Metadata: map[string]any{
			"sample_rate": spec.SampleRate,
		},
	}, nil
}

type fakeProcessorUserRepo struct {
	users map[uuid.UUID]*entity.User
}

func (r *fakeProcessorUserRepo) Create(context.Context, *entity.User) error { return nil }
func (r *fakeProcessorUserRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.User, error) {
	if user := r.users[id]; user != nil {
		return user, nil
	}
	return nil, entityNotFoundError()
}
func (r *fakeProcessorUserRepo) GetByEmail(context.Context, string) (*entity.User, error) {
	return nil, entityNotFoundError()
}
func (r *fakeProcessorUserRepo) Update(context.Context, *entity.User) error { return nil }

func entityNotFoundError() error {
	return cerrors.NotFound("user not found")
}
