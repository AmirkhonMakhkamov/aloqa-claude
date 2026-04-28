package recording

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/platform/storage"
)

type ProcessedRecording struct {
	Format          entity.RecordingFormat
	StoragePath     string
	StorageTier     entity.RecordingStorageTier
	StorageClass    string
	FileSize        int64
	DurationSeconds int
	IntegritySHA256 string
	Downloadable    bool
	Metadata        map[string]any
	Artifacts       []entity.RecordingArtifact
}

type Processor interface {
	Process(ctx context.Context, rec entity.Recording) (*ProcessedRecording, error)
}

type SpoolProcessorConfig struct {
	Calls                 repository.CallRepository
	Users                 repository.UserRepository
	Renderer              CompositeRenderer
	FFmpegBinary          string
	CompositeFormat       entity.RecordingFormat
	CompositeWidth        int
	CompositeHeight       int
	CompositeVideoBitrate string
	CompositeAudioBitrate string
	TranscriptSampleRate  int
}

type SpoolProcessor struct {
	baseDir              string
	store                storage.Storage
	calls                repository.CallRepository
	users                repository.UserRepository
	renderer             CompositeRenderer
	compositeFormat      entity.RecordingFormat
	compositeWidth       int
	compositeHeight      int
	transcriptSampleRate int
}

type participantContext struct {
	UserID        uuid.UUID
	DisplayName   string
	Role          entity.CallRole
	Status        entity.ParticipantStatus
	AudioTracks   []SpoolTrackState
	VideoTracks   []SpoolTrackState
	ScreenTracks  []SpoolTrackState
	AudioPackets  int64
	VideoPackets  int64
	ScreenPackets int64
}

type localArtifactSpec struct {
	kind         entity.RecordingArtifactKind
	sourceUserID *uuid.UUID
	trackID      string
	streamID     string
	layer        string
	codec        string
	mimeType     string
	format       string
	localPath    string
	bundlePath   string
	storageKey   string
	downloadable bool
	packetCount  int64
	duration     *int
	metadata     map[string]any
}

type recordingContext struct {
	call         *entity.Call
	participants map[uuid.UUID]*participantContext
}

func NewSpoolProcessor(baseDir string, store storage.Storage, cfg SpoolProcessorConfig) *SpoolProcessor {
	format := cfg.CompositeFormat
	if format != entity.RecordingFormatMP4 && format != entity.RecordingFormatWebM {
		format = entity.RecordingFormatMP4
	}
	renderer := cfg.Renderer
	if renderer == nil {
		renderer = NewFFmpegRenderer(FFmpegRendererConfig{
			Binary:               cfg.FFmpegBinary,
			Width:                cfg.CompositeWidth,
			Height:               cfg.CompositeHeight,
			OutputFormat:         format,
			VideoBitrate:         cfg.CompositeVideoBitrate,
			AudioBitrate:         cfg.CompositeAudioBitrate,
			TranscriptSampleRate: cfg.TranscriptSampleRate,
		})
	}
	sampleRate := cfg.TranscriptSampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	width := cfg.CompositeWidth
	if width <= 0 {
		width = 1280
	}
	height := cfg.CompositeHeight
	if height <= 0 {
		height = 720
	}
	return &SpoolProcessor{
		baseDir:              baseDir,
		store:                store,
		calls:                cfg.Calls,
		users:                cfg.Users,
		renderer:             renderer,
		compositeFormat:      format,
		compositeWidth:       width,
		compositeHeight:      height,
		transcriptSampleRate: sampleRate,
	}
}

// Validate checks that all runtime dependencies (e.g. FFmpeg) are available.
// Call once at startup so the binary is discovered before any recording runs.
func (p *SpoolProcessor) Validate() error {
	type validator interface{ Validate() error }
	if v, ok := p.renderer.(validator); ok {
		return v.Validate()
	}
	return nil
}

