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

// Service manages guest invite lifecycle.
type Service struct {
	invites    repository.GuestInviteRepository
	grants     repository.GuestAccessRepository
	users      repository.UserRepository
	workspaces repository.WorkspaceRepository
	channels   repository.ChannelRepository
	tokens     TokenIssuer
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

// CreateInviteInput holds parameters for creating a guest invite.
type CreateInviteInput struct {
	WorkspaceID uuid.UUID
	CreatedBy   uuid.UUID
	Email       string      // Optional: restrict to specific email
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

	grant := &entity.GuestAccessGrant{
		ID:          id.New(),
		InviteID:    invite.ID,
		WorkspaceID: invite.WorkspaceID,
		UserID:      user.ID,
		ChannelIDs:  append([]uuid.UUID(nil), invite.ChannelIDs...),
		ExpiresAt:   invite.ExpiresAt,
		CreatedAt:   time.Now().UTC(),
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
	var accessToken, refreshToken string
	var expiresIn int
	if s.tokens != nil {
		tokenResult, err := s.tokens.CreateSessionForUser(ctx, user.ID, input.DeviceInfo, input.IPAddress)
		if err != nil {
			slog.ErrorContext(ctx, "failed to create guest session", "user_id", user.ID, "error", err)
			return nil, cerrors.Internal("failed to create session for guest", err)
		}
		accessToken = tokenResult.AccessToken
		refreshToken = tokenResult.RefreshToken
		expiresIn = tokenResult.ExpiresIn
	}

	slog.InfoContext(ctx, "guest invite redeemed",
		"invite_id", invite.ID,
		"user_id", user.ID,
		"workspace_id", invite.WorkspaceID,
	)

	return &RedeemResult{
		User:         user,
		WorkspaceID:  invite.WorkspaceID,
		ChannelIDs:   invite.ChannelIDs,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    expiresIn,
	}, nil
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
		if ch.WorkspaceID != workspaceID {
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
		if ch.WorkspaceID != invite.WorkspaceID || ch.Archived {
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
