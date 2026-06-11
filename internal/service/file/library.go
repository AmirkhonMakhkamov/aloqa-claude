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
)

const defaultUserStorageLimitBytes int64 = 10 * 1024 * 1024 * 1024

var categoryUploadCaps = map[string]int64{
	"image":    10 * 1024 * 1024,
	"document": 50 * 1024 * 1024,
	"text":     5 * 1024 * 1024,
	"archive":  100 * 1024 * 1024,
	"video":    200 * 1024 * 1024,
	"audio":    50 * 1024 * 1024,
}

type LibraryUploadResult struct {
	File entity.LibraryFile `json:"file"`
}

func (s *Service) ResolveUploadWorkspace(ctx context.Context, contextID, userID uuid.UUID) (uuid.UUID, error) {
	if contextID == uuid.Nil {
		return uuid.Nil, cerrors.InvalidInput("workspace_id or context_id is required")
	}
	if _, err := s.members.GetMember(ctx, contextID, userID); err == nil {
		return contextID, nil
	} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return uuid.Nil, cerrors.Internal("failed to verify workspace membership", err)
	}

	channel, err := s.channels.GetByID(ctx, contextID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return uuid.Nil, cerrors.InvalidInput("workspace_id or context_id is required")
		}
		return uuid.Nil, cerrors.Internal("failed to resolve upload context", err)
	}
	if channel.WorkspaceID == nil {
		return uuid.Nil, cerrors.InvalidInput("context_id must belong to a workspace")
	}
	if s.access != nil {
		if _, err := s.access.Channel(ctx, channel.ID, userID, accesspolicy.CapabilityParticipate); err != nil {
			return uuid.Nil, err
		}
		return *channel.WorkspaceID, nil
	}
	if _, err := s.members.GetMember(ctx, *channel.WorkspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return uuid.Nil, cerrors.Forbidden("user is not a member of this workspace")
		}
		return uuid.Nil, cerrors.Internal("failed to verify workspace membership", err)
	}
	if channel.Type != entity.ChannelTypePublic {
		if _, err := s.channels.GetMember(ctx, channel.ID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return uuid.Nil, cerrors.Forbidden("you do not have access to this channel")
			}
			return uuid.Nil, cerrors.Internal("failed to verify channel membership", err)
		}
	}
	return *channel.WorkspaceID, nil
}

func (s *Service) ListLibraryFiles(ctx context.Context, params repository.FileListParams) (entity.FileListResult, error) {
	if s.files == nil {
		return entity.FileListResult{}, cerrors.Unavailable("file library is not configured")
	}
	if params.WorkspaceID == uuid.Nil {
		return entity.FileListResult{}, cerrors.InvalidInput("workspace_id is required")
	}
	if err := s.requireWorkspaceMember(ctx, params.WorkspaceID, params.UserID); err != nil {
		return entity.FileListResult{}, err
	}
	return s.files.ListFiles(ctx, params)
}

func (s *Service) UploadLibrary(
	ctx context.Context,
	workspaceID uuid.UUID,
	userID uuid.UUID,
	filename string,
	reader io.Reader,
	size int64,
) (*LibraryUploadResult, error) {
	if s.files == nil {
		return nil, cerrors.Unavailable("file library is not configured")
	}
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	limits := s.effectiveUploadLimits()
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if ext == "" {
		ext = "bin"
	}
	mimeType := mime.TypeByExtension("." + ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	category := fileCategory(mimeType, ext)
	if capBytes := limits[category]; capBytes > 0 && size > capBytes {
		return nil, cerrors.InvalidInput(fmt.Sprintf("file size exceeds %s limit", category))
	}
	if len(s.allowedTypes) > 0 {
		baseType := strings.Split(mimeType, ";")[0]
		if !s.allowedTypes[baseType] {
			return nil, cerrors.InvalidInput(fmt.Sprintf("file type %s is not allowed", baseType))
		}
	}

	tmp, err := os.CreateTemp("", "aloqa-library-upload-*")
	if err != nil {
		return nil, cerrors.Internal("failed to prepare upload", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, "failed to remove temporary library upload file", "path", tmpName, "error", err)
		}
	}()
	defer func() {
		if err := tmp.Close(); err != nil {
			slog.WarnContext(ctx, "failed to close temporary library upload file", "path", tmpName, "error", err)
		}
	}()

	written, err := io.Copy(tmp, reader)
	if err != nil {
		return nil, cerrors.Internal("failed to read upload", err)
	}
	if capBytes := limits[category]; capBytes > 0 && written > capBytes {
		return nil, cerrors.InvalidInput(fmt.Sprintf("file size exceeds %s limit", category))
	}
	used, err := s.files.StorageUsedBytes(ctx, userID)
	if err != nil {
		return nil, cerrors.Internal("failed to read storage usage", err)
	}
	if used+written > defaultUserStorageLimitBytes {
		return nil, cerrors.QuotaExceeded("user storage quota exceeded")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, cerrors.Internal("failed to prepare upload scan", err)
	}
	if err := s.scanner.Scan(ctx, tmp, filename); err != nil {
		slog.WarnContext(ctx, "virus scan rejected library file", "filename", filename, "error", err)
		return nil, cerrors.InvalidInput("file rejected by security scan")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, cerrors.Internal("failed to prepare upload storage", err)
	}

	key := storage.GenerateKey("library", ext)
	if err := s.store.Put(ctx, key, tmp, written, mimeType); err != nil {
		return nil, cerrors.Internal("failed to store file", err)
	}

	now := time.Now().UTC()
	record := &entity.LibraryFile{
		ID:          id.New(),
		UserID:      userID,
		WorkspaceID: workspaceID,
		Filename:    filename,
		Extension:   ext,
		MimeType:    mimeType,
		Size:        written,
		StoragePath: key,
		CreatedAt:   now,
		SharedWith:  []entity.FileSharedTarget{},
	}
	if err := s.files.CreateFile(ctx, record); err != nil {
		if cleanupErr := s.store.Delete(ctx, key); cleanupErr != nil {
			slog.ErrorContext(ctx, "failed to clean up stored library file after metadata error", "key", key, "error", cleanupErr)
		}
		return nil, cerrors.Internal("failed to create file record", err)
	}
	file, err := s.files.GetAccessibleFile(ctx, record.ID, userID)
	if err != nil {
		return nil, err
	}
	return &LibraryUploadResult{File: *file}, nil
}

