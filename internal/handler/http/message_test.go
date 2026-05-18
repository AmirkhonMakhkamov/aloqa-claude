package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	eventpkg "aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	chatsvc "aloqa/internal/service/chat"
)

func TestMessagePostForwardedFromCases(t *testing.T) {
	forwardedFrom := json.RawMessage(`{"message_id":"m1","snapshot":{"content":"original","attachments":[]}}`)

	t.Run("forward with content echoes forwarded_from", func(t *testing.T) {
		f := newMessageHTTPFixture()
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", `{"content":"hi","forwarded_from":`+string(forwardedFrom)+`}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !jsonEqual(body["forwarded_from"], forwardedFrom) {
			t.Fatalf("forwarded_from = %s, want %s", body["forwarded_from"], forwardedFrom)
		}
	})

	t.Run("empty content without forwarded_from rejected", func(t *testing.T) {
		f := newMessageHTTPFixture()
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", `{"content":""}`)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "content is required") {
			t.Fatalf("body = %s, want content required error", res.Body.String())
		}
	})

	t.Run("empty content with forwarded_from accepted", func(t *testing.T) {
		f := newMessageHTTPFixture()
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", `{"content":"","forwarded_from":`+string(forwardedFrom)+`}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
		}
		var msg entity.Message
		if err := json.Unmarshal(res.Body.Bytes(), &msg); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if msg.Content != "" || !jsonEqual(msg.ForwardedFrom, forwardedFrom) {
			t.Fatalf("message = %+v, want empty content with forwarded_from", msg)
		}
	})

	t.Run("legacy content omits forwarded_from", func(t *testing.T) {
		f := newMessageHTTPFixture()
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", `{"content":"hi"}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if _, ok := body["forwarded_from"]; ok {
			t.Fatalf("response included forwarded_from for legacy message: %s", body["forwarded_from"])
		}
	})
}

