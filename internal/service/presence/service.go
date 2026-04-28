package presence

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/platform/reliability"
)

// Status represents a user's online status.
type Status string

const (
	StatusOnline  Status = "online"
	StatusAway    Status = "away"
	StatusDND     Status = "dnd"
	StatusOffline Status = "offline"
)

// heartbeatTTL is how long a session presence record lives before it's
// considered stale. Clients should refresh before it expires.
const heartbeatTTL = 90 * time.Second

// UserPresence represents the aggregated presence state exposed to API clients.
type UserPresence struct {
	UserID       uuid.UUID `json:"user_id"`
	WorkspaceID  uuid.UUID `json:"workspace_id"`
	Status       Status    `json:"status"`
	CustomStatus string    `json:"custom_status,omitempty"`
	CustomEmoji  string    `json:"custom_emoji,omitempty"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

type sessionPresence struct {
	SessionID    string    `json:"session_id"`
	UserID       uuid.UUID `json:"user_id"`
	WorkspaceID  uuid.UUID `json:"workspace_id"`
	Status       Status    `json:"status"`
	CustomStatus string    `json:"custom_status,omitempty"`
	CustomEmoji  string    `json:"custom_emoji,omitempty"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// Service manages session-scoped user presence in Redis. Presence is shared
// across nodes by storing one record per session and aggregating to a user view.
type Service struct {
	rdb              *redis.Client
	opTimeout        time.Duration
	onlineShardCount int
}

type Option func(*Service)

func WithOperationTimeout(timeout time.Duration) Option {
	return func(s *Service) {
		if timeout > 0 {
			s.opTimeout = timeout
		}
	}
}

func WithOnlineShardCount(count int) Option {
	return func(s *Service) {
		if count > 0 {
			s.onlineShardCount = count
		}
	}
}

func NewService(rdb *redis.Client, opts ...Option) *Service {
	svc := &Service{
		rdb:              rdb,
		opTimeout:        3 * time.Second,
		onlineShardCount: 32,
	}
	for _, opt := range opts {
		opt(svc)
	}
	if svc.onlineShardCount <= 0 {
		svc.onlineShardCount = 32
	}
	return svc
}

func (s *Service) SetOnline(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string) error {
	return s.setPresence(ctx, userID, workspaceID, sessionID, StatusOnline)
}

func (s *Service) SetAway(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string) error {
	return s.setPresence(ctx, userID, workspaceID, sessionID, StatusAway)
}

func (s *Service) SetDND(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string) error {
	return s.setPresence(ctx, userID, workspaceID, sessionID, StatusDND)
}

func (s *Service) SetOffline(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string) error {
	return s.removeSessionPresence(ctx, userID, workspaceID, normalizeSessionID(sessionID, userID))
}

func (s *Service) SetCustomStatus(ctx context.Context, userID, workspaceID uuid.UUID, sessionID, text, emoji string) error {
	sessionID = normalizeSessionID(sessionID, userID)
	key := sessionPresenceKey(workspaceID, sessionID)

	existing, err := s.getSession(ctx, key)
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			return fmt.Errorf("presence: get %s: %w", key, err)
		}
		existing = &sessionPresence{
			SessionID:   sessionID,
			UserID:      userID,
			WorkspaceID: workspaceID,
			Status:      StatusOnline,
			LastSeenAt:  time.Now().UTC(),
		}
	}

	existing.CustomStatus = text
	existing.CustomEmoji = emoji
	existing.LastSeenAt = time.Now().UTC()

	return s.saveSession(ctx, existing)
}

func (s *Service) Heartbeat(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string) error {
	sessionID = normalizeSessionID(sessionID, userID)
	key := sessionPresenceKey(workspaceID, sessionID)

	existing, err := s.getSession(ctx, key)
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			return fmt.Errorf("presence: get %s: %w", key, err)
		}
		return s.setPresence(ctx, userID, workspaceID, sessionID, StatusOnline)
	}

	existing.LastSeenAt = time.Now().UTC()
	return s.saveSession(ctx, existing)
}

