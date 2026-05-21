package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/pkg/validate"
)

// Service handles authentication, registration, and token management.
type Service struct {
	users         repository.UserRepository
	workspaces    repository.WorkspaceRepository
	sessions      *SessionManager
	jwtSecret     []byte
	accessTTL     time.Duration
	refreshTTL    time.Duration
	newUserSeeder NewUserSeeder
	search        interface {
		IndexUser(ctx context.Context, workspaceID, userID uuid.UUID, displayName, email string, createdAt, updatedAt time.Time) error
	}
}

type NewUserSeeder interface {
	SeedNewUser(ctx context.Context, user *entity.User) error
}

// NewService creates a new auth service with Redis-backed session management.
func NewService(
	users repository.UserRepository,
	workspaces repository.WorkspaceRepository,
	rdb *redis.Client,
	jwtSecret []byte,
	accessTTL, refreshTTL time.Duration,
	search interface {
		IndexUser(ctx context.Context, workspaceID, userID uuid.UUID, displayName, email string, createdAt, updatedAt time.Time) error
	},
) *Service {
	return &Service{
		users:      users,
		workspaces: workspaces,
		sessions:   NewSessionManager(rdb, 5, refreshTTL),
		jwtSecret:  jwtSecret,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		search:     search,
	}
}

func (s *Service) SetNewUserSeeder(seeder NewUserSeeder) {
	if s == nil {
		return
	}
	s.newUserSeeder = seeder
}

func (s *Service) SetSessionNotifier(notifier SessionEventNotifier) {
	if s == nil || s.sessions == nil {
		return
	}
	s.sessions.SetNotifier(notifier)
}

func (s *Service) SetSessionOperationTimeout(timeout time.Duration) {
	if s == nil || s.sessions == nil {
		return
	}
	s.sessions.SetOperationTimeout(timeout)
}

// RunSessionTouchWorker starts the background session-touch batch worker.
// It must be running for DeferTouch calls (from ValidateAccessToken) to take
// effect. Exits when ctx is cancelled.
func (s *Service) RunSessionTouchWorker(ctx context.Context, interval time.Duration) {
	if s == nil || s.sessions == nil {
		return
	}
	s.sessions.RunTouchWorker(ctx, interval)
}

// --- Input validation structs ---

// RegisterInput holds validated registration parameters.
type RegisterInput struct {
	Email       string `validate:"required,email,max=255"`
	Password    string `validate:"required,min=8,max=128"`
	DisplayName string `validate:"required,min=1,max=100"`
}

// LoginInput holds validated login parameters.
type LoginInput struct {
	Email    string `validate:"required,email"`
	Password string `validate:"required"`
}

// LoginResult holds the result of a successful login.
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	ExpiresIn    int
}

type UpdateProfileInput struct {
	DisplayName *string
	AvatarURL   *string
	Position    *string
	Department  *string
	Locale      *string
}

type CreateWorkspaceInput struct {
	Name      string `validate:"required,min=1,max=120"`
	Slug      string `validate:"omitempty,min=3,max=80"`
	AvatarURL string `validate:"omitempty,max=2048"`
}

type workspaceOwnerCreator interface {
	CreateWithOwner(ctx context.Context, ws *entity.Workspace, owner *entity.WorkspaceMember) error
}

// validatePasswordPolicy enforces complexity requirements:
// at least 8 chars, one uppercase, one lowercase, one digit, one special char.
func validatePasswordPolicy(password string) error {
	if len(password) < 8 {
		return cerrors.InvalidInput("password must be at least 8 characters")
	}
	if len(password) > 128 {
		return cerrors.InvalidInput("password must be at most 128 characters")
	}

	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			hasSpecial = true
		}
	}

	if !hasUpper {
		return cerrors.InvalidInput("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return cerrors.InvalidInput("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return cerrors.InvalidInput("password must contain at least one digit")
	}
	if !hasSpecial {
		return cerrors.InvalidInput("password must contain at least one special character")
	}
	return nil
}

