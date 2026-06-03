package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/service/chat"
)

type MessageHandler struct {
	svc *chat.Service
}

func NewMessageHandler(svc *chat.Service) *MessageHandler {
	return &MessageHandler{svc: svc}
}

type sendMessageRequest struct {
	Content         string                    `json:"content"`
	ParentID        *string                   `json:"parent_id,omitempty"`
	ForwardedFrom   *json.RawMessage          `json:"forwarded_from,omitempty"`
	QuotedMessageID *string                   `json:"quoted_message_id,omitempty"`
	QuotedSnapshot  *chat.QuotedSnapshotInput `json:"quoted_snapshot,omitempty"`
	ProfileShare    *profileShareRequest      `json:"profile_share,omitempty"`
	// Optional client-generated id (the optimistic message id). Echoed back on
	// the message.created event so the client can dedup by exact id (ALK-440).
	ClientMessageID *string `json:"client_message_id,omitempty"`
}

type profileShareRequest struct {
	UserID string `json:"user_id"`
}

func (p *profileShareRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	p.UserID = wire.UserID
	return nil
}

func (h *MessageHandler) Send(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req sendMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	var parentID *uuid.UUID
	if req.ParentID != nil {
		parsed, err := id.Parse(*req.ParentID)
		if err != nil {
			writeErr(w, err)
			return
		}
		parentID = &parsed
	}

	var quotedMessageID *uuid.UUID
	if req.QuotedMessageID != nil {
		parsed, err := uuid.Parse(*req.QuotedMessageID)
		if err != nil {
			writeErr(w, cerrors.InvalidInput("invalid quoted_message_id"))
			return
		}
		quotedMessageID = &parsed
	}

	quotedSnapshot, err := parseQuotedSnapshotInput(req.QuotedSnapshot)
	if err != nil {
		writeErr(w, err)
		return
	}
	profileShare, err := parseProfileShareInput(req.ProfileShare)
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	// The client's optimistic message id is the Idempotency-Key it already
	// sends; fall back to it when no explicit client_message_id body field is
	// present, so the created-message echo carries it for exact-id dedup
	// (ALK-440) without requiring a body change.
	clientMessageID := req.ClientMessageID
	if clientMessageID == nil {
		if headerKey := r.Header.Get("Idempotency-Key"); headerKey != "" {
			clientMessageID = &headerKey
		}
	}

	msg, err := h.svc.SendMessage(r.Context(), channelID, userID, chat.SendMessageInput{
		Content:         req.Content,
		ParentID:        parentID,
		ForwardedFrom:   req.ForwardedFrom,
		QuotedMessageID: quotedMessageID,
		QuotedSnapshot:  quotedSnapshot,
		ProfileShare:    profileShare,
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, msg)
}

func parseProfileShareInput(input *profileShareRequest) (*chat.ProfileShareInput, error) {
	if input == nil {
		return nil, nil
	}
	userID, err := uuid.Parse(input.UserID)
	if err != nil {
		return nil, cerrors.InvalidInput("invalid profile_share.user_id")
	}
	return &chat.ProfileShareInput{UserID: userID}, nil
}

func parseQuotedSnapshotInput(input *chat.QuotedSnapshotInput) (*chat.ParsedQuotedSnapshotInput, error) {
	if input == nil {
		return nil, nil
	}
	userID, err := uuid.Parse(input.UserID)
	if err != nil {
		return nil, cerrors.InvalidInput("invalid quoted_snapshot.user_id")
	}
	createdAt, err := time.Parse(time.RFC3339, input.CreatedAt)
	if err != nil {
		return nil, cerrors.InvalidInput("invalid quoted_snapshot.created_at")
	}
	var parentMessageID *uuid.UUID
	if input.ParentMessageID != nil {
		parsed, err := uuid.Parse(*input.ParentMessageID)
		if err != nil {
			return nil, cerrors.InvalidInput("invalid quoted_snapshot.parent_message_id")
		}
		parentMessageID = &parsed
	}
	return &chat.ParsedQuotedSnapshotInput{
		UserID:          userID,
		ContentExcerpt:  input.ContentExcerpt,
		CreatedAt:       createdAt,
		ParentMessageID: parentMessageID,
	}, nil
}

func (h *MessageHandler) Get(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	msg, err := h.svc.GetMessage(r.Context(), messageID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, msg)
}

func (h *MessageHandler) List(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	p := parsePagination(r)

	page, err := h.svc.GetMessages(r.Context(), channelID, userID, p)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, page)
}

