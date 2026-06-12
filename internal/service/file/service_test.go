package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/storage"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/collabaccess"
	"aloqa/internal/security/guestaccess"
)

func TestUploadScannerDoesNotConsumeStoredBody(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	userID := uuid.New()
	body := []byte("hello scanned world")

	store := &memoryStorage{}
	scanner := &consumingScanner{}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "file"},
		},
		attachmentsByKey: map[string]*entity.Attachment{},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}, members: map[[2]uuid.UUID]*entity.ChannelMember{
		{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleMember},
	}}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}

	svc := NewService(store, messages, channels, members, scanner, nil, Config{MaxFileSize: 1024}, nil)
	result, err := svc.Upload(ctx, channelID, messageID, userID, "note.txt", bytes.NewReader(body), int64(len(body)), "")
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if !bytes.Equal(scanner.scanned, body) {
		t.Fatalf("scanner read %q, want %q", scanner.scanned, body)
	}
	if !bytes.Equal(store.objects[result.Attachment.StoragePath], body) {
		t.Fatalf("stored body = %q, want %q", store.objects[result.Attachment.StoragePath], body)
	}
}

func TestDownloadByKeyRequiresMessageAccess(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	ownerID := uuid.New()
	intruderID := uuid.New()
	key := "attachments/2026/04/07/file.txt"

	store := &memoryStorage{objects: map[string][]byte{key: []byte("secret")}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: ownerID, Content: "secret"},
		},
		attachmentsByKey: map[string]*entity.Attachment{
			key: {ID: uuid.New(), MessageID: messageID, FileName: "file.txt", FileSize: 6, MimeType: "text/plain", StoragePath: key},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
	}, members: map[[2]uuid.UUID]*entity.ChannelMember{
		{channelID, ownerID}: {ChannelID: channelID, UserID: ownerID, Role: entity.ChannelRoleMember},
	}}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, ownerID}: {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleMember},
	}}

	svc := NewService(store, messages, channels, members, nil, nil, Config{}, nil)
	if _, _, err := svc.DownloadByKey(ctx, key, intruderID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("DownloadByKey intruder error = %v, want FORBIDDEN", err)
	}
}

func TestDownloadByKeyDistinguishesMissingFileFromStorageFailure(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	userID := uuid.New()
	key := "attachments/2026/04/07/file.txt"

	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "file"},
		},
		attachmentsByKey: map[string]*entity.Attachment{
			key: {ID: uuid.New(), MessageID: messageID, FileName: "file.txt", FileSize: 6, MimeType: "text/plain", StoragePath: key},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}

	missingStore := &memoryStorage{objects: map[string][]byte{}}
	svc := NewService(missingStore, messages, channels, members, nil, nil, Config{}, nil)
	if _, _, err := svc.DownloadByKey(ctx, key, userID); !hasCode(err, cerrors.CodeNotFound) {
		t.Fatalf("DownloadByKey missing file error = %v, want NOT_FOUND", err)
	}

	storageErr := errors.New("storage unavailable")
	failingStore := &memoryStorage{getErr: storageErr}
	svc = NewService(failingStore, messages, channels, members, nil, nil, Config{}, nil)
	errReader, _, err := svc.DownloadByKey(ctx, key, userID)
	if errReader != nil {
		errReader.Close()
	}
	if !hasCode(err, cerrors.CodeInternal) {
		t.Fatalf("DownloadByKey storage failure error = %v, want INTERNAL", err)
	}
	if !errors.Is(err, storageErr) {
		t.Fatalf("DownloadByKey storage failure should wrap original error, got %v", err)
	}
}

func TestGuestGrantAllowsFileDownloadForInvitedChannel(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	userID := uuid.New()
	key := "attachments/2026/04/07/file.txt"

	store := &memoryStorage{objects: map[string][]byte{key: []byte("hello")}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "file"},
		},
		attachmentsByKey: map[string]*entity.Attachment{
			key: {ID: uuid.New(), MessageID: messageID, FileName: "file.txt", FileSize: 5, MimeType: "text/plain", StoragePath: key},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
	}}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})

	svc := NewService(store, messages, channels, &fakeWorkspaceRepo{}, nil, nil, Config{}, guests)
	reader, info, err := svc.DownloadByKey(ctx, key, userID)
	if err != nil {
		t.Fatalf("DownloadByKey returned error: %v", err)
	}
	defer reader.Close()
	if info.MimeType != "text/plain" {
		t.Fatalf("MimeType = %q, want text/plain", info.MimeType)
	}
}

