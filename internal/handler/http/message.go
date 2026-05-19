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
	ForwardedFrom   json.RawMessage           `json:"forwarded_from,omitempty"`
	QuotedMessageID *string                   `json:"quoted_message_id,omitempty"`
	QuotedSnapshot  *chat.QuotedSnapshotInput `json:"quoted_snapshot,omitempty"`
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

	userID := middleware.UserIDFromContext(r.Context())

	msg, err := h.svc.SendMessage(r.Context(), channelID, userID, chat.SendMessageInput{
		Content:         req.Content,
		ParentID:        parentID,
		ForwardedFrom:   req.ForwardedFrom,
		QuotedMessageID: quotedMessageID,
		QuotedSnapshot:  quotedSnapshot,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, msg)
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

	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
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