func (p *SpoolProcessor) Process(ctx context.Context, rec entity.Recording) (_ *ProcessedRecording, err error) {
	if p.store == nil {
		return nil, fmt.Errorf("recording object storage is not configured")
	}
	state, err := LoadSpoolState(p.baseDir, rec.ID)
	if err != nil {
		return nil, fmt.Errorf("load recording spool state: %w", err)
	}
	if len(state.Tracks) == 0 {
		return nil, fmt.Errorf("recording has no captured tracks")
	}
	sort.SliceStable(state.Tracks, func(i, j int) bool {
		return state.Tracks[i].FileName < state.Tracks[j].FileName
	})

	ctxInfo := p.buildRecordingContext(ctx, rec, state)
	layout := selectCompositeLayout(ctxInfo, state)
	transcriptTracks := buildAudioInputs(ctxInfo)
	compositeTracks := buildVisualInputs(ctxInfo, layout)

	var generatedSpecs []localArtifactSpec
	var uploadedKeys []string
	defer func() {
		if err == nil {
			return
		}
		for _, key := range uploadedKeys {
			_ = p.store.Delete(context.Background(), key)
		}
	}()

	rawSpecs := p.buildRawTrackSpecs(rec, state, ctxInfo)

	if len(transcriptTracks) > 0 {
		transcriptAsset, renderErr := p.renderer.ExtractTranscriptAudio(ctx, TranscriptAudioSpec{
			WorkDir:     filepath.Join(p.baseDir, rec.ID.String(), "derived"),
			SampleRate:  p.transcriptSampleRate,
			AudioTracks: transcriptTracks,
		})
		if renderErr != nil {
			return nil, fmt.Errorf("extract transcript audio: %w", renderErr)
		}
		generatedSpecs = append(generatedSpecs, localArtifactSpec{
			kind:         entity.RecordingArtifactKindTranscript,
			mimeType:     transcriptAsset.MimeType,
			format:       transcriptAsset.Format,
			localPath:    transcriptAsset.LocalPath,
			bundlePath:   filepath.Join("derived", transcriptAsset.FileName),
			storageKey:   p.objectKey(rec, filepath.Join("derived", transcriptAsset.FileName)),
			downloadable: false,
			metadata:     transcriptAsset.Metadata,
		})
	}

	if rec.Strategy == entity.RecordingStrategyComposite || rec.Strategy == entity.RecordingStrategyBoth {
		compositeAsset, renderErr := p.renderer.RenderComposite(ctx, CompositeRenderSpec{
			WorkDir:      filepath.Join(p.baseDir, rec.ID.String(), "playback"),
			Layout:       layout,
			OutputFormat: p.compositeFormat,
			Width:        p.compositeWidth,
			Height:       p.compositeHeight,
			Title:        recordingTitle(ctxInfo, rec),
			VideoTracks:  compositeTracks,
			AudioTracks:  transcriptTracks,
		})
		if renderErr != nil {
			return nil, fmt.Errorf("render composite playback: %w", renderErr)
		}
		generatedSpecs = append(generatedSpecs, localArtifactSpec{
			kind:         entity.RecordingArtifactKindComposite,
			mimeType:     compositeAsset.MimeType,
			format:       compositeAsset.Format,
			localPath:    compositeAsset.LocalPath,
			bundlePath:   filepath.Join("playback", compositeAsset.FileName),
			storageKey:   p.objectKey(rec, filepath.Join("playback", compositeAsset.FileName)),
			downloadable: true,
			metadata: mergeMaps(compositeAsset.Metadata, map[string]any{
				"layout": string(layout),
			}),
		})
	}

	combined := append([]localArtifactSpec{}, rawSpecs...)
	combined = append(combined, generatedSpecs...)

	manifestData, aiManifestData, err := p.buildManifestPayload(rec, state, ctxInfo, layout, combined)
	if err != nil {
		return nil, fmt.Errorf("build recording manifests: %w", err)
	}

	manifestSpec, err := p.writeJSONArtifact(rec, "manifests/recording_manifest.json", entity.RecordingArtifactKindManifest, manifestData, "application/json")
	if err != nil {
		return nil, err
	}
	aiManifestSpec, err := p.writeJSONArtifact(rec, "manifests/ai_manifest.json", entity.RecordingArtifactKindAIManifest, aiManifestData, "application/json")
	if err != nil {
		return nil, err
	}
	combined = append(combined, manifestSpec, aiManifestSpec)

	bundleSpec, err := p.writeBundleArtifact(rec, combined)
	if err != nil {
		return nil, err
	}
	combined = append(combined, bundleSpec)

	artifacts := make([]entity.RecordingArtifact, 0, len(combined))
	now := time.Now().UTC()
	for _, spec := range combined {
		artifact, uploadErr := p.uploadArtifact(ctx, rec, spec, now)
		if uploadErr != nil {
			return nil, uploadErr
		}
		uploadedKeys = append(uploadedKeys, artifact.StoragePath)
		artifacts = append(artifacts, artifact)
	}

	mainArtifact := selectPrimaryArtifact(rec.Strategy, artifacts)
	if mainArtifact == nil {
		return nil, fmt.Errorf("recording processing produced no primary artifact")
	}
	duration := recordingDurationSeconds(state)
	return &ProcessedRecording{
		Format:          primaryRecordingFormat(mainArtifact),
		StoragePath:     mainArtifact.StoragePath,
		StorageTier:     entity.RecordingStorageTierHot,
		StorageClass:    defaultRecordingStorageClass(p.store),
		FileSize:        mainArtifact.FileSize,
		DurationSeconds: duration,
		IntegritySHA256: mainArtifact.IntegritySHA256,
		Downloadable:    mainArtifact.Downloadable,
		Metadata: map[string]any{
			"strategy":                 rec.Strategy,
			"call_type":                callTypeValue(ctxInfo),
			"composite_layout":         string(layout),
			"artifact_count":           len(artifacts),
			"manifest_storage":         manifestSpec.storageKey,
			"ai_manifest_storage":      aiManifestSpec.storageKey,
			"archive_bundle_storage":   bundleSpec.storageKey,
			"composite_storage":        storagePathByKind(artifacts, entity.RecordingArtifactKindComposite),
			"transcript_audio_storage": storagePathByKind(artifacts, entity.RecordingArtifactKindTranscript),
			"per_track_captured":       true,
		},
		Artifacts: artifacts,
	}, nil
}