// hashPassword pre-hashes with SHA-256 then bcrypt to avoid the 72-byte
// truncation limit of bcrypt. This ensures all password bytes contribute
// to the hash regardless of length.
func hashPassword(password string) (string, error) {
	sha := sha256.Sum256([]byte(password))
	preHash := hex.EncodeToString(sha[:])
	// cost 12 is the 2026 baseline for bcrypt on commodity hardware; bcrypt.DefaultCost (10)
	// is too low. Increase only if you've benchmarked your hot path.
	hash, err := bcrypt.GenerateFromPassword([]byte(preHash), 12)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// verifyPassword compares a password against its stored hash using the same
// SHA-256 pre-hash before bcrypt comparison.
func verifyPassword(password, storedHash string) error {
	sha := sha256.Sum256([]byte(password))
	preHash := hex.EncodeToString(sha[:])
	return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(preHash))
}

// Register creates a new user account with a hashed password.
func (s *Service) Register(ctx context.Context, email, password, displayName string) (*entity.User, error) {
	input := RegisterInput{
		Email:       email,
		Password:    password,
		DisplayName: displayName,
	}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	if err := validatePasswordPolicy(password); err != nil {
		return nil, err
	}

	// Check if a user with this email already exists.
	existing, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			slog.ErrorContext(ctx, "failed to check existing user", "error", err)
			return nil, cerrors.Internal("failed to check existing user", err)
		}
	}
	if existing != nil {
		return nil, cerrors.AlreadyExists("user with this email already exists")
	}

	passwordHash, err := hashPassword(password)
	if err != nil {
		slog.ErrorContext(ctx, "failed to hash password", "error", err)
		return nil, cerrors.Internal("failed to hash password", err)
	}

	now := time.Now()
	user := &entity.User{
		ID:           id.New(),
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		Status:       entity.UserStatusActive,
		Locale:       "en",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.users.Create(ctx, user); err != nil {
		slog.ErrorContext(ctx, "failed to create user", "error", err)
		return nil, cerrors.Internal("failed to create user", err)
	}

	if s.newUserSeeder != nil {
		if err := s.newUserSeeder.SeedNewUser(ctx, user); err != nil {
			slog.ErrorContext(ctx, "failed to seed demo workspace for new user", "user_id", user.ID, "error", err)
		}
	}

	slog.InfoContext(ctx, "user registered", "user_id", user.ID)
	return user, nil
}

func (s *Service) GetUser(ctx context.Context, userID uuid.UUID) (*entity.User, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("user not found")
		}
		slog.ErrorContext(ctx, "failed to fetch user", "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to fetch user", err)
	}
	return user, nil
}

func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, input UpdateProfileInput) (*entity.User, error) {
	if input.DisplayName == nil && input.AvatarURL == nil && input.Position == nil && input.Department == nil && input.Locale == nil {
		return nil, cerrors.InvalidInput("at least one profile field must be provided")
	}

	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	if input.DisplayName != nil {
		name := strings.TrimSpace(*input.DisplayName)
		if name == "" {
			return nil, cerrors.InvalidInput("display_name cannot be empty")
		}
		if len(name) > 100 {
			return nil, cerrors.InvalidInput("display_name must be at most 100 characters")
		}
		user.DisplayName = name
	}
	if input.AvatarURL != nil {
		avatarURL := strings.TrimSpace(*input.AvatarURL)
		if len(avatarURL) > 2048 {
			return nil, cerrors.InvalidInput("avatar_url must be at most 2048 characters")
		}
		user.AvatarURL = avatarURL
	}
	if input.Position != nil {
		position := strings.TrimSpace(*input.Position)
		if len(position) > 200 {
			return nil, cerrors.InvalidInput("position must be at most 200 characters")
		}
		if position == "" {
			user.Position = nil
		} else {
			user.Position = &position
		}
	}
	if input.Department != nil {
		department := strings.TrimSpace(*input.Department)
		if len(department) > 120 {
			return nil, cerrors.InvalidInput("department must be at most 120 characters")
		}
		if department == "" {
			user.Department = nil
		} else {
			user.Department = &department
		}
	}
	if input.Locale != nil {
		locale := strings.TrimSpace(*input.Locale)
		if len(locale) < 2 || len(locale) > 16 {
			return nil, cerrors.InvalidInput("locale must be between 2 and 16 characters")
		}
		user.Locale = locale
	}
	user.UpdatedAt = time.Now().UTC()

	if err := s.users.Update(ctx, user); err != nil {
		slog.ErrorContext(ctx, "failed to update user profile", "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to update user profile", err)
	}
	if s.search != nil {
		workspaces, err := s.workspaces.ListByUser(ctx, userID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list workspaces for user search reindex", "user_id", userID, "error", err)
		} else {
			for _, workspace := range workspaces {
				if err := s.search.IndexUser(ctx, workspace.ID, user.ID, user.DisplayName, user.Email, user.CreatedAt, user.UpdatedAt); err != nil {
					slog.ErrorContext(ctx, "failed to enqueue user search reindex", "workspace_id", workspace.ID, "user_id", user.ID, "error", err)
				}
			}
		}
	}

	return user, nil
}