func (s *Service) GetPresence(ctx context.Context, userID, workspaceID uuid.UUID) (*UserPresence, error) {
	aggregate, err := s.aggregatePresence(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if aggregate == nil {
		return &UserPresence{
			UserID:      userID,
			WorkspaceID: workspaceID,
			Status:      StatusOffline,
		}, nil
	}
	return aggregate, nil
}

func (s *Service) GetWorkspacePresence(ctx context.Context, workspaceID uuid.UUID) ([]UserPresence, error) {
	setKey := workspaceOnlineKey(workspaceID)
	memberIDs, err := reliability.DoValue(ctx, s.redisPolicy(2), func(ctx context.Context) ([]string, error) {
		return s.workspaceOnlineMembers(ctx, workspaceID)
	})
	if err != nil {
		return nil, fmt.Errorf("presence: smembers %s: %w", setKey, err)
	}
	if len(memberIDs) == 0 {
		return nil, nil
	}

	result := make([]UserPresence, 0, len(memberIDs))
	for _, mid := range memberIDs {
		userID, err := uuid.Parse(mid)
		if err != nil {
			slog.WarnContext(ctx, "presence: ignoring malformed online-set member", "workspace_id", workspaceID, "member_id", mid)
			continue
		}
		presence, err := s.GetPresence(ctx, userID, workspaceID)
		if err != nil {
			return nil, err
		}
		if presence.Status == StatusOffline {
			continue
		}
		result = append(result, *presence)
	}
	return result, nil
}

// ClearSession removes all presence state associated with a revoked or closed
// session across every workspace where it was active.
func (s *Service) ClearSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil
	}

	workspaces, err := s.rdb.SMembers(ctx, sessionWorkspacesKey(sessionID)).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("presence: smembers %s: %w", sessionWorkspacesKey(sessionID), err)
	}
	for _, wid := range workspaces {
		workspaceID, err := uuid.Parse(wid)
		if err != nil {
			slog.WarnContext(ctx, "presence: ignoring malformed session workspace", "session_id", sessionID, "workspace_id", wid)
			continue
		}
		key := sessionPresenceKey(workspaceID, sessionID)
		record, err := s.getSession(ctx, key)
		if err != nil && !errors.Is(err, redis.Nil) {
			return fmt.Errorf("presence: get %s: %w", key, err)
		}
		userID := uuid.Nil
		if record != nil {
			userID = record.UserID
		}
		if err := s.removeSessionPresence(ctx, userID, workspaceID, sessionID); err != nil {
			return err
		}
	}
	if err := s.rdb.Del(ctx, sessionWorkspacesKey(sessionID)).Err(); err != nil {
		return fmt.Errorf("presence: delete %s: %w", sessionWorkspacesKey(sessionID), err)
	}
	return nil
}

func (s *Service) setPresence(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string, status Status) error {
	record := &sessionPresence{
		SessionID:   normalizeSessionID(sessionID, userID),
		UserID:      userID,
		WorkspaceID: workspaceID,
		Status:      status,
		LastSeenAt:  time.Now().UTC(),
	}
	return s.saveSession(ctx, record)
}

func (s *Service) saveSession(ctx context.Context, record *sessionPresence) error {
	if record == nil {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("presence: marshal: %w", err)
	}

	sessionKey := sessionPresenceKey(record.WorkspaceID, record.SessionID)
	userSessionsKey := workspaceUserSessionsKey(record.WorkspaceID, record.UserID)
	onlineKey := workspaceOnlineKeyForUser(record.WorkspaceID, record.UserID, s.onlineShardCount)
	sessionWorkspaces := sessionWorkspacesKey(record.SessionID)

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, sessionKey, data, heartbeatTTL)
	pipe.SAdd(ctx, userSessionsKey, record.SessionID)
	pipe.Expire(ctx, userSessionsKey, heartbeatTTL*2)
	pipe.SAdd(ctx, onlineKey, record.UserID.String())
	pipe.Expire(ctx, onlineKey, heartbeatTTL*2)
	pipe.SAdd(ctx, sessionWorkspaces, record.WorkspaceID.String())
	pipe.Expire(ctx, sessionWorkspaces, heartbeatTTL*2)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("presence: save session presence: %w", err)
	}
	return nil
}

func (s *Service) removeSessionPresence(ctx context.Context, userID, workspaceID uuid.UUID, sessionID string) error {
	if s == nil || s.rdb == nil || sessionID == "" || workspaceID == uuid.Nil {
		return nil
	}

	if userID == uuid.Nil {
		record, err := s.getSession(ctx, sessionPresenceKey(workspaceID, sessionID))
		if err != nil && !errors.Is(err, redis.Nil) {
			return fmt.Errorf("presence: get %s: %w", sessionPresenceKey(workspaceID, sessionID), err)
		}
		if record != nil {
			userID = record.UserID
		}
	}

	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, sessionPresenceKey(workspaceID, sessionID))
	if userID != uuid.Nil {
		pipe.SRem(ctx, workspaceUserSessionsKey(workspaceID, userID), sessionID)
	}
	pipe.SRem(ctx, sessionWorkspacesKey(sessionID), workspaceID.String())
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("presence: remove session presence: %w", err)
	}
	if userID != uuid.Nil {
		if err := s.refreshWorkspaceOnlineMembership(ctx, workspaceID, userID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) aggregatePresence(ctx context.Context, userID, workspaceID uuid.UUID) (*UserPresence, error) {
	sessionIDs, err := s.rdb.SMembers(ctx, workspaceUserSessionsKey(workspaceID, userID)).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("presence: smembers %s: %w", workspaceUserSessionsKey(workspaceID, userID), err)
	}
	if len(sessionIDs) == 0 {
		onlineKey := workspaceOnlineKeyForUser(workspaceID, userID, s.onlineShardCount)
		if err := s.rdb.SRem(ctx, onlineKey, userID.String()).Err(); err != nil {
			return nil, fmt.Errorf("presence: srem %s: %w", onlineKey, err)
		}
		return nil, nil
	}

	var latest *sessionPresence
	for _, sessionID := range sessionIDs {
		record, err := s.getSession(ctx, sessionPresenceKey(workspaceID, sessionID))
		if err != nil {
			if errors.Is(err, redis.Nil) {
				if err := s.rdb.SRem(ctx, workspaceUserSessionsKey(workspaceID, userID), sessionID).Err(); err != nil {
					return nil, fmt.Errorf("presence: cleanup stale session %s: %w", sessionID, err)
				}
				continue
			}
			return nil, fmt.Errorf("presence: get session presence: %w", err)
		}
		if latest == nil || record.LastSeenAt.After(latest.LastSeenAt) || (record.LastSeenAt.Equal(latest.LastSeenAt) && statusRank(record.Status) > statusRank(latest.Status)) {
			latest = record
		}
	}

	if latest == nil {
		onlineKey := workspaceOnlineKeyForUser(workspaceID, userID, s.onlineShardCount)
		if err := s.rdb.SRem(ctx, onlineKey, userID.String()).Err(); err != nil {
			return nil, fmt.Errorf("presence: srem %s: %w", onlineKey, err)
		}
		return nil, nil
	}

	onlineKey := workspaceOnlineKeyForUser(workspaceID, userID, s.onlineShardCount)
	if err := s.rdb.SAdd(ctx, onlineKey, userID.String()).Err(); err != nil {
		return nil, fmt.Errorf("presence: sadd %s: %w", onlineKey, err)
	}
	if err := s.rdb.Expire(ctx, onlineKey, heartbeatTTL*2).Err(); err != nil {
		return nil, fmt.Errorf("presence: expire %s: %w", onlineKey, err)
	}

	return &UserPresence{
		UserID:       latest.UserID,
		WorkspaceID:  latest.WorkspaceID,
		Status:       latest.Status,
		CustomStatus: latest.CustomStatus,
		CustomEmoji:  latest.CustomEmoji,
		LastSeenAt:   latest.LastSeenAt,
	}, nil
}