func (p *SpoolProcessor) buildRecordingContext(ctx context.Context, rec entity.Recording, state *SpoolState) recordingContext {
	ctxInfo := recordingContext{participants: map[uuid.UUID]*participantContext{}}
	if p.calls != nil {
		if call, err := p.calls.GetByID(ctx, rec.CallID); err == nil {
			ctxInfo.call = call
		}
		if participants, err := p.calls.ListParticipants(ctx, rec.CallID); err == nil {
			for _, participant := range participants {
				profile := ensureParticipantContext(ctxInfo.participants, participant.UserID)
				profile.Role = participant.Role
				profile.Status = participant.Status
			}
		}
	}
	for _, track := range state.Tracks {
		if track.SourceUserID == nil {
			continue
		}
		profile := ensureParticipantContext(ctxInfo.participants, *track.SourceUserID)
		switch track.Kind {
		case entity.RecordingArtifactKindAudioTrack:
			profile.AudioTracks = append(profile.AudioTracks, track)
			profile.AudioPackets += track.PacketCount
		case entity.RecordingArtifactKindScreenTrack:
			profile.ScreenTracks = append(profile.ScreenTracks, track)
			profile.ScreenPackets += track.PacketCount
		default:
			profile.VideoTracks = append(profile.VideoTracks, track)
			profile.VideoPackets += track.PacketCount
		}
	}
	if p.users != nil {
		for userID, profile := range ctxInfo.participants {
			if user, err := p.users.GetByID(ctx, userID); err == nil {
				profile.DisplayName = user.DisplayName
			}
		}
	}
	return ctxInfo
}

