package guest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/platform/txscope"
)

// TokenIssuer creates authenticated sessions for users without password.
// Implemented by auth.Service.
type TokenIssuer interface {
	CreateSessionForUser(ctx context.Context, userID uuid.UUID, deviceInfo, ipAddress string) (*TokenResult, error)
}

// TokenResult is the tokens returned after session creation.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	ExpiresIn    int
}

// CallLookup is the minimal read needed to validate a call-scoped guest link at
// redeem time (reject a link once the target call has ended). Satisfied by the
// postgres CallRepo; optional — when nil the ended-call guard is skipped and the
// stale-link case is caught later at JoinCall (CALL_ENDED). (ALK-700)
type CallLookup interface {
	GetByID(ctx context.Context, callID uuid.UUID) (*entity.Call, error)
}

// Service manages guest invite lifecycle.
type Service struct {
	invites    repository.GuestInviteRepository
	grants     repository.GuestAccessRepository
	users      repository.UserRepository
	workspaces repository.WorkspaceRepository
	channels   repository.ChannelRepository
	tokens     TokenIssuer
	calls      CallLookup
	tx         txscope.Manager
}

// NewService creates a new guest service.
func NewService(
	invites repository.GuestInviteRepository,
	grants repository.GuestAccessRepository,
	users repository.UserRepository,
	workspaces repository.WorkspaceRepository,
	channels repository.ChannelRepository,
	tokens TokenIssuer,
) *Service {
	return &Service{
		invites:    invites,
		grants:     grants,
		users:      users,
		workspaces: workspaces,
		channels:   channels,
		tokens:     tokens,
	}
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

// SetCallLookup wires the optional call reader used to reject call-scoped guest
// links once the target call has ended (ALK-700).
func (s *Service) SetCallLookup(calls CallLookup) {
	s.calls = calls
}

// CreateInviteInput holds parameters for creating a guest invite.
type CreateInviteInput struct {
	WorkspaceID uuid.UUID
	CreatedBy   uuid.UUID
	Email       string      // Optional: restrict to specific email
	CallID      *uuid.UUID  // Set for call-scoped guest links (ALK-700)
	ChannelIDs  []uuid.UUID // Channels the guest can access
	MaxUses     int         // 0 = single use
	TTL         time.Duration
}

// CreateInvite generates a new guest invite link.
func (s *Service) CreateInvite(ctx context.Context, input CreateInviteInput) (*entity.GuestInvite, error) {
	// Verify creator is a member.
	member, err := s.workspaces.GetMember(ctx, input.WorkspaceID, input.CreatedBy)
	if err != nil {
		return nil, cerrors.Forbidden("not a workspace member")
	}

	// Only admins/owners/members can create invites (not guests).
	if member.Role == entity.WorkspaceRoleGuest {
		return nil, cerrors.Forbidden("guests cannot create invites")
	}

	// Default TTL: 7 days.
	if input.TTL == 0 {
		input.TTL = 7 * 24 * time.Hour
	}
	if input.TTL < 0 {
		return nil, cerrors.InvalidInput("invite ttl must be positive")
	}
	// Cap at 30 days.
	if input.TTL > 30*24*time.Hour {
		input.TTL = 30 * 24 * time.Hour
	}

	if input.MaxUses == 0 {
		input.MaxUses = 1
	}
	if input.MaxUses < 0 {
		return nil, cerrors.InvalidInput("invite max uses must be positive")
	}

	if err := s.validateInviteChannels(ctx, input.WorkspaceID, input.CreatedBy, input.ChannelIDs); err != nil {
		return nil, err
	}

	token, err := generateToken()
	if err != nil {
		return nil, cerrors.Internal("failed to generate invite token", err)
	}

	now := time.Now()
	invite := &entity.GuestInvite{
		ID:          id.New(),
		WorkspaceID: input.WorkspaceID,
		CreatedBy:   input.CreatedBy,
		Token:       token,
		Email:       input.Email,
		CallID:      input.CallID,
		ChannelIDs:  input.ChannelIDs,
		MaxUses:     input.MaxUses,
		Status:      entity.GuestInviteStatusActive,
		ExpiresAt:   now.Add(input.TTL),
		CreatedAt:   now,
	}

	if err := s.invites.Create(ctx, invite); err != nil {
		slog.ErrorContext(ctx, "failed to create guest invite", "error", err)
		return nil, cerrors.Internal("failed to create invite", err)
	}

	slog.InfoContext(ctx, "guest invite created",
		"invite_id", invite.ID,
		"workspace_id", input.WorkspaceID,
		"created_by", input.CreatedBy,
	)
	return invite, nil
}

// RedeemInviteInput holds parameters for redeeming a guest invite.
type RedeemInviteInput struct {
	Token       string
	Email       string
	DisplayName string
	DeviceInfo  string
	IPAddress   string
}

// RedeemResult contains the user, workspace, and auth tokens after redeeming.
type RedeemResult struct {
	User         *entity.User `json:"user"`
	WorkspaceID  uuid.UUID    `json:"workspace_id"`
	CallID       *uuid.UUID   `json:"call_id,omitempty"`    // Target call for call-scoped guest links (ALK-700)
	SessionID    string       `json:"session_id,omitempty"` // So the web client can install the session cookie (login parity)
	ChannelIDs   []uuid.UUID  `json:"channel_ids"`
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresIn    int          `json:"expires_in"`
}

// RedeemInvite validates an invite token, creates a guest user, and grants non-member access.
func (s *Service) RedeemInvite(ctx context.Context, input RedeemInviteInput) (*RedeemResult, error) {
	invite, err := s.invites.GetByToken(ctx, input.Token)
	if err != nil {
		return nil, cerrors.NotFound("invalid or expired invite")
	}

	if !invite.IsValid() {
		return nil, cerrors.Forbidden("invite is no longer valid")
	}

	// Call-scoped guest link (ALK-700): each redeem mints a DISTINCT ephemeral
	// guest user. Reject the link if the target call has already ended, then
	// synthesize a unique email so the existing create-fresh-user path runs (no
	// email-locked / existing-user / already-a-member merge collides between
	// successive guests on the same reusable link).
	if invite.CallID != nil {
		if s.calls != nil {
			call, callErr := s.calls.GetByID(ctx, *invite.CallID)
			if callErr != nil {
				return nil, cerrors.NotFound("the call for this invite no longer exists")
			}
			if call.Status == entity.CallStatusEnded {
				return nil, cerrors.CallEnded("this call has already ended")
			}
		}
		input.Email = "guest+" + uuid.New().String() + "@guests.invalid"
	}

	// If invite is email-locked, verify the email matches.
	if invite.Email != "" && invite.Email != input.Email {
		return nil, cerrors.Forbidden("this invite is restricted to a specific email")
	}

	if input.DisplayName == "" {
		return nil, cerrors.InvalidInput("display name is required")
	}
	if input.Email == "" {
		return nil, cerrors.InvalidInput("email is required")
	}
	if err := s.validateRedeemChannels(ctx, invite); err != nil {
		return nil, err
	}

	// Check if user already exists.
	existingUser, err := s.users.GetByEmail(ctx, input.Email)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			return nil, cerrors.Internal("failed to check existing user", err)
		}
	}

	var user *entity.User
	createdUser := false
	if existingUser != nil {
		user = existingUser
		// Check if already a member.
		_, err := s.workspaces.GetMember(ctx, invite.WorkspaceID, user.ID)
		if err == nil {
			return nil, cerrors.AlreadyExists("you are already a member of this workspace")
		}
		if !isNotFound(err) {
			return nil, cerrors.Internal("failed to check workspace membership", err)
		}
	} else {
		// Create a new guest user (no password — cannot log in through normal flow).
		now := time.Now()
		user = &entity.User{
			ID:          id.New(),
			Email:       input.Email,
			DisplayName: input.DisplayName,
			AvatarColor: entity.AvatarColorForDisplayName(input.DisplayName),
			Status:      entity.UserStatusActive,
			Locale:      "en",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if s.tx == nil {
			if err := s.users.Create(ctx, user); err != nil {
				return nil, cerrors.Internal("failed to create guest user", err)
			}
		}
		createdUser = true
	}

	// Scope the grant to the invite's call (if any) so a call-scoped guest link
	// confers access to that one call only — never channels or workspace content.
	// Both the tx and non-tx persistence paths below use this single literal, so
	// CallID lands in either path. (unified guest link)
	grant := &entity.GuestAccessGrant{
		ID:          id.New(),
		InviteID:    invite.ID,
		WorkspaceID: invite.WorkspaceID,
		UserID:      user.ID,
		// Non-nil empty slice when the invite has no channels (call-scoped links):
		// guest_access_grants.channel_ids is NOT NULL and a nil []uuid.UUID binds as
		// SQL NULL, which 500s the redeem (surfaced as a guest "Network error").
		ChannelIDs: append(make([]uuid.UUID, 0, len(invite.ChannelIDs)), invite.ChannelIDs...),
		CallID:     invite.CallID,
		ExpiresAt:  invite.ExpiresAt,
		CreatedAt:  time.Now().UTC(),
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			userRepo := s.users
			if scope.Users() != nil {
				userRepo = scope.Users()
			}
			inviteRepo := s.invites
			if scope.Invites() != nil {
				inviteRepo = scope.Invites()
			}
			grantRepo := s.grants
			if scope.GuestGrants() != nil {
				grantRepo = scope.GuestGrants()
			}
			if createdUser {
				if err := userRepo.Create(ctx, user); err != nil {
					return err
				}
			}
			if grantRepo != nil {
				if err := grantRepo.CreateGrant(ctx, grant); err != nil {
					return err
				}
			}
			return inviteRepo.IncrementUseCount(ctx, invite.ID)
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeAlreadyExists {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to redeem guest invite", err)
		}
	} else {
		if s.grants != nil {
			if err := s.grants.CreateGrant(ctx, grant); err != nil {
				if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeAlreadyExists {
					return nil, appErr
				}
				return nil, cerrors.Internal("failed to create guest access grant", err)
			}
		}

		// Increment usage counter.
		if err := s.invites.IncrementUseCount(ctx, invite.ID); err != nil {
			slog.ErrorContext(ctx, "failed to increment invite use count", "invite_id", invite.ID, "error", err)
		}
	}

	// Issue authentication tokens so the guest can use the API immediately.
	var accessToken, refreshToken, sessionID string
	var expiresIn int
	if s.tokens != nil {
		tokenResult, err := s.tokens.CreateSessionForUser(ctx, user.ID, input.DeviceInfo, input.IPAddress)
		if err != nil {
			slog.ErrorContext(ctx, "failed to create guest session", "user_id", user.ID, "error", err)
			return nil, cerrors.Internal("failed to create session for guest", err)
		}
		accessToken = tokenResult.AccessToken
		refreshToken = tokenResult.RefreshToken
		sessionID = tokenResult.SessionID
		expiresIn = tokenResult.ExpiresIn
	}

	slog.InfoContext(ctx, "guest invite redeemed",
		"invite_id", invite.ID,
		"user_id", user.ID,
		"workspace_id", invite.WorkspaceID,
	)

	return &RedeemResult{
		User:        user,
		WorkspaceID: invite.WorkspaceID,
		CallID:      invite.CallID,
		SessionID:   sessionID,
		// Non-nil empty slice when the invite has no channels (call-scoped
		// links): a nil []uuid.UUID marshals as JSON `null`, which the web
		// client's strict channel_ids schema (z.array) rejects with a ZodError
		// surfaced as a guest "Network error" — never letting the guest reach
		// the call. Mirrors the guest_access_grants insert guard above.
		ChannelIDs:   append(make([]uuid.UUID, 0, len(invite.ChannelIDs)), invite.ChannelIDs...),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    expiresIn,
	}, nil
}