func (s *Service) refreshWorkspaceOnlineMembership(ctx context.Context, workspaceID, userID uuid.UUID) error {
	presence, err := s.aggregatePresence(ctx, userID, workspaceID)
	if err != nil {
		return err
	}
	if presence == nil {
		return nil
	}
	onlineKey := workspaceOnlineKeyForUser(workspaceID, userID, s.onlineShardCount)
	if err := s.rdb.Expire(ctx, onlineKey, heartbeatTTL*2).Err(); err != nil {
		return fmt.Errorf("presence: expire %s: %w", onlineKey, err)
	}
	return nil
}

func (s *Service) workspaceOnlineMembers(ctx context.Context, workspaceID uuid.UUID) ([]string, error) {
	if s.onlineShardCount <= 1 {
		return s.rdb.SMembers(ctx, workspaceOnlineKey(workspaceID)).Result()
	}
	memberSet := make(map[string]struct{})
	for shard := 0; shard < s.onlineShardCount; shard++ {
		ids, err := s.rdb.SMembers(ctx, workspaceOnlineShardKey(workspaceID, shard)).Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		for _, id := range ids {
			memberSet[id] = struct{}{}
		}
	}
	members := make([]string, 0, len(memberSet))
	for id := range memberSet {
		members = append(members, id)
	}
	return members, nil
}

func (s *Service) redisPolicy(maxAttempts int) reliability.Policy {
	return reliability.Policy{
		Timeout:      s.opTimeout,
		MaxAttempts:  maxAttempts,
		RetryBackoff: 100 * time.Millisecond,
		MaxBackoff:   500 * time.Millisecond,
	}
}

func (s *Service) getSession(ctx context.Context, key string) (*sessionPresence, error) {
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}

	var record sessionPresence
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("presence: unmarshal: %w", err)
	}
	return &record, nil
}

func normalizeSessionID(sessionID string, userID uuid.UUID) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		return sessionID
	}
	return userID.String()
}

func statusRank(status Status) int {
	switch status {
	case StatusDND:
		return 3
	case StatusAway:
		return 2
	case StatusOnline:
		return 1
	default:
		return 0
	}
}

func sessionPresenceKey(workspaceID uuid.UUID, sessionID string) string {
	return fmt.Sprintf("presence:session:%s:%s", workspaceID, sessionID)
}

func workspaceUserSessionsKey(workspaceID, userID uuid.UUID) string {
	return fmt.Sprintf("presence:user_sessions:%s:%s", workspaceID, userID)
}

func sessionWorkspacesKey(sessionID string) string {
	return fmt.Sprintf("presence:workspaces:%s", sessionID)
}

func workspaceOnlineKey(workspaceID uuid.UUID) string {
	return fmt.Sprintf("presence:online:%s", workspaceID)
}

func workspaceOnlineShardKey(workspaceID uuid.UUID, shard int) string {
	return fmt.Sprintf("presence:online:%s:%02d", workspaceID, shard)
}

func workspaceOnlineKeyForUser(workspaceID, userID uuid.UUID, shardCount int) string {
	if shardCount <= 1 {
		return workspaceOnlineKey(workspaceID)
	}
	sum := sha1.Sum([]byte(userID.String()))
	shard := int(sum[0]) % shardCount
	return workspaceOnlineShardKey(workspaceID, shard)
}