func (s *Service) FavoriteFile(ctx context.Context, fileID, userID uuid.UUID, starred bool) error {
	if s.files == nil {
		return cerrors.Unavailable("file library is not configured")
	}
	return s.files.SetFavorite(ctx, userID, fileID, starred)
}

func (s *Service) StorageUsage(ctx context.Context, userID uuid.UUID) (*entity.FileStorageUsage, error) {
	if s.files == nil {
		return nil, cerrors.Unavailable("file library is not configured")
	}
	used, err := s.files.StorageUsedBytes(ctx, userID)
	if err != nil {
		return nil, cerrors.Internal("failed to read storage usage", err)
	}
	return &entity.FileStorageUsage{
		UsedBytes:    used,
		LimitBytes:   defaultUserStorageLimitBytes,
		UploadLimits: s.effectiveUploadLimits(),
	}, nil
}

func (s *Service) FileURL(ctx context.Context, fileID, userID uuid.UUID, disposition string) (*entity.FileDownloadURL, error) {
	if s.files == nil {
		return nil, cerrors.Unavailable("file library is not configured")
	}
	file, err := s.files.GetAccessibleFile(ctx, fileID, userID)
	if err != nil {
		return nil, err
	}
	disposition = normalizeDisposition(disposition, file.MimeType, file.Extension)
	url := s.sameOriginFileContentURL(file.ID, disposition)
	if signer, ok := s.store.(storage.DownloadSigner); ok && signer != nil {
		signedURL, err := signer.SignedDownloadURL(ctx, file.StoragePath, storage.SignedURLOptions{
			Filename:    file.Filename,
			ContentType: file.MimeType,
			ExpiresIn:   s.signedURLTTL,
			Attachment:  disposition != "inline",
		})
		if err != nil && !errors.Is(err, storage.ErrNotSupported) {
			return nil, cerrors.Internal("failed to sign file download", err)
		}
		if signedURL != "" {
			url = signedURL
		}
	}
	return &entity.FileDownloadURL{
		URL:       url,
		FileID:    file.ID,
		Filename:  file.Filename,
		Extension: file.Extension,
		MimeType:  file.MimeType,
		Size:      file.Size,
	}, nil
}

func (s *Service) DownloadLibraryFile(ctx context.Context, fileID, userID uuid.UUID) (io.ReadCloser, *entity.LibraryFile, error) {
	if s.files == nil {
		return nil, nil, cerrors.Unavailable("file library is not configured")
	}
	file, err := s.files.GetAccessibleFile(ctx, fileID, userID)
	if err != nil {
		return nil, nil, err
	}
	reader, _, err := s.store.Get(ctx, file.StoragePath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, cerrors.NotFound("file not found")
		}
		return nil, nil, cerrors.Internal("failed to read file", err)
	}
	return reader, file, nil
}

func (s *Service) DeleteLibraryFile(ctx context.Context, fileID, userID uuid.UUID) error {
	if s.files == nil {
		return cerrors.Unavailable("file library is not configured")
	}
	var deleted *entity.LibraryFile
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Files() == nil {
				return cerrors.Unavailable("file transaction scope is not configured")
			}
			file, err := scope.Files().DeleteFile(ctx, fileID, userID)
			if err != nil {
				return err
			}
			deleted = file
			return nil
		}); err != nil {
			return err
		}
	} else {
		file, err := s.files.DeleteFile(ctx, fileID, userID)
		if err != nil {
			return err
		}
		deleted = file
	}
	if deleted != nil {
		if err := s.store.Delete(ctx, deleted.StoragePath); err != nil {
			slog.ErrorContext(ctx, "failed to delete library object after metadata delete", "file_id", fileID, "error", err)
			return cerrors.Internal("failed to delete file", err)
		}
	}
	return nil
}