func (s *Service) UpdateSavedMessagesMode(ctx context.Context, userID uuid.UUID, mode entity.SavedMessagesMode) (*entity.User, error) {
	if mode != entity.SavedMessagesModePerWorkspace && mode != entity.SavedMessagesModeGlobal {
		return nil, cerrors.InvalidInput("invalid saved_messages_mode")
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	user.SavedMessagesMode = mode
	user.UpdatedAt = time.Now().UTC()
	if err := s.users.Update(ctx, user); err != nil {
		slog.ErrorContext(ctx, "failed to update saved messages mode", "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to update saved messages mode", err)
	}
	return user, nil
}

func (s *Service) ListWorkspaces(ctx context.Context, userID uuid.UUID) ([]entity.Workspace, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	if _, err := s.GetOrCreatePersonalWorkspace(ctx, userID); err != nil {
		return nil, err
	}

	workspaces, err := s.workspaces.ListByUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list user workspaces", "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to list workspaces", err)
	}
	for i := range workspaces {
		workspaces[i].Kind = s.workspaceKind(workspaces[i], userID)
	}
	return workspaces, nil
}

// ListWorkspaceMembers returns the paginated list of members of the given
// workspace, callable by any member of that workspace (not admin-gated).
// This is the read-only sibling of admin.Service.ListMembers, intended for
// clients that need to render member avatars / open a DM with another member.
func (s *Service) ListWorkspaceMembers(ctx context.Context, workspaceID, actorID uuid.UUID, p pagination.Params) ([]entity.WorkspaceMember, error) {
	if _, err := s.workspaces.GetMember(ctx, workspaceID, actorID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("user is not a member of this workspace")
		}
		return nil, cerrors.Internal("failed to verify workspace membership", err)
	}
	return s.workspaces.ListMembers(ctx, workspaceID, p)
}

func (s *Service) GetWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) (*entity.Workspace, error) {
	if _, err := s.workspaces.GetMember(ctx, workspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("user is not a member of this workspace")
		}
		return nil, cerrors.Internal("failed to verify workspace membership", err)
	}

	workspace, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("workspace not found")
		}
		return nil, cerrors.Internal("failed to fetch workspace", err)
	}
	workspace.Kind = s.workspaceKind(*workspace, userID)
	return workspace, nil
}

func (s *Service) GetOrCreatePersonalWorkspace(ctx context.Context, userID uuid.UUID) (*entity.Workspace, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("account is not active")
	}

	slug := personalWorkspaceSlug(userID)
	workspace, err := s.workspaces.GetBySlug(ctx, slug)
	if err == nil {
		workspace.Kind = entity.WorkspaceKindPersonal
		if _, memberErr := s.workspaces.GetMember(ctx, workspace.ID, userID); memberErr != nil {
			if appErr, ok := cerrors.AsAppError(memberErr); ok && appErr.Code == cerrors.CodeNotFound {
				member := &entity.WorkspaceMember{
					ID:          id.New(),
					WorkspaceID: workspace.ID,
					UserID:      userID,
					Role:        entity.WorkspaceRoleOwner,
					JoinedAt:    time.Now().UTC(),
				}
				if err := s.workspaces.AddMember(ctx, member); err != nil {
					return nil, cerrors.Internal("failed to restore personal workspace membership", err)
				}
				if s.search != nil {
					if err := s.search.IndexUser(ctx, workspace.ID, user.ID, user.DisplayName, user.Email, user.CreatedAt, user.UpdatedAt); err != nil {
						slog.ErrorContext(ctx, "failed to enqueue restored personal workspace user search index", "workspace_id", workspace.ID, "user_id", user.ID, "error", err)
					}
				}
			} else {
				return nil, cerrors.Internal("failed to verify personal workspace membership", memberErr)
			}
		}
		return workspace, nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return nil, cerrors.Internal("failed to fetch personal workspace", err)
	}

	return s.createWorkspaceWithKind(ctx, user, CreateWorkspaceInput{
		Name: user.DisplayName + "'s Space",
		Slug: slug,
	}, entity.WorkspaceKindPersonal)
}

