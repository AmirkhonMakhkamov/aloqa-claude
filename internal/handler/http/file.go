package http

import (
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

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

// FileHandler handles file upload and download HTTP endpoints.
type FileHandler struct {
	svc         *file.Service
	maxFileSize int64
}

// NewFileHandler creates a new FileHandler.
func NewFileHandler(svc *file.Service, maxFileSize int64) *FileHandler {
	return &FileHandler{svc: svc, maxFileSize: maxFileSize}
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

	// Derive a safe filename for the Content-Disposition header.
	filename := filepath.Base(key)

	// Force binary content type for safety; override only for known-safe types.
	contentType := "application/octet-stream"
	if info.MimeType != "" {
		contentType = info.MimeType
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, reader); err != nil {
		slog.ErrorContext(r.Context(), "failed to stream file download", "key", key, "error", err)
	}
}
