package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/platform/storage"
	"aloqa/internal/platform/txscope"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/guestaccess"
	searchsvc "aloqa/internal/service/search"
)

// Scanner provides virus scanning for uploaded files.
// Implementations can integrate ClamAV, external APIs, etc.
type Scanner interface {
	// Scan checks the reader for malware. Returns nil if clean, an error
	// describing the threat if infected, and a separate error for scan failures.
	Scan(ctx context.Context, reader io.Reader, filename string) error
}

// noopScanner is used when no virus scanner is configured.
type noopScanner struct{}

func (noopScanner) Scan(context.Context, io.Reader, string) error { return nil }

type SearchIndexer interface {
	IndexFile(ctx context.Context, workspaceID, channelID, attachmentID, messageID uuid.UUID, fileName, mimeType string, createdAt time.Time) error
	DeleteFile(ctx context.Context, workspaceID, attachmentID uuid.UUID) error
}

// Service handles file uploads, downloads, and lifecycle management.
type Service struct {
	store    storage.Storage
	messages repository.MessageRepository
	channels repository.ChannelRepository
	members  repository.WorkspaceRepository
	scanner  Scanner
	guests   *guestaccess.Checker
	access   *accesspolicy.Checker
	search   SearchIndexer
	tx       txscope.Manager

	maxFileSize  int64
	allowedTypes map[string]bool
	signedURLTTL time.Duration
}

// Config holds file service configuration.
type Config struct {
	MaxFileSize  int64    // Maximum upload size in bytes.
	AllowedTypes []string // Allowed MIME types (empty = allow all).
	SignedURLTTL time.Duration
}

// NewService creates a new file service.
func NewService(
	store storage.Storage,
	messages repository.MessageRepository,
	channels repository.ChannelRepository,
	members repository.WorkspaceRepository,
	scanner Scanner,
	search SearchIndexer,
	cfg Config,
	guests *guestaccess.Checker,
) *Service {
	allowed := make(map[string]bool, len(cfg.AllowedTypes))
	for _, t := range cfg.AllowedTypes {
		allowed[t] = true
	}

	if scanner == nil {
		scanner = noopScanner{}
	}

	return &Service{
		store:        store,
		messages:     messages,
		channels:     channels,
		members:      members,
		scanner:      scanner,
		guests:       guests,
		search:       search,
		maxFileSize:  cfg.MaxFileSize,
		allowedTypes: allowed,
		signedURLTTL: cfg.SignedURLTTL,
	}
}

func (s *Service) SetAccessPolicy(access *accesspolicy.Checker) {
	s.access = access
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

// UploadResult contains information about a successfully uploaded file.
type UploadResult struct {
	Attachment *entity.Attachment `json:"attachment"`
}

func (s *Service) canAccessMessage(
	ctx context.Context,
	messageID, expectedChannelID, userID uuid.UUID,
	capability accesspolicy.Capability,
) error {
	msg, err := s.messages.GetByID(ctx, messageID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("message not found")
		}
		return cerrors.Internal("failed to get message", err)
	}
	if msg.DeletedAt != nil {
		return cerrors.NotFound("message has been deleted")
	}
	if expectedChannelID != uuid.Nil && msg.ChannelID != expectedChannelID {
		return cerrors.InvalidInput("message does not belong to this channel")
	}

	if s.access != nil {
		_, err := s.access.Channel(ctx, msg.ChannelID, userID, capability)
		return err
	}

	ch, err := s.channels.GetByID(ctx, msg.ChannelID)
	if err != nil {
		return cerrors.Internal("failed to get channel", err)
	}
	if _, err := s.members.GetMember(ctx, ch.WorkspaceID, userID); err == nil {
		if ch.Type == entity.ChannelTypePublic && capability == accesspolicy.CapabilityView {
			return nil
		}
		if _, err := s.channels.GetMember(ctx, ch.ID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return cerrors.Forbidden("you do not have access to this channel")
			}
			return cerrors.Internal("failed to verify channel membership", err)
		}
		return nil
	} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return cerrors.Internal("failed to verify workspace membership", err)
	}

	if s.guests != nil {
		allowed, err := s.guests.HasChannelAccess(ctx, ch.WorkspaceID, ch.ID, userID)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}

	return cerrors.Forbidden("you do not have access to this channel")
}

