package entity

import (
	"time"

	"github.com/google/uuid"
)

type FileCategory string

const (
	FileCategoryImage    FileCategory = "image"
	FileCategoryDocument FileCategory = "document"
	FileCategoryArchive  FileCategory = "archive"
	FileCategoryVideo    FileCategory = "video"
	FileCategoryAudio    FileCategory = "audio"
	FileCategoryCode     FileCategory = "code"
)

type FileShareTargetType string

const (
	FileShareTargetChannel FileShareTargetType = "channel"
	FileShareTargetUser    FileShareTargetType = "user"
)

type FileSharedWithType string

const (
	FileSharedWithChannel FileSharedWithType = "channel"
	FileSharedWithDM      FileSharedWithType = "dm"
)

type FileOwner struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
}

type FileSharedTarget struct {
	Type        FileSharedWithType `json:"type"`
	TargetID    uuid.UUID          `json:"target_id"`
	WorkspaceID uuid.UUID          `json:"workspace_id"`
	DisplayName string             `json:"display_name"`
	AvatarURL   string             `json:"avatar_url,omitempty"`
	MemberCount *int               `json:"member_count,omitempty"`
}

type FileChatFacet struct {
	FileSharedTarget
	Count int `json:"count"`
}

type FileTypeFacet struct {
	Type  FileCategory `json:"type"`
	Count int          `json:"count"`
}

type FileListFacets struct {
	Types []FileTypeFacet `json:"types"`
	Chats []FileChatFacet `json:"chats"`
}

type LibraryFile struct {
	ID          uuid.UUID          `json:"id"`
	UserID      uuid.UUID          `json:"user_id"`
	WorkspaceID uuid.UUID          `json:"workspace_id,omitempty"`
	Filename    string             `json:"filename"`
	Extension   string             `json:"extension"`
	MimeType    string             `json:"mime_type"`
	Size        int64              `json:"size"`
	StoragePath string             `json:"-"`
	CreatedAt   time.Time          `json:"created_at"`
	DeletedAt   *time.Time         `json:"-"`
	Starred     bool               `json:"starred"`
	PreviewURL  string             `json:"preview_url,omitempty"`
	Owner       FileOwner          `json:"owner"`
	SharedWith  []FileSharedTarget `json:"shared_with"`
}

type FileShare struct {
	ID        uuid.UUID           `json:"id"`
	FileID    uuid.UUID           `json:"file_id"`
	Type      FileShareTargetType `json:"type"`
	TargetID  uuid.UUID           `json:"target_id"`
	CreatedAt time.Time           `json:"created_at"`
}

type FileListResult struct {
	Files      []LibraryFile  `json:"files"`
	NextCursor string         `json:"next_cursor,omitempty"`
	TotalCount int            `json:"total_count"`
	TotalBytes int64          `json:"total_bytes"`
	Facets     FileListFacets `json:"facets"`
}

type FileDownloadURL struct {
	URL       string    `json:"url"`
	FileID    uuid.UUID `json:"file_id"`
	Filename  string    `json:"filename"`
	Extension string    `json:"extension"`
	MimeType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
}

type FileStorageUsage struct {
	UsedBytes    int64            `json:"used_bytes"`
	LimitBytes   int64            `json:"limit_bytes"`
	UploadLimits map[string]int64 `json:"upload_limits"`
}

type MessageFile struct {
	ID         uuid.UUID `json:"id"`
	Status     string    `json:"status,omitempty"`
	Filename   string    `json:"filename,omitempty"`
	Extension  string    `json:"extension,omitempty"`
	MimeType   string    `json:"mime_type,omitempty"`
	Size       int64     `json:"size,omitempty"`
	PreviewURL string    `json:"preview_url,omitempty"`
}