func TestCollaboratorUploadUsesSharedAccessPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	sourceUserID := uuid.New()
	collaboratorID := uuid.New()
	body := []byte("shared")

	store := &memoryStorage{}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: sourceUserID, Content: "shared"},
		},
		attachmentsByKey: map[string]*entity.Attachment{},
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, sourceUserID}:   {ChannelID: channelID, UserID: sourceUserID, Role: entity.ChannelRoleMember},
			{channelID, collaboratorID}: {ChannelID: channelID, UserID: collaboratorID, Role: entity.ChannelRoleMember},
		},
	}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, sourceUserID}: {WorkspaceID: workspaceID, UserID: sourceUserID, Role: entity.WorkspaceRoleMember},
	}}

	svc := NewService(store, messages, channels, members, nil, nil, Config{}, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(members, channels, nil, fakeCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: true},
	}))

	result, err := svc.Upload(ctx, channelID, messageID, collaboratorID, "shared.txt", bytes.NewReader(body), int64(len(body)), "")
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if len(store.objects[result.Attachment.StoragePath]) == 0 {
		t.Fatalf("expected collaborator upload to be stored")
	}
}

func TestUploadDisplayMode(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	userID := uuid.New()
	body := []byte("image-bytes")

	newSvc := func() *Service {
		store := &memoryStorage{}
		scanner := &consumingScanner{}
		messages := &fakeMessageRepo{
			messages: map[uuid.UUID]*entity.Message{
				messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "file"},
			},
			attachmentsByKey: map[string]*entity.Attachment{},
		}
		channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		}, members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleMember},
		}}
		members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
		}}
		return NewService(store, messages, channels, members, scanner, nil, Config{MaxFileSize: 1024}, nil)
	}

	for _, mode := range []string{"", "photo", "file"} {
		t.Run("persists the valid mode "+strconv.Quote(mode), func(t *testing.T) {
			result, err := newSvc().Upload(ctx, channelID, messageID, userID, "photo.png", bytes.NewReader(body), int64(len(body)), mode)
			if err != nil {
				t.Fatalf("Upload returned error: %v", err)
			}
			if result.Attachment.DisplayMode != mode {
				t.Fatalf("DisplayMode = %q, want %q", result.Attachment.DisplayMode, mode)
			}
		})
	}

	t.Run("rejects an unknown mode", func(t *testing.T) {
		_, err := newSvc().Upload(ctx, channelID, messageID, userID, "photo.png", bytes.NewReader(body), int64(len(body)), "thumbnail")
		if err == nil {
			t.Fatalf("expected an error for an invalid display_mode")
		}
	})
}

func TestPresignDownloadByKeyUsesObjectStoreSignerWhenAvailable(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	userID := uuid.New()
	key := "attachments/2026/04/15/file.txt"

	store := &memoryStorage{
		objects:   map[string][]byte{key: []byte("hello")},
		signedURL: "https://objects.example.com/presigned/file.txt",
	}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "file"},
		},
		attachmentsByKey: map[string]*entity.Attachment{
			key: {ID: uuid.New(), MessageID: messageID, FileName: "file.txt", FileSize: 5, MimeType: "text/plain", StoragePath: key},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}, members: map[[2]uuid.UUID]*entity.ChannelMember{
		{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleMember},
	}}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}

	svc := NewService(store, messages, channels, members, nil, nil, Config{SignedURLTTL: time.Minute}, nil)
	url, err := svc.PresignDownloadByKey(ctx, key, userID)
	if err != nil {
		t.Fatalf("PresignDownloadByKey returned error: %v", err)
	}
	if url != store.signedURL {
		t.Fatalf("signed URL = %q, want %q", url, store.signedURL)
	}
	if len(store.signedKeys) != 1 || store.signedKeys[0] != key {
		t.Fatalf("signed keys = %v, want [%s]", store.signedKeys, key)
	}
	if len(store.signedOpts) != 1 || !store.signedOpts[0].Attachment {
		t.Fatalf("signed options = %+v, want attachment disposition", store.signedOpts)
	}
}

