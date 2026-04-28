package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/platform/reliability"
)

// Session represents an authenticated user session stored in Redis.
type Session struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	RefreshToken string    `json:"-"` // hashed, never exposed
	DeviceInfo   string    `json:"device_info"`
	IPAddress    string    `json:"ip_address"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// sessionStorable is the internal representation stored in Redis (includes the hash).
type sessionStorable struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	RefreshTokenHash string    `json:"refresh_token_hash"`
	DeviceInfo       string    `json:"device_info"`
	IPAddress        string    `json:"ip_address"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	LastActiveAt     time.Time `json:"last_active_at"`
}

func (s *sessionStorable) toSession() *Session {
	return &Session{
		ID:           s.ID,
		UserID:       s.UserID,
		DeviceInfo:   s.DeviceInfo,
		IPAddress:    s.IPAddress,
		CreatedAt:    s.CreatedAt,
		ExpiresAt:    s.ExpiresAt,
		LastActiveAt: s.LastActiveAt,
	}
}

// SessionManager handles server-side session tracking in Redis.
type SessionManager struct {
	rdb         *redis.Client
	maxSessions int
	sessionTTL  time.Duration
	notifier    SessionEventNotifier
	opTimeout   time.Duration

	// Pending touches are batched and flushed by RunTouchWorker instead of
	// spawning a goroutine per request.
	pendingTouches sync.Map // sessionID string -> struct{}
}

// NewSessionManager creates a new session manager.
func NewSessionManager(rdb *redis.Client, maxSessions int, sessionTTL time.Duration) *SessionManager {
	if maxSessions <= 0 {
		maxSessions = 5
	}
	return &SessionManager{
		rdb:         rdb,
		maxSessions: maxSessions,
		sessionTTL:  sessionTTL,
		opTimeout:   3 * time.Second,
	}
}

func (sm *SessionManager) SetNotifier(notifier SessionEventNotifier) {
	sm.notifier = notifier
}

func (sm *SessionManager) SetOperationTimeout(timeout time.Duration) {
	if sm == nil || timeout <= 0 {
		return
	}
	sm.opTimeout = timeout
}

// Redis key helpers.
func sessionKey(sessionID string) string   { return "session:" + sessionID }
func userSessionsKey(userID string) string { return "user_sessions:" + userID }
func refreshKey(tokenHash string) string   { return "refresh:" + tokenHash }

// generateRefreshToken creates a crypto-random 32-byte opaque token encoded as hex.
func generateRefreshToken() (token string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate random bytes: %w", err)
	}
	token = hex.EncodeToString(b)
	hash = hashToken(token)
	return token, hash, nil
}

// hashToken produces the SHA-256 hex digest of a token string.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Create creates a new session, generating an opaque refresh token.
// If the user exceeds maxSessions, the oldest session is revoked.
func (sm *SessionManager) Create(ctx context.Context, userID, deviceInfo, ipAddress string) (*Session, string, error) {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	token, tokenHash, err := generateRefreshToken()
	if err != nil {
		return nil, "", fmt.Errorf("generate refresh token: %w", err)
	}

	now := time.Now()
	sessionID := uuid.New().String()

	storable := sessionStorable{
		ID:               sessionID,
		UserID:           userID,
		RefreshTokenHash: tokenHash,
		DeviceInfo:       deviceInfo,
		IPAddress:        ipAddress,
		CreatedAt:        now,
		ExpiresAt:        now.Add(sm.sessionTTL),
		LastActiveAt:     now,
	}

	data, err := json.Marshal(storable)
	if err != nil {
		return nil, "", fmt.Errorf("marshal session: %w", err)
	}

	pipe := sm.rdb.Pipeline()

	// Store session data.
	pipe.Set(ctx, sessionKey(sessionID), data, sm.sessionTTL)

	// Map refresh token hash -> session ID.
	pipe.Set(ctx, refreshKey(tokenHash), sessionID, sm.sessionTTL)

	// Add session ID to user's session set.
	pipe.SAdd(ctx, userSessionsKey(userID), sessionID)

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, "", fmt.Errorf("store session in redis: %w", err)
	}

	// Enforce max concurrent sessions.
	if err := sm.enforceMaxSessions(ctx, userID); err != nil {
		slog.WarnContext(ctx, "failed to enforce max sessions", "user_id", userID, "error", err)
		// Non-fatal: the session was already created successfully.
	}

	session := storable.toSession()
	slog.InfoContext(ctx, "session created",
		"session_id", sessionID,
		"user_id", userID,
		"device_info", deviceInfo,
	)
	return session, token, nil
}

