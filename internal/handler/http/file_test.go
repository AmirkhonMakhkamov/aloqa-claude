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
	wshandler "aloqa/internal/handler/ws"
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

func TestDownloadLibraryContentAcceptsSessionCookie(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	fileID := uuid.New()
	storagePath := "library/2026/06/12/image.png"
	body := []byte("pngdata")

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
			Filename:    "image.png",
			Extension:   "png",
			MimeType:    "image/png",
			Size:        int64(len(body)),
			StoragePath: storagePath,
			CreatedAt:   time.Now().UTC(),
		},
	}})

	handler := NewFileHandler(svc, 1024)
	router := NewRouter(RouterDeps{
		Auth:             &AuthHandler{},
		Account:          &AccountHandler{},
		Channels:         &ChannelHandler{},
		Saved:            &SavedHandler{},
		Messages:         &MessageHandler{},
		Calls:            &CallHandler{},
		Breakout:         &BreakoutHandler{},
		Files:            handler,
		Presence:         &PresenceHandler{},
		Recordings:       &RecordingHandler{},
		Notifications:    &NotificationHandler{},
		Search:           &SearchHandler{},
		Admin:            &AdminHandler{},
		Guests:           &GuestHandler{},
		WS:               &wshandler.Handler{},
		Validator:        fakeTokenValidator{userID: uuid.New()},
		PersonalResolver: fakePersonalResolver{workspaceID: uuid.New()},
		SessionResolver:  fakeSessionResolver{bySession: map[string]uuid.UUID{"sess-1": userID}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+fileID.String()+"/content?disposition=inline", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-1"})
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	if !bytes.Equal(res.Body.Bytes(), body) {
		t.Fatalf("body = %q, want %q", res.Body.Bytes(), body)
	}
	if got := res.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	disposition, _, err := mime.ParseMediaType(res.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	if disposition != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", disposition)
	}
}

func TestDownloadAttachmentAcceptsSessionCookie(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	key := "attachments/2026/06/12/image.png"
	body := []byte("pngdata")
	createdAt := time.Now().UTC()

	svc := filesvc.NewService(
		&fileHTTPStorage{objects: map[string][]byte{key: body}},
		&messageHTTPMessageRepo{
			messages: map[uuid.UUID]*entity.Message{
				messageID: {
					ID:        messageID,
					ChannelID: channelID,
					UserID:    userID,
					Content:   "image",
					Type:      entity.MessageTypeText,
					CreatedAt: createdAt,
					UpdatedAt: createdAt,
				},
			},
			attachmentsByKey: map[string]*entity.Attachment{
				key: {
					ID:          uuid.New(),
					MessageID:   messageID,
					FileName:    "image.png",
					FileSize:    int64(len(body)),
					MimeType:    "image/png",
					StoragePath: key,
					URL:         "/files/" + key,
					CreatedAt:   createdAt,
				},
			},
		},
		&messageHTTPChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		}},
		&fakeHTTPWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
		}},
		nil,
		nil,
		filesvc.Config{},
		nil,
	)
	handler := NewFileHandler(svc, 1024)
	handler.SetSessionResolver(fakeSessionResolver{bySession: map[string]uuid.UUID{"sess-1": userID}})

	router := chi.NewRouter()
	router.Get("/files/*", handler.Download)

	req := httptest.NewRequest(http.MethodGet, "/files/"+key, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-1"})
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	if !bytes.Equal(res.Body.Bytes(), body) {
		t.Fatalf("body = %q, want %q", res.Body.Bytes(), body)
	}
	if got := res.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	disposition, _, err := mime.ParseMediaType(res.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	if disposition != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", disposition)
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
