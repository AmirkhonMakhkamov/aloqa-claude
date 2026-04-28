package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("file not found")
var ErrNotSupported = errors.New("storage capability is not supported")

// FileInfo describes a stored file.
type FileInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	MimeType     string    `json:"mime_type"`
	CreatedAt    time.Time `json:"created_at"`
	ETag         string    `json:"etag,omitempty"`
	StorageClass string    `json:"storage_class,omitempty"`
}

type ObjectTier string

const (
	ObjectTierHot     ObjectTier = "hot"
	ObjectTierWarm    ObjectTier = "warm"
	ObjectTierArchive ObjectTier = "archive"
)

type SignedURLOptions struct {
	Filename    string
	ContentType string
	ExpiresIn   time.Duration
	Attachment  bool
}

type DownloadSigner interface {
	SignedDownloadURL(ctx context.Context, key string, opts SignedURLOptions) (string, error)
}

type TieredStorage interface {
	TransitionObject(ctx context.Context, key string, tier ObjectTier, storageClass string) error
	DefaultStorageClass(tier ObjectTier) string
}

// Storage abstracts file storage operations. Implementations may use local
// disk, S3-compatible object storage, or any other backend.
type Storage interface {
	// Put stores the contents of reader under the given key.
	Put(ctx context.Context, key string, reader io.Reader, size int64, mimeType string) error
	// Get returns a reader for the file at the given key.
	Get(ctx context.Context, key string) (io.ReadCloser, *FileInfo, error)
	// Delete removes the file at the given key.
	Delete(ctx context.Context, key string) error
	// Exists returns true if a file exists at the given key.
	Exists(ctx context.Context, key string) (bool, error)
}

// GenerateKey creates a storage key with path partitioning to avoid directory
// hotspots. Format: {prefix}/{YYYY}/{MM}/{DD}/{uuid}.{ext}
func GenerateKey(prefix, ext string) string {
	now := time.Now()
	id := uuid.New()
	return fmt.Sprintf("%s/%d/%02d/%02d/%s.%s",
		prefix, now.Year(), now.Month(), now.Day(), id, ext,
	)
}

// --- Local filesystem implementation ---

// LocalStorage stores files on the local filesystem.
type LocalStorage struct {
	basePath string
}

// NewLocalStorage creates a LocalStorage rooted at basePath. Creates the
// directory if it doesn't exist.
func NewLocalStorage(basePath string) (*LocalStorage, error) {
	if err := os.MkdirAll(basePath, 0o750); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	return &LocalStorage{basePath: basePath}, nil
}

// safePath resolves key against basePath and ensures the result stays within
// the base directory, preventing path traversal attacks.
func (s *LocalStorage) safePath(key string) (string, error) {
	fullPath := filepath.Join(s.basePath, filepath.Clean("/"+key))
	absBase, err := filepath.Abs(s.basePath)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	if !strings.HasPrefix(absPath, absBase+string(filepath.Separator)) && absPath != absBase {
		return "", fmt.Errorf("path traversal attempt: %s", key)
	}
	return absPath, nil
}

func (s *LocalStorage) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	fullPath, err := s.safePath(key)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
		return fmt.Errorf("create directory for %s: %w", key, err)
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("create file %s: %w", key, err)
	}

	if _, err := io.Copy(f, reader); err != nil {
		closeErr := f.Close()
		removeErr := os.Remove(fullPath)
		return fmt.Errorf("write file %s: %w", key, errors.Join(err, closeErr, removeErr))
	}
	if err := f.Close(); err != nil {
		removeErr := os.Remove(fullPath)
		return fmt.Errorf("close file %s: %w", key, errors.Join(err, removeErr))
	}

	return nil
}

func (s *LocalStorage) Get(_ context.Context, key string) (io.ReadCloser, *FileInfo, error) {
	fullPath, err := s.safePath(key)
	if err != nil {
		return nil, nil, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, nil, fmt.Errorf("stat file %s: %w", key, err)
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open file %s: %w", key, err)
	}

	return f, &FileInfo{
		Key:       key,
		Size:      info.Size(),
		CreatedAt: info.ModTime(),
	}, nil
}

func (s *LocalStorage) Delete(_ context.Context, key string) error {
	fullPath, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file %s: %w", key, err)
	}
	return nil
}

func (s *LocalStorage) Exists(_ context.Context, key string) (bool, error) {
	fullPath, err := s.safePath(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat file %s: %w", key, err)
	}
	return true, nil
}
