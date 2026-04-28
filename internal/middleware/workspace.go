package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/pkg/id"
)

type PersonalWorkspaceResolver interface {
	GetOrCreatePersonalWorkspaceID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

// CachedPersonalWorkspaceResolver wraps a resolver with a Redis cache to avoid
// hitting the database on every request to a personal workspace endpoint.
type CachedPersonalWorkspaceResolver struct {
	Inner PersonalWorkspaceResolver
	RDB   *redis.Client
	TTL   time.Duration
}

func (c *CachedPersonalWorkspaceResolver) GetOrCreatePersonalWorkspaceID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	if c.RDB != nil {
		key := fmt.Sprintf("personal_ws:%s", userID)
		if val, err := c.RDB.Get(ctx, key).Result(); err == nil {
			if wsID, parseErr := uuid.Parse(val); parseErr == nil {
				return wsID, nil
			}
		}

		wsID, err := c.Inner.GetOrCreatePersonalWorkspaceID(ctx, userID)
		if err != nil {
			return wsID, err
		}

		ttl := c.TTL
		if ttl <= 0 {
			ttl = time.Hour
		}
		_ = c.RDB.Set(ctx, key, wsID.String(), ttl).Err()
		return wsID, nil
	}
	return c.Inner.GetOrCreatePersonalWorkspaceID(ctx, userID)
}

// WorkspaceCtx extracts the workspace_id URL parameter and places it in context.
func WorkspaceCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := chi.URLParam(r, "workspaceID")
		if raw == "" {
			writeError(w, http.StatusBadRequest, "missing workspace_id")
			return
		}

		wsID, err := id.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid workspace_id")
			return
		}

		ctx := context.WithValue(r.Context(), WorkspaceIDKey, wsID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func PersonalWorkspaceCtx(resolver PersonalWorkspaceResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID == uuid.Nil {
				writeError(w, http.StatusUnauthorized, "missing authenticated user")
				return
			}
			workspaceID, err := resolver.GetOrCreatePersonalWorkspaceID(r.Context(), userID)
			if err != nil {
				writeError(w, http.StatusForbidden, "personal workspace unavailable")
				return
			}

			ctx := context.WithValue(r.Context(), WorkspaceIDKey, workspaceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