func (s *Service) GetOrCreatePersonalWorkspaceID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	workspace, err := s.GetOrCreatePersonalWorkspace(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	return workspace.ID, nil
}

func (s *Service) CreateWorkspace(ctx context.Context, userID uuid.UUID, input CreateWorkspaceInput) (*entity.Workspace, error) {
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("account is not active")
	}

	return s.createWorkspaceWithKind(ctx, user, input, entity.WorkspaceKindOrganization)
}

func (s *Service) createWorkspaceWithKind(ctx context.Context, user *entity.User, input CreateWorkspaceInput, kind entity.WorkspaceKind) (*entity.Workspace, error) {
	if kind == "" {
		kind = entity.WorkspaceKindOrganization
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, cerrors.InvalidInput("name is required")
	}

	baseSlug := strings.TrimSpace(input.Slug)
	if baseSlug == "" {
		baseSlug = name
	}
	slug, err := s.uniqueWorkspaceSlug(ctx, baseSlug)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	workspace := &entity.Workspace{
		ID:        id.New(),
		Name:      name,
		Slug:      slug,
		Kind:      kind,
		AvatarURL: strings.TrimSpace(input.AvatarURL),
		CreatedBy: user.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	owner := &entity.WorkspaceMember{
		ID:          id.New(),
		WorkspaceID: workspace.ID,
		UserID:      user.ID,
		Role:        entity.WorkspaceRoleOwner,
		JoinedAt:    now,
	}

	if creator, ok := s.workspaces.(workspaceOwnerCreator); ok {
		if err := creator.CreateWithOwner(ctx, workspace, owner); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			slog.ErrorContext(ctx, "failed to create workspace with owner", "user_id", user.ID, "slug", slug, "error", err)
			return nil, cerrors.Internal("failed to create workspace", err)
		}
	} else {
		if err := s.workspaces.Create(ctx, workspace); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			slog.ErrorContext(ctx, "failed to create workspace", "user_id", user.ID, "slug", slug, "error", err)
			return nil, cerrors.Internal("failed to create workspace", err)
		}
		if err := s.workspaces.AddMember(ctx, owner); err != nil {
			slog.ErrorContext(ctx, "failed to add workspace owner", "workspace_id", workspace.ID, "user_id", user.ID, "error", err)
			return nil, cerrors.Internal("failed to add workspace owner", err)
		}
	}

	if s.search != nil {
		if err := s.search.IndexUser(ctx, workspace.ID, user.ID, user.DisplayName, user.Email, user.CreatedAt, user.UpdatedAt); err != nil {
			slog.ErrorContext(ctx, "failed to enqueue workspace owner search index", "workspace_id", workspace.ID, "user_id", user.ID, "error", err)
		}
	}

	slog.InfoContext(ctx, "workspace created", "workspace_id", workspace.ID, "user_id", user.ID, "slug", slug, "kind", kind)
	return workspace, nil
}

