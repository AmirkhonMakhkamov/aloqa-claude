package call

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// turnTestFixtures bundles ids + repos so tests can mutate svc.media before invoking.
type turnTestFixtures struct {
	workspaceID uuid.UUID
	callID      uuid.UUID
	userID      uuid.UUID
}

func newTestServiceWithConnectedParticipant(t *testing.T) (*Service, turnTestFixtures) {
	t.Helper()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	return svc, turnTestFixtures{workspaceID: workspaceID, callID: callID, userID: userID}
}

func newTestServiceWithCallButNoParticipant(t *testing.T) (*Service, turnTestFixtures) {
	t.Helper()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	return svc, turnTestFixtures{workspaceID: workspaceID, callID: callID, userID: userID}
}

func TestIssueTurnCredentials_StunOnly_WhenNoTurnConfigured(t *testing.T) {
	svc, fx := newTestServiceWithConnectedParticipant(t)
	svc.media.TURNURLs = nil
	svc.media.TURNUsername = ""
	svc.media.TURNCredential = ""
	svc.media.TURNSecret = ""

	creds, err := svc.IssueTurnCredentials(context.Background(), fx.workspaceID, fx.callID, fx.userID)
	if err != nil {
		t.Fatalf("IssueTurnCredentials err = %v, want nil", err)
	}
	if len(creds.URLs) != 1 || creds.URLs[0] != "stun:stun.l.google.com:19302" {
		t.Fatalf("URLs = %+v, want [stun:stun.l.google.com:19302]", creds.URLs)
	}
	if creds.Username != "" {
		t.Fatalf("Username = %q, want empty", creds.Username)
	}
	if creds.Credential != "" {
		t.Fatalf("Credential = %q, want empty", creds.Credential)
	}
	if creds.TTL != 300 {
		t.Fatalf("TTL = %d, want 300", creds.TTL)
	}
}

func TestIssueTurnCredentials_StunOnly_JsonMarshalingPreservesEmptyStrings(t *testing.T) {
	t.Parallel()
	creds := &TurnCredentials{
		URLs:       []string{"stun:stun.l.google.com:19302"},
		Username:   "",
		Credential: "",
		TTL:        300,
	}
	body, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("Marshal err = %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `"username":""`) {
		t.Fatalf("body = %s, missing \"username\":\"\" (FE Zod schema requires non-optional strings)", got)
	}
	if !strings.Contains(got, `"credential":""`) {
		t.Fatalf("body = %s, missing \"credential\":\"\"", got)
	}
}

func TestIssueTurnCredentials_NonParticipant_403(t *testing.T) {
	svc, fx := newTestServiceWithCallButNoParticipant(t)
	svc.media.TURNURLs = nil // STUN-only mode

	_, err := svc.IssueTurnCredentials(context.Background(), fx.workspaceID, fx.callID, fx.userID)
	if err == nil {
		t.Fatal("IssueTurnCredentials err = nil, want FORBIDDEN")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok {
		t.Fatalf("err = %T (%v), want *cerrors.AppError", err, err)
	}
	if appErr.Code != cerrors.CodeForbidden {
		t.Fatalf("code = %q, want %q (access check must run BEFORE STUN-only fallback)", appErr.Code, cerrors.CodeForbidden)
	}
}

// Partial-TURN misconfig — URLs set, neither static auth nor HMAC secret —
// surfaces a 500 (permanent misconfig) instead of silently degrading to a
// broken TURN response with empty credentials.
func TestIssueTurnCredentials_PartialMisconfig_URLsSetButNoAuth_Returns500(t *testing.T) {
	svc, fx := newTestServiceWithConnectedParticipant(t)
	svc.media.TURNURLs = []string{"turn:80.240.27.72:3478?transport=udp"}
	svc.media.TURNUsername = ""
	svc.media.TURNCredential = ""
	svc.media.TURNSecret = ""

	_, err := svc.IssueTurnCredentials(context.Background(), fx.workspaceID, fx.callID, fx.userID)
	if err == nil {
		t.Fatal("IssueTurnCredentials err = nil, want INTERNAL")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok {
		t.Fatalf("err = %T (%v), want *cerrors.AppError", err, err)
	}
	if appErr.Code != cerrors.CodeInternal {
		t.Fatalf("code = %q, want %q", appErr.Code, cerrors.CodeInternal)
	}
	if !strings.Contains(appErr.Message, "turn service partially configured") {
		t.Fatalf("message = %q, want to contain 'turn service partially configured'", appErr.Message)
	}
}