// enforceMaxSessions revokes the oldest sessions if the user has more than maxSessions.
func (sm *SessionManager) enforceMaxSessions(ctx context.Context, userID string) error {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	sessionIDs, err := sm.rdb.SMembers(ctx, userSessionsKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("list user sessions: %w", err)
	}

	if len(sessionIDs) <= sm.maxSessions {
		return nil
	}

	// Load all sessions to sort by CreatedAt.
	type sessionWithID struct {
		id        string
		createdAt time.Time
	}

	var sessions []sessionWithID
	for _, sid := range sessionIDs {
		data, err := sm.rdb.Get(ctx, sessionKey(sid)).Result()
		if err == redis.Nil {
			// Session expired, clean up the set.
			if err := sm.rdb.SRem(ctx, userSessionsKey(userID), sid).Err(); err != nil {
				slog.WarnContext(ctx, "failed to remove expired session from set", "session_id", sid, "user_id", userID, "error", err)
			}
			continue
		}
		if err != nil {
			continue
		}

		var s sessionStorable
		if err := json.Unmarshal([]byte(data), &s); err != nil {
			continue
		}
		sessions = append(sessions, sessionWithID{id: sid, createdAt: s.CreatedAt})
	}

	if len(sessions) <= sm.maxSessions {
		return nil
	}

	// Sort oldest first.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].createdAt.Before(sessions[j].createdAt)
	})

	// Revoke the oldest sessions.
	excess := len(sessions) - sm.maxSessions
	for i := 0; i < excess; i++ {
		if err := sm.Revoke(ctx, sessions[i].id); err != nil {
			slog.WarnContext(ctx, "failed to revoke excess session",
				"session_id", sessions[i].id,
				"error", err,
			)
		}
	}

	return nil
}