// Login verifies credentials, creates a server-side session, and returns tokens.
func (s *Service) Login(ctx context.Context, email, password, deviceInfo, ipAddress string) (*LoginResult, error) {
	input := LoginInput{
		Email:    email,
		Password: password,
	}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Unauthorized("invalid email or password")
		}
		slog.ErrorContext(ctx, "failed to fetch user by email", "error", err)
		return nil, cerrors.Internal("failed to fetch user", err)
	}

	if err := verifyPassword(password, user.PasswordHash); err != nil {
		return nil, cerrors.Unauthorized("invalid email or password")
	}

	if user.Status == entity.UserStatusSuspended {
		return nil, cerrors.AccountSuspended()
	}
	if user.Status == entity.UserStatusDeactivated {
		return nil, cerrors.AccountDeactivated()
	}
	if user.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("account is not active")
	}

	// Create server-side session.
	session, refreshToken, err := s.sessions.Create(ctx, user.ID.String(), deviceInfo, ipAddress)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create session", "user_id", user.ID, "error", err)
		return nil, cerrors.Internal("failed to create session", err)
	}

	// Generate short-lived JWT access token with session ID.
	accessToken, err := s.generateToken(user.ID, session.ID, s.accessTTL)
	if err != nil {
		if revokeErr := s.sessions.Revoke(ctx, session.ID); revokeErr != nil {
			slog.ErrorContext(ctx, "failed to revoke session after token generation failure", "session_id", session.ID, "error", revokeErr)
		}
		return nil, cerrors.Internal("failed to generate access token", err)
	}

	slog.InfoContext(ctx, "user logged in",
		"user_id", user.ID,
		"session_id", session.ID,
	)

	return &LoginResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		SessionID:    session.ID,
		ExpiresIn:    int(s.accessTTL.Seconds()),
	}, nil
}

// RefreshToken validates an opaque refresh token and issues new tokens.
// The old refresh token is rotated (invalidated and replaced).
func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (*LoginResult, error) {
	session, err := s.sessions.ValidateRefreshToken(ctx, refreshToken)
	if err != nil {
		slog.WarnContext(ctx, "refresh token validation failed", "error", err)
		return nil, cerrors.Unauthorized("invalid refresh token")
	}

	userID, err := uuid.Parse(session.UserID)
	if err != nil {
		return nil, cerrors.Internal("invalid user ID in session", err)
	}

	// Verify the user still exists and is active.
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Unauthorized("user not found")
		}
		slog.ErrorContext(ctx, "failed to fetch user for token refresh", "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to fetch user", err)
	}

	if user.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("account is not active")
	}

	// Rotate the refresh token on the existing session.
	newRefreshToken, err := s.sessions.RotateRefreshToken(ctx, session.ID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to rotate refresh token", "session_id", session.ID, "error", err)
		return nil, cerrors.Internal("failed to rotate refresh token", err)
	}

	// Generate new short-lived JWT access token.
	newAccessToken, err := s.generateToken(userID, session.ID, s.accessTTL)
	if err != nil {
		return nil, cerrors.Internal("failed to generate access token", err)
	}

	slog.InfoContext(ctx, "tokens refreshed", "user_id", userID, "session_id", session.ID)

	return &LoginResult{
		AccessToken:  newAccessToken,
		RefreshToken: newRefreshToken,
		SessionID:    session.ID,
		ExpiresIn:    int(s.accessTTL.Seconds()),
	}, nil
}

// ValidateToken parses a JWT access token and validates the associated session in Redis.
// Returns both userID and sessionID for Zero Trust verification on every request.
func (s *Service) ValidateToken(tokenString string) (uuid.UUID, string, error) {
	claims, err := s.parseToken(tokenString)
	if err != nil {
		return uuid.Nil, "", cerrors.Unauthorized("invalid token")
	}

	// Zero Trust: verify the session is still valid in Redis.
	valid, err := s.sessions.IsSessionValid(context.Background(), claims.SessionID)
	if err != nil {
		slog.Error("failed to validate session in redis", "session_id", claims.SessionID, "error", err)
		// Fail-closed: if Redis is down, deny access.
		return uuid.Nil, "", cerrors.Unauthorized("session validation failed")
	}
	if !valid {
		return uuid.Nil, "", cerrors.Unauthorized("session revoked or expired")
	}

	// Defer the touch to the background worker; no goroutine per request.
	s.sessions.DeferTouch(claims.SessionID)

	return claims.UserID, claims.SessionID, nil
}

