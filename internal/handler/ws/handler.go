package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"aloqa/internal/domain/event"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/id"
	"aloqa/internal/platform/reliability"
	platformws "aloqa/internal/platform/ws"
	calldomain "aloqa/internal/service/call"
	chatdomain "aloqa/internal/service/chat"
)

// Handler manages WebSocket connections for real-time communication.
type Handler struct {
	hub      *platformws.Hub
	chatSvc  *chatdomain.Service
	callSvc  *calldomain.Service
	state    platformws.SubscriptionStateStore
	replayer interface {
		ReplayRoom(ctx context.Context, room string, afterSequence int64, limit int) ([]event.Event, error)
	}
	replayLimit    int
	originPatterns []string
	observer       interface {
		RecordWSRestore(restoredRooms, replayedEvents, unauthorized int)
		RecordWSReplayFailure()
		RecordWSDropped()
	}
}

func NewHandler(
	hub *platformws.Hub,
	chatSvc *chatdomain.Service,
	callSvc *calldomain.Service,
	state platformws.SubscriptionStateStore,
	replayer interface {
		ReplayRoom(ctx context.Context, room string, afterSequence int64, limit int) ([]event.Event, error)
	},
	replayLimit int,
	originPatterns ...string,
) *Handler {
	if len(originPatterns) == 0 {
		originPatterns = []string{"http://localhost:3000", "http://localhost:5173"}
	}
	if replayLimit <= 0 {
		replayLimit = 200
	}
	return &Handler{
		hub:            hub,
		chatSvc:        chatSvc,
		callSvc:        callSvc,
		state:          state,
		replayer:       replayer,
		replayLimit:    replayLimit,
		originPatterns: normalizeOriginPatterns(originPatterns),
	}
}

