package http

import (
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/file"
)

// validStorageKey rejects obvious path-traversal and malformed storage keys
// at the edge. The service layer also performs attachment-table lookups that
// enforce exact-match authorization, but rejecting bad input early keeps
// audit logs clean and limits the attack surface.
func validStorageKey(key string) bool {
	if key == "" || len(key) > 512 {
		return false
	}
	if strings.ContainsAny(key, "\x00\r\n\\") {
		return false
	}
	if strings.HasPrefix(key, "/") {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func libraryDisposition(requested string, file *entity.LibraryFile) string {
	if requested != "inline" || file == nil {
		return "attachment"
	}
	mimeType := strings.ToLower(file.MimeType)
	extension := strings.TrimPrefix(strings.ToLower(file.Extension), ".")
	if extension == "svg" || mimeType == "image/svg+xml" {
		return "attachment"
	}
	if strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "video/") || strings.HasPrefix(mimeType, "audio/") {
		return "inline"
	}
	if mimeType == "application/pdf" || extension == "pdf" {
		return "inline"
	}
	return "attachment"
}

func contentDispositionHeader(disposition, filename string) string {
	if disposition != "inline" {
		disposition = "attachment"
	}
	safeFilename := contentDispositionFilename(filename)
	header := mime.FormatMediaType(disposition, map[string]string{"filename": safeFilename})
	if header == "" {
		return disposition + `; filename="download"`
	}
	return header
}

func contentDispositionFilename(filename string) string {
	base := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	base = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, base)
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "download"
	}
	return base
}

// FileHandler handles file upload and download HTTP endpoints.
type FileHandler struct {
	svc         *file.Service
	maxFileSize int64
}

// NewFileHandler creates a new FileHandler.
func NewFileHandler(svc *file.Service, maxFileSize int64) *FileHandler {
	return &FileHandler{svc: svc, maxFileSize: maxFileSize}
}