func ensureParticipantContext(participants map[uuid.UUID]*participantContext, userID uuid.UUID) *participantContext {
	if profile := participants[userID]; profile != nil {
		return profile
	}
	profile := &participantContext{UserID: userID, DisplayName: userID.String(), Role: entity.CallRoleParticipant}
	participants[userID] = profile
	return profile
}

func (p *SpoolProcessor) buildRawTrackSpecs(rec entity.Recording, state *SpoolState, ctxInfo recordingContext) []localArtifactSpec {
	specs := make([]localArtifactSpec, 0, len(state.Tracks))
	allowRawDownload := rec.Strategy == entity.RecordingStrategyPerTrack || rec.Strategy == entity.RecordingStrategyBoth
	for _, track := range state.Tracks {
		metadata := map[string]any{
			"local_file": track.FileName,
		}
		if track.SourceUserID != nil {
			if profile := ctxInfo.participants[*track.SourceUserID]; profile != nil {
				metadata["display_name"] = profile.DisplayName
				metadata["role"] = string(profile.Role)
			}
		}
		specs = append(specs, localArtifactSpec{
			kind:         track.Kind,
			sourceUserID: track.SourceUserID,
			trackID:      track.TrackID,
			streamID:     track.StreamID,
			layer:        track.Layer,
			codec:        track.Codec,
			mimeType:     track.MimeType,
			format:       track.Format,
			localPath:    track.LocalPath,
			bundlePath:   filepath.Join("tracks", track.FileName),
			storageKey:   p.objectKey(rec, filepath.Join("tracks", track.FileName)),
			downloadable: allowRawDownload,
			packetCount:  track.PacketCount,
			metadata:     metadata,
		})
	}
	return specs
}