func TestMessageGetIncludesForwardedFrom(t *testing.T) {
	f := newMessageHTTPFixture()
	forwardedFrom := json.RawMessage(`{"message_id":"m1","snapshot":{"content":"original"}}`)
	msg := &entity.Message{
		ID:            uuid.New(),
		ChannelID:     f.channelID,
		UserID:        f.userID,
		Content:       "forward",
		Type:          entity.MessageTypeText,
		ForwardedFrom: forwardedFrom,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	f.messages.messages[msg.ID] = msg

	res := f.serve(http.MethodGet, "/messages/"+msg.ID.String(), "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !jsonEqual(body["forwarded_from"], forwardedFrom) {
		t.Fatalf("forwarded_from = %s, want %s", body["forwarded_from"], forwardedFrom)
	}
}

func TestMessageCreatedEventIncludesForwardedFrom(t *testing.T) {
	f := newMessageHTTPFixture()
	forwardedFrom := json.RawMessage(`{"message_id":"m1","snapshot":{"content":"original"}}`)
	res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", `{"content":"hi","forwarded_from":`+string(forwardedFrom)+`}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
	}
	if len(f.publisher.events) != 2 {
		t.Fatalf("published events = %d, want channel + workspace message.created events", len(f.publisher.events))
	}

	var envelope struct {
		Type    eventpkg.Type `json:"type"`
		Payload struct {
			Message entity.Message `json:"message"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(f.publisher.events[0], &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Type != eventpkg.TypeMessageCreated {
		t.Fatalf("event type = %s, want %s", envelope.Type, eventpkg.TypeMessageCreated)
	}
	if !jsonEqual(envelope.Payload.Message.ForwardedFrom, forwardedFrom) {
		t.Fatalf("event forwarded_from = %s, want %s", envelope.Payload.Message.ForwardedFrom, forwardedFrom)
	}
}

type messageHTTPFixture struct {
	workspaceID uuid.UUID
	channelID   uuid.UUID
	userID      uuid.UUID
	messages    *messageHTTPMessageRepo
	publisher   *messageHTTPPublisher
	router      *chi.Mux
}

func newMessageHTTPFixture() messageHTTPFixture {
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	channels := &messageHTTPChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeHTTPWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &messageHTTPMessageRepo{messages: map[uuid.UUID]*entity.Message{}}
	publisher := &messageHTTPPublisher{}
	svc := chatsvc.NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)
	handler := NewMessageHandler(svc)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.WorkspaceIDKey, workspaceID)
			ctx = context.WithValue(ctx, middleware.UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Post("/channels/{channelID}/messages", handler.Send)
	router.Get("/messages/{messageID}", handler.Get)

	return messageHTTPFixture{
		workspaceID: workspaceID,
		channelID:   channelID,
		userID:      userID,
		messages:    messages,
		publisher:   publisher,
		router:      router,
	}
}

func (f messageHTTPFixture) serve(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	return res
}

type messageHTTPPublisher struct {
	events [][]byte
}

func (p *messageHTTPPublisher) Publish(_ context.Context, _ string, data []byte) error {
	p.events = append(p.events, append([]byte(nil), data...))
	return nil
}

type messageHTTPChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
}

func (r *messageHTTPChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (r *messageHTTPChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if ch := r.channels[id]; ch != nil {
		return ch, nil
	}
	return nil, cerrors.NotFound("channel not found")
}
func (r *messageHTTPChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}
func (r *messageHTTPChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (r *messageHTTPChannelRepo) Update(context.Context, *entity.Channel) error { return nil }
func (r *messageHTTPChannelRepo) Archive(context.Context, uuid.UUID) error      { return nil }
func (r *messageHTTPChannelRepo) AddMember(context.Context, *entity.ChannelMember) error {
	return nil
}
func (r *messageHTTPChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("channel member not found")
}
func (r *messageHTTPChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *messageHTTPChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *messageHTTPChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *messageHTTPChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

type messageHTTPMessageRepo struct {
	messages map[uuid.UUID]*entity.Message
}

func (r *messageHTTPMessageRepo) Create(_ context.Context, msg *entity.Message) error {
	copy := *msg
	r.messages[msg.ID] = &copy
	return nil
}
func (r *messageHTTPMessageRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Message, error) {
	if msg := r.messages[id]; msg != nil {
		copy := *msg
		return &copy, nil
	}
	return nil, cerrors.NotFound("message not found")
}
func (r *messageHTTPMessageRepo) ListByChannel(_ context.Context, channelID uuid.UUID, _ pagination.Params) ([]entity.Message, error) {
	var messages []entity.Message
	for _, msg := range r.messages {
		if msg.ChannelID == channelID {
			messages = append(messages, *msg)
		}
	}
	return messages, nil
}
func (r *messageHTTPMessageRepo) ListThreadReplies(context.Context, uuid.UUID, pagination.Params) ([]entity.Message, error) {
	return nil, nil
}
func (r *messageHTTPMessageRepo) HasActiveMessage(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}
func (r *messageHTTPMessageRepo) Update(context.Context, *entity.Message) error { return nil }
func (r *messageHTTPMessageRepo) SoftDelete(context.Context, uuid.UUID) error   { return nil }
func (r *messageHTTPMessageRepo) Pin(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *messageHTTPMessageRepo) Unpin(context.Context, uuid.UUID) error { return nil }
func (r *messageHTTPMessageRepo) ListPinned(context.Context, uuid.UUID) ([]entity.Message, error) {
	return nil, nil
}
func (r *messageHTTPMessageRepo) AddReaction(context.Context, *entity.Reaction) error { return nil }
func (r *messageHTTPMessageRepo) RemoveReaction(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (r *messageHTTPMessageRepo) ListReactions(context.Context, uuid.UUID) ([]entity.Reaction, error) {
	return nil, nil
}
func (r *messageHTTPMessageRepo) CreateAttachment(context.Context, *entity.Attachment) error {
	return nil
}
func (r *messageHTTPMessageRepo) DeleteAttachment(context.Context, uuid.UUID) error { return nil }
func (r *messageHTTPMessageRepo) GetAttachmentByStoragePath(context.Context, string) (*entity.Attachment, error) {
	return nil, cerrors.NotFound("attachment not found")
}
func (r *messageHTTPMessageRepo) ListAttachments(context.Context, uuid.UUID) ([]entity.Attachment, error) {
	return nil, nil
}
func (r *messageHTTPMessageRepo) CountUnread(context.Context, uuid.UUID, uuid.UUID, time.Time) (int, error) {
	return 0, nil
}
func (r *messageHTTPMessageRepo) BatchUnreadCounts(context.Context, uuid.UUID, uuid.UUID) ([]repository.UnreadSummary, error) {
	return nil, nil
}
func (r *messageHTTPMessageRepo) CountThreadReplies(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}

func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return jsonValueEqual(av, bv)
}

func jsonValueEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
