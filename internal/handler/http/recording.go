package http

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/recording"
)

// safeMIMETypes is a whitelist of MIME types allowed to be served inline.
// Anything else is forced to application/octet-stream to prevent stored XSS.
var safeMIMETypes = map[string]bool{
	"audio/aac":        true,
	"audio/mp4":        true,
	"audio/mpeg":       true,
	"audio/ogg":        true,
	"audio/opus":       true,
	"audio/wav":        true,
	"audio/webm":       true,
	"video/mp4":        true,
	"video/webm":       true,
	"video/ogg":        true,
	"application/json": true,
	"text/plain":       true,
	"text/vtt":         true,
}

// RecordingHandler handles recording HTTP endpoints.
type RecordingHandler struct {
	svc *recording.Service
}

// NewRecordingHandler creates a new RecordingHandler.
func NewRecordingHandler(svc *recording.Service) *RecordingHandler {
	return &RecordingHandler{svc: svc}
}

type startRecordingRequest struct {
	Strategy entity.RecordingStrategy `json:"strategy,omitempty"`
}

func (h *RecordingHandler) Start(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req startRecordingRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	rec, err := h.svc.StartRecording(r.Context(), workspaceID, callID, userID, req.Strategy)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, rec)
}

func (h *RecordingHandler) Stop(w http.ResponseWriter, r *http.Request) {
	recordingID, err := id.Parse(chi.URLParam(r, "recordingID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.StopRecording(r.Context(), workspaceID, recordingID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *RecordingHandler) Get(w http.ResponseWriter, r *http.Request) {
	recordingID, err := id.Parse(chi.URLParam(r, "recordingID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	rec, err := h.svc.GetRecording(r.Context(), workspaceID, recordingID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, rec)
}

func (h *RecordingHandler) ListByCall(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	recordings, err := h.svc.ListByCall(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, recordings)
}

func (h *RecordingHandler) ListArtifacts(w http.ResponseWriter, r *http.Request) {
	recordingID, err := id.Parse(chi.URLParam(r, "recordingID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	artifacts, err := h.svc.ListArtifacts(r.Context(), workspaceID, recordingID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, artifacts)
}

func (h *RecordingHandler) DownloadArtifact(w http.ResponseWriter, r *http.Request) {
	recordingID, err := id.Parse(chi.URLParam(r, "recordingID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	artifactID, err := id.Parse(chi.URLParam(r, "artifactID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if signedURL, _, err := h.svc.PresignArtifactDownload(r.Context(), workspaceID, recordingID, artifactID, userID); err != nil {
		writeErr(w, err)
		return
	} else if signedURL != "" {
		http.Redirect(w, r, signedURL, http.StatusTemporaryRedirect)
		return
	}
	reader, info, artifact, err := h.svc.DownloadArtifact(r.Context(), workspaceID, recordingID, artifactID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer reader.Close()

	// Only allow whitelisted MIME types to prevent stored XSS via Content-Type
	// manipulation. Fall back to octet-stream for anything else.
	contentType := "application/octet-stream"
	if artifact.MimeType != "" && safeMIMETypes[strings.ToLower(artifact.MimeType)] {
		contentType = artifact.MimeType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size))
	// Sanitize filename using RFC 5987 encoding to prevent header injection.
	safeFilename := url.PathEscape(artifactID.String() + "." + artifact.Format)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+safeFilename)
	if _, err := io.Copy(w, reader); err != nil {
		// At this point a binary response may already be in flight, so avoid
		// attempting to serialize a JSON error body on top of it.
		return
	}
}