// ResolveInviteResult is the public, read-only projection of a guest invite for
// the unified /join/<token> link. It NEVER returns an error for an invalid /
// expired / used / revoked token — Valid is false instead — so the FE can show
// a friendly state. IsWorkspaceMember is reported only when an authenticated
// member user is supplied. (unified guest link)
type ResolveInviteResult struct {
	Valid             bool
	WorkspaceID       uuid.UUID
	CallID            *uuid.UUID
	CallTitle         string
	ExpiresAt         time.Time
	IsWorkspaceMember bool
}

// ResolveInvite looks up an invite token for the unified link page. optionalUserID
// may be uuid.Nil for an anonymous visitor; when it is a real user, the result
// reports whether that user is already a member of the invite's workspace (so
// the FE can route a member straight to the call deep-link instead of the guest
// name-entry form).
func (s *Service) ResolveInvite(ctx context.Context, token string, optionalUserID uuid.UUID) (*ResolveInviteResult, error) {
	if token == "" {
		return &ResolveInviteResult{Valid: false}, nil
	}

	invite, err := s.invites.GetByToken(ctx, token)
	if err != nil {
		// A genuine not-found token is an expected invalid state, not a failure.
		// Any OTHER error (transient DB / internal) must surface so the handler
		// returns 5xx — collapsing it to valid:false would mask the outage and
		// downgrade a member's good link to the guest entry form. (unified guest link)
		if isNotFound(err) {
			return &ResolveInviteResult{Valid: false}, nil
		}
		return nil, err
	}
	if !invite.IsValid() {
		return &ResolveInviteResult{Valid: false}, nil
	}

	// Mirror RedeemInvite's call-status pre-check: a call-scoped invite whose
	// target call has ended (or no longer exists) can never grant access, so it
	// must resolve as invalid with NO call/workspace metadata leaked — otherwise
	// the FE shows a joinable state for a dead link. Only a transient lookup error
	// surfaces as an error. (unified guest link)
	var callTitle string
	if invite.CallID != nil && s.calls != nil {
		call, callErr := s.calls.GetByID(ctx, *invite.CallID)
		if callErr != nil {
			if isNotFound(callErr) {
				return &ResolveInviteResult{Valid: false}, nil
			}
			return nil, callErr
		}
		if call == nil || call.Status == entity.CallStatusEnded {
			return &ResolveInviteResult{Valid: false}, nil
		}
		callTitle = call.Title
	}

	result := &ResolveInviteResult{
		Valid:       true,
		WorkspaceID: invite.WorkspaceID,
		CallID:      invite.CallID,
		CallTitle:   callTitle,
		ExpiresAt:   invite.ExpiresAt,
	}

	// Membership is only meaningful (and only disclosed) for an authenticated
	// user — anonymous visitors never get is_workspace_member: true.
	if optionalUserID != uuid.Nil {
		if _, mErr := s.workspaces.GetMember(ctx, invite.WorkspaceID, optionalUserID); mErr == nil {
			result.IsWorkspaceMember = true
		}
	}

	return result, nil
}