func TestPresignDownloadByKeyUsesInlineDispositionForImages(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	messageID := uuid.New()
	userID := uuid.New()
	key := "attachments/2026/04/15/image.png"

	store := &memoryStorage{
		objects:   map[string][]byte{key: []byte("image")},
		signedURL: "https://objects.example.com/presigned/image.png",
	}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "image"},
		},
		attachmentsByKey: map[string]*entity.Attachment{
			key: {ID: uuid.New(), MessageID: messageID, FileName: "image.png", FileSize: 5, MimeType: "image/png", StoragePath: key},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}, members: map[[2]uuid.UUID]*entity.ChannelMember{
		{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleMember},
	}}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}

	svc := NewService(store, messages, channels, members, nil, nil, Config{SignedURLTTL: time.Minute}, nil)
	if _, err := svc.PresignDownloadByKey(ctx, key, userID); err != nil {
		t.Fatalf("PresignDownloadByKey returned error: %v", err)
	}
	if len(store.signedOpts) != 1 || store.signedOpts[0].Attachment {
		t.Fatalf("signed options = %+v, want inline disposition", store.signedOpts)
	}
}

func TestUploadLibraryReturnsQuotaExceededBeforePersisting(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	store := &memoryStorage{}
	files := &fakeLibraryFileRepo{usedBytes: defaultUserStorageLimitBytes - 1}
	members := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}

	svc := NewService(store, &fakeMessageRepo{}, &fakeChannelRepo{}, members, nil, nil, Config{MaxFileSize: 1024}, nil)
	svc.SetFileRepository(files)

	_, err := svc.UploadLibrary(ctx, workspaceID, userID, "note.txt", bytes.NewReader([]byte("hello")), 5)
	if !hasCode(err, cerrors.CodeQuotaExceeded) {
		t.Fatalf("UploadLibrary error = %v, want QUOTA_EXCEEDED", err)
	}
	if len(store.objects) != 0 {
		t.Fatalf("stored objects = %v, want none after quota failure", store.objects)
	}
	if len(files.created) != 0 {
		t.Fatalf("created records = %d, want none after quota failure", len(files.created))
	}
}

func TestFileURLInlineDispositionFallsBackForSVG(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	fileID := uuid.New()
	files := &fakeLibraryFileRepo{files: map[uuid.UUID]*entity.LibraryFile{
		fileID: {
			ID:          fileID,
			UserID:      userID,
			WorkspaceID: workspaceID,
			Filename:    "icon.svg",
			Extension:   "svg",
			MimeType:    "image/svg+xml",
			Size:        100,
			StoragePath: "library/icon.svg",
		},
	}}

	svc := NewService(&memoryStorage{}, &fakeMessageRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, nil, nil, Config{}, nil)
	svc.SetFileRepository(files)

	result, err := svc.FileURL(ctx, fileID, userID, "inline")
	if err != nil {
		t.Fatalf("FileURL returned error: %v", err)
	}
	if !strings.Contains(result.URL, "disposition=attachment") {
		t.Fatalf("FileURL URL = %q, want attachment disposition for SVG", result.URL)
	}
}

type consumingScanner struct {
	scanned []byte
}

func (s *consumingScanner) Scan(_ context.Context, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.scanned = data
	return nil
}

type memoryStorage struct {
	objects    map[string][]byte
	deleted    []string
	getErr     error
	signedURL  string
	signedKeys []string
	signedOpts []storage.SignedURLOptions
}

func (s *memoryStorage) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
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

func (s *memoryStorage) Get(_ context.Context, key string) (io.ReadCloser, *storage.FileInfo, error) {
	if s.getErr != nil {
		return nil, nil, s.getErr
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), &storage.FileInfo{Key: key, Size: int64(len(data))}, nil
}

func (s *memoryStorage) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.objects, key)
	return nil
}

func (s *memoryStorage) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.objects[key]
	return ok, nil
}

func (s *memoryStorage) SignedDownloadURL(_ context.Context, key string, opts storage.SignedURLOptions) (string, error) {
	s.signedKeys = append(s.signedKeys, key)
	s.signedOpts = append(s.signedOpts, opts)
	if s.signedURL == "" {
		return "", storage.ErrNotSupported
	}
	return s.signedURL, nil
}

