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
	DeleteRoomByName(ctx context.Context, name string) error
	RemoveParticipant(ctx context.Context, callID, userID uuid.UUID) error
	ListParticipants(ctx context.Context, callID uuid.UUID) ([]*livekitpb.ParticipantInfo, error)
}

type LiveKitEnsureRoomArgs struct {
	CallID uuid.UUID
	// RoomName overrides the LiveKit room name. When empty it falls back to
	// CallID.String() (the main-room convention). Breakout rooms pass the
	// "{callID}:breakout:{breakoutRoomID}" scheme here.
	RoomName        string
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
	// The detailed twirp text is kept on the wrapped cause (AppError.Err is
	// `json:"-"` — logged, never serialized to clients). Transient (503) and
	// bad-input (400) responses surface the detail in the client-facing Message
	// where it is actionable; auth / internal failures (500) return only the
	// generic defaultMsg so LiveKit admin-key and internal error details don't
	// leak to callers.
	detail := fmt.Errorf("%s (twirp:%s): %w", twerr.Msg(), twerr.Code(), err)
	msg := fmt.Sprintf("%s: %s (twirp:%s)", defaultMsg, twerr.Msg(), twerr.Code())
	switch twerr.Code() {
	case twirp.Unavailable, twirp.DeadlineExceeded:
		return cerrors.Unavailable(msg)
	case twirp.InvalidArgument:
		return cerrors.InvalidInput(msg)
	case twirp.Unauthenticated, twirp.PermissionDenied, twirp.Internal:
		return cerrors.Internal(defaultMsg, detail)
	default:
		return cerrors.Internal(defaultMsg, detail)
	}
}

func (c *liveKitRoomServiceClient) EnsureRoom(ctx context.Context, args LiveKitEnsureRoomArgs) error {
	emptyTimeout := uint32(args.EmptyTimeout.Seconds())
	if emptyTimeout == 0 {
		emptyTimeout = uint32(defaultLiveKitEmptyTimeout.Seconds())
	}

	name := args.RoomName
	if name == "" {
		name = args.CallID.String()
	}

	_, err := c.client.CreateRoom(ctx, &livekitpb.CreateRoomRequest{
		Name:            name,
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
		Room:     name,
		Metadata: args.Metadata,
	})
	if updateErr != nil {
		return mapTwirpErrorToAppError(updateErr, "failed to update livekit room metadata")
	}
	return nil
}

func (c *liveKitRoomServiceClient) DeleteRoom(ctx context.Context, callID uuid.UUID) error {
	return c.DeleteRoomByName(ctx, callID.String())
}

// DeleteRoomByName deletes a LiveKit room by its raw name. Used for breakout
// rooms whose names are not bare call UUIDs. NotFound is swallowed (idempotent).
func (c *liveKitRoomServiceClient) DeleteRoomByName(ctx context.Context, name string) error {
	_, err := c.client.DeleteRoom(ctx, &livekitpb.DeleteRoomRequest{Room: name})
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