// RevokeInvite revokes an active invite.
func (s *Service) RevokeInvite(ctx context.Context, inviteID, actorID uuid.UUID) error {
	invite, err := s.invites.GetByID(ctx, inviteID)
	if err != nil {
		return cerrors.NotFound("invite not found")
	}

	// Verify actor is admin/owner of the workspace.
	member, err := s.workspaces.GetMember(ctx, invite.WorkspaceID, actorID)
	if err != nil {
		return cerrors.Forbidden("not a workspace member")
	}
	if member.Role != entity.WorkspaceRoleOwner && member.Role != entity.WorkspaceRoleAdmin {
		return cerrors.Forbidden("admin access required to revoke invites")
	}

	return s.invites.Revoke(ctx, inviteID)
}

// ListInvites returns all invites for a workspace.
func (s *Service) ListInvites(ctx context.Context, workspaceID, actorID uuid.UUID) ([]entity.GuestInvite, error) {
	member, err := s.workspaces.GetMember(ctx, workspaceID, actorID)
	if err != nil {
		return nil, cerrors.Forbidden("not a workspace member")
	}
	if member.Role == entity.WorkspaceRoleGuest {
		return nil, cerrors.Forbidden("guests cannot view invites")
	}

	return s.invites.ListByWorkspace(ctx, workspaceID)
}

