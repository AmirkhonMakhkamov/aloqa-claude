package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"aloqa/internal/domain/event"
)

const (
	// sendBufSize limits the number of pending outbound messages per client.
	// If the buffer fills, the client is considered slow and will be closed.
	sendBufSize = 256

	// maxSlowDrops is the number of consecutive send-buffer drops before the
	// hub force-closes the client connection.
	maxSlowDrops = 5
)

// Client represents a single WebSocket connection.
type Client struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	SessionID   string
	ResumeKey   string
	WorkspaceID uuid.UUID
	Conn        *websocket.Conn
	Send        chan []byte
	dropCount   atomic.Int32 // consecutive send failures
}

// NewClientSendChan returns a buffered send channel sized for a Client.
func NewClientSendChan() chan []byte {
	return make(chan []byte, sendBufSize)
}

// Hub maintains the set of active clients, room subscriptions, and broadcasts
// messages to the appropriate recipients.
type Hub struct {
	clients    map[uuid.UUID]*Client         // client ID -> client
	rooms      map[string]map[uuid.UUID]bool // room name -> set of client IDs
	unregister chan *Client
	evict      chan string
	state      SubscriptionStateStore
	observer   interface {
		RecordWSDropped()
	}
	mu sync.RWMutex
}

// NewHub allocates and returns a ready-to-run Hub.
func NewHub(state SubscriptionStateStore) *Hub {
	return &Hub{
		clients:    make(map[uuid.UUID]*Client),
		rooms:      make(map[string]map[uuid.UUID]bool),
		unregister: make(chan *Client),
		evict:      make(chan string),
		state:      state,
	}
}

func (h *Hub) SetObserver(observer interface {
	RecordWSDropped()
}) {
	h.observer = observer
}

// Run processes register, unregister, and cleanup events. It should be called
// in its own goroutine. It returns when ctx is cancelled, allowing graceful
// shutdown.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for clientID := range h.clients {
				if removed := h.removeClientLocked(clientID); removed != nil && removed.Conn != nil {
					_ = removed.Conn.Close(websocket.StatusGoingAway, "server shutting down")
				}
			}
			h.mu.Unlock()
			return
		case client := <-h.unregister:
			h.mu.Lock()
			removed := h.removeClientLocked(client.ID)
			h.mu.Unlock()

			if removed != nil {
				slog.Info("client unregistered",
					slog.String("client_id", removed.ID.String()),
					slog.String("user_id", removed.UserID.String()),
					slog.String("session_id", removed.SessionID),
				)
			}
		case sessionID := <-h.evict:
			if sessionID == "" {
				continue
			}
			var evicted []*Client
			h.mu.Lock()
			for clientID, client := range h.clients {
				if client.SessionID != sessionID {
					continue
				}
				if removed := h.removeClientLocked(clientID); removed != nil {
					evicted = append(evicted, removed)
				}
			}
			h.mu.Unlock()

			for _, client := range evicted {
				if client.Conn != nil {
					_ = client.Conn.Close(websocket.StatusPolicyViolation, "session revoked")
				}
				slog.Info("client evicted",
					slog.String("client_id", client.ID.String()),
					slog.String("user_id", client.UserID.String()),
					slog.String("session_id", client.SessionID),
				)
			}
		}
	}
}

// Register adds a client to the hub and returns only after subscriptions can
// see it. The WebSocket reader may receive a subscribe message immediately
// after the handshake opens, so registration must not be eventually visible.
func (h *Hub) Register(client *Client) {
	h.mu.Lock()
	h.clients[client.ID] = client
	signalRoom := "aloqa.signal." + client.UserID.String()
	if h.rooms[signalRoom] == nil {
		h.rooms[signalRoom] = make(map[uuid.UUID]bool)
	}
	h.rooms[signalRoom][client.ID] = true
	h.mu.Unlock()

	slog.Info("client registered",
		slog.String("client_id", client.ID.String()),
		slog.String("user_id", client.UserID.String()),
	)
}

// Unregister queues a client for removal from the hub.
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// EvictSession disconnects all local clients belonging to a session.
func (h *Hub) EvictSession(sessionID string) {
	h.evict <- sessionID
}