// LogoutSessionForUser revokes a session by id, but only if it belongs to
// userID. Returns NotFound if the target session does not exist or belongs
// to a different user — UUID unpredictability is not an authorization
// boundary.
func (s *Service) LogoutSessionForUser(ctx context.Context, userID, targetSessionID string) error {
	ok, err := s.sessions.SessionBelongsToUser(ctx, userID, targetSessionID)
	if err != nil {
		return cerrors.Internal("session ownership check failed", err)
	}
	if !ok {
		return cerrors.NotFound("session not found")
	}
	if err := s.sessions.Revoke(ctx, targetSessionID); err != nil {
		slog.ErrorContext(ctx, "failed to revoke session", "session_id", targetSessionID, "error", err)
		return cerrors.Internal("failed to revoke session", err)
	}
	return nil
}

// LogoutAll revokes all sessions for a user.
func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	if err := s.sessions.RevokeAllUserSessions(ctx, userID); err != nil {
		slog.ErrorContext(ctx, "failed to revoke all sessions", "user_id", userID, "error", err)
		return cerrors.Internal("failed to revoke sessions", err)
	}
	return nil
}

// Deactivate marks an account deactivated after revoking every refresh session.
func (s *Service) Deactivate(ctx context.Context, userID uuid.UUID) error {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user.Status == entity.UserStatusSuspended {
		return cerrors.Forbidden("suspended accounts cannot self-deactivate")
	}
	if user.Status == entity.UserStatusDeactivated {
		return nil
	}

	if err := s.LogoutAll(ctx, userID.String()); err != nil {
		return err
	}

	now := time.Now().UTC()
	user.Status = entity.UserStatusDeactivated
	user.DeactivatedAt = &now
	user.UpdatedAt = now
	if err := s.users.Update(ctx, user); err != nil {
		return cerrors.Internal("failed to deactivate user", err)
	}
	return nil
}

// ReactivateAndLogin verifies credentials, reactivates a deactivated account,
// and issues the same token response as Login.
func (s *Service) ReactivateAndLogin(ctx context.Context, email, password, deviceInfo, ipAddress string) (*LoginResult, error) {
	input := LoginInput{
		Email:    email,
		Password: password,
	}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Unauthorized("invalid email or password")
		}
		slog.ErrorContext(ctx, "failed to fetch user by email", "error", err)
		return nil, cerrors.Internal("failed to fetch user", err)
	}

	if err := verifyPassword(password, user.PasswordHash); err != nil {
		return nil, cerrors.Unauthorized("invalid email or password")
	}

	if user.Status == entity.UserStatusSuspended {
		return nil, cerrors.AccountSuspended()
	}
	if user.Status == entity.UserStatusActive {
		return nil, cerrors.NotDeactivated()
	}
	if user.Status != entity.UserStatusDeactivated {
		return nil, cerrors.Forbidden("account is not in a reactivatable state")
	}

	user.Status = entity.UserStatusActive
	user.DeactivatedAt = nil
	user.UpdatedAt = time.Now().UTC()
	if err := s.users.Update(ctx, user); err != nil {
		return nil, cerrors.Internal("failed to reactivate user", err)
	}

	result, err := s.CreateSessionForUser(ctx, user.ID, deviceInfo, ipAddress)
	if err != nil {
		now := time.Now().UTC()
		user.Status = entity.UserStatusDeactivated
		user.DeactivatedAt = &now
		user.UpdatedAt = now
		if rbErr := s.users.Update(ctx, user); rbErr != nil {
			slog.ErrorContext(ctx, "reactivate-and-login rollback failed",
				"user_id", user.ID,
				"rollback_err", rbErr,
				"session_err", err,
			)
		}
		return nil, err
	}

	return result, nil
}

// LogoutOthers revokes every session for the user except keepSessionID.
// Returns a non-nil error if ANY target session fails to revoke — the
// HTTP layer must NOT report 204 in that case.
func (s *Service) LogoutOthers(ctx context.Context, userID, keepSessionID string) error {
	if err := s.sessions.RevokeAllUserSessionsExcept(ctx, userID, keepSessionID); err != nil {
		slog.ErrorContext(ctx, "failed to revoke other sessions", "user_id", userID, "error", err)
		return cerrors.Internal("failed to revoke other sessions", err)
	}
	return nil
}

