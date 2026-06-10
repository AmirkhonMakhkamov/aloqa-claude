package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRotateRefreshTokenRejectsSupersededRotation(t *testing.T) {
	ctx := context.Background()
	sm, _ := newTestSessionManager(t)

	session, token, err := sm.Create(ctx, "user-1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	oldHash := hashToken(token)

	// First rotation with the validated hash wins.
	newToken1, err := sm.RotateRefreshToken(ctx, session.ID, oldHash)
	if err != nil {
		t.Fatalf("first RotateRefreshToken returned error: %v", err)
	}
	if newToken1 == token {
		t.Fatalf("expected a rotated token")
	}

	// A second rotation presenting the same (now consumed) hash is superseded —
	// the atomic CAS prevents a silent double-rotation.
	if _, err := sm.RotateRefreshToken(ctx, session.ID, oldHash); !errors.Is(err, ErrRotationSuperseded) {
		t.Fatalf("second RotateRefreshToken err = %v, want ErrRotationSuperseded", err)
	}

	// The grace record for the consumed token resolves to the winner's successor.
	_, successor, err := sm.ReplayRotation(ctx, token)
	if err != nil {
		t.Fatalf("ReplayRotation returned error: %v", err)
	}
	if successor != newToken1 {
		t.Fatalf("grace successor = %q, want %q", successor, newToken1)
	}
}

func TestSessionBelongsToUser(t *testing.T) {
	ctx := context.Background()
	sm, _ := newTestSessionManager(t)
	session, _, err := sm.Create(ctx, "user-1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	ok, err := sm.SessionBelongsToUser(ctx, "user-1", session.ID)
	if err != nil {
		t.Fatalf("SessionBelongsToUser returned error: %v", err)
	}
	if !ok {
		t.Fatalf("SessionBelongsToUser = false, want true")
	}

	ok, err = sm.SessionBelongsToUser(ctx, "user-2", session.ID)
	if err != nil {
		t.Fatalf("SessionBelongsToUser wrong owner returned error: %v", err)
	}
	if ok {
		t.Fatalf("SessionBelongsToUser wrong owner = true, want false")
	}

	ok, err = sm.SessionBelongsToUser(ctx, "user-1", "missing")
	if err != nil {
		t.Fatalf("SessionBelongsToUser missing returned error: %v", err)
	}
	if ok {
		t.Fatalf("SessionBelongsToUser missing = true, want false")
	}
}

func TestRevokeAllUserSessionsExceptKeepsRequestedSession(t *testing.T) {
	ctx := context.Background()
	sm, _ := newTestSessionManager(t)
	keep := createTestSession(t, sm, "user-1")
	revokeA := createTestSession(t, sm, "user-1")
	revokeB := createTestSession(t, sm, "user-1")

	if err := sm.RevokeAllUserSessionsExcept(ctx, "user-1", keep.ID); err != nil {
		t.Fatalf("RevokeAllUserSessionsExcept returned error: %v", err)
	}

	assertSessionValid(t, sm, keep.ID, true)
	assertSessionValid(t, sm, revokeA.ID, false)
	assertSessionValid(t, sm, revokeB.ID, false)

	members, err := sm.rdb.SMembers(ctx, userSessionsKey("user-1")).Result()
	if err != nil {
		t.Fatalf("SMembers returned error: %v", err)
	}
	if len(members) != 1 || members[0] != keep.ID {
		t.Fatalf("user session members = %v, want only %s", members, keep.ID)
	}
}

func TestRevokeAllUserSessionsExceptReturnsJoinedError(t *testing.T) {
	ctx := context.Background()
	sm, _ := newTestSessionManager(t)
	keep := createTestSession(t, sm, "user-1")
	good := createTestSession(t, sm, "user-1")
	bad := createTestSession(t, sm, "user-1")

	if err := sm.rdb.Set(ctx, sessionKey(bad.ID), "not-json", time.Hour).Err(); err != nil {
		t.Fatalf("corrupt session returned error: %v", err)
	}

	err := sm.RevokeAllUserSessionsExcept(ctx, "user-1", keep.ID)
	if err == nil {
		t.Fatalf("RevokeAllUserSessionsExcept returned nil, want joined error")
	}
	if !strings.Contains(err.Error(), bad.ID) {
		t.Fatalf("error = %q, want failing session id %s", err.Error(), bad.ID)
	}

	assertSessionValid(t, sm, keep.ID, true)
	assertSessionValid(t, sm, good.ID, false)
	exists, existsErr := sm.rdb.Exists(ctx, sessionKey(bad.ID)).Result()
	if existsErr != nil {
		t.Fatalf("Exists returned error: %v", existsErr)
	}
	if exists != 1 {
		t.Fatalf("bad session key exists = %d, want 1", exists)
	}
}

func TestRevokeAllUserSessionsExceptRevokesAllWhenKeepIDAbsent(t *testing.T) {
	ctx := context.Background()
	sm, _ := newTestSessionManager(t)
	first := createTestSession(t, sm, "user-1")
	second := createTestSession(t, sm, "user-1")

	if err := sm.RevokeAllUserSessionsExcept(ctx, "user-1", "not-in-set"); err != nil {
		t.Fatalf("RevokeAllUserSessionsExcept returned error: %v", err)
	}

	assertSessionValid(t, sm, first.ID, false)
	assertSessionValid(t, sm, second.ID, false)
	members, err := sm.rdb.SMembers(ctx, userSessionsKey("user-1")).Result()
	if err != nil {
		t.Fatalf("SMembers returned error: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("user session members = %v, want empty", members)
	}
}

func TestRevokeAllUserSessionsExceptSMembersError(t *testing.T) {
	ctx := context.Background()
	sm, mr := newTestSessionManager(t)
	sm.SetOperationTimeout(20 * time.Millisecond)
	mr.Close()

	err := sm.RevokeAllUserSessionsExcept(ctx, "user-1", "keep")
	if err == nil {
		t.Fatalf("RevokeAllUserSessionsExcept returned nil, want error")
	}
	if !strings.Contains(err.Error(), "list user sessions") {
		t.Fatalf("error = %q, want list user sessions wrapper", err.Error())
	}
}

func newTestSessionManager(t *testing.T) (*SessionManager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	sm := NewSessionManager(rdb, 10, time.Hour)
	sm.SetOperationTimeout(500 * time.Millisecond)
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return sm, mr
}

func createTestSession(t *testing.T, sm *SessionManager, userID string) *Session {
	t.Helper()
	session, _, err := sm.Create(context.Background(), userID, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	return session
}

func assertSessionValid(t *testing.T, sm *SessionManager, sessionID string, want bool) {
	t.Helper()
	got, err := sm.IsSessionValid(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("IsSessionValid(%s) returned error: %v", sessionID, err)
	}
	if got != want {
		t.Fatalf("IsSessionValid(%s) = %v, want %v", sessionID, got, want)
	}
}