// ValidateRefreshToken looks up a session by the opaque refresh token.
// It verifies the session has not expired and updates LastActiveAt.
func (sm *SessionManager) ValidateRefreshToken(ctx context.Context, refreshToken string) (*Session, error) {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	tokenHash := hashToken(refreshToken)

	sessionID, err := sm.rdb.Get(ctx, refreshKey(tokenHash)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("refresh token not found or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("lookup refresh token: %w", err)
	}

	data, err := sm.rdb.Get(ctx, sessionKey(sessionID)).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("session not found or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}

	var storable sessionStorable
	if err := json.Unmarshal([]byte(data), &storable); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	if time.Now().After(storable.ExpiresAt) {
		// Expired — clean up.
		if err := sm.Revoke(ctx, sessionID); err != nil {
			slog.WarnContext(ctx, "failed to revoke expired session", "session_id", sessionID, "error", err)
		}
		return nil, fmt.Errorf("session expired")
	}

	// Update LastActiveAt.
	storable.LastActiveAt = time.Now()
	updated, err := json.Marshal(storable)
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}
	ttl := time.Until(storable.ExpiresAt)
	if ttl > 0 {
		if err := sm.rdb.Set(ctx, sessionKey(sessionID), updated, ttl).Err(); err != nil {
			return nil, fmt.Errorf("touch session: %w", err)
		}
	}

	return storable.toSession(), nil
}

// RotateRefreshToken replaces the refresh token on an existing session.
// Returns the new opaque refresh token.
func (sm *SessionManager) RotateRefreshToken(ctx context.Context, sessionID string) (string, error) {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	data, err := sm.rdb.Get(ctx, sessionKey(sessionID)).Result()
	if err != nil {
		return "", fmt.Errorf("lookup session: %w", err)
	}

	var storable sessionStorable
	if err := json.Unmarshal([]byte(data), &storable); err != nil {
		return "", fmt.Errorf("unmarshal session: %w", err)
	}
	oldHash := storable.RefreshTokenHash

	// Generate new refresh token.
	newToken, newHash, err := generateRefreshToken()
	if err != nil {
		return "", fmt.Errorf("generate new refresh token: %w", err)
	}

	storable.RefreshTokenHash = newHash
	storable.LastActiveAt = time.Now()

	updated, err := json.Marshal(storable)
	if err != nil {
		return "", fmt.Errorf("marshal session: %w", err)
	}

	ttl := time.Until(storable.ExpiresAt)
	if ttl <= 0 {
		return "", fmt.Errorf("session expired")
	}

	pipe := sm.rdb.Pipeline()
	pipe.Del(ctx, refreshKey(oldHash))
	pipe.Set(ctx, sessionKey(sessionID), updated, ttl)
	pipe.Set(ctx, refreshKey(newHash), sessionID, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("rotate refresh token in redis: %w", err)
	}

	return newToken, nil
}

// Revoke deletes a session and its associated refresh token mapping.
func (sm *SessionManager) Revoke(ctx context.Context, sessionID string) error {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	data, err := sm.rdb.Get(ctx, sessionKey(sessionID)).Result()
	if err == redis.Nil {
		return nil // already gone
	}
	if err != nil {
		return fmt.Errorf("lookup session for revocation: %w", err)
	}

	var storable sessionStorable
	if err := json.Unmarshal([]byte(data), &storable); err != nil {
		return fmt.Errorf("unmarshal session: %w", err)
	}

	pipe := sm.rdb.Pipeline()
	pipe.Del(ctx, sessionKey(sessionID))
	pipe.Del(ctx, refreshKey(storable.RefreshTokenHash))
	pipe.SRem(ctx, userSessionsKey(storable.UserID), sessionID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("revoke session in redis: %w", err)
	}

	if sm.notifier != nil {
		userID, parseErr := uuid.Parse(storable.UserID)
		if parseErr != nil {
			slog.WarnContext(ctx, "failed to parse user id for session revocation event", "session_id", sessionID, "user_id", storable.UserID, "error", parseErr)
		} else {
			notifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := sm.notifier.SessionRevoked(notifyCtx, sessionID, userID); err != nil {
				slog.WarnContext(ctx, "failed to publish session revocation event", "session_id", sessionID, "user_id", userID, "error", err)
			}
		}
	}

	slog.InfoContext(ctx, "session revoked", "session_id", sessionID, "user_id", storable.UserID)
	return nil
}

// RevokeAllUserSessions revokes every session for a given user.
func (sm *SessionManager) RevokeAllUserSessions(ctx context.Context, userID string) error {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	sessionIDs, err := sm.rdb.SMembers(ctx, userSessionsKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("list user sessions: %w", err)
	}

	for _, sid := range sessionIDs {
		if err := sm.Revoke(ctx, sid); err != nil {
			slog.WarnContext(ctx, "failed to revoke session during bulk revocation",
				"session_id", sid,
				"user_id", userID,
				"error", err,
			)
		}
	}

	// Clean up the set itself.
	if err := sm.rdb.Del(ctx, userSessionsKey(userID)).Err(); err != nil {
		return fmt.Errorf("delete user session set: %w", err)
	}

	slog.InfoContext(ctx, "all sessions revoked", "user_id", userID, "count", len(sessionIDs))
	return nil
}

// ListUserSessions returns all active sessions for a user.
func (sm *SessionManager) ListUserSessions(ctx context.Context, userID string) ([]Session, error) {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	sessionIDs, err := sm.rdb.SMembers(ctx, userSessionsKey(userID)).Result()
	if err != nil {
		return nil, fmt.Errorf("list user sessions: %w", err)
	}

	var sessions []Session
	for _, sid := range sessionIDs {
		data, err := sm.rdb.Get(ctx, sessionKey(sid)).Result()
		if err == redis.Nil {
			// Session expired, clean up the set.
			if err := sm.rdb.SRem(ctx, userSessionsKey(userID), sid).Err(); err != nil {
				slog.WarnContext(ctx, "failed to remove expired session from set", "session_id", sid, "user_id", userID, "error", err)
			}
			continue
		}
		if err != nil {
			slog.WarnContext(ctx, "failed to read session", "session_id", sid, "error", err)
			continue
		}

		var storable sessionStorable
		if err := json.Unmarshal([]byte(data), &storable); err != nil {
			slog.WarnContext(ctx, "failed to unmarshal session", "session_id", sid, "error", err)
			continue
		}

		if time.Now().After(storable.ExpiresAt) {
			if err := sm.Revoke(ctx, sid); err != nil {
				slog.WarnContext(ctx, "failed to revoke expired session during listing", "session_id", sid, "error", err)
			}
			continue
		}

		sessions = append(sessions, *storable.toSession())
	}

	// Sort by most recently active first.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActiveAt.After(sessions[j].LastActiveAt)
	})

	return sessions, nil
}