// normalizeOriginPatterns converts full-URL origins ("http://localhost:3000")
// into host patterns ("localhost:3000") expected by nhooyr/websocket.
// The library matches OriginPatterns against r.Header.Get("Origin")'s host
// using filepath.Match, so leaving the scheme in front would never match.
// Accepts either form so a config value like CORS_ALLOWED_ORIGINS can be
// passed directly.
func normalizeOriginPatterns(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "://") {
			if u, err := url.Parse(p); err == nil && u.Host != "" {
				out = append(out, u.Host)
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func (h *Handler) SetObserver(observer interface {
	RecordWSRestore(restoredRooms, replayedEvents, unauthorized int)
	RecordWSReplayFailure()
	RecordWSDropped()
}) {
	h.observer = observer
}

// ClientMessage is the envelope for all messages from clients.
type ClientMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ServerMessage is the envelope for all messages sent to clients.
type ServerMessage struct {
	Type      string    `json:"type"`
	Payload   any       `json:"payload"`
	Timestamp time.Time `json:"timestamp"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessionID := middleware.SessionIDFromContext(r.Context())
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.hub == nil || h.chatSvc == nil || h.callSvc == nil {
		slog.Error("websocket service unavailable",
			"hub_configured", h.hub != nil,
			"chat_configured", h.chatSvc != nil,
			"call_configured", h.callSvc != nil,
		)
		http.Error(w, "websocket service unavailable", http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: h.originPatterns,
	})
	if err != nil {
		slog.Error("websocket accept failed", "error", err)
		return
	}

	resumeKey := strings.TrimSpace(r.URL.Query().Get("resume_key"))
	if resumeKey == "" {
		resumeKey = sessionID
	}

	client := &platformws.Client{
		ID:        id.New(),
		UserID:    userID,
		SessionID: sessionID,
		ResumeKey: resumeKey,
		Conn:      conn,
		Send:      platformws.NewClientSendChan(),
	}

	h.hub.Register(client)
	defer h.hub.Unregister(client)

	// Start writer goroutine.
	reliability.SafeGo("ws_write_pump", func() { h.writePump(r.Context(), client) })
	h.restoreSubscriptions(r.Context(), client)

	// Reader loop (blocking).
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("ws read pump panicked",
				"user_id", client.UserID, "panic", rec)
		}
	}()
	h.readPump(r.Context(), client)
}

func (h *Handler) readPump(ctx context.Context, client *platformws.Client) {
	defer client.Conn.Close(websocket.StatusNormalClosure, "closing")

	for {
		_, data, err := client.Conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway {
				return
			}
			slog.Debug("websocket read error", "error", err, "user_id", client.UserID)
			return
		}

		var msg ClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Debug("invalid websocket message", "error", err)
			continue
		}

		h.handleMessage(ctx, client, msg)
	}
}

func (h *Handler) writePump(ctx context.Context, client *platformws.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-client.Send:
			if !ok {
				client.Conn.Close(websocket.StatusNormalClosure, "closing")
				return
			}
			if err := client.Conn.Write(ctx, websocket.MessageText, msg); err != nil {
				slog.Debug("websocket write error", "error", err, "user_id", client.UserID)
				return
			}
		}
	}
}

func (h *Handler) handleMessage(ctx context.Context, client *platformws.Client, msg ClientMessage) {
	switch msg.Type {
	case "subscribe":
		h.handleSubscribe(ctx, client, msg.Payload)
	case "unsubscribe":
		h.handleUnsubscribe(client, msg.Payload)
	case "typing":
		h.handleTyping(ctx, client, msg.Payload)
	case "signal.offer", "signal.answer", "signal.candidate":
		h.handleSignal(ctx, client, msg.Type, msg.Payload)
	default:
		slog.Debug("unknown message type", "type", msg.Type)
	}
}

type subscribePayload struct {
	Channel string `json:"channel"`
}

func (h *Handler) handleSubscribe(ctx context.Context, client *platformws.Client, payload json.RawMessage) {
	var p subscribePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if h.hub == nil {
		h.sendError(client, "websocket service unavailable")
		return
	}
	if _, err := h.authorizeSubscription(ctx, client, p.Channel); err != nil {
		h.sendError(client, "not allowed to subscribe to this room")
		return
	}
	h.hub.Subscribe(client.ID.String(), p.Channel)
}

func (h *Handler) handleUnsubscribe(client *platformws.Client, payload json.RawMessage) {
	var p subscribePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if h.hub == nil {
		h.sendError(client, "websocket service unavailable")
		return
	}
	h.hub.Unsubscribe(client.ID.String(), p.Channel)
}

type typingPayload struct {
	ChannelID string `json:"channel_id"`
}

func (h *Handler) handleTyping(ctx context.Context, client *platformws.Client, payload json.RawMessage) {
	var p typingPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	channelID, err := uuid.Parse(p.ChannelID)
	if err != nil {
		h.sendError(client, "invalid channel_id")
		return
	}
	if h.chatSvc == nil || h.hub == nil {
		h.sendError(client, "websocket service unavailable")
		return
	}
	if err := h.chatSvc.CanAccessChannel(ctx, channelID, client.UserID); err != nil {
		h.sendError(client, "you do not have access to this channel")
		return
	}

	evt := ServerMessage{
		Type: string(event.TypeTypingStarted),
		Payload: event.TypingPayload{
			ChannelID: channelID,
			UserID:    client.UserID,
		},
		Timestamp: time.Now().UTC(),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		return
	}

	// Broadcast typing indicator to the channel room.
	h.hub.BroadcastToRoom("channel:"+p.ChannelID, data)
}

type signalPayload struct {
	CallID        string `json:"call_id"`
	ToUser        string `json:"to_user"`
	SDP           string `json:"sdp,omitempty"`
	SDPType       string `json:"sdp_type,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	SDPMid        string `json:"sdp_mid,omitempty"`
	SDPMLineIndex *int   `json:"sdp_mline_index,omitempty"`
}

func (h *Handler) handleSignal(ctx context.Context, client *platformws.Client, sigType string, payload json.RawMessage) {
	var p signalPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}

	callID, err := uuid.Parse(p.CallID)
	if err != nil {
		h.sendError(client, "invalid call_id")
		return
	}
	toUser, err := uuid.Parse(p.ToUser)
	if err != nil || toUser == uuid.Nil {
		h.sendError(client, "invalid to_user")
		return
	}
	if h.callSvc == nil {
		h.sendError(client, "websocket service unavailable")
		return
	}

	sig := event.SignalPayload{
		CallID:        callID,
		FromUser:      client.UserID,
		ToUser:        toUser,
		SDP:           p.SDP,
		Type:          p.SDPType,
		Candidate:     p.Candidate,
		SDPMid:        p.SDPMid,
		SDPMLineIndex: p.SDPMLineIndex,
	}

	signalType := strings.TrimPrefix(sigType, "signal.")
	if err := h.callSvc.ForwardSignal(ctx, callID, client.UserID, toUser, signalType, sig); err != nil {
		slog.WarnContext(ctx, "websocket signal rejected", "call_id", callID, "from_user", client.UserID, "to_user", toUser, "error", err)
		h.sendError(client, "signal rejected")
	}
}

func (h *Handler) canSubscribe(ctx context.Context, client *platformws.Client, room string) bool {
	_, err := h.authorizeSubscription(ctx, client, room)
	return err == nil
}

func (h *Handler) authorizeSubscription(ctx context.Context, client *platformws.Client, room string) (uuid.UUID, error) {
	switch {
	case strings.HasPrefix(room, "aloqa.ws."):
		if h.chatSvc == nil {
			return uuid.Nil, errNotAllowed()
		}
		workspaceID, err := uuid.Parse(strings.TrimPrefix(room, "aloqa.ws."))
		if err != nil {
			return uuid.Nil, errNotAllowed()
		}
		if err := h.chatSvc.CanAccessWorkspace(ctx, workspaceID, client.UserID); err != nil {
			return uuid.Nil, err
		}
		return workspaceID, nil
	case strings.HasPrefix(room, "channel:"):
		if h.chatSvc == nil {
			return uuid.Nil, errNotAllowed()
		}
		channelID, err := uuid.Parse(strings.TrimPrefix(room, "channel:"))
		if err != nil {
			return uuid.Nil, errNotAllowed()
		}
		return uuid.Nil, h.chatSvc.CanAccessChannel(ctx, channelID, client.UserID)
	case strings.HasPrefix(room, "aloqa.chat."):
		if h.chatSvc == nil {
			return uuid.Nil, errNotAllowed()
		}
		channelID, err := uuid.Parse(strings.TrimPrefix(room, "aloqa.chat."))
		if err != nil {
			return uuid.Nil, errNotAllowed()
		}
		return uuid.Nil, h.chatSvc.CanAccessChannel(ctx, channelID, client.UserID)
	case strings.HasPrefix(room, "aloqa.signal."):
		userID, err := uuid.Parse(strings.TrimPrefix(room, "aloqa.signal."))
		if err != nil || userID != client.UserID {
			return uuid.Nil, errNotAllowed()
		}
		return uuid.Nil, nil
	default:
		return uuid.Nil, errNotAllowed()
	}
}

func (h *Handler) sendError(client *platformws.Client, message string) {
	evt := ServerMessage{
		Type:      "error",
		Payload:   map[string]string{"message": message},
		Timestamp: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	select {
	case client.Send <- data:
	default:
		if h.observer != nil {
			h.observer.RecordWSDropped()
		}
		slog.Warn("dropping websocket error, send buffer full", "client_id", client.ID)
	}
}

func (h *Handler) restoreSubscriptions(ctx context.Context, client *platformws.Client) {
	if h.state == nil || h.hub == nil || client == nil || client.ResumeKey == "" {
		return
	}

	rooms, err := h.state.ListSubscriptions(ctx, client.ResumeKey)
	if err != nil {
		slog.WarnContext(ctx, "failed to load websocket subscription state", "user_id", client.UserID, "session_id", client.SessionID, "resume_key", client.ResumeKey, "error", err)
		return
	}
	restoredRooms := 0
	replayedEvents := 0
	unauthorized := 0
	for _, room := range rooms {
		if _, err := h.authorizeSubscription(ctx, client, room); err != nil {
			unauthorized++
			if err := h.state.RemoveSubscription(ctx, client.ResumeKey, room); err != nil {
				slog.WarnContext(ctx, "failed to remove unauthorized websocket subscription state", "user_id", client.UserID, "room", room, "error", err)
			}
			continue
		}
		h.hub.Subscribe(client.ID.String(), room)
		restoredRooms++
		if h.replayer == nil {
			continue
		}
		lastSequence, err := h.state.LastDeliveredSequence(ctx, client.ResumeKey, room)
		if err != nil {
			slog.WarnContext(ctx, "failed to load websocket replay cursor", "user_id", client.UserID, "room", room, "error", err)
			continue
		}
		events, err := h.replayer.ReplayRoom(ctx, room, lastSequence, h.replayLimit)
		if err != nil {
			if h.observer != nil {
				h.observer.RecordWSReplayFailure()
			}
			slog.WarnContext(ctx, "failed to replay websocket events", "user_id", client.UserID, "room", room, "error", err)
			continue
		}
		for _, evt := range events {
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			select {
			case client.Send <- data:
				replayedEvents++
			default:
				if h.observer != nil {
					h.observer.RecordWSDropped()
				}
				slog.WarnContext(ctx, "dropping websocket replay event, send buffer full", "user_id", client.UserID, "room", room)
				return
			}
		}
	}
	if h.observer != nil && (restoredRooms > 0 || replayedEvents > 0 || unauthorized > 0) {
		h.observer.RecordWSRestore(restoredRooms, replayedEvents, unauthorized)
	}
}

func errNotAllowed() error {
	return http.ErrNotSupported
}
