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

func TestMessagePostProfileShareBuildsCardPayload(t *testing.T) {
	f := newMessageHTTPFixture()
	f.channels.channels[f.channelID].Type = entity.ChannelTypeDM
	targetID := uuid.New()
	position := "Product Manager"
	department := "Product"
	f.workspaces.members[[2]uuid.UUID{f.workspaceID, targetID}] = &entity.WorkspaceMember{
		WorkspaceID: f.workspaceID,
		UserID:      targetID,
		Role:        entity.WorkspaceRoleAdmin,
		User: &entity.User{
			ID:          targetID,
			DisplayName: "Madina Karimova",
			AvatarURL:   "https://cdn.test/madina.png",
			AvatarColor: "#0EA5E9",
			Position:    &position,
			Department:  &department,
			Status:      entity.UserStatusActive,
		},
	}

	res := f.serve(
		http.MethodPost,
		"/channels/"+f.channelID.String()+"/messages",
		`{"content":"","profile_share":{"user_id":"`+targetID.String()+`"}}`,
	)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
	}

	var msg entity.Message
	if err := json.Unmarshal(res.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msg.ProfileShare == nil {
		t.Fatalf("profile_share = nil, want value")
	}
	if msg.ProfileShare.UserID != targetID || msg.ProfileShare.WorkspaceID != f.workspaceID {
		t.Fatalf("profile_share ids = %s/%s, want %s/%s", msg.ProfileShare.UserID, msg.ProfileShare.WorkspaceID, targetID, f.workspaceID)
	}
	if msg.ProfileShare.Snapshot.DisplayName != "Madina Karimova" ||
		msg.ProfileShare.Snapshot.AvatarURL != "https://cdn.test/madina.png" ||
		msg.ProfileShare.Snapshot.AvatarColor != "#0EA5E9" ||
		msg.ProfileShare.Snapshot.Role != entity.WorkspaceRoleAdmin {
		t.Fatalf("profile_share snapshot = %+v, want hydrated target profile", msg.ProfileShare.Snapshot)
	}
	if msg.ProfileShare.Snapshot.Position == nil || *msg.ProfileShare.Snapshot.Position != position {
		t.Fatalf("profile_share position = %v, want %q", msg.ProfileShare.Snapshot.Position, position)
	}
	if msg.ProfileShare.Snapshot.Department == nil || *msg.ProfileShare.Snapshot.Department != department {
		t.Fatalf("profile_share department = %v, want %q", msg.ProfileShare.Snapshot.Department, department)
	}

	if len(f.publisher.events) == 0 {
		t.Fatalf("expected message.created event")
	}
	var envelope struct {
		Payload struct {
			Message entity.Message `json:"message"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(f.publisher.events[0], &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Payload.Message.ProfileShare == nil || envelope.Payload.Message.ProfileShare.UserID != targetID {
		t.Fatalf("event profile_share = %+v, want target profile", envelope.Payload.Message.ProfileShare)
	}
}

func TestMessagePostProfileShareRejectsInvalidUserID(t *testing.T) {
	f := newMessageHTTPFixture()
	f.channels.channels[f.channelID].Type = entity.ChannelTypeDM

	res := f.serve(
		http.MethodPost,
		"/channels/"+f.channelID.String()+"/messages",
		`{"content":"","profile_share":{"user_id":"not-a-uuid"}}`,
	)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "invalid profile_share.user_id") {
		t.Fatalf("body = %s, want invalid profile_share.user_id error", res.Body.String())
	}
}

func TestMessagePostEchoesClientMessageID(t *testing.T) {
	f := newMessageHTTPFixture()
	clientID := "019e7300-0000-7000-8000-000000000440"
	res := f.serve(
		http.MethodPost,
		"/channels/"+f.channelID.String()+"/messages",
		`{"content":"hi","client_message_id":"`+clientID+`"}`,
	)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
	}

	var msg entity.Message
	if err := json.Unmarshal(res.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msg.ClientMessageID == nil || *msg.ClientMessageID != clientID {
		t.Fatalf("response client_message_id = %v, want %s", msg.ClientMessageID, clientID)
	}

	// The message.created event must also carry it so the client can dedup by id.
	if len(f.publisher.events) == 0 {
		t.Fatalf("expected at least one published event")
	}
	var envelope struct {
		Payload struct {
			Message entity.Message `json:"message"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(f.publisher.events[0], &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Payload.Message.ClientMessageID == nil ||
		*envelope.Payload.Message.ClientMessageID != clientID {
		t.Fatalf(
			"event client_message_id = %v, want %s",
			envelope.Payload.Message.ClientMessageID,
			clientID,
		)
	}
}

func TestMessagePostEchoesIdempotencyKeyHeaderWhenNoBodyField(t *testing.T) {
	f := newMessageHTTPFixture()
	clientID := "019e7300-0000-7000-8000-0000004400ff"
	// No client_message_id body field — the handler falls back to the
	// Idempotency-Key header the client already sends (ALK-440).
	req := httptest.NewRequest(
		http.MethodPost,
		"/channels/"+f.channelID.String()+"/messages",
		strings.NewReader(`{"content":"hi"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", clientID)
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
	}
	var msg entity.Message
	if err := json.Unmarshal(res.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msg.ClientMessageID == nil || *msg.ClientMessageID != clientID {
		t.Fatalf(
			"client_message_id = %v, want %s (from Idempotency-Key header)",
			msg.ClientMessageID,
			clientID,
		)
	}
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

func TestParsePaginationAcceptsBeforeAlias(t *testing.T) {
	cursorID := uuid.New()
	cursor := pagination.EncodeCursor(cursorID)
	req := httptest.NewRequest(http.MethodGet, "/messages?before="+cursor+"&limit=7", nil)

	got := parsePagination(req)

	if got.Cursor != cursorID {
		t.Fatalf("cursor = %s, want %s", got.Cursor, cursorID)
	}
	if got.Limit != 7 {
		t.Fatalf("limit = %d, want 7", got.Limit)
	}
}

func TestMessageAddReactionReturnsReactionAndPublishesSchema(t *testing.T) {
	f := newMessageHTTPFixture()
	msg := &entity.Message{
		ID:        uuid.New(),
		ChannelID: f.channelID,
		UserID:    f.userID,
		Content:   "hello",
		Type:      entity.MessageTypeText,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	f.messages.messages[msg.ID] = msg

	res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages/"+msg.ID.String()+"/reactions", `{"emoji":"👍"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
	}

	var reaction entity.Reaction
	if err := json.Unmarshal(res.Body.Bytes(), &reaction); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if reaction.ID == uuid.Nil || reaction.MessageID != msg.ID || reaction.UserID != f.userID || reaction.Emoji != "👍" || reaction.CreatedAt.IsZero() {
		t.Fatalf("reaction = %+v, want full reaction object", reaction)
	}

	// reaction.added now fans out to both the channel and workspace subjects (ALK-654).
	if len(f.publisher.events) != 2 {
		t.Fatalf("published events = %d, want 2 (channel + workspace) reaction.added", len(f.publisher.events))
	}
	var envelope struct {
		Type    eventpkg.Type   `json:"type"`
		Payload entity.Reaction `json:"payload"`
	}
	if err := json.Unmarshal(f.publisher.events[0], &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Type != eventpkg.TypeReactionAdded {
		t.Fatalf("event type = %s, want %s", envelope.Type, eventpkg.TypeReactionAdded)
	}
	if envelope.Payload.ID != reaction.ID || envelope.Payload.CreatedAt.IsZero() {
		t.Fatalf("event payload = %+v, want full reaction", envelope.Payload)
	}
}

func TestMessageAddReactionDuplicateReturnsExistingReaction(t *testing.T) {
	f := newMessageHTTPFixture()
	msg := &entity.Message{
		ID:        uuid.New(),
		ChannelID: f.channelID,
		UserID:    f.userID,
		Content:   "hello",
		Type:      entity.MessageTypeText,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	f.messages.messages[msg.ID] = msg

	path := "/channels/" + f.channelID.String() + "/messages/" + msg.ID.String() + "/reactions"
	first := f.serve(http.MethodPost, path, `{"emoji":"👍"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201; body=%s", first.Code, first.Body.String())
	}

	var created entity.Reaction
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	second := f.serve(http.MethodPost, path, `{"emoji":"👍"}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("second status = %d, want 201; body=%s", second.Code, second.Body.String())
	}

	var existing entity.Reaction
	if err := json.Unmarshal(second.Body.Bytes(), &existing); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if existing.ID != created.ID || existing.CreatedAt.IsZero() {
		t.Fatalf("duplicate reaction = %+v, want existing reaction %+v", existing, created)
	}
	// Initial add fans out to channel + workspace (2); the duplicate publishes
	// nothing, so the total stays 2 (ALK-654).
	if len(f.publisher.events) != 2 {
		t.Fatalf("published events = %d, want 2 for the initial reaction only", len(f.publisher.events))
	}
}

func TestMessageListIncludesPersistedReactions(t *testing.T) {
	f := newMessageHTTPFixture()
	msg := &entity.Message{
		ID:        uuid.New(),
		ChannelID: f.channelID,
		UserID:    f.userID,
		Content:   "hello",
		Type:      entity.MessageTypeText,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	reaction := entity.Reaction{
		ID:        uuid.New(),
		MessageID: msg.ID,
		UserID:    f.userID,
		Emoji:     "👍",
		CreatedAt: time.Now().UTC(),
	}
	f.messages.messages[msg.ID] = msg
	f.messages.reactions[reaction.ID] = reaction

	res := f.serve(http.MethodGet, "/channels/"+f.channelID.String()+"/messages", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}

	var page pagination.Page[entity.Message]
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(page.Items))
	}
	if len(page.Items[0].Reactions) != 1 || page.Items[0].Reactions[0].ID != reaction.ID {
		t.Fatalf("reactions = %+v, want persisted reaction %+v", page.Items[0].Reactions, reaction)
	}
}

func TestMessageRemoveReactionByID(t *testing.T) {
	f := newMessageHTTPFixture()
	msg := &entity.Message{
		ID:        uuid.New(),
		ChannelID: f.channelID,
		UserID:    f.userID,
		Content:   "hello",
		Type:      entity.MessageTypeText,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	reaction := entity.Reaction{
		ID:        uuid.New(),
		MessageID: msg.ID,
		UserID:    f.userID,
		Emoji:     "👍",
		CreatedAt: time.Now().UTC(),
	}
	f.messages.messages[msg.ID] = msg
	f.messages.reactions[reaction.ID] = reaction

	res := f.serve(http.MethodDelete, "/reactions/"+reaction.ID.String(), "")
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", res.Code, res.Body.String())
	}
	if _, ok := f.messages.reactions[reaction.ID]; ok {
		t.Fatalf("reaction %s still exists after delete", reaction.ID)
	}
	// reaction.removed now fans out to both the channel and workspace subjects (ALK-654).
	if len(f.publisher.events) != 2 {
		t.Fatalf("published events = %d, want 2 (channel + workspace) reaction.removed", len(f.publisher.events))
	}
	var envelope struct {
		Type    eventpkg.Type   `json:"type"`
		Payload entity.Reaction `json:"payload"`
	}
	if err := json.Unmarshal(f.publisher.events[0], &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Type != eventpkg.TypeReactionRemoved {
		t.Fatalf("event type = %s, want %s", envelope.Type, eventpkg.TypeReactionRemoved)
	}
	if envelope.Payload.ID != reaction.ID || envelope.Payload.CreatedAt.IsZero() {
		t.Fatalf("event payload = %+v, want deleted reaction", envelope.Payload)
	}
}

func TestMessagePostQuoteFieldsCases(t *testing.T) {
	t.Run("quote fields echo and omit deleted", func(t *testing.T) {
		f := newMessageHTTPFixture()
		quotedMessageID := uuid.New()
		quotedUserID := uuid.New()
		parentMessageID := uuid.New()
		createdAt := time.Now().UTC()
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", quotePostBody(t, "reply", quotedMessageID, quotedUserID, createdAt, &parentMessageID, "quoted text"))
		if res.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		var gotQuotedMessageID uuid.UUID
		if err := json.Unmarshal(body["quoted_message_id"], &gotQuotedMessageID); err != nil {
			t.Fatalf("decode quoted_message_id: %v", err)
		}
		if gotQuotedMessageID != quotedMessageID {
			t.Fatalf("quoted_message_id = %s, want %s", gotQuotedMessageID, quotedMessageID)
		}
		var snapshot map[string]json.RawMessage
		if err := json.Unmarshal(body["quoted_snapshot"], &snapshot); err != nil {
			t.Fatalf("decode quoted_snapshot: %v", err)
		}
		if _, ok := snapshot["deleted"]; ok {
			t.Fatalf("quoted_snapshot.deleted present in response: %s", body["quoted_snapshot"])
		}
		if string(snapshot["content_excerpt"]) != `"quoted text"` {
			t.Fatalf("content_excerpt = %s, want quoted text", snapshot["content_excerpt"])
		}
	})

	t.Run("partial quote fields rejected", func(t *testing.T) {
		f := newMessageHTTPFixture()
		body, err := json.Marshal(map[string]any{
			"content":           "reply",
			"quoted_message_id": uuid.New().String(),
		})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", string(body))
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "INVALID_INPUT") {
			t.Fatalf("body = %s, want INVALID_INPUT", res.Body.String())
		}
	})

	t.Run("deleted spoof rejected at decode boundary", func(t *testing.T) {
		f := newMessageHTTPFixture()
		body := `{"content":"reply","quoted_message_id":"` + uuid.New().String() + `","quoted_snapshot":{"user_id":"` + f.userID.String() + `","content_excerpt":"quoted","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `","deleted":true}}`
		res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", body)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
		}
		var envelope errorBody
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode error envelope: %v", err)
		}
		if envelope.Error.Code != string(cerrors.CodeInvalidInput) {
			t.Fatalf("error code = %s, want INVALID_INPUT", envelope.Error.Code)
		}
		if len(f.messages.messages) != 0 {
			t.Fatalf("messages inserted = %d, want 0", len(f.messages.messages))
		}
	})
}

func TestMessageGetIncludesQuoteFieldsAfterSend(t *testing.T) {
	f := newMessageHTTPFixture()
	quotedMessageID := uuid.New()
	quotedUserID := uuid.New()
	createdAt := time.Now().UTC()
	post := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", quotePostBody(t, "reply", quotedMessageID, quotedUserID, createdAt, nil, "quoted text"))
	if post.Code != http.StatusCreated {
		t.Fatalf("post status = %d, want 201; body=%s", post.Code, post.Body.String())
	}
	var created entity.Message
	if err := json.Unmarshal(post.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created message: %v", err)
	}
	res := f.serve(http.MethodGet, "/messages/"+created.ID.String(), "")
	if res.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	var got entity.Message
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.QuotedMessageID == nil || *got.QuotedMessageID != quotedMessageID {
		t.Fatalf("quoted_message_id = %v, want %s", got.QuotedMessageID, quotedMessageID)
	}
	if got.QuotedSnapshot == nil || got.QuotedSnapshot.ContentExcerpt != "quoted text" {
		t.Fatalf("quoted_snapshot = %+v, want quoted text", got.QuotedSnapshot)
	}
}

func TestMessageCreatedEventIncludesQuoteFields(t *testing.T) {
	f := newMessageHTTPFixture()
	quotedMessageID := uuid.New()
	quotedUserID := uuid.New()
	createdAt := time.Now().UTC()
	res := f.serve(http.MethodPost, "/channels/"+f.channelID.String()+"/messages", quotePostBody(t, "reply", quotedMessageID, quotedUserID, createdAt, nil, "quoted text"))
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
	if envelope.Payload.Message.QuotedMessageID == nil || *envelope.Payload.Message.QuotedMessageID != quotedMessageID {
		t.Fatalf("event quoted_message_id = %v, want %s", envelope.Payload.Message.QuotedMessageID, quotedMessageID)
	}
	if envelope.Payload.Message.QuotedSnapshot == nil || envelope.Payload.Message.QuotedSnapshot.ContentExcerpt != "quoted text" {
		t.Fatalf("event quoted_snapshot = %+v, want quoted text", envelope.Payload.Message.QuotedSnapshot)
	}
}

func TestMessageDeletePublishesCascadeQuoteUpdatedEvents(t *testing.T) {
	f := newMessageHTTPFixture()
	sourceID := uuid.New()
	now := time.Now().UTC()
	f.messages.messages[sourceID] = &entity.Message{
		ID:        sourceID,
		ChannelID: f.channelID,
		UserID:    f.userID,
		Content:   "source",
		CreatedAt: now,
		UpdatedAt: now,
	}
	quotedIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range quotedIDs {
		f.messages.messages[id] = &entity.Message{
			ID:              id,
			ChannelID:       f.channelID,
			UserID:          f.userID,
			Content:         "quote",
			QuotedMessageID: &sourceID,
			QuotedSnapshot: &entity.QuotedSnapshot{
				UserID:         f.userID,
				ContentExcerpt: "source",
				CreatedAt:      now,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	res := f.serve(http.MethodDelete, "/messages/"+sourceID.String(), "")
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", res.Code, res.Body.String())
	}

	updated := map[uuid.UUID]entity.Message{}
	for _, raw := range f.publisher.events {
		var envelope struct {
			Type    eventpkg.Type `json:"type"`
			Payload struct {
				Message entity.Message `json:"message"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if envelope.Type == eventpkg.TypeMessageUpdated {
			updated[envelope.Payload.Message.ID] = envelope.Payload.Message
		}
	}
	if len(updated) != len(quotedIDs) {
		t.Fatalf("message.updated events = %d, want %d", len(updated), len(quotedIDs))
	}
	for _, id := range quotedIDs {
		msg, ok := updated[id]
		if !ok {
			t.Fatalf("missing message.updated for %s", id)
		}
		if msg.QuotedSnapshot == nil || msg.QuotedSnapshot.Deleted == nil || !*msg.QuotedSnapshot.Deleted {
			t.Fatalf("quoted_snapshot.deleted for %s = %+v, want true", id, msg.QuotedSnapshot)
		}
	}
}

type messageHTTPFixture struct {
	workspaceID uuid.UUID
	channelID   uuid.UUID
	userID      uuid.UUID
	channels    *messageHTTPChannelRepo
	workspaces  *fakeHTTPWorkspaceRepo
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
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeHTTPWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &messageHTTPMessageRepo{
		messages:  map[uuid.UUID]*entity.Message{},
		reactions: map[uuid.UUID]entity.Reaction{},
	}
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
	router.Get("/channels/{channelID}/messages", handler.List)
	router.Post("/channels/{channelID}/messages/{messageID}/reactions", handler.AddReaction)
	router.Get("/messages/{messageID}", handler.Get)
	router.Delete("/messages/{messageID}", handler.Delete)
	router.Delete("/reactions/{reactionID}", handler.RemoveReactionByID)

	return messageHTTPFixture{
		workspaceID: workspaceID,
		channelID:   channelID,
		userID:      userID,
		channels:    channels,
		workspaces:  workspaces,
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
func (r *messageHTTPChannelRepo) ListArchivedByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
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
	messages  map[uuid.UUID]*entity.Message
	reactions map[uuid.UUID]entity.Reaction
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
func (r *messageHTTPMessageRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	msg := r.messages[id]
	if msg == nil {
		return cerrors.NotFound("message not found")
	}
	now := time.Now().UTC()
	msg.Content = ""
	msg.Edited = false
	msg.EditedAt = nil
	msg.Pinned = false
	msg.PinnedBy = nil
	msg.PinnedAt = nil
	msg.ForwardedFrom = nil
	msg.QuotedMessageID = nil
	msg.QuotedSnapshot = nil
	msg.ProfileShare = nil
	msg.UpdatedAt = now
	msg.DeletedAt = &now
	return nil
}
func (r *messageHTTPMessageRepo) SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	if err := r.SoftDelete(ctx, id); err != nil {
		return nil, err
	}
	deleted := true
	var affected []uuid.UUID
	for _, msg := range r.messages {
		if msg.QuotedMessageID == nil || *msg.QuotedMessageID != id {
			continue
		}
		if msg.QuotedSnapshot == nil {
			msg.QuotedSnapshot = &entity.QuotedSnapshot{}
		}
		msg.QuotedSnapshot.Deleted = &deleted
		msg.UpdatedAt = time.Now().UTC()
		affected = append(affected, msg.ID)
	}
	return affected, nil
}
func (r *messageHTTPMessageRepo) Pin(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *messageHTTPMessageRepo) Unpin(context.Context, uuid.UUID) error { return nil }
func (r *messageHTTPMessageRepo) ListPinned(context.Context, uuid.UUID) ([]entity.Message, error) {
	return nil, nil
}
func (r *messageHTTPMessageRepo) AddReaction(_ context.Context, reaction *entity.Reaction) error {
	if r.reactions == nil {
		r.reactions = map[uuid.UUID]entity.Reaction{}
	}
	for _, existing := range r.reactions {
		if existing.MessageID == reaction.MessageID && existing.UserID == reaction.UserID && existing.Emoji == reaction.Emoji {
			return cerrors.AlreadyExists("reaction already exists")
		}
	}
	r.reactions[reaction.ID] = *reaction
	return nil
}
func (r *messageHTTPMessageRepo) GetReactionByID(_ context.Context, id uuid.UUID) (*entity.Reaction, error) {
	if reaction, ok := r.reactions[id]; ok {
		return &reaction, nil
	}
	return nil, cerrors.NotFound("reaction not found")
}
func (r *messageHTTPMessageRepo) GetReactionByMessageUserEmoji(_ context.Context, messageID, userID uuid.UUID, emoji string) (*entity.Reaction, error) {
	for _, reaction := range r.reactions {
		if reaction.MessageID == messageID && reaction.UserID == userID && reaction.Emoji == emoji {
			return &reaction, nil
		}
	}
	return nil, cerrors.NotFound("reaction not found")
}
func (r *messageHTTPMessageRepo) RemoveReaction(_ context.Context, messageID, userID uuid.UUID, emoji string) error {
	for id, reaction := range r.reactions {
		if reaction.MessageID == messageID && reaction.UserID == userID && reaction.Emoji == emoji {
			delete(r.reactions, id)
			return nil
		}
	}
	return cerrors.NotFound("reaction not found")
}
func (r *messageHTTPMessageRepo) RemoveReactionByID(_ context.Context, id uuid.UUID) error {
	if _, ok := r.reactions[id]; !ok {
		return cerrors.NotFound("reaction not found")
	}
	delete(r.reactions, id)
	return nil
}
func (r *messageHTTPMessageRepo) ListReactions(_ context.Context, messageID uuid.UUID) ([]entity.Reaction, error) {
	var reactions []entity.Reaction
	for _, reaction := range r.reactions {
		if reaction.MessageID == messageID {
			reactions = append(reactions, reaction)
		}
	}
	return reactions, nil
}
func (r *messageHTTPMessageRepo) ListReactionsByMessageIDs(_ context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]entity.Reaction, error) {
	messageIDSet := make(map[uuid.UUID]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		messageIDSet[messageID] = struct{}{}
	}

	reactionsByMessageID := make(map[uuid.UUID][]entity.Reaction, len(messageIDs))
	for _, reaction := range r.reactions {
		if _, ok := messageIDSet[reaction.MessageID]; ok {
			reactionsByMessageID[reaction.MessageID] = append(reactionsByMessageID[reaction.MessageID], reaction)
		}
	}
	return reactionsByMessageID, nil
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

func quotePostBody(t *testing.T, content string, quotedMessageID, quotedUserID uuid.UUID, createdAt time.Time, parentMessageID *uuid.UUID, excerpt string) string {
	t.Helper()
	snapshot := map[string]any{
		"user_id":         quotedUserID.String(),
		"content_excerpt": excerpt,
		"created_at":      createdAt.Format(time.RFC3339),
	}
	if parentMessageID != nil {
		snapshot["parent_message_id"] = parentMessageID.String()
	}
	body, err := json.Marshal(map[string]any{
		"content":           content,
		"quoted_message_id": quotedMessageID.String(),
		"quoted_snapshot":   snapshot,
	})
	if err != nil {
		t.Fatalf("marshal quote body: %v", err)
	}
	return string(body)
}