// IsSessionValid checks if a session exists and is not expired.
// On Redis read failure, returns false (fail-closed for Zero Trust).
func (sm *SessionManager) IsSessionValid(ctx context.Context, sessionID string) (bool, error) {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	data, err := sm.rdb.Get(ctx, sessionKey(sessionID)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check session validity: %w", err)
	}

	var storable sessionStorable
	if err := json.Unmarshal([]byte(data), &storable); err != nil {
		return false, fmt.Errorf("unmarshal session: %w", err)
	}

	if time.Now().After(storable.ExpiresAt) {
		return false, nil
	}

	return true, nil
}

// TouchSession updates the LastActiveAt timestamp on a session.
func (sm *SessionManager) TouchSession(ctx context.Context, sessionID string) error {
	ctx, cancel := sm.operationCtx(ctx)
	defer cancel()

	data, err := sm.rdb.Get(ctx, sessionKey(sessionID)).Result()
	if err != nil {
		return fmt.Errorf("lookup session for touch: %w", err)
	}

	var storable sessionStorable
	if err := json.Unmarshal([]byte(data), &storable); err != nil {
		return fmt.Errorf("unmarshal session: %w", err)
	}

	storable.LastActiveAt = time.Now()
	updated, err := json.Marshal(storable)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	ttl := time.Until(storable.ExpiresAt)
	if ttl <= 0 {
		return fmt.Errorf("session expired")
	}

	return sm.rdb.Set(ctx, sessionKey(sessionID), updated, ttl).Err()
}

// DeferTouch schedules a LastActiveAt update for the given session.
// It is safe to call from any goroutine; the actual Redis write is coalesced
// by RunTouchWorker so there is at most one write per session per flush interval.
func (sm *SessionManager) DeferTouch(sessionID string) {
	sm.pendingTouches.Store(sessionID, struct{}{})
}

// RunTouchWorker periodically flushes all pending DeferTouch calls in a single
// pipelined Redis batch. Run it in a dedicated goroutine; it exits when ctx
// is cancelled.
func (sm *SessionManager) RunTouchWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.flushTouches(ctx)
		}
	}
}

func (sm *SessionManager) flushTouches(ctx context.Context) {
	// Collect and clear pending set atomically.
	var ids []string
	sm.pendingTouches.Range(func(k, _ any) bool {
		ids = append(ids, k.(string))
		sm.pendingTouches.Delete(k)
		return true
	})
	if len(ids) == 0 {
		return
	}

	opCtx, cancel := sm.operationCtx(ctx)
	defer cancel()

	pipe := sm.rdb.Pipeline()
	// We need to read current TTL for each session before we can update. Do a
	// GETEX approach: rewrite the key extending TTL without changing the data.
	// Batching reads then writes in two pipeline rounds is cheaper than N
	// individual GET+SET pairs.

	// Round 1: fetch all sessions.
	gets := make([]*redis.StringCmd, len(ids))
	for i, sid := range ids {
		gets[i] = pipe.Get(opCtx, sessionKey(sid))
	}
	if _, err := pipe.Exec(opCtx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, "session touch worker: pipeline get failed", "error", err)
		// Re-queue for next tick.
		for _, sid := range ids {
			sm.pendingTouches.Store(sid, struct{}{})
		}
		return
	}

	// Round 2: write back with updated LastActiveAt.
	now := time.Now()
	pipe2 := sm.rdb.Pipeline()
	wrote := 0
	for i, sid := range ids {
		data, err := gets[i].Bytes()
		if err != nil {
			continue // expired or missing
		}
		var storable sessionStorable
		if err := json.Unmarshal(data, &storable); err != nil {
			continue
		}
		ttl := time.Until(storable.ExpiresAt)
		if ttl <= 0 {
			continue
		}
		storable.LastActiveAt = now
		updated, err := json.Marshal(storable)
		if err != nil {
			continue
		}
		pipe2.Set(opCtx, sessionKey(sid), updated, ttl)
		wrote++
	}
	if wrote == 0 {
		return
	}
	if _, err := pipe2.Exec(opCtx); err != nil {
		slog.WarnContext(ctx, "session touch worker: pipeline set failed", "error", err)
	}
}

func (sm *SessionManager) operationCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if sm == nil {
		return reliability.WithTimeout(ctx, 0)
	}
	return reliability.WithTimeout(ctx, sm.opTimeout)
}
