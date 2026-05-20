package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
)

func TestNewMessagePayloadEnrichesSavedChannel(t *testing.T) {
	workspaceID := uuid.New()
	sourceMessageID := uuid.New()
	message := newSavedPayloadMessage(t, sourceMessageID)
	channel := &entity.Channel{ID: message.ChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeSaved}

	payload := marshalMessagePayload(t, NewMessagePayload(message, channel))

	if payload["channel_type"] != string(entity.ChannelTypeSaved) {
		t.Fatalf("channel_type = %v, want %s", payload["channel_type"], entity.ChannelTypeSaved)
	}
	if payload["channel_workspace_id"] != workspaceID.String() {
		t.Fatalf("channel_workspace_id = %v, want %s", payload["channel_workspace_id"], workspaceID)
	}
	if payload["saved_from_message_id"] != sourceMessageID.String() {
		t.Fatalf("saved_from_message_id = %v, want %s", payload["saved_from_message_id"], sourceMessageID)
	}
}

func TestNewMessagePayloadEnrichesSavedGlobalChannelWithNullWorkspace(t *testing.T) {
	sourceMessageID := uuid.New()
	message := newSavedPayloadMessage(t, sourceMessageID)
	channel := &entity.Channel{ID: message.ChannelID, Type: entity.ChannelTypeSavedGlobal}

	payload := marshalMessagePayload(t, NewMessagePayload(message, channel))

	if payload["channel_type"] != string(entity.ChannelTypeSavedGlobal) {
		t.Fatalf("channel_type = %v, want %s", payload["channel_type"], entity.ChannelTypeSavedGlobal)
	}
	if value, ok := payload["channel_workspace_id"]; !ok || value != nil {
		t.Fatalf("channel_workspace_id = %v, want explicit null", value)
	}
	if payload["saved_from_message_id"] != sourceMessageID.String() {
		t.Fatalf("saved_from_message_id = %v, want %s", payload["saved_from_message_id"], sourceMessageID)
	}
}

func TestNewMessagePayloadOmitsSelfChannelFieldsForPublicChannel(t *testing.T) {
	workspaceID := uuid.New()
	message := newSavedPayloadMessage(t, uuid.New())
	channel := &entity.Channel{ID: message.ChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic}

	payload := marshalMessagePayload(t, NewMessagePayload(message, channel))

	if _, ok := payload["channel_type"]; ok {
		t.Fatalf("channel_type should be omitted for public channels")
	}
	if _, ok := payload["channel_workspace_id"]; ok {
		t.Fatalf("channel_workspace_id should be omitted for public channels")
	}
	if _, ok := payload["saved_from_message_id"]; ok {
		t.Fatalf("saved_from_message_id should be omitted for public channels")
	}
}

func newSavedPayloadMessage(t *testing.T, sourceMessageID uuid.UUID) *entity.Message {
	t.Helper()
	sourceChannelID := uuid.New()
	sourceUserID := uuid.New()
	createdAt := time.Now().UTC()
	savedFrom, err := json.Marshal(entity.SavedFrom{
		UserID:    sourceUserID,
		MessageID: sourceMessageID,
		ChannelID: sourceChannelID,
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("marshal saved_from: %v", err)
	}
	return &entity.Message{
		ID:        uuid.New(),
		ChannelID: uuid.New(),
		UserID:    uuid.New(),
		Content:   "saved",
		Type:      entity.MessageTypeText,
		SavedFrom: savedFrom,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func marshalMessagePayload(t *testing.T, payload MessagePayload) map[string]any {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}