// Upload stores a file and creates an attachment record linked to a message.
// It validates file size, MIME type, and optionally scans for viruses.
func (s *Service) Upload(
	ctx context.Context,
	channelID uuid.UUID,
	messageID uuid.UUID,
	userID uuid.UUID,
	filename string,
	reader io.Reader,
	size int64,
) (*UploadResult, error) {
	if err := s.canAccessMessage(ctx, messageID, channelID, userID, accesspolicy.CapabilityParticipate); err != nil {
		return nil, err
	}

	// Validate size.
	if s.maxFileSize > 0 && size > s.maxFileSize {
		return nil, cerrors.InvalidInput(fmt.Sprintf(
			"file size %d exceeds maximum of %d bytes", size, s.maxFileSize,
		))
	}

	// Detect MIME type from extension.
	ext := filepath.Ext(filename)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Validate MIME type.
	if len(s.allowedTypes) > 0 {
		baseType := strings.Split(mimeType, ";")[0]
		if !s.allowedTypes[baseType] {
			return nil, cerrors.InvalidInput(fmt.Sprintf("file type %s is not allowed", baseType))
		}
	}

	tmp, err := os.CreateTemp("", "aloqa-upload-*")
	if err != nil {
		return nil, cerrors.Internal("failed to prepare upload", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, "failed to remove temporary upload file", "path", tmpName, "error", err)
		}
	}()
	defer func() {
		if err := tmp.Close(); err != nil {
			slog.WarnContext(ctx, "failed to close temporary upload file", "path", tmpName, "error", err)
		}
	}()

	written, err := io.Copy(tmp, reader)
	if err != nil {
		return nil, cerrors.Internal("failed to read upload", err)
	}
	if s.maxFileSize > 0 && written > s.maxFileSize {
		return nil, cerrors.InvalidInput(fmt.Sprintf(
			"file size %d exceeds maximum of %d bytes", written, s.maxFileSize,
		))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, cerrors.Internal("failed to prepare upload scan", err)
	}

	if err := s.scanner.Scan(ctx, tmp, filename); err != nil {
		slog.WarnContext(ctx, "virus scan rejected file", "filename", filename, "error", err)
		return nil, cerrors.Forbidden("file rejected by security scan")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, cerrors.Internal("failed to prepare upload storage", err)
	}

	// Generate storage key.
	cleanExt := strings.TrimPrefix(ext, ".")
	if cleanExt == "" {
		cleanExt = "bin"
	}
	key := storage.GenerateKey("attachments", cleanExt)

	// Store the file.
	if err := s.store.Put(ctx, key, tmp, written, mimeType); err != nil {
		slog.ErrorContext(ctx, "failed to store file", "key", key, "error", err)
		return nil, cerrors.Internal("failed to store file", err)
	}

	// Create the attachment record.
	attachment := &entity.Attachment{
		ID:          id.New(),
		MessageID:   messageID,
		FileName:    filename,
		FileSize:    written,
		MimeType:    mimeType,
		StoragePath: key,
		URL:         "/files/" + key,
		CreatedAt:   time.Now(),
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("file transaction scope is not configured")
			}
			if err := scope.Messages().CreateAttachment(ctx, attachment); err != nil {
				return err
			}
			return s.enqueueFileSearchTx(ctx, scope, attachment, channelID, messageID, filename, mimeType)
		}); err != nil {
			if cleanupErr := s.store.Delete(ctx, key); cleanupErr != nil {
				slog.ErrorContext(ctx, "failed to clean up stored file after attachment transaction error", "key", key, "error", cleanupErr)
			}
			slog.ErrorContext(ctx, "failed to create attachment transaction", "key", key, "error", err)
			return nil, cerrors.Internal("failed to create attachment", err)
		}
	} else {
		if err := s.messages.CreateAttachment(ctx, attachment); err != nil {
			// Clean up the stored file on DB failure.
			if err := s.store.Delete(ctx, key); err != nil {
				slog.ErrorContext(ctx, "failed to clean up stored file after attachment error", "key", key, "error", err)
			}
			slog.ErrorContext(ctx, "failed to create attachment record", "key", key, "error", err)
			return nil, cerrors.Internal("failed to create attachment", err)
		}

		channel, err := s.channels.GetByID(ctx, channelID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to load channel for file search indexing", "channel_id", channelID, "error", err)
		} else if s.search != nil {
			if err := s.search.IndexFile(ctx, channel.WorkspaceID, channelID, attachment.ID, messageID, filename, mimeType, attachment.CreatedAt); err != nil {
				slog.ErrorContext(ctx, "failed to enqueue file search index", "attachment_id", attachment.ID, "error", err)
			}
		}
	}

	slog.InfoContext(ctx, "file uploaded",
		"attachment_id", attachment.ID,
		"message_id", messageID,
		"filename", filename,
		"size", size,
		"mime_type", mimeType,
	)

	return &UploadResult{Attachment: attachment}, nil
}

// Download returns a reader for a stored attachment.
func (s *Service) Download(ctx context.Context, attachmentID uuid.UUID) (io.ReadCloser, *entity.Attachment, error) {
	// Look up attachment in all messages (we need the storage path).
	// For now we'll search by querying the attachment directly.
	// This requires a GetAttachment method - we'll use the message repo's list.
	return nil, nil, cerrors.Unavailable("download by attachment ID is not available")
}

