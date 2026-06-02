package call

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/security/guestaccess"
)

// TestJoinCallEntryModes covers the entry-mode admission matrix for a non-guest
// member (#4): open joins directly, manual_admit lands in the waiting room, and
// password mode admits on the correct password while rejecting wrong/missing.
func TestJoinCallEntryModes(t *testing.T) {
	const password = "s3cret-pass"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash setup failed: %v", err)
	}

	cases := []struct {
		name       string
		settings   entity.CallSettings
		hash       string
		password   string
		wantStatus entity.ParticipantStatus
		wantCode   cerrors.Code
	}{
		{
			name:       "open joins directly",
			settings:   entity.CallSettings{EntryMode: entity.EntryModeOpen},
			wantStatus: entity.ParticipantStatusConnected,
		},
		{
			name:       "manual_admit waits",
			settings:   entity.CallSettings{EntryMode: entity.EntryModeManualAdmit},
			wantStatus: entity.ParticipantStatusWaiting,
		},
		{
			name:       "legacy waiting_room derives manual_admit",
			settings:   entity.CallSettings{WaitingRoom: true},
			wantStatus: entity.ParticipantStatusWaiting,
		},
		{
			name:       "password correct admits",
			settings:   entity.CallSettings{EntryMode: entity.EntryModePassword},
			hash:       string(hash),
			password:   password,
			wantStatus: entity.ParticipantStatusConnected,
		},
		{
			name:     "password wrong forbidden",
			settings: entity.CallSettings{EntryMode: entity.EntryModePassword},
			hash:     string(hash),
			password: "nope",
			wantCode: cerrors.CodeForbidden,
		},
		{
			name:     "password missing unauthorized",
			settings: entity.CallSettings{EntryMode: entity.EntryModePassword},
			hash:     string(hash),
			password: "",
			wantCode: cerrors.CodeUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workspaceID := uuid.New()
			callID := uuid.New()
			hostID := uuid.New()
			joinerID := uuid.New()

			workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
				{workspaceID, hostID}:   {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
				{workspaceID, joinerID}: {WorkspaceID: workspaceID, UserID: joinerID, Role: entity.WorkspaceRoleMember},
			}}
			calls := &fakeCallRepo{
				calls: map[uuid.UUID]*entity.Call{
					callID: {
						ID:               callID,
						WorkspaceID:      workspaceID,
						Type:             entity.CallTypeGroup,
						Status:           entity.CallStatusActive,
						Settings:         tc.settings,
						JoinPasswordHash: tc.hash,
					},
				},
				participants: map[[2]uuid.UUID]*entity.CallParticipant{
					{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
				},
			}
			svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

			participant, err := svc.JoinCall(ctx, workspaceID, callID, joinerID, tc.password)
			if tc.wantCode != "" {
				if !hasCode(err, tc.wantCode) {
					t.Fatalf("JoinCall error = %v, want code %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("JoinCall error = %v, want nil", err)
			}
			if participant.Status != tc.wantStatus {
				t.Fatalf("JoinCall status = %s, want %s", participant.Status, tc.wantStatus)
			}
		})
	}
}

// TestJoinCallGuestBypassesPasswordButWaits asserts a guest on a password-mode
// call is held in the waiting room without supplying the password — the
// one-time link is the host's approval (#4 D5, ALK-700).
func TestJoinCallGuestBypassesPasswordButWaits(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	guestID := uuid.New()
	channelID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:               callID,
				WorkspaceID:      workspaceID,
				ChannelID:        &channelID,
				Type:             entity.CallTypeGroup,
				Status:           entity.CallStatusActive,
				Settings:         entity.CallSettings{EntryMode: entity.EntryModePassword},
				JoinPasswordHash: "irrelevant-for-guest",
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
	}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), guests, nil)

	participant, err := svc.JoinCall(ctx, workspaceID, callID, guestID, "")
	if err != nil {
		t.Fatalf("JoinCall guest on password call error = %v, want nil", err)
	}
	if participant.Status != entity.ParticipantStatusWaiting {
		t.Fatalf("guest status = %s, want waiting", participant.Status)
	}
}

// TestStartCallNormalisesEntryMode covers the creation-time normalisation (#4):
// callers that omit entry_mode derive it from waiting_room (backwards
// compatible), an explicit password mode hashes the supplied password, and
// password mode without a password is rejected.
func TestStartCallNormalisesEntryMode(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	hostID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}

	newSvc := func() *Service {
		calls := &fakeCallRepo{
			calls:        map[uuid.UUID]*entity.Call{},
			participants: map[[2]uuid.UUID]*entity.CallParticipant{},
		}
		return NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	}

	t.Run("omitted entry_mode with no waiting room derives open", func(t *testing.T) {
		call, err := newSvc().StartCall(ctx, workspaceID, hostID, entity.CallTypeGroup, "g", nil, entity.CallSettings{}, "")
		if err != nil {
			t.Fatalf("StartCall error = %v", err)
		}
		if call.Settings.EntryMode != entity.EntryModeOpen {
			t.Fatalf("entry_mode = %s, want open", call.Settings.EntryMode)
		}
		if call.Settings.WaitingRoom {
			t.Fatalf("waiting_room = true, want false")
		}
	})

	t.Run("legacy waiting_room derives manual_admit", func(t *testing.T) {
		call, err := newSvc().StartCall(ctx, workspaceID, hostID, entity.CallTypeGroup, "g", nil, entity.CallSettings{WaitingRoom: true}, "")
		if err != nil {
			t.Fatalf("StartCall error = %v", err)
		}
		if call.Settings.EntryMode != entity.EntryModeManualAdmit {
			t.Fatalf("entry_mode = %s, want manual_admit", call.Settings.EntryMode)
		}
	})

	t.Run("password mode hashes the supplied password", func(t *testing.T) {
		call, err := newSvc().StartCall(ctx, workspaceID, hostID, entity.CallTypeGroup, "g", nil, entity.CallSettings{EntryMode: entity.EntryModePassword}, "let-me-in")
		if err != nil {
			t.Fatalf("StartCall error = %v", err)
		}
		if call.Settings.EntryMode != entity.EntryModePassword {
			t.Fatalf("entry_mode = %s, want password", call.Settings.EntryMode)
		}
		if call.Settings.WaitingRoom {
			t.Fatalf("waiting_room = true, want false for password mode")
		}
		if call.JoinPasswordHash == "" {
			t.Fatalf("JoinPasswordHash is empty, want a bcrypt hash")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(call.JoinPasswordHash), []byte("let-me-in")); err != nil {
			t.Fatalf("stored hash does not match password: %v", err)
		}
	})

	t.Run("password mode without a password is rejected", func(t *testing.T) {
		_, err := newSvc().StartCall(ctx, workspaceID, hostID, entity.CallTypeGroup, "g", nil, entity.CallSettings{EntryMode: entity.EntryModePassword}, "")
		if !hasCode(err, cerrors.CodeInvalidInput) {
			t.Fatalf("StartCall password-without-password error = %v, want INVALID_INPUT", err)
		}
	})
}
