package call

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/twitchtv/twirp"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

const (
	defaultLiveKitEmptyTimeout = 2 * time.Minute
)

// LiveKitRoomClient hides the LiveKit RoomService SDK behind a small service
// boundary so call lifecycle tests can use a deterministic fake.
type LiveKitRoomClient interface {
	EnsureRoom(ctx context.Context, args LiveKitEnsureRoomArgs) error
	DeleteRoom(ctx context.Context, callID uuid.UUID) error
	RemoveParticipant(ctx context.Context, callID, userID uuid.UUID) error
	ListParticipants(ctx context.Context, callID uuid.UUID) ([]*livekitpb.ParticipantInfo, error)
}

type LiveKitEnsureRoomArgs struct {
	CallID          uuid.UUID
	WorkspaceID     uuid.UUID
	CallType        entity.CallType
	MaxParticipants uint32
	EmptyTimeout    time.Duration
	Metadata        string
}

type liveKitRoomServiceClient struct {
	client *lksdk.RoomServiceClient
}

func newLiveKitRoomServiceClient(settings LiveKitSettings) LiveKitRoomClient {
	return &liveKitRoomServiceClient{
		client: lksdk.NewRoomServiceClient(settings.URL, settings.APIKey, settings.APISecret),
	}
}

// mapTwirpErrorToAppError converts a LiveKit RoomService twirp error into a
// typed *cerrors.AppError so HTTP handlers and FE classifiers can distinguish
// transient outage (503) from permanent misconfig (500) and bad input (400).
func mapTwirpErrorToAppError(err error, defaultMsg string) error {
	var twerr twirp.Error
	if !errors.As(err, &twerr) {
		return cerrors.Internal(defaultMsg, err)
	}
	msg := fmt.Sprintf("%s: %s (twirp:%s)", defaultMsg, twerr.Msg(), twerr.Code())
	switch twerr.Code() {
	case twirp.Unavailable, twirp.DeadlineExceeded:
		return cerrors.Unavailable(msg)
	case twirp.InvalidArgument:
		return cerrors.InvalidInput(msg)
	case twirp.Unauthenticated, twirp.PermissionDenied, twirp.Internal:
		return cerrors.Internal(msg, err)
	default:
		return cerrors.Internal(msg, err)
	}
}

func (c *liveKitRoomServiceClient) EnsureRoom(ctx context.Context, args LiveKitEnsureRoomArgs) error {
	emptyTimeout := uint32(args.EmptyTimeout.Seconds())
	if emptyTimeout == 0 {
		emptyTimeout = uint32(defaultLiveKitEmptyTimeout.Seconds())
	}

	_, err := c.client.CreateRoom(ctx, &livekitpb.CreateRoomRequest{
		Name:            args.CallID.String(),
		EmptyTimeout:    emptyTimeout,
		MaxParticipants: args.MaxParticipants,
		Metadata:        args.Metadata,
	})
	if err == nil {
		return nil
	}
	if !isTwirpCode(err, twirp.AlreadyExists) {
		return mapTwirpErrorToAppError(err, "failed to create livekit room")
	}

	_, updateErr := c.client.UpdateRoomMetadata(ctx, &livekitpb.UpdateRoomMetadataRequest{
		Room:     args.CallID.String(),
		Metadata: args.Metadata,
	})
	if updateErr != nil {
		return mapTwirpErrorToAppError(updateErr, "failed to update livekit room metadata")
	}
	return nil
}

func (c *liveKitRoomServiceClient) DeleteRoom(ctx context.Context, callID uuid.UUID) error {
	_, err := c.client.DeleteRoom(ctx, &livekitpb.DeleteRoomRequest{Room: callID.String()})
	if isTwirpCode(err, twirp.NotFound) {
		return nil
	}
	if err != nil {
		return mapTwirpErrorToAppError(err, "failed to delete livekit room")
	}
	return nil
}

func (c *liveKitRoomServiceClient) RemoveParticipant(ctx context.Context, callID, userID uuid.UUID) error {
	_, err := c.client.RemoveParticipant(ctx, &livekitpb.RoomParticipantIdentity{
		Room:     callID.String(),
		Identity: userID.String(),
	})
	if isTwirpCode(err, twirp.NotFound) {
		return nil
	}
	if err != nil {
		return mapTwirpErrorToAppError(err, "failed to remove livekit participant")
	}
	return nil
}

func (c *liveKitRoomServiceClient) ListParticipants(ctx context.Context, callID uuid.UUID) ([]*livekitpb.ParticipantInfo, error) {
	resp, err := c.client.ListParticipants(ctx, &livekitpb.ListParticipantsRequest{Room: callID.String()})
	if isTwirpCode(err, twirp.NotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, mapTwirpErrorToAppError(err, "failed to list livekit participants")
	}
	return resp.GetParticipants(), nil
}

func isTwirpCode(err error, code twirp.ErrorCode) bool {
	if err == nil {
		return false
	}
	var twerr twirp.Error
	return errors.As(err, &twerr) && twerr.Code() == code
}

func liveKitRoomMetadata(call *entity.Call) string {
	if call == nil {
		return "{}"
	}
	payload := struct {
		CallID      string          `json:"call_id"`
		CallType    entity.CallType `json:"call_type"`
		WorkspaceID string          `json:"workspace_id"`
	}{
		CallID:      call.ID.String(),
		CallType:    call.Type,
		WorkspaceID: call.WorkspaceID.String(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