func (h *FileHandler) ListLibrary(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := id.Parse(r.URL.Query().Get("workspace_id"))
	if err != nil {
		writeErr(w, cerrors.InvalidInput("workspace_id is required"))
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > 100 {
			writeErr(w, cerrors.InvalidInput("limit must be between 1 and 100"))
			return
		}
		limit = parsed
	}
	var chatID *uuid.UUID
	if value := r.URL.Query().Get("chat_id"); value != "" {
		parsed, err := id.Parse(value)
		if err != nil {
			writeErr(w, cerrors.InvalidInput("chat_id must be a valid id"))
			return
		}
		chatID = &parsed
	}
	userID := middleware.UserIDFromContext(r.Context())
	result, err := h.svc.ListLibraryFiles(r.Context(), repository.FileListParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Query:       r.URL.Query().Get("q"),
		Sort:        r.URL.Query().Get("sort"),
		Dir:         r.URL.Query().Get("dir"),
		Scope:       r.URL.Query().Get("scope"),
		ChatID:      chatID,
		Limit:       limit,
		Cursor:      r.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (h *FileHandler) UploadLibrary(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxFileSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, cerrors.InvalidInput("file too large"))
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, cerrors.InvalidInput("missing file field"))
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.ErrorContext(r.Context(), "failed to close library multipart upload file", "filename", header.Filename, "error", err)
		}
	}()

	contextValue := r.FormValue("workspace_id")
	if contextValue == "" {
		contextValue = r.FormValue("context_id")
	}
	contextID, err := id.Parse(contextValue)
	if err != nil {
		writeErr(w, cerrors.InvalidInput("workspace_id or context_id is required"))
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID, err := h.svc.ResolveUploadWorkspace(r.Context(), contextID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := h.svc.UploadLibrary(r.Context(), workspaceID, userID, header.Filename, f, header.Size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeCreated(w, result.File)
}

func (h *FileHandler) FileURL(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	result, err := h.svc.FileURL(r.Context(), fileID, userID, r.URL.Query().Get("disposition"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (h *FileHandler) DownloadLibraryContent(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	reader, file, err := h.svc.DownloadLibraryFile(r.Context(), fileID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer func() {
		if err := reader.Close(); err != nil {
			slog.ErrorContext(r.Context(), "failed to close library file reader", "file_id", fileID, "error", err)
		}
	}()

	disposition := libraryDisposition(r.URL.Query().Get("disposition"), file)
	w.Header().Set("Content-Type", file.MimeType)
	w.Header().Set("Content-Disposition", contentDispositionHeader(disposition, file.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, reader); err != nil {
		if cerrors.IsClientDisconnect(err) {
			slog.DebugContext(r.Context(), "library file download aborted: client disconnected", "file_id", fileID, "error", err)
			return
		}
		slog.ErrorContext(r.Context(), "failed to stream library file download", "file_id", fileID, "error", err)
	}
}

func (h *FileHandler) DeleteLibrary(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.DeleteLibraryFile(r.Context(), fileID, userID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *FileHandler) Favorite(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.FavoriteFile(r.Context(), fileID, userID, true); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *FileHandler) Unfavorite(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.FavoriteFile(r.Context(), fileID, userID, false); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *FileHandler) StorageUsage(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	result, err := h.svc.StorageUsage(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

type fileShareRequest struct {
	Type     entity.FileShareTargetType `json:"type"`
	TargetID string                     `json:"target_id"`
}

func (h *FileHandler) Share(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req fileShareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Type != entity.FileShareTargetChannel && req.Type != entity.FileShareTargetUser {
		writeErr(w, cerrors.InvalidInput("type must be channel or user"))
		return
	}
	targetID, err := id.Parse(req.TargetID)
	if err != nil {
		writeErr(w, cerrors.InvalidInput("target_id must be a valid id"))
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.ShareFile(r.Context(), fileID, userID, req.Type, targetID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *FileHandler) RevokeShare(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req fileShareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Type != entity.FileShareTargetChannel && req.Type != entity.FileShareTargetUser {
		writeErr(w, cerrors.InvalidInput("type must be channel or user"))
		return
	}
	targetID, err := id.Parse(req.TargetID)
	if err != nil {
		writeErr(w, cerrors.InvalidInput("target_id must be a valid id"))
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.RevokeFileShare(r.Context(), fileID, userID, req.Type, targetID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *FileHandler) ListShares(w http.ResponseWriter, r *http.Request) {
	fileID, err := id.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	shares, err := h.svc.ListFileShares(r.Context(), fileID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if shares == nil {
		shares = []entity.FileShare{}
	}
	writeOK(w, map[string][]entity.FileShare{"shares": shares})
}

// Upload handles multipart file uploads. The file is attached to a message.
func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	// Limit request body size to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxFileSize)

	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB memory limit
		writeErr(w, cerrors.InvalidInput("file too large"))
		return
	}

	f, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, cerrors.InvalidInput("missing file field"))
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.ErrorContext(r.Context(), "failed to close multipart upload file", "filename", header.Filename, "error", err)
		}
	}()

	userID := middleware.UserIDFromContext(r.Context())
	result, err := h.svc.Upload(r.Context(), channelID, messageID, userID, header.Filename, f, header.Size)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, result)
}

// Download serves a file by its storage key. Forces attachment disposition to
// prevent inline rendering of potentially malicious content (XSS via HTML/SVG).
func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "*")
	if !validStorageKey(key) {
		writeErr(w, cerrors.InvalidInput("invalid file key"))
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	if signedURL, err := h.svc.PresignDownloadByKey(r.Context(), key, userID); err != nil {
		writeErr(w, err)
		return
	} else if signedURL != "" {
		http.Redirect(w, r, signedURL, http.StatusTemporaryRedirect)
		return
	}
	reader, info, err := h.svc.DownloadByKey(r.Context(), key, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer func() {
		if err := reader.Close(); err != nil {
			slog.ErrorContext(r.Context(), "failed to close file download reader", "key", key, "error", err)
		}
	}()

	// Force binary content type for safety; override only for known-safe types.
	contentType := "application/octet-stream"
	if info.MimeType != "" {
		contentType = info.MimeType
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDispositionHeader("attachment", key))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, reader); err != nil {
		if cerrors.IsClientDisconnect(err) {
			// Client navigated away / aborted mid-download — not a server fault.
			slog.DebugContext(r.Context(), "file download aborted: client disconnected", "key", key, "error", err)
			return
		}
		slog.ErrorContext(r.Context(), "failed to stream file download", "key", key, "error", err)
	}
}