func (h *MessageHandler) ListPinned(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	messages, err := h.svc.GetPinnedMessages(r.Context(), channelID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	if messages == nil {
		messages = []entity.Message{}
	}

	writeOK(w, messages)
}

// ListMentions returns recent @-mentions of the calling user across every
// channel they are a member of inside the workspace. The response is
// denormalised so the frontend can render mention cards without per-row
// roundtrips.
func (h *MessageHandler) ListMentions(w http.ResponseWriter, r *http.Request) {
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if workspaceID == uuid.Nil {
		writeErr(w, cerrors.InvalidInput("workspace context is required"))
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	mentions, err := h.svc.ListMentions(r.Context(), workspaceID, userID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}

	if mentions == nil {
		mentions = []entity.MentionRow{}
	}

	writeOK(w, map[string]any{"mentions": mentions})
}

func (h *MessageHandler) ListThread(w http.ResponseWriter, r *http.Request) {
	parentID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	p := parsePagination(r)
	userID := middleware.UserIDFromContext(r.Context())

	page, err := h.svc.GetThreadReplies(r.Context(), parentID, userID, p)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, page)
}

type editMessageRequest struct {
	Content string `json:"content"`
}

type moveMessageRequest struct {
	ChannelID string  `json:"channel_id"`
	ParentID  *string `json:"parent_id,omitempty"`
}

func (h *MessageHandler) Edit(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req editMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	msg, err := h.svc.EditMessage(r.Context(), messageID, userID, req.Content)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, msg)
}

func (h *MessageHandler) Move(w http.ResponseWriter, r *http.Request) {
	sourceChannelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req moveMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	targetChannelID, err := id.Parse(req.ChannelID)
	if err != nil {
		writeErr(w, err)
		return
	}
	var parentID *uuid.UUID
	if req.ParentID != nil {
		parsed, err := id.Parse(*req.ParentID)
		if err != nil {
			writeErr(w, err)
			return
		}
		parentID = &parsed
	}

	userID := middleware.UserIDFromContext(r.Context())

	msg, err := h.svc.MoveMessage(r.Context(), sourceChannelID, messageID, userID, targetChannelID, parentID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, msg)
}

func (h *MessageHandler) Delete(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.DeleteMessage(r.Context(), messageID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

type reactionRequest struct {
	Emoji string `json:"emoji"`
}

func (h *MessageHandler) AddReaction(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req reactionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	reaction, err := h.svc.AddReaction(r.Context(), messageID, userID, req.Emoji)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, reaction)
}

func (h *MessageHandler) RemoveReaction(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	emoji := chi.URLParam(r, "emoji")
	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.RemoveReaction(r.Context(), messageID, userID, emoji); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *MessageHandler) RemoveReactionByID(w http.ResponseWriter, r *http.Request) {
	reactionID, err := id.Parse(chi.URLParam(r, "reactionID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.RemoveReactionByID(r.Context(), reactionID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *MessageHandler) Pin(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.PinMessage(r.Context(), messageID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *MessageHandler) Unpin(w http.ResponseWriter, r *http.Request) {
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.UnpinMessage(r.Context(), messageID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func parsePagination(r *http.Request) pagination.Params {
	p := pagination.Params{}

	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = r.URL.Query().Get("before")
	}
	if cursor != "" {
		if parsed, err := pagination.DecodeCursor(cursor); err == nil {
			p.Cursor = parsed
		}
	}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil {
			p.Limit = n
		}
	}

	p.Normalize()
	return p
}