func (p *SpoolProcessor) buildManifestPayload(rec entity.Recording, state *SpoolState, ctxInfo recordingContext, layout CompositeLayout, artifacts []localArtifactSpec) ([]byte, []byte, error) {
	participants := make([]map[string]any, 0, len(ctxInfo.participants))
	ids := make([]uuid.UUID, 0, len(ctxInfo.participants))
	for userID := range ctxInfo.participants {
		ids = append(ids, userID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, userID := range ids {
		profile := ctxInfo.participants[userID]
		participants = append(participants, map[string]any{
			"user_id":        userID,
			"display_name":   profile.DisplayName,
			"role":           profile.Role,
			"status":         profile.Status,
			"audio_tracks":   len(profile.AudioTracks),
			"video_tracks":   len(profile.VideoTracks),
			"screen_tracks":  len(profile.ScreenTracks),
			"audio_packets":  profile.AudioPackets,
			"video_packets":  profile.VideoPackets,
			"screen_packets": profile.ScreenPackets,
		})
	}

	artifactSummaries := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifactSummaries = append(artifactSummaries, map[string]any{
			"kind":         artifact.kind,
			"format":       artifact.format,
			"mime_type":    artifact.mimeType,
			"storage_path": artifact.storageKey,
			"downloadable": artifact.downloadable,
			"bundle_path":  artifact.bundlePath,
			"track_id":     artifact.trackID,
			"stream_id":    artifact.streamID,
			"layer":        artifact.layer,
			"codec":        artifact.codec,
			"metadata":     artifact.metadata,
		})
	}

	recordingManifest := map[string]any{
		"schema_version": 2,
		"recording": map[string]any{
			"id":         rec.ID,
			"workspace":  rec.WorkspaceID,
			"call_id":    rec.CallID,
			"strategy":   rec.Strategy,
			"started_by": rec.StartedBy,
			"started_at": rec.StartedAt,
			"stopped_at": state.StoppedAt,
			"duration":   recordingDurationSeconds(state),
		},
		"call": map[string]any{
			"type":  callTypeValue(ctxInfo),
			"title": recordingTitle(ctxInfo, rec),
		},
		"playback": map[string]any{
			"layout":               string(layout),
			"primary_artifact":     primaryStoragePathFromSpecs(rec.Strategy, artifacts),
			"screen_share_present": hasTrackKind(state.Tracks, entity.RecordingArtifactKindScreenTrack),
		},
		"participants": participants,
		"tracks":       state.Tracks,
		"artifacts":    artifactSummaries,
	}
	aiManifest := map[string]any{
		"schema_version": 1,
		"ingest": map[string]any{
			"recording_id": rec.ID,
			"workspace_id": rec.WorkspaceID,
			"call_id":      rec.CallID,
			"call_type":    callTypeValue(ctxInfo),
			"title":        recordingTitle(ctxInfo, rec),
			"layout":       string(layout),
			"started_at":   rec.StartedAt,
			"stopped_at":   state.StoppedAt,
			"duration":     recordingDurationSeconds(state),
		},
		"transcript_audio": map[string]any{
			"storage_path": storagePathByKindSpec(artifacts, entity.RecordingArtifactKindTranscript),
			"sample_rate":  p.transcriptSampleRate,
			"channels":     1,
			"mix":          "mono_amix",
		},
		"composite": map[string]any{
			"storage_path": primaryStoragePathFromSpecs(rec.Strategy, artifacts),
			"layout":       string(layout),
		},
		"participants": participants,
		"raw_tracks":   artifactSummariesByKinds(artifacts, entity.RecordingArtifactKindAudioTrack, entity.RecordingArtifactKindVideoTrack, entity.RecordingArtifactKindScreenTrack),
	}
	recordingData, err := json.MarshalIndent(recordingManifest, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	aiData, err := json.MarshalIndent(aiManifest, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return recordingData, aiData, nil
}

func (p *SpoolProcessor) writeJSONArtifact(rec entity.Recording, relativePath string, kind entity.RecordingArtifactKind, data []byte, mimeType string) (localArtifactSpec, error) {
	localPath := filepath.Join(p.baseDir, rec.ID.String(), relativePath)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return localArtifactSpec{}, fmt.Errorf("create manifest dir: %w", err)
	}
	if err := os.WriteFile(localPath, data, 0o640); err != nil {
		return localArtifactSpec{}, fmt.Errorf("write %s: %w", kind, err)
	}
	return localArtifactSpec{
		kind:         kind,
		mimeType:     mimeType,
		format:       "json",
		localPath:    localPath,
		bundlePath:   relativePath,
		storageKey:   p.objectKey(rec, relativePath),
		downloadable: false,
	}, nil
}

func (p *SpoolProcessor) writeBundleArtifact(rec entity.Recording, artifacts []localArtifactSpec) (localArtifactSpec, error) {
	relative := "bundle.zip"
	localPath := filepath.Join(p.baseDir, rec.ID.String(), relative)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return localArtifactSpec{}, fmt.Errorf("create bundle dir: %w", err)
	}
	file, err := os.Create(localPath)
	if err != nil {
		return localArtifactSpec{}, fmt.Errorf("create recording bundle: %w", err)
	}
	zw := zip.NewWriter(file)
	for _, artifact := range artifacts {
		if artifact.localPath == "" || artifact.kind == entity.RecordingArtifactKindSessionBundle {
			continue
		}
		if err := addFileToZip(zw, artifact.bundlePath, artifact.localPath); err != nil {
			_ = zw.Close()
			_ = file.Close()
			return localArtifactSpec{}, err
		}
	}
	if err := zw.Close(); err != nil {
		_ = file.Close()
		return localArtifactSpec{}, fmt.Errorf("close recording bundle zip: %w", err)
	}
	if err := file.Close(); err != nil {
		return localArtifactSpec{}, fmt.Errorf("close recording bundle file: %w", err)
	}
	return localArtifactSpec{
		kind:         entity.RecordingArtifactKindSessionBundle,
		mimeType:     "application/zip",
		format:       "zip",
		localPath:    localPath,
		bundlePath:   relative,
		storageKey:   p.objectKey(rec, relative),
		downloadable: rec.Strategy != entity.RecordingStrategyPerTrack,
		metadata: map[string]any{
			"artifact_count": len(artifacts),
		},
	}, nil
}

func (p *SpoolProcessor) uploadArtifact(ctx context.Context, rec entity.Recording, spec localArtifactSpec, createdAt time.Time) (entity.RecordingArtifact, error) {
	info, err := buildArtifactInfo(spec.localPath)
	if err != nil {
		return entity.RecordingArtifact{}, fmt.Errorf("inspect artifact %s: %w", spec.localPath, err)
	}
	if err := p.uploadFile(ctx, spec.storageKey, spec.localPath, spec.mimeType); err != nil {
		return entity.RecordingArtifact{}, fmt.Errorf("upload artifact %s: %w", spec.storageKey, err)
	}
	return entity.RecordingArtifact{
		ID:              uuid.New(),
		RecordingID:     rec.ID,
		WorkspaceID:     rec.WorkspaceID,
		Kind:            spec.kind,
		SourceUserID:    spec.sourceUserID,
		TrackID:         spec.trackID,
		StreamID:        spec.streamID,
		Layer:           spec.layer,
		Codec:           spec.codec,
		MimeType:        spec.mimeType,
		Format:          spec.format,
		StoragePath:     spec.storageKey,
		StorageTier:     entity.RecordingStorageTierHot,
		StorageClass:    defaultRecordingStorageClass(p.store),
		FileSize:        info.Size,
		IntegritySHA256: info.SHA256,
		PacketCount:     spec.packetCount,
		Duration:        spec.duration,
		Downloadable:    spec.downloadable,
		Metadata:        spec.metadata,
		CreatedAt:       createdAt,
	}, nil
}

func buildAudioInputs(ctxInfo recordingContext) []CompositeTrackInput {
	var inputs []CompositeTrackInput
	for userID, profile := range ctxInfo.participants {
		for _, track := range profile.AudioTracks {
			inputs = append(inputs, CompositeTrackInput{
				LocalPath:   track.LocalPath,
				UserID:      userID.String(),
				DisplayName: profile.DisplayName,
				Role:        profile.Role,
				Kind:        track.Kind,
				PacketCount: track.PacketCount,
			})
		}
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		if inputs[i].PacketCount == inputs[j].PacketCount {
			return inputs[i].UserID < inputs[j].UserID
		}
		return inputs[i].PacketCount > inputs[j].PacketCount
	})
	return inputs
}