// DownloadByKey returns a reader for a file stored at the given key after authorization.
func (s *Service) DownloadByKey(ctx context.Context, key string, userID uuid.UUID) (io.ReadCloser, *storage.FileInfo, error) {
	attachment, err := s.messages.GetAttachmentByStoragePath(ctx, key)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, nil, cerrors.NotFound("file not found")
		}
		return nil, nil, cerrors.Internal("failed to look up file", err)
	}
	if err := s.canAccessMessage(ctx, attachment.MessageID, uuid.Nil, userID, accesspolicy.CapabilityView); err != nil {
		return nil, nil, err
	}

	reader, info, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, cerrors.NotFound("file not found")
		}
		return nil, nil, cerrors.Internal("failed to read file", err)
	}
	info.MimeType = attachment.MimeType
	info.Size = attachment.FileSize
	return reader, info, nil
}

func (s *Service) PresignDownloadByKey(ctx context.Context, key string, userID uuid.UUID) (string, error) {
	signer, ok := s.store.(storage.DownloadSigner)
	if !ok || signer == nil {
		return "", nil
	}
	attachment, err := s.messages.GetAttachmentByStoragePath(ctx, key)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return "", cerrors.NotFound("file not found")
		}
		return "", cerrors.Internal("failed to look up file", err)
	}
	if err := s.canAccessMessage(ctx, attachment.MessageID, uuid.Nil, userID, accesspolicy.CapabilityView); err != nil {
		return "", err
	}
	url, err := signer.SignedDownloadURL(ctx, key, storage.SignedURLOptions{
		Filename:    attachment.FileName,
		ContentType: attachment.MimeType,
		ExpiresIn:   s.signedURLTTL,
		Attachment:  true,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			return "", nil
		}
		return "", cerrors.Internal("failed to sign file download", err)
	}
	return url, nil
}

// Delete removes a file from storage and its attachment record.
func (s *Service) Delete(ctx context.Context, key string, userID uuid.UUID) error {
	attachment, err := s.messages.GetAttachmentByStoragePath(ctx, key)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("file not found")
		}
		return cerrors.Internal("failed to look up file", err)
	}

	msg, err := s.messages.GetByID(ctx, attachment.MessageID)
	if err != nil {
		return cerrors.Internal("failed to load file message", err)
	}
	channel, err := s.channels.GetByID(ctx, msg.ChannelID)
	if err != nil {
		return cerrors.Internal("failed to load file channel", err)
	}
	if err := s.canAccessMessage(ctx, attachment.MessageID, uuid.Nil, userID, accesspolicy.CapabilityParticipate); err != nil {
		return err
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("file transaction scope is not configured")
			}
			if err := scope.Messages().DeleteAttachment(ctx, attachment.ID); err != nil {
				return err
			}
			return s.enqueueFileDeleteSearchTx(ctx, scope, channel.WorkspaceID, attachment.ID)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to delete attachment transaction", "attachment_id", attachment.ID, "error", err)
			return cerrors.Internal("failed to delete attachment record", err)
		}
		if err := s.store.Delete(ctx, key); err != nil {
			slog.ErrorContext(ctx, "failed to delete file after metadata transaction", "key", key, "error", err)
			return cerrors.Internal("failed to delete file", err)
		}
		return nil
	}
	if err := s.store.Delete(ctx, key); err != nil {
		slog.ErrorContext(ctx, "failed to delete file", "key", key, "error", err)
		return cerrors.Internal("failed to delete file", err)
	}
	if err := s.messages.DeleteAttachment(ctx, attachment.ID); err != nil {
		slog.ErrorContext(ctx, "failed to delete attachment record", "attachment_id", attachment.ID, "error", err)
		return cerrors.Internal("failed to delete attachment record", err)
	}
	if s.search != nil {
		if err := s.search.DeleteFile(ctx, channel.WorkspaceID, attachment.ID); err != nil {
			slog.ErrorContext(ctx, "failed to enqueue file search delete", "attachment_id", attachment.ID, "error", err)
		}
	}
	return nil
}

func (s *Service) enqueueFileSearchTx(ctx context.Context, scope txscope.Scope, attachment *entity.Attachment, channelID, messageID uuid.UUID, fileName, mimeType string) error {
	if scope == nil || scope.SearchIndexer() == nil || attachment == nil {
		return nil
	}
	channelRepo := s.channels
	if scope.Channels() != nil {
		channelRepo = scope.Channels()
	}
	channel, err := channelRepo.GetByID(ctx, channelID)
	if err != nil {
		return err
	}
	return scope.SearchIndexer().EnqueueUpsert(ctx, searchsvc.Document{
		WorkspaceID: channel.WorkspaceID,
		ResourceID:  attachment.ID,
		ChannelID:   &channelID,
		Type:        searchsvc.ResourceTypeFile,
		Title:       fileName,
		Content:     strings.TrimSpace(fileName + " " + mimeType),
		Metadata: map[string]any{
			"message_id": messageID.String(),
			"mime_type":  mimeType,
		},
		CreatedAt: attachment.CreatedAt,
		UpdatedAt: attachment.CreatedAt,
	})
}

func (s *Service) enqueueFileDeleteSearchTx(ctx context.Context, scope txscope.Scope, workspaceID, attachmentID uuid.UUID) error {
	if scope == nil || scope.SearchIndexer() == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeFile, attachmentID)
}
