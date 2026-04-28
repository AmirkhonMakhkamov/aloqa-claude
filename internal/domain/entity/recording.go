package entity

import (
	"time"

	"github.com/google/uuid"
)

type RecordingStatus string

const (
	RecordingStatusRecording  RecordingStatus = "recording"
	RecordingStatusProcessing RecordingStatus = "processing"
	RecordingStatusReady      RecordingStatus = "ready"
	RecordingStatusFailed     RecordingStatus = "failed"
)

type RecordingStrategy string

const (
	RecordingStrategyComposite RecordingStrategy = "composite"
	RecordingStrategyPerTrack  RecordingStrategy = "per_track"
	RecordingStrategyBoth      RecordingStrategy = "both"
)

type RecordingFormat string

const (
	RecordingFormatWebM   RecordingFormat = "webm"
	RecordingFormatMP4    RecordingFormat = "mp4"
	RecordingFormatBundle RecordingFormat = "bundle"
)

type RecordingArtifactKind string

const (
	RecordingArtifactKindAudioTrack    RecordingArtifactKind = "audio_track"
	RecordingArtifactKindVideoTrack    RecordingArtifactKind = "video_track"
	RecordingArtifactKindScreenTrack   RecordingArtifactKind = "screen_track"
	RecordingArtifactKindComposite     RecordingArtifactKind = "composite_playback"
	RecordingArtifactKindTranscript    RecordingArtifactKind = "transcript_audio"
	RecordingArtifactKindManifest      RecordingArtifactKind = "manifest"
	RecordingArtifactKindAIManifest    RecordingArtifactKind = "ai_manifest"
	RecordingArtifactKindSessionBundle RecordingArtifactKind = "session_bundle"
)

type RecordingStorageTier string

const (
	RecordingStorageTierHot     RecordingStorageTier = "hot"
	RecordingStorageTierWarm    RecordingStorageTier = "warm"
	RecordingStorageTierArchive RecordingStorageTier = "archive"
)

// Recording represents a recorded call session. Recordings are stored in
// on-premise object storage with configurable retention policies.
type Recording struct {
	ID                    uuid.UUID            `json:"id"`
	CallID                uuid.UUID            `json:"call_id"`
	WorkspaceID           uuid.UUID            `json:"workspace_id"`
	StartedBy             uuid.UUID            `json:"started_by"`
	Strategy              RecordingStrategy    `json:"strategy"`
	Format                RecordingFormat      `json:"format"`
	Status                RecordingStatus      `json:"status"`
	Duration              *int                 `json:"duration,omitempty"`  // seconds
	FileSize              *int64               `json:"file_size,omitempty"` // bytes
	StoragePath           string               `json:"storage_path,omitempty"`
	StorageTier           RecordingStorageTier `json:"storage_tier"`
	StorageClass          string               `json:"storage_class,omitempty"`
	IntegritySHA256       string               `json:"integrity_sha256,omitempty"`
	Downloadable          bool                 `json:"downloadable"`
	ProcessingAttempts    int                  `json:"processing_attempts"`
	MaxProcessingAttempts int                  `json:"max_processing_attempts"`
	LastError             string               `json:"last_error,omitempty"`
	Metadata              map[string]any       `json:"metadata,omitempty"`
	LegalHold             bool                 `json:"legal_hold"`
	StartedAt             time.Time            `json:"started_at"`
	StoppedAt             *time.Time           `json:"stopped_at,omitempty"`
	ReadyAt               *time.Time           `json:"ready_at,omitempty"`
	NextRetryAt           *time.Time           `json:"next_retry_at,omitempty"`
	TierUpdatedAt         *time.Time           `json:"tier_updated_at,omitempty"`
	ExpiresAt             *time.Time           `json:"expires_at,omitempty"` // retention policy
	CreatedAt             time.Time            `json:"created_at"`
}

type RecordingArtifact struct {
	ID              uuid.UUID             `json:"id"`
	RecordingID     uuid.UUID             `json:"recording_id"`
	WorkspaceID     uuid.UUID             `json:"workspace_id"`
	Kind            RecordingArtifactKind `json:"kind"`
	SourceUserID    *uuid.UUID            `json:"source_user_id,omitempty"`
	TrackID         string                `json:"track_id,omitempty"`
	StreamID        string                `json:"stream_id,omitempty"`
	Layer           string                `json:"layer,omitempty"`
	Codec           string                `json:"codec,omitempty"`
	MimeType        string                `json:"mime_type,omitempty"`
	Format          string                `json:"format"`
	StoragePath     string                `json:"storage_path"`
	StorageTier     RecordingStorageTier  `json:"storage_tier"`
	StorageClass    string                `json:"storage_class,omitempty"`
	FileSize        int64                 `json:"file_size"`
	IntegritySHA256 string                `json:"integrity_sha256"`
	PacketCount     int64                 `json:"packet_count"`
	Duration        *int                  `json:"duration,omitempty"`
	Downloadable    bool                  `json:"downloadable"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
	TierUpdatedAt   *time.Time            `json:"tier_updated_at,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
}