// Subscribe adds a client to a room.
func (h *Hub) Subscribe(clientID, room string) {
	id, err := uuid.Parse(clientID)
	if err != nil {
		slog.Error("invalid client id for subscribe", slog.String("client_id", clientID))
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[id]; !ok {
		slog.Warn("subscribe: client not found", slog.String("client_id", clientID))
		return
	}

	if h.rooms[room] == nil {
		h.rooms[room] = make(map[uuid.UUID]bool)
	}
	h.rooms[room][id] = true
	resumeKey := h.clients[id].ResumeKey

	slog.Debug("client subscribed to room",
		slog.String("client_id", clientID),
		slog.String("room", room),
	)
	if h.state != nil {
		if err := h.state.AddSubscription(context.Background(), resumeKey, room); err != nil {
			slog.Warn("failed to persist websocket subscription", "client_id", clientID, "room", room, "error", err)
		}
	}
}

// Unsubscribe removes a client from a room.
func (h *Hub) Unsubscribe(clientID, room string) {
	id, err := uuid.Parse(clientID)
	if err != nil {
		slog.Error("invalid client id for unsubscribe", slog.String("client_id", clientID))
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	members, ok := h.rooms[room]
	if !ok {
		return
	}

	delete(members, id)
	if len(members) == 0 {
		delete(h.rooms, room)
	}
	resumeKey := ""
	if client := h.clients[id]; client != nil {
		resumeKey = client.ResumeKey
	}

	slog.Debug("client unsubscribed from room",
		slog.String("client_id", clientID),
		slog.String("room", room),
	)
	if h.state != nil && resumeKey != "" {
		if err := h.state.RemoveSubscription(context.Background(), resumeKey, room); err != nil {
			slog.Warn("failed to remove persisted websocket subscription", "client_id", clientID, "room", room, "error", err)
		}
	}
}

// BroadcastToRoom sends data to every client subscribed to the given room.
// Clients that fail to keep up are closed after maxSlowDrops consecutive drops.
//
// Sends happen under the read lock: this blocks the writers that close
// channels (removeClientLocked runs under write Lock), so non-blocking
// sends cannot race with close(client.Send). Drop bookkeeping uses
// atomic.Int32 so writers never need to coordinate with broadcasters.
// Eviction and external state writes happen after RUnlock.
func (h *Hub) BroadcastToRoom(room string, data []byte) {
	type delivery struct {
		client    *Client
		resumeKey string
	}

	h.mu.RLock()
	members, ok := h.rooms[room]
	if !ok {
		h.mu.RUnlock()
		return
	}
	delivered := make([]delivery, 0, len(members))
	var toEvict []uuid.UUID
	for clientID := range members {
		client, exists := h.clients[clientID]
		if !exists {
			continue
		}
		select {
		case client.Send <- data:
			client.dropCount.Store(0)
			delivered = append(delivered, delivery{client, client.ResumeKey})
		default:
			if h.observer != nil {
				h.observer.RecordWSDropped()
			}
			drops := client.dropCount.Add(1)
			if drops >= maxSlowDrops {
				toEvict = append(toEvict, clientID)
			} else {
				slog.Warn("dropping message, send buffer full",
					slog.String("client_id", clientID.String()),
					slog.String("room", room),
					slog.Int("consecutive_drops", int(drops)),
				)
			}
		}
	}
	h.mu.RUnlock()

	for _, d := range delivered {
		h.recordDelivery(d.client, d.resumeKey, room, data)
	}

	h.evictSlowClients(toEvict)
}

// SendToUser delivers data to all connections belonging to a specific user.
// If workspaceID is non-nil, only connections within that workspace are targeted.
func (h *Hub) SendToUser(workspaceID, userID uuid.UUID, data []byte) {
	filterByWorkspace := workspaceID != uuid.Nil

	type delivery struct {
		client    *Client
		resumeKey string
	}

	h.mu.RLock()
	var delivered []delivery
	var toEvict []uuid.UUID
	for _, client := range h.clients {
		if client.UserID != userID {
			continue
		}
		if filterByWorkspace && client.WorkspaceID != workspaceID {
			continue
		}
		select {
		case client.Send <- data:
			client.dropCount.Store(0)
			delivered = append(delivered, delivery{client, client.ResumeKey})
		default:
			if h.observer != nil {
				h.observer.RecordWSDropped()
			}
			drops := client.dropCount.Add(1)
			if drops >= maxSlowDrops {
				toEvict = append(toEvict, client.ID)
			}
		}
	}
	h.mu.RUnlock()

	for _, d := range delivered {
		h.recordDelivery(d.client, d.resumeKey, "", data)
	}

	h.evictSlowClients(toEvict)
}

// evictSlowClients acquires the write lock and removes any clients whose
// drop counters have exceeded the threshold, closing their connections.
func (h *Hub) evictSlowClients(ids []uuid.UUID) {
	if len(ids) == 0 {
		return
	}
	var evicted []*Client
	h.mu.Lock()
	for _, id := range ids {
		if removed := h.removeClientLocked(id); removed != nil {
			evicted = append(evicted, removed)
		}
	}
	h.mu.Unlock()
	for _, removed := range evicted {
		slog.Warn("evicting slow client",
			slog.String("client_id", removed.ID.String()),
			slog.String("user_id", removed.UserID.String()),
		)
		if removed.Conn != nil {
			_ = removed.Conn.Close(websocket.StatusPolicyViolation, "send buffer overflow")
		}
	}
}

func (h *Hub) recordDelivery(client *Client, resumeKey, room string, data []byte) {
	if h.state == nil || client == nil || resumeKey == "" {
		return
	}
	var evt event.Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return
	}
	if evt.Sequence <= 0 {
		return
	}
	if room == "" {
		room = evt.Subject
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.state.RecordDeliveredSequence(ctx, resumeKey, room, evt.Sequence); err != nil {
		slog.Warn("failed to record websocket delivery sequence", "client_id", client.ID.String(), "room", room, "sequence", evt.Sequence, "error", err)
	}
}

func (h *Hub) removeClientLocked(clientID uuid.UUID) *Client {
	client, ok := h.clients[clientID]
	if !ok {
		return nil
	}
	delete(h.clients, clientID)
	close(client.Send)
	for room, members := range h.rooms {
		delete(members, clientID)
		if len(members) == 0 {
			delete(h.rooms, room)
		}
	}
	return client
}