func buildVisualInputs(ctxInfo recordingContext, layout CompositeLayout) []CompositeTrackInput {
	var inputs []CompositeTrackInput
	switch layout {
	case CompositeLayoutScreenShare:
		screen := topTracksByKind(ctxInfo, entity.RecordingArtifactKindScreenTrack)
		video := topTracksByKind(ctxInfo, entity.RecordingArtifactKindVideoTrack)
		if len(screen) > 0 {
			inputs = append(inputs, screen[0])
		}
		inputs = append(inputs, video...)
	case CompositeLayoutWebinarStage:
		inputs = topWebinarStageTracks(ctxInfo)
	default:
		inputs = topActiveSpeakerTracks(ctxInfo)
	}
	if len(inputs) == 0 {
		inputs = topTracksByKind(ctxInfo, entity.RecordingArtifactKindVideoTrack)
	}
	if len(inputs) > 5 {
		inputs = inputs[:5]
	}
	return inputs
}

func topTracksByKind(ctxInfo recordingContext, kind entity.RecordingArtifactKind) []CompositeTrackInput {
	var inputs []CompositeTrackInput
	for userID, profile := range ctxInfo.participants {
		var selected *SpoolTrackState
		switch kind {
		case entity.RecordingArtifactKindScreenTrack:
			selected = highestPacketTrack(profile.ScreenTracks)
		default:
			selected = highestPacketTrack(profile.VideoTracks)
		}
		if selected == nil {
			continue
		}
		inputs = append(inputs, CompositeTrackInput{
			LocalPath:   selected.LocalPath,
			UserID:      userID.String(),
			DisplayName: profile.DisplayName,
			Role:        profile.Role,
			Kind:        selected.Kind,
			PacketCount: selected.PacketCount,
		})
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		if inputs[i].PacketCount == inputs[j].PacketCount {
			return inputs[i].UserID < inputs[j].UserID
		}
		return inputs[i].PacketCount > inputs[j].PacketCount
	})
	return inputs
}

