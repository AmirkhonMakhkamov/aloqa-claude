package call

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/twitchtv/twirp"

	"aloqa/internal/domain/entity"
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
		return err
	}

	_, updateErr := c.client.UpdateRoomMetadata(ctx, &livekitpb.UpdateRoomMetadataRequest{
		Room:     args.CallID.String(),
		Metadata: args.Metadata,
	})
	return updateErr
}

func (c *liveKitRoomServiceClient) DeleteRoom(ctx context.Context, callID uuid.UUID) error {
	_, err := c.client.DeleteRoom(ctx, &livekitpb.DeleteRoomRequest{Room: callID.String()})
	if isTwirpCode(err, twirp.NotFound) {
		return nil
	}
	return err
}

func (c *liveKitRoomServiceClient) RemoveParticipant(ctx context.Context, callID, userID uuid.UUID) error {
	_, err := c.client.RemoveParticipant(ctx, &livekitpb.RoomParticipantIdentity{
		Room:     callID.String(),
		Identity: userID.String(),
	})
	if isTwirpCode(err, twirp.NotFound) {
		return nil
	}
	return err
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