type fakeWorkspaceRepo struct {
	members map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *fakeWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error          { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
func (r *fakeWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (r *fakeWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *fakeWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params, string) ([]entity.WorkspaceMember, error) {
	return nil, nil
}

type fakeGuestAccessRepo struct {
	grants []entity.GuestAccessGrant
}

func (r *fakeGuestAccessRepo) CreateGrant(context.Context, *entity.GuestAccessGrant) error {
	return nil
}
func (r *fakeGuestAccessRepo) ListActiveByUserWorkspace(_ context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error) {
	var active []entity.GuestAccessGrant
	for _, grant := range r.grants {
		if grant.UserID == userID && grant.WorkspaceID == workspaceID && grant.ExpiresAt.After(now) {
			active = append(active, grant)
		}
	}
	return active, nil
}

type fakeCollabChecker struct {
	decision collabaccess.Decision
	err      error
}

func (f fakeCollabChecker) AuthorizeChannel(context.Context, uuid.UUID, uuid.UUID) (collabaccess.Decision, error) {
	return f.decision, f.err
}

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
}

func (r *fakeChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (r *fakeChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if ch := r.channels[id]; ch != nil {
		return ch, nil
	}
	return nil, cerrors.NotFound("channel not found")
}
func (r *fakeChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}
func (r *fakeChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (r *fakeChannelRepo) ListArchivedByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	return nil, nil
}
func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error          { return nil }
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error               { return nil }
func (r *fakeChannelRepo) AddMember(context.Context, *entity.ChannelMember) error { return nil }
func (r *fakeChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("channel member not found")
}
func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *fakeChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

type fakeMessageRepo struct {
	messages         map[uuid.UUID]*entity.Message
	attachmentsByKey map[string]*entity.Attachment
}

func (r *fakeMessageRepo) Create(context.Context, *entity.Message) error { return nil }
func (r *fakeMessageRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Message, error) {
	if msg := r.messages[id]; msg != nil {
		return msg, nil
	}
	return nil, cerrors.NotFound("message not found")
}
func (r *fakeMessageRepo) ListByChannel(context.Context, uuid.UUID, pagination.Params) ([]entity.Message, error) {
	return nil, nil
}
func (r *fakeMessageRepo) ListThreadReplies(context.Context, uuid.UUID, pagination.Params) ([]entity.Message, error) {
	return nil, nil
}
func (r *fakeMessageRepo) HasActiveMessage(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}
func (r *fakeMessageRepo) Update(context.Context, *entity.Message) error { return nil }
func (r *fakeMessageRepo) SoftDelete(context.Context, uuid.UUID) error   { return nil }
func (r *fakeMessageRepo) SoftDeleteWithCascade(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (r *fakeMessageRepo) Pin(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeMessageRepo) Unpin(context.Context, uuid.UUID) error { return nil }
func (r *fakeMessageRepo) ListPinned(context.Context, uuid.UUID) ([]entity.Message, error) {
	return nil, nil
}
func (r *fakeMessageRepo) AddReaction(context.Context, *entity.Reaction) error { return nil }
func (r *fakeMessageRepo) GetReactionByID(context.Context, uuid.UUID) (*entity.Reaction, error) {
	return nil, cerrors.NotFound("reaction not found")
}
func (r *fakeMessageRepo) GetReactionByMessageUserEmoji(context.Context, uuid.UUID, uuid.UUID, string) (*entity.Reaction, error) {
	return nil, cerrors.NotFound("reaction not found")
}
func (r *fakeMessageRepo) RemoveReaction(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (r *fakeMessageRepo) RemoveReactionByID(context.Context, uuid.UUID) error { return nil }
func (r *fakeMessageRepo) ListReactions(context.Context, uuid.UUID) ([]entity.Reaction, error) {
	return nil, nil
}
func (r *fakeMessageRepo) ListReactionsByMessageIDs(context.Context, []uuid.UUID) (map[uuid.UUID][]entity.Reaction, error) {
	return nil, nil
}
func (r *fakeMessageRepo) CreateAttachment(_ context.Context, a *entity.Attachment) error {
	if r.attachmentsByKey == nil {
		r.attachmentsByKey = map[string]*entity.Attachment{}
	}
	r.attachmentsByKey[a.StoragePath] = a
	return nil
}
func (r *fakeMessageRepo) DeleteAttachment(_ context.Context, id uuid.UUID) error {
	for key, attachment := range r.attachmentsByKey {
		if attachment != nil && attachment.ID == id {
			delete(r.attachmentsByKey, key)
			return nil
		}
	}
	return cerrors.NotFound("attachment not found")
}
func (r *fakeMessageRepo) GetAttachmentByStoragePath(_ context.Context, storagePath string) (*entity.Attachment, error) {
	if attachment := r.attachmentsByKey[storagePath]; attachment != nil {
		return attachment, nil
	}
	return nil, cerrors.NotFound("attachment not found")
}
func (r *fakeMessageRepo) ListAttachments(context.Context, uuid.UUID) ([]entity.Attachment, error) {
	return nil, nil
}
func (r *fakeMessageRepo) ListAttachmentsByMessageIDs(context.Context, []uuid.UUID) (map[uuid.UUID][]entity.Attachment, error) {
	return nil, nil
}
func (r *fakeMessageRepo) CountUnread(context.Context, uuid.UUID, uuid.UUID, time.Time) (int, error) {
	return 0, nil
}
func (r *fakeMessageRepo) BatchUnreadCounts(context.Context, uuid.UUID, uuid.UUID) ([]repository.UnreadSummary, error) {
	return nil, nil
}
func (r *fakeMessageRepo) CountThreadReplies(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

func (r *fakeMessageRepo) HardDelete(context.Context, uuid.UUID) error { return nil }

type fakeLibraryFileRepo struct {
	files     map[uuid.UUID]*entity.LibraryFile
	usedBytes int64
	created   []*entity.LibraryFile
}

func (r *fakeLibraryFileRepo) CreateFile(_ context.Context, file *entity.LibraryFile) error {
	if r.files == nil {
		r.files = map[uuid.UUID]*entity.LibraryFile{}
	}
	copy := *file
	r.files[file.ID] = &copy
	r.created = append(r.created, &copy)
	return nil
}

func (r *fakeLibraryFileRepo) GetAccessibleFile(_ context.Context, fileID, _ uuid.UUID) (*entity.LibraryFile, error) {
	if file := r.files[fileID]; file != nil {
		copy := *file
		return &copy, nil
	}
	return nil, cerrors.NotFound("file not found")
}

func (r *fakeLibraryFileRepo) GetAccessibleFileByStoragePath(_ context.Context, storagePath string, _ uuid.UUID) (*entity.LibraryFile, error) {
	for _, file := range r.files {
		if file.StoragePath == storagePath {
			copy := *file
			return &copy, nil
		}
	}
	return nil, cerrors.NotFound("file not found")
}

func (r *fakeLibraryFileRepo) ListFiles(context.Context, repository.FileListParams) (entity.FileListResult, error) {
	return entity.FileListResult{}, nil
}

func (r *fakeLibraryFileRepo) SetFavorite(context.Context, uuid.UUID, uuid.UUID, bool) error {
	return nil
}

func (r *fakeLibraryFileRepo) StorageUsedBytes(context.Context, uuid.UUID) (int64, error) {
	return r.usedBytes, nil
}

func (r *fakeLibraryFileRepo) DeleteFile(context.Context, uuid.UUID, uuid.UUID) (*entity.LibraryFile, error) {
	return nil, nil
}

func (r *fakeLibraryFileRepo) ShareFile(context.Context, uuid.UUID, repository.FileShareOptions) error {
	return nil
}

func (r *fakeLibraryFileRepo) RevokeFileShare(context.Context, uuid.UUID, repository.FileShareOptions) error {
	return nil
}

func (r *fakeLibraryFileRepo) ListFileShares(context.Context, uuid.UUID, uuid.UUID) ([]entity.FileShare, error) {
	return nil, nil
}

func (r *fakeLibraryFileRepo) ResolveMessageFiles(context.Context, []uuid.UUID) ([]entity.MessageFile, error) {
	return nil, nil
}