func topActiveSpeakerTracks(ctxInfo recordingContext) []CompositeTrackInput {
	videos := topTracksByKind(ctxInfo, entity.RecordingArtifactKindVideoTrack)
	if len(videos) == 0 {
		return nil
	}
	audioByUser := map[string]int64{}
	for _, input := range buildAudioInputs(ctxInfo) {
		audioByUser[input.UserID] += input.PacketCount
	}
	sort.SliceStable(videos, func(i, j int) bool {
		if audioByUser[videos[i].UserID] == audioByUser[videos[j].UserID] {
			return videos[i].PacketCount > videos[j].PacketCount
		}
		return audioByUser[videos[i].UserID] > audioByUser[videos[j].UserID]
	})
	return videos
}

func topWebinarStageTracks(ctxInfo recordingContext) []CompositeTrackInput {
	videos := topTracksByKind(ctxInfo, entity.RecordingArtifactKindVideoTrack)
	sort.SliceStable(videos, func(i, j int) bool {
		roleI := webinarRolePriority(videos[i].Role)
		roleJ := webinarRolePriority(videos[j].Role)
		if roleI == roleJ {
			return videos[i].PacketCount > videos[j].PacketCount
		}
		return roleI < roleJ
	})
	return videos
}

func webinarRolePriority(role entity.CallRole) int {
	switch role {
	case entity.CallRoleHost:
		return 0
	case entity.CallRoleCoHost:
		return 1
	case entity.CallRolePresenter:
		return 2
	case entity.CallRoleParticipant:
		return 3
	default:
		return 4
	}
}

func highestPacketTrack(tracks []SpoolTrackState) *SpoolTrackState {
	if len(tracks) == 0 {
		return nil
	}
	best := tracks[0]
	for _, track := range tracks[1:] {
		if track.PacketCount > best.PacketCount {
			best = track
		}
	}
	return &best
}

func selectCompositeLayout(ctxInfo recordingContext, state *SpoolState) CompositeLayout {
	if hasTrackKind(state.Tracks, entity.RecordingArtifactKindScreenTrack) {
		return CompositeLayoutScreenShare
	}
	if ctxInfo.call != nil && (ctxInfo.call.Type == entity.CallTypeWebinar || ctxInfo.call.Type == entity.CallTypeSelector) {
		return CompositeLayoutWebinarStage
	}
	return CompositeLayoutActiveSpeaker
}

func hasTrackKind(tracks []SpoolTrackState, kind entity.RecordingArtifactKind) bool {
	for _, track := range tracks {
		if track.Kind == kind {
			return true
		}
	}
	return false
}

func recordingDurationSeconds(state *SpoolState) int {
	if state == nil {
		return 0
	}
	end := state.StartedAt
	if state.StoppedAt != nil {
		end = *state.StoppedAt
	}
	for _, track := range state.Tracks {
		if track.StoppedAt != nil && track.StoppedAt.After(end) {
			end = *track.StoppedAt
		}
	}
	duration := int(end.Sub(state.StartedAt).Seconds())
	if duration < 0 {
		return 0
	}
	return duration
}

func primaryRecordingFormat(artifact *entity.RecordingArtifact) entity.RecordingFormat {
	if artifact == nil {
		return entity.RecordingFormatBundle
	}
	switch artifact.Kind {
	case entity.RecordingArtifactKindComposite:
		if strings.EqualFold(artifact.Format, "webm") {
			return entity.RecordingFormatWebM
		}
		return entity.RecordingFormatMP4
	default:
		return entity.RecordingFormatBundle
	}
}

func selectPrimaryArtifact(strategy entity.RecordingStrategy, artifacts []entity.RecordingArtifact) *entity.RecordingArtifact {
	if strategy == entity.RecordingStrategyComposite || strategy == entity.RecordingStrategyBoth {
		for i := range artifacts {
			if artifacts[i].Kind == entity.RecordingArtifactKindComposite {
				return &artifacts[i]
			}
		}
	}
	for i := range artifacts {
		if artifacts[i].Kind == entity.RecordingArtifactKindSessionBundle {
			return &artifacts[i]
		}
	}
	if len(artifacts) == 0 {
		return nil
	}
	return &artifacts[0]
}