func (s *Service) ShareFile(ctx context.Context, fileID, userID uuid.UUID, targetType entity.FileShareTargetType, targetID uuid.UUID) error {
	if s.files == nil {
		return cerrors.Unavailable("file library is not configured")
	}
	workspaceID, err := s.workspaceForShareTarget(ctx, targetType, targetID, userID)
	if err != nil {
		return err
	}
	return s.files.ShareFile(ctx, fileID, repository.FileShareOptions{
		TargetType:  targetType,
		TargetID:    targetID,
		WorkspaceID: workspaceID,
		ActorID:     userID,
		OwnerOnly:   true,
	})
}

func (s *Service) RevokeFileShare(ctx context.Context, fileID, userID uuid.UUID, targetType entity.FileShareTargetType, targetID uuid.UUID) error {
	if s.files == nil {
		return cerrors.Unavailable("file library is not configured")
	}
	workspaceID, err := s.workspaceForShareTarget(ctx, targetType, targetID, userID)
	if err != nil {
		return err
	}
	return s.files.RevokeFileShare(ctx, fileID, repository.FileShareOptions{
		TargetType:  targetType,
		TargetID:    targetID,
		WorkspaceID: workspaceID,
		ActorID:     userID,
		OwnerOnly:   true,
	})
}

func (s *Service) ListFileShares(ctx context.Context, fileID, userID uuid.UUID) ([]entity.FileShare, error) {
	if s.files == nil {
		return nil, cerrors.Unavailable("file library is not configured")
	}
	return s.files.ListFileShares(ctx, fileID, userID)
}

func (s *Service) requireWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if _, err := s.members.GetMember(ctx, workspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a member of this workspace")
		}
		return cerrors.Internal("failed to verify workspace membership", err)
	}
	return nil
}

func (s *Service) workspaceForShareTarget(ctx context.Context, targetType entity.FileShareTargetType, targetID, userID uuid.UUID) (uuid.UUID, error) {
	if targetType == entity.FileShareTargetUser {
		return uuid.Nil, nil
	}
	channel, err := s.channels.GetByID(ctx, targetID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return uuid.Nil, cerrors.NotFound("target chat not found")
		}
		return uuid.Nil, cerrors.Internal("failed to load target chat", err)
	}
	if channel.WorkspaceID == nil {
		return uuid.Nil, cerrors.NotFound("target chat not found")
	}
	if s.access != nil {
		if _, err := s.access.Channel(ctx, targetID, userID, accesspolicy.CapabilityParticipate); err != nil {
			return uuid.Nil, err
		}
		return *channel.WorkspaceID, nil
	}
	if err := s.requireWorkspaceMember(ctx, *channel.WorkspaceID, userID); err != nil {
		return uuid.Nil, err
	}
	if channel.Type != entity.ChannelTypePublic {
		if _, err := s.channels.GetMember(ctx, targetID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return uuid.Nil, cerrors.Forbidden("you do not have access to this chat")
			}
			return uuid.Nil, cerrors.Internal("failed to verify chat membership", err)
		}
	}
	return *channel.WorkspaceID, nil
}

func (s *Service) effectiveUploadLimits() map[string]int64 {
	limits := make(map[string]int64, len(categoryUploadCaps))
	for category, capBytes := range categoryUploadCaps {
		limit := capBytes
		if s.maxFileSize > 0 && s.maxFileSize < limit {
			limit = s.maxFileSize
		}
		limits[category] = limit
	}
	return limits
}

func fileCategory(mimeType string, extension string) string {
	lowerMime := strings.ToLower(mimeType)
	lowerExt := strings.TrimPrefix(strings.ToLower(extension), ".")
	if strings.HasPrefix(lowerMime, "image/") {
		return "image"
	}
	if strings.HasPrefix(lowerMime, "video/") {
		return "video"
	}
	if strings.HasPrefix(lowerMime, "audio/") {
		return "audio"
	}
	if strings.HasPrefix(lowerMime, "text/") {
		return "text"
	}
	switch lowerExt {
	case "zip", "rar", "7z", "tar", "gz":
		return "archive"
	case "txt", "md", "csv", "json", "xml", "yaml", "yml":
		return "text"
	default:
		return "document"
	}
}

func normalizeDisposition(value string, mimeType string, extension string) string {
	if value == "inline" && inlinePreviewAllowed(mimeType, extension) {
		return "inline"
	}
	return "attachment"
}

func inlinePreviewAllowed(mimeType string, extension string) bool {
	lowerMime := strings.ToLower(mimeType)
	lowerExt := strings.TrimPrefix(strings.ToLower(extension), ".")
	if lowerExt == "svg" || lowerMime == "image/svg+xml" {
		return false
	}
	if strings.HasPrefix(lowerMime, "image/") || strings.HasPrefix(lowerMime, "video/") || strings.HasPrefix(lowerMime, "audio/") {
		return true
	}
	return lowerMime == "application/pdf" || lowerExt == "pdf"
}

func (s *Service) sameOriginFileContentURL(fileID uuid.UUID, disposition string) string {
	return "/api/v1/files/" + fileID.String() + "/content?disposition=" + disposition
}
