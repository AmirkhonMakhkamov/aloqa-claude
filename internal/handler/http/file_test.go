package http

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/platform/storage"
	filesvc "aloqa/internal/service/file"
)

func TestDownloadLibraryContentEscapesContentDispositionFilename(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	fileID := uuid.New()
	storagePath := "library/2026/06/10/report.pdf"
	body := []byte("%PDF test")

	svc := filesvc.NewService(
		&fileHTTPStorage{objects: map[string][]byte{storagePath: body}},
		nil,
		nil,
		nil,
		nil,
		nil,
		filesvc.Config{},
		nil,
	)
	svc.SetFileRepository(&messageHTTPFileRepo{files: map[uuid.UUID]*entity.LibraryFile{
		fileID: {
			ID:          fileID,
			UserID:      userID,
			WorkspaceID: workspaceID,
			Filename:    "../quarterly\"\r\nX-Injected: yes.pdf",
			Extension:   "pdf",
			MimeType:    "application/pdf",
			Size:        int64(len(body)),
			StoragePath: storagePath,
			CreatedAt:   time.Now().UTC(),
		},
	}})

	handler := NewFileHandler(svc, 1024)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Get("/api/v1/files/{fileID}/content", handler.DownloadLibraryContent)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+fileID.String()+"/content?disposition=inline", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	if !bytes.Equal(res.Body.Bytes(), body) {
		t.Fatalf("body = %q, want %q", res.Body.Bytes(), body)
	}

	got := res.Header().Get("Content-Disposition")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("Content-Disposition contains a header separator: %q", got)
	}
	disposition, params, err := mime.ParseMediaType(got)
	if err != nil {
		t.Fatalf("parse Content-Disposition %q: %v", got, err)
	}
	if disposition != "inline" {
		t.Fatalf("disposition = %q, want inline", disposition)
	}
	if params["filename"] != "quarterly\"X-Injected: yes.pdf" {
		t.Fatalf("filename param = %q, want sanitized filename", params["filename"])
	}
}

type fileHTTPStorage struct {
	objects map[string][]byte
}

func (s *fileHTTPStorage) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}

func (s *fileHTTPStorage) Get(_ context.Context, key string) (io.ReadCloser, *storage.FileInfo, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), &storage.FileInfo{
		Key:  key,
		Size: int64(len(data)),
	}, nil
}

func (s *fileHTTPStorage) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func (s *fileHTTPStorage) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.objects[key]
	return ok, nil
}