func storagePathByKind(artifacts []entity.RecordingArtifact, kind entity.RecordingArtifactKind) string {
	for _, artifact := range artifacts {
		if artifact.Kind == kind {
			return artifact.StoragePath
		}
	}
	return ""
}

func storagePathByKindSpec(artifacts []localArtifactSpec, kind entity.RecordingArtifactKind) string {
	for _, artifact := range artifacts {
		if artifact.kind == kind {
			return artifact.storageKey
		}
	}
	return ""
}

func primaryStoragePathFromSpecs(strategy entity.RecordingStrategy, artifacts []localArtifactSpec) string {
	if strategy == entity.RecordingStrategyComposite || strategy == entity.RecordingStrategyBoth {
		if key := storagePathByKindSpec(artifacts, entity.RecordingArtifactKindComposite); key != "" {
			return key
		}
	}
	return storagePathByKindSpec(artifacts, entity.RecordingArtifactKindSessionBundle)
}

func artifactSummariesByKinds(artifacts []localArtifactSpec, kinds ...entity.RecordingArtifactKind) []map[string]any {
	set := map[entity.RecordingArtifactKind]struct{}{}
	for _, kind := range kinds {
		set[kind] = struct{}{}
	}
	out := make([]map[string]any, 0)
	for _, artifact := range artifacts {
		if _, ok := set[artifact.kind]; !ok {
			continue
		}
		out = append(out, map[string]any{
			"kind":         artifact.kind,
			"storage_path": artifact.storageKey,
			"bundle_path":  artifact.bundlePath,
			"track_id":     artifact.trackID,
			"stream_id":    artifact.streamID,
			"codec":        artifact.codec,
			"format":       artifact.format,
			"mime_type":    artifact.mimeType,
			"metadata":     artifact.metadata,
		})
	}
	return out
}

func defaultRecordingStorageClass(store storage.Storage) string {
	tieredStore, ok := store.(storage.TieredStorage)
	if !ok || tieredStore == nil {
		return ""
	}
	return tieredStore.DefaultStorageClass(storage.ObjectTierHot)
}

func recordingTitle(ctxInfo recordingContext, rec entity.Recording) string {
	if ctxInfo.call != nil && strings.TrimSpace(ctxInfo.call.Title) != "" {
		return ctxInfo.call.Title
	}
	return rec.ID.String()
}

func callTypeValue(ctxInfo recordingContext) string {
	if ctxInfo.call == nil {
		return ""
	}
	return string(ctxInfo.call.Type)
}

func buildArtifactInfo(path string) (*fileArtifactInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return nil, err
	}
	return &fileArtifactInfo{Size: size, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

type fileArtifactInfo struct {
	Size   int64
	SHA256 string
}

func (p *SpoolProcessor) uploadFile(ctx context.Context, key, localPath, mimeType string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	return p.store.Put(ctx, key, file, info.Size(), mimeType)
}

func addFileToZip(zw *zip.Writer, name, localPath string) error {
	entry, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create zip file entry %s: %w", name, err)
	}
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local file %s: %w", localPath, err)
	}
	defer file.Close()
	if _, err := io.Copy(entry, file); err != nil {
		return fmt.Errorf("copy zip file entry %s: %w", name, err)
	}
	return nil
}

func (p *SpoolProcessor) objectKey(rec entity.Recording, name string) string {
	name = strings.TrimPrefix(name, "/")
	return fmt.Sprintf("recordings/%s/%s/%s", rec.WorkspaceID.String(), rec.ID.String(), name)
}

func mergeMaps(base map[string]any, extras map[string]any) map[string]any {
	if len(base) == 0 && len(extras) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(extras))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extras {
		out[key] = value
	}
	return out
}
