package ws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type SubscriptionStateStore interface {
	AddSubscription(ctx context.Context, resumeKey, room string) error
	RemoveSubscription(ctx context.Context, resumeKey, room string) error
	ListSubscriptions(ctx context.Context, resumeKey string) ([]string, error)
	LastDeliveredSequence(ctx context.Context, resumeKey, room string) (int64, error)
	RecordDeliveredSequence(ctx context.Context, resumeKey, room string, sequence int64) error
}

type StateStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewStateStore(rdb *redis.Client, ttl time.Duration) *StateStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &StateStore{rdb: rdb, ttl: ttl}
}

func (s *StateStore) AddSubscription(ctx context.Context, resumeKey, room string) error {
	resumeKey = normalizeResumeKey(resumeKey)
	if s == nil || s.rdb == nil || resumeKey == "" || strings.TrimSpace(room) == "" {
		return nil
	}
	key := subscriptionKey(resumeKey)
	if err := s.rdb.SAdd(ctx, key, room).Err(); err != nil {
		return fmt.Errorf("ws state: sadd %s: %w", key, err)
	}
	if err := s.rdb.Expire(ctx, key, s.ttl).Err(); err != nil {
		return fmt.Errorf("ws state: expire %s: %w", key, err)
	}
	return nil
}

func (s *StateStore) RemoveSubscription(ctx context.Context, resumeKey, room string) error {
	resumeKey = normalizeResumeKey(resumeKey)
	if s == nil || s.rdb == nil || resumeKey == "" || strings.TrimSpace(room) == "" {
		return nil
	}
	key := subscriptionKey(resumeKey)
	if err := s.rdb.SRem(ctx, key, room).Err(); err != nil {
		return fmt.Errorf("ws state: srem %s: %w", key, err)
	}
	return nil
}

func (s *StateStore) ListSubscriptions(ctx context.Context, resumeKey string) ([]string, error) {
	resumeKey = normalizeResumeKey(resumeKey)
	if s == nil || s.rdb == nil || resumeKey == "" {
		return nil, nil
	}
	key := subscriptionKey(resumeKey)
	rooms, err := s.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("ws state: smembers %s: %w", key, err)
	}
	return rooms, nil
}

func (s *StateStore) LastDeliveredSequence(ctx context.Context, resumeKey, room string) (int64, error) {
	resumeKey = normalizeResumeKey(resumeKey)
	if s == nil || s.rdb == nil || resumeKey == "" || strings.TrimSpace(room) == "" {
		return 0, nil
	}
	key := deliveredSequenceKey(resumeKey, room)
	value, err := s.rdb.Get(ctx, key).Int64()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, fmt.Errorf("ws state: get %s: %w", key, err)
	}
	return value, nil
}

func (s *StateStore) RecordDeliveredSequence(ctx context.Context, resumeKey, room string, sequence int64) error {
	resumeKey = normalizeResumeKey(resumeKey)
	if s == nil || s.rdb == nil || resumeKey == "" || strings.TrimSpace(room) == "" || sequence <= 0 {
		return nil
	}
	key := deliveredSequenceKey(resumeKey, room)
	current, err := s.rdb.Get(ctx, key).Int64()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("ws state: get %s: %w", key, err)
	}
	if current >= sequence {
		return nil
	}
	if err := s.rdb.Set(ctx, key, sequence, s.ttl).Err(); err != nil {
		return fmt.Errorf("ws state: set %s: %w", key, err)
	}
	return nil
}

func normalizeResumeKey(resumeKey string) string {
	return strings.TrimSpace(resumeKey)
}

func subscriptionKey(resumeKey string) string {
	return "ws:subscriptions:" + resumeKey
}

func deliveredSequenceKey(resumeKey, room string) string {
	return "ws:sequence:" + resumeKey + ":" + room
}