func (s *Service) validateInviteChannels(ctx context.Context, workspaceID, actorID uuid.UUID, channelIDs []uuid.UUID) error {
	seen := make(map[uuid.UUID]struct{}, len(channelIDs))
	for _, chID := range channelIDs {
		if chID == uuid.Nil {
			return cerrors.InvalidInput("channel id is required")
		}
		if _, ok := seen[chID]; ok {
			return cerrors.InvalidInput("duplicate invite channel")
		}
		seen[chID] = struct{}{}

		ch, err := s.channels.GetByID(ctx, chID)
		if err != nil {
			if isNotFound(err) {
				return cerrors.NotFound("invite channel not found")
			}
			return cerrors.Internal("failed to load invite channel", err)
		}
		if ch.WorkspaceID == nil || *ch.WorkspaceID != workspaceID {
			return cerrors.Forbidden("invite channel must belong to the workspace")
		}
		if ch.Archived {
			return cerrors.InvalidInput("cannot invite guests to archived channels")
		}
		if ch.Type == entity.ChannelTypeDM || ch.Type == entity.ChannelTypeGroupDM {
			return cerrors.InvalidInput("guest invites cannot target direct message channels")
		}
		if ch.Type == entity.ChannelTypePrivate {
			if _, err := s.channels.GetMember(ctx, chID, actorID); err != nil {
				if isNotFound(err) {
					return cerrors.Forbidden("you must be a member of private invite channels")
				}
				return cerrors.Internal("failed to verify channel membership", err)
			}
		}
	}
	return nil
}

func (s *Service) validateRedeemChannels(ctx context.Context, invite *entity.GuestInvite) error {
	seen := make(map[uuid.UUID]struct{}, len(invite.ChannelIDs))
	for _, chID := range invite.ChannelIDs {
		if chID == uuid.Nil {
			return cerrors.InvalidInput("invite contains an invalid channel")
		}
		if _, ok := seen[chID]; ok {
			return cerrors.InvalidInput("invite contains duplicate channels")
		}
		seen[chID] = struct{}{}

		ch, err := s.channels.GetByID(ctx, chID)
		if err != nil {
			if isNotFound(err) {
				return cerrors.NotFound("invite channel not found")
			}
			return cerrors.Internal("failed to load invite channel", err)
		}
		if ch.WorkspaceID == nil || *ch.WorkspaceID != invite.WorkspaceID || ch.Archived {
			return cerrors.Forbidden("invite contains an inaccessible channel")
		}
		if ch.Type == entity.ChannelTypeDM || ch.Type == entity.ChannelTypeGroupDM {
			return cerrors.InvalidInput("invite contains an invalid channel")
		}
	}
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == cerrors.CodeNotFound
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