// CreateSessionForUser creates a session and JWT for an already-authenticated
// user (e.g. guest invite redemption, SSO). Skips password verification.
func (s *Service) CreateSessionForUser(ctx context.Context, userID uuid.UUID, deviceInfo, ipAddress string) (*LoginResult, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("user not found")
		}
		return nil, cerrors.Internal("failed to fetch user", err)
	}
	if user.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("account is not active")
	}

	session, refreshToken, err := s.sessions.Create(ctx, user.ID.String(), deviceInfo, ipAddress)
	if err != nil {
		return nil, cerrors.Internal("failed to create session", err)
	}

	accessToken, err := s.generateToken(user.ID, session.ID, s.accessTTL)
	if err != nil {
		if revokeErr := s.sessions.Revoke(ctx, session.ID); revokeErr != nil {
			slog.ErrorContext(ctx, "failed to revoke session after token generation failure", "session_id", session.ID, "error", revokeErr)
		}
		return nil, cerrors.Internal("failed to generate access token", err)
	}

	slog.InfoContext(ctx, "session created for user", "user_id", userID, "session_id", session.ID)

	return &LoginResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		SessionID:    session.ID,
		ExpiresIn:    int(s.accessTTL.Seconds()),
	}, nil
}

// ListSessions returns all active sessions for a user.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	sessions, err := s.sessions.ListUserSessions(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list sessions", "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to list sessions", err)
	}
	return sessions, nil
}

func (s *Service) uniqueWorkspaceSlug(ctx context.Context, raw string) (string, error) {
	base := slugify(raw)
	if base == "" {
		base = "workspace"
	}
	if len(base) > 80 {
		base = strings.Trim(base[:80], "-")
	}
	if base == "" {
		base = "workspace"
	}

	candidate := base
	for i := 1; i <= 25; i++ {
		_, err := s.workspaces.GetBySlug(ctx, candidate)
		if err == nil {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
			if len(candidate) > 80 {
				candidate = candidate[:80]
				candidate = strings.Trim(candidate, "-")
			}
			continue
		}
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return candidate, nil
		}
		return "", cerrors.Internal("failed to verify workspace slug", err)
	}

	return "", cerrors.Conflict("unable to generate a unique workspace slug")
}

func slugify(value string) string {
	var b strings.Builder
	b.Grow(len(value))

	lastHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastHyphen = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if b.Len() == 0 || lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}

	return strings.Trim(b.String(), "-")
}

func personalWorkspaceSlug(userID uuid.UUID) string {
	return "personal-" + strings.ReplaceAll(userID.String(), "-", "")
}

func (s *Service) workspaceKind(workspace entity.Workspace, userID uuid.UUID) entity.WorkspaceKind {
	if workspace.Slug == personalWorkspaceSlug(userID) && workspace.CreatedBy == userID {
		return entity.WorkspaceKindPersonal
	}
	return entity.WorkspaceKindOrganization
}

// --- JWT helpers ---

// tokenClaims extends the standard JWT claims with application-specific fields.
type tokenClaims struct {
	jwt.RegisteredClaims
	SessionID string `json:"sid"`
}

// tokenClaimsParsed is the parsed result after extracting the user ID and session ID.
type tokenClaimsParsed struct {
	UserID    uuid.UUID
	SessionID string
}

func (s *Service) generateToken(userID uuid.UUID, sessionID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		SessionID: sessionID,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

func (s *Service) parseToken(tokenString string) (*tokenClaimsParsed, error) {
	claims := &tokenClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		// Pin to HS256 exactly; rejecting other HMAC variants (HS384/HS512) and
		// any non-HMAC method prevents algorithm-confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing algorithm: %s", t.Method.Alg())
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if token == nil || !token.Valid {
		return nil, errors.New("invalid token")
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID in token subject: %w", err)
	}

	if claims.SessionID == "" {
		return nil, fmt.Errorf("missing session ID in token")
	}

	return &tokenClaimsParsed{
		UserID:    userID,
		SessionID: claims.SessionID,
	}, nil
}
