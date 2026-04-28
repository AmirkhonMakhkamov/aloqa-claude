package search

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/security/accesspolicy"
)

type ResourceType string

const (
	ResourceTypeMessage ResourceType = "message"
	ResourceTypeFile    ResourceType = "file"
	ResourceTypeChannel ResourceType = "channel"
	ResourceTypeUser    ResourceType = "user"
)

// Result represents a single search result with type, ID, and score.
type Result struct {
	Type        string     `json:"type"` // "message", "file", "channel", "user"
	ID          uuid.UUID  `json:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ChannelID   *uuid.UUID `json:"channel_id,omitempty"`
	Title       string     `json:"title"`
	Snippet     string     `json:"snippet"`
	Score       float64    `json:"score"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// SearchResults is a paginated list of search results.
type SearchResults struct {
	Results    []Result `json:"results"`
	Total      int      `json:"total"`
	NextOffset int      `json:"next_offset,omitempty"`
}

// Params holds search query parameters.
type Params struct {
	Query       string     `json:"query"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ChannelID   *uuid.UUID `json:"channel_id,omitempty"`
	UserID      *uuid.UUID `json:"user_id,omitempty"`
	Type        string     `json:"type,omitempty"` // "message", "file", "channel", ""=all
	DateFrom    *time.Time `json:"date_from,omitempty"`
	DateTo      *time.Time `json:"date_to,omitempty"`
	Limit       int        `json:"limit"`
	Offset      int        `json:"offset"`

	AccessibleChannelIDs []uuid.UUID `json:"-"`
	AllowUserResults     bool        `json:"-"`
}

type Document struct {
	WorkspaceID uuid.UUID
	ResourceID  uuid.UUID
	ChannelID   *uuid.UUID
	Type        ResourceType
	Title       string
	Content     string
	Metadata    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Indexer enqueues content updates for durable search indexing.
type Indexer interface {
	EnqueueUpsert(ctx context.Context, doc Document) error
	EnqueueDelete(ctx context.Context, workspaceID uuid.UUID, resourceType ResourceType, resourceID uuid.UUID) error
}

// Searcher executes search queries against the search backend.
type Searcher interface {
	Search(ctx context.Context, params Params) (*SearchResults, error)
}

// Service combines indexing and search operations. Implementations can
// use OpenSearch/Elasticsearch, PostgreSQL full-text search, or any other
// backend.
type Service struct {
	indexer  Indexer
	searcher Searcher
	members  repository.WorkspaceRepository
	channels repository.ChannelRepository
	audit    repository.AuditRepository
	access   *accesspolicy.Checker
}

// NewService creates a new search service with the given backend.
func NewService(
	indexer Indexer,
	searcher Searcher,
	members repository.WorkspaceRepository,
	channels repository.ChannelRepository,
	audit repository.AuditRepository,
) *Service {
	return &Service{indexer: indexer, searcher: searcher, members: members, channels: channels, audit: audit}
}

func (s *Service) SetAccessPolicy(access *accesspolicy.Checker) {
	s.access = access
}

// Search performs a full-text search across the workspace.
func (s *Service) Search(ctx context.Context, params Params) (*SearchResults, error) {
	if s.searcher == nil {
		return &SearchResults{}, nil
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}
	if params.UserID == nil {
		return nil, cerrors.Unauthorized("user context is required")
	}

	if s.access != nil {
		subject, err := s.access.WorkspaceAccess(ctx, params.WorkspaceID, *params.UserID)
		if err == nil {
			params.AllowUserResults = subject == accesspolicy.SubjectMember
		} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeForbidden {
			return nil, err
		}

		accessible, err := s.access.ListChannels(ctx, params.WorkspaceID, *params.UserID, accesspolicy.CapabilityView)
		if err != nil {
			return nil, err
		}
		params.AccessibleChannelIDs = make([]uuid.UUID, 0, len(accessible))
		for _, ch := range accessible {
			params.AccessibleChannelIDs = append(params.AccessibleChannelIDs, ch.ID)
		}
		if len(params.AccessibleChannelIDs) == 0 && !params.AllowUserResults {
			return nil, cerrors.Forbidden("user does not have access to this workspace")
		}
		if params.ChannelID != nil {
			if _, err := s.access.Channel(ctx, *params.ChannelID, *params.UserID, accesspolicy.CapabilityView); err != nil {
				return nil, err
			}
		}
	} else {
		if err := s.requireWorkspaceMember(ctx, params.WorkspaceID, *params.UserID); err != nil {
			return nil, err
		}
		if params.ChannelID != nil {
			if err := s.requireChannelAccess(ctx, *params.ChannelID, *params.UserID); err != nil {
				return nil, err
			}
		}
	}
	results, err := s.searcher.Search(ctx, params)
	if err != nil {
		return nil, err
	}
	results.Results = s.filterAuthorizedResults(ctx, results.Results, *params.UserID)
	if results.Total < len(results.Results) || len(results.Results) < params.Limit {
		results.Total = params.Offset + len(results.Results)
	}
	if results.NextOffset > 0 && results.NextOffset >= results.Total {
		results.NextOffset = 0
	}
	s.logAudit(ctx, params, len(results.Results))
	return results, nil
}

func (s *Service) requireWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if _, err := s.members.GetMember(ctx, workspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a member of this workspace")
		}
		slog.ErrorContext(ctx, "failed to verify workspace membership", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to verify workspace membership", err)
	}
	return nil
}

func (s *Service) requireChannelAccess(ctx context.Context, channelID, userID uuid.UUID) error {
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("channel not found")
		}
		return cerrors.Internal("failed to get channel", err)
	}
	if err := s.requireWorkspaceMember(ctx, ch.WorkspaceID, userID); err != nil {
		return err
	}
	if ch.Type != entity.ChannelTypePublic {
		if _, err := s.channels.GetMember(ctx, ch.ID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return cerrors.Forbidden("you do not have access to this channel")
			}
			return cerrors.Internal("failed to verify channel membership", err)
		}
	}
	return nil
}

func (s *Service) IndexMessage(ctx context.Context, workspaceID, channelID, messageID uuid.UUID, content string, createdAt time.Time) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueUpsert(ctx, Document{
		WorkspaceID: workspaceID,
		ResourceID:  messageID,
		ChannelID:   &channelID,
		Type:        ResourceTypeMessage,
		Content:     content,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	})
}

func (s *Service) DeleteMessage(ctx context.Context, workspaceID, messageID uuid.UUID) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueDelete(ctx, workspaceID, ResourceTypeMessage, messageID)
}

func (s *Service) IndexFile(ctx context.Context, workspaceID, channelID, attachmentID, messageID uuid.UUID, fileName, mimeType string, createdAt time.Time) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueUpsert(ctx, Document{
		WorkspaceID: workspaceID,
		ResourceID:  attachmentID,
		ChannelID:   &channelID,
		Type:        ResourceTypeFile,
		Title:       fileName,
		Content:     strings.TrimSpace(fileName + " " + mimeType),
		Metadata: map[string]any{
			"message_id": messageID.String(),
			"mime_type":  mimeType,
		},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
}

func (s *Service) DeleteFile(ctx context.Context, workspaceID, attachmentID uuid.UUID) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueDelete(ctx, workspaceID, ResourceTypeFile, attachmentID)
}

func (s *Service) IndexChannel(ctx context.Context, workspaceID, channelID uuid.UUID, name, topic string, createdAt, updatedAt time.Time) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueUpsert(ctx, Document{
		WorkspaceID: workspaceID,
		ResourceID:  channelID,
		ChannelID:   &channelID,
		Type:        ResourceTypeChannel,
		Title:       name,
		Content:     strings.TrimSpace(name + " " + topic),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	})
}

func (s *Service) DeleteChannel(ctx context.Context, workspaceID, channelID uuid.UUID) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueDelete(ctx, workspaceID, ResourceTypeChannel, channelID)
}

func (s *Service) IndexUser(ctx context.Context, workspaceID, userID uuid.UUID, displayName, email string, createdAt, updatedAt time.Time) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueUpsert(ctx, Document{
		WorkspaceID: workspaceID,
		ResourceID:  userID,
		Type:        ResourceTypeUser,
		Title:       displayName,
		Content:     strings.TrimSpace(displayName + " " + email),
		Metadata: map[string]any{
			"email": email,
		},
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
}

func (s *Service) DeleteUserFromWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if s.indexer == nil {
		return nil
	}
	return s.indexer.EnqueueDelete(ctx, workspaceID, ResourceTypeUser, userID)
}

func (s *Service) filterAuthorizedResults(ctx context.Context, results []Result, userID uuid.UUID) []Result {
	filtered := results[:0]
	for _, result := range results {
		switch ResourceType(result.Type) {
		case ResourceTypeMessage, ResourceTypeFile, ResourceTypeChannel:
			if result.ChannelID == nil {
				continue
			}
			if s.access != nil {
				if _, err := s.access.Channel(ctx, *result.ChannelID, userID, accesspolicy.CapabilityView); err == nil {
					filtered = append(filtered, result)
				}
				continue
			}
			if err := s.requireChannelAccess(ctx, *result.ChannelID, userID); err == nil {
				filtered = append(filtered, result)
			}
		case ResourceTypeUser:
			if s.access != nil {
				if subject, err := s.access.WorkspaceAccess(ctx, result.WorkspaceID, userID); err == nil && subject == accesspolicy.SubjectMember {
					filtered = append(filtered, result)
				}
				continue
			}
			if err := s.requireWorkspaceMember(ctx, result.WorkspaceID, userID); err == nil {
				filtered = append(filtered, result)
			}
		default:
			continue
		}
	}
	return filtered
}

func (s *Service) logAudit(ctx context.Context, params Params, resultCount int) {
	if s.audit == nil || params.UserID == nil {
		return
	}
	query := strings.TrimSpace(params.Query)
	if len(query) > 512 {
		query = query[:512]
	}
	metadata := map[string]any{
		"query":        query,
		"type":         params.Type,
		"limit":        params.Limit,
		"offset":       params.Offset,
		"result_count": resultCount,
	}
	if params.ChannelID != nil {
		metadata["channel_id"] = params.ChannelID.String()
	}
	if params.DateFrom != nil {
		metadata["date_from"] = params.DateFrom.UTC()
	}
	if params.DateTo != nil {
		metadata["date_to"] = params.DateTo.UTC()
	}
	entry := &entity.AuditEntry{
		ID:          uuid.New(),
		WorkspaceID: params.WorkspaceID,
		ActorID:     *params.UserID,
		Action:      entity.AuditActionSearchPerformed,
		TargetType:  "search",
		Metadata:    metadata,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.audit.Create(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "failed to audit search", "workspace_id", params.WorkspaceID, "actor_id", *params.UserID, "error", err)
	}
}
