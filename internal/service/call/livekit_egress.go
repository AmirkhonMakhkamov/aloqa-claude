package call

import (
	"context"
	"path"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/twitchtv/twirp"

	"aloqa/internal/pkg/cerrors"
)

// EgressClient drives LiveKit Egress to record a call room into a single
// composite MP4 (ALK-701). It replaces the dead Pion-SFU RTP capture path.
type EgressClient interface {
	// StartRoomCompositeFile begins a room-composite (grid) egress that writes
	// one MP4 to the deterministic storage key. It returns the LiveKit egress id
	// used to correlate egress_* webhooks back to the recording row.
	StartRoomCompositeFile(ctx context.Context, callID uuid.UUID, storageKey string) (string, error)
	// StopEgress stops an in-flight egress job. A not-found job is treated as
	// success so stop is idempotent.
	StopEgress(ctx context.Context, egressID string) error
}

// EgressEventPhase classifies a LiveKit egress_* webhook.
type EgressEventPhase int

const (
	EgressEventStarted EgressEventPhase = iota
	EgressEventUpdated
	EgressEventEnded
)

// EgressEventInfo is the call-package projection of a LiveKit EgressInfo webhook,
// handed to the recording finalizer (EgressWebhookSink) by the webhook bridge.
type EgressEventInfo struct {
	EgressID        string
	CallID          uuid.UUID
	Phase           EgressEventPhase
	Succeeded       bool // only meaningful when Phase == EgressEventEnded
	FileSizeBytes   int64
	DurationSeconds int
}

// EgressWebhookSink finalizes recordings from egress webhook events. Implemented
// by the recording service; the call service's webhook bridge invokes it. Kept
// in the call package (using only entity/uuid types) so the call service never
// imports the recording service.
type EgressWebhookSink interface {
	HandleEgressEvent(ctx context.Context, info EgressEventInfo) error
	// StopActiveEgressForCall best-effort stops a call's in-flight egress on room
	// close so the file finalizes promptly. No-op if there is no active recording
	// or its egress_id has not been backfilled yet.
	StopActiveEgressForCall(ctx context.Context, callID uuid.UUID) error
}

// NewEgressClient builds the LiveKit egress client, or returns nil when LiveKit
// or egress is not configured (so callers can treat nil as "recording disabled").
func NewEgressClient(livekit LiveKitSettings, egress EgressSettings) EgressClient {
	if !livekit.IsConfigured() || !egress.IsConfigured() {
		return nil
	}
	return newLiveKitEgressClient(livekit, egress)
}

// EgressS3Settings configures direct S3/MinIO upload from the egress worker
// (production output destination).
type EgressS3Settings struct {
	AccessKey      string
	Secret         string
	Region         string
	Endpoint       string
	Bucket         string
	ForcePathStyle bool
}

// EgressSettings configures the egress output destination.
type EgressSettings struct {
	Enabled bool
	// FileRoot is the directory INSIDE the egress container bind-mounted to the
	// API's MEDIA_STORAGE_PATH. Local egress writes <FileRoot>/<storageKey> and
	// the API's local storage resolves <storageKey> from the same volume. Used
	// when S3 is nil (dev default).
	FileRoot string
	// S3, when set, makes egress upload the file directly to S3/MinIO under
	// <storageKey> (production).
	S3 *EgressS3Settings
}

// IsConfigured reports whether egress recording is wired up.
func (s EgressSettings) IsConfigured() bool {
	return s.Enabled
}

type liveKitEgressClient struct {
	client *lksdk.EgressClient
	output EgressSettings
}

func newLiveKitEgressClient(livekit LiveKitSettings, egress EgressSettings) EgressClient {
	return &liveKitEgressClient{
		client: lksdk.NewEgressClient(livekit.URL, livekit.APIKey, livekit.APISecret),
		output: egress,
	}
}

// buildEncodedFileOutput maps the configured output destination to a LiveKit
// EncodedFileOutput. The storage key is identical to RecordingArtifact.StoragePath
// in both modes (§3.8 round-trip guarantee): S3 writes <bucket>/<key>; local
// writes <FileRoot>/<key> on the egress-API shared volume.
func buildEncodedFileOutput(out EgressSettings, storageKey string) *livekitpb.EncodedFileOutput {
	output := &livekitpb.EncodedFileOutput{FileType: livekitpb.EncodedFileType_MP4}
	if out.S3 != nil {
		output.Filepath = storageKey
		output.Output = &livekitpb.EncodedFileOutput_S3{S3: &livekitpb.S3Upload{
			AccessKey:      out.S3.AccessKey,
			Secret:         out.S3.Secret,
			Region:         out.S3.Region,
			Endpoint:       out.S3.Endpoint,
			Bucket:         out.S3.Bucket,
			ForcePathStyle: out.S3.ForcePathStyle,
		}}
		return output
	}
	output.Filepath = path.Join(out.FileRoot, storageKey)
	return output
}

func (c *liveKitEgressClient) StartRoomCompositeFile(ctx context.Context, callID uuid.UUID, storageKey string) (string, error) {
	info, err := c.client.StartRoomCompositeEgress(ctx, &livekitpb.RoomCompositeEgressRequest{
		RoomName:    callID.String(),
		Layout:      "grid",
		FileOutputs: []*livekitpb.EncodedFileOutput{buildEncodedFileOutput(c.output, storageKey)},
	})
	if err != nil {
		return "", mapTwirpErrorToAppError(err, "failed to start livekit room composite egress")
	}
	egressID := info.GetEgressId()
	if egressID == "" {
		return "", cerrors.Internal("livekit egress returned an empty egress id", nil)
	}
	return egressID, nil
}

func (c *liveKitEgressClient) StopEgress(ctx context.Context, egressID string) error {
	_, err := c.client.StopEgress(ctx, &livekitpb.StopEgressRequest{EgressId: egressID})
	if isTwirpCode(err, twirp.NotFound) {
		return nil
	}
	if err != nil {
		return mapTwirpErrorToAppError(err, "failed to stop livekit egress")
	}
	return nil
}
