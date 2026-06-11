package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
)

func TestCategorizeFileMatchesFrontendContract(t *testing.T) {
	tests := []struct {
		name      string
		mimeType  string
		extension string
		want      entity.FileCategory
	}{
		{name: "image extension wins over mime", mimeType: "application/octet-stream", extension: "heic", want: entity.FileCategoryImage},
		{name: "video extension", extension: "mp4", want: entity.FileCategoryVideo},
		{name: "audio extension", extension: "mp3", want: entity.FileCategoryAudio},
		{name: "archive extension", extension: "zip", want: entity.FileCategoryArchive},
		{name: "code extension is public document category", extension: "md", want: entity.FileCategoryDocument},
		{name: "text extension is public document category", extension: "txt", want: entity.FileCategoryDocument},
		{name: "mime fallback image", mimeType: "image/png", extension: "bin", want: entity.FileCategoryImage},
		{name: "mime fallback archive", mimeType: "application/gzip", extension: "bin", want: entity.FileCategoryArchive},
		{name: "text mime falls back to document", mimeType: "text/plain", extension: "bin", want: entity.FileCategoryDocument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := categorizeFile(tt.mimeType, tt.extension); got != tt.want {
				t.Fatalf("categorizeFile(%q, %q) = %q, want %q", tt.mimeType, tt.extension, got, tt.want)
			}
		})
	}
}

func TestFilterLibraryFilesByCategory(t *testing.T) {
	workspaceID := uuid.New()
	otherWorkspaceID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	files := []entity.LibraryFile{
		{
			ID:          uuid.New(),
			UserID:      userID,
			WorkspaceID: workspaceID,
			Filename:    "notes.md",
			Extension:   "md",
			MimeType:    "text/markdown",
			CreatedAt:   now,
		},
		{
			ID:          uuid.New(),
			UserID:      userID,
			WorkspaceID: workspaceID,
			Filename:    "photo.png",
			Extension:   "png",
			MimeType:    "image/png",
			CreatedAt:   now,
		},
		{
			ID:          uuid.New(),
			UserID:      userID,
			WorkspaceID: otherWorkspaceID,
			Filename:    "other.png",
			Extension:   "png",
			MimeType:    "image/png",
			CreatedAt:   now,
		},
	}

	documents := filterLibraryFiles(files, workspaceID, "", "all", nil, entity.FileCategoryDocument, userID)
	if len(documents) != 1 || documents[0].Filename != "notes.md" {
		t.Fatalf("document category files = %+v, want notes.md only", documents)
	}

	images := filterLibraryFiles(files, workspaceID, "", "all", nil, entity.FileCategoryImage, userID)
	if len(images) != 1 || images[0].Filename != "photo.png" {
		t.Fatalf("image category files = %+v, want photo.png only", images)
	}
}

func TestBuildFileFacetsNeverEmitsTextCategory(t *testing.T) {
	workspaceID := uuid.New()
	userID := uuid.New()
	facets := buildFileFacets([]entity.LibraryFile{
		{
			ID:          uuid.New(),
			UserID:      userID,
			WorkspaceID: workspaceID,
			Filename:    "plain.txt",
			Extension:   "txt",
			MimeType:    "text/plain",
		},
		{
			ID:          uuid.New(),
			UserID:      userID,
			WorkspaceID: workspaceID,
			Filename:    "data.json",
			Extension:   "json",
			MimeType:    "application/json",
		},
	})

	if len(facets.Types) != 1 {
		t.Fatalf("facet types = %+v, want one document facet", facets.Types)
	}
	if got := facets.Types[0]; got.Type != entity.FileCategoryDocument || got.Count != 2 {
		t.Fatalf("facet = %+v, want document count 2", got)
	}
}
