package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
)

type FileRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

type fileCursor struct {
	ID string `json:"id"`
}

func NewFileRepo(pool *pgxpool.Pool) *FileRepo {
	return &FileRepo{pool: pool, db: pool}
}

func (r *FileRepo) withTx(tx pgx.Tx) *FileRepo {
	if r == nil {
		return nil
	}
	return &FileRepo{pool: r.pool, db: tx}
}

func (r *FileRepo) CreateFile(ctx context.Context, file *entity.LibraryFile) error {
	query := `
		INSERT INTO library_files (id, user_id, workspace_id, filename, extension, mime_type, size, storage_path, created_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err := r.db.Exec(
		ctx,
		query,
		file.ID,
		file.UserID,
		file.WorkspaceID,
		file.Filename,
		file.Extension,
		file.MimeType,
		file.Size,
		file.StoragePath,
		file.CreatedAt,
		file.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create library file: %w", err)
	}
	return nil
}

func (r *FileRepo) GetAccessibleFile(ctx context.Context, fileID, userID uuid.UUID) (*entity.LibraryFile, error) {
	files, err := r.loadAccessibleFiles(ctx, userID, &fileID, "")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, cerrors.NotFound("file not found")
	}
	if err := r.hydrateSharedTargets(ctx, userID, files); err != nil {
		return nil, err
	}
	return &files[0], nil
}

func (r *FileRepo) GetAccessibleFileByStoragePath(ctx context.Context, storagePath string, userID uuid.UUID) (*entity.LibraryFile, error) {
	files, err := r.loadAccessibleFiles(ctx, userID, nil, storagePath)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, cerrors.NotFound("file not found")
	}
	if err := r.hydrateSharedTargets(ctx, userID, files); err != nil {
		return nil, err
	}
	return &files[0], nil
}

func (r *FileRepo) ListFiles(ctx context.Context, params repository.FileListParams) (entity.FileListResult, error) {
	limit := params.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	sortKey := normalizeFileSort(params.Sort)
	dir := normalizeFileDir(params.Dir, sortKey)
	scope := normalizeFileScope(params.Scope)
	category := normalizeFileCategory(params.Category)

	files, err := r.loadAccessibleFiles(ctx, params.UserID, nil, "")
	if err != nil {
		return entity.FileListResult{}, err
	}
	if err := r.hydrateSharedTargets(ctx, params.UserID, files); err != nil {
		return entity.FileListResult{}, err
	}

	filtered := filterLibraryFiles(files, params.WorkspaceID, strings.TrimSpace(params.Query), scope, params.ChatID, category, params.UserID)
	sortLibraryFiles(filtered, sortKey, dir)

	totalBytes := int64(0)
	for _, file := range filtered {
		totalBytes += file.Size
	}
	facets := buildFileFacets(filtered)

	start := 0
	if params.Cursor != "" {
		cursorID, err := decodeFileCursor(params.Cursor)
		if err != nil {
			return entity.FileListResult{}, err
		}
		found := false
		for i := range filtered {
			if filtered[i].ID == cursorID {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return entity.FileListResult{}, cerrors.InvalidInput("invalid file cursor")
		}
	}

	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := filtered[start:end]
	nextCursor := ""
	if end < len(filtered) && len(page) > 0 {
		nextCursor = encodeFileCursor(page[len(page)-1].ID)
	}

	return entity.FileListResult{
		Files:      page,
		NextCursor: nextCursor,
		TotalCount: len(filtered),
		TotalBytes: totalBytes,
		Facets:     facets,
	}, nil
}

func (r *FileRepo) SetFavorite(ctx context.Context, userID, fileID uuid.UUID, starred bool) error {
	if _, err := r.GetAccessibleFile(ctx, fileID, userID); err != nil {
		return err
	}
	if starred {
		_, err := r.db.Exec(ctx, `
			INSERT INTO file_favorites (user_id, file_id)
			VALUES ($1, $2)
			ON CONFLICT (user_id, file_id) DO NOTHING`, userID, fileID)
		if err != nil {
			return fmt.Errorf("postgres: favorite file: %w", err)
		}
		return nil
	}
	_, err := r.db.Exec(ctx, `DELETE FROM file_favorites WHERE user_id = $1 AND file_id = $2`, userID, fileID)
	if err != nil {
		return fmt.Errorf("postgres: unfavorite file: %w", err)
	}
	return nil
}

func (r *FileRepo) StorageUsedBytes(ctx context.Context, userID uuid.UUID) (int64, error) {
	var used int64
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(size), 0)
		FROM library_files
		WHERE user_id = $1 AND deleted_at IS NULL`, userID).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("postgres: file storage usage: %w", err)
	}
	return used, nil
}

func (r *FileRepo) DeleteFile(ctx context.Context, fileID, userID uuid.UUID) (*entity.LibraryFile, error) {
	accessible, err := r.GetAccessibleFile(ctx, fileID, userID)
	if err != nil {
		return nil, err
	}
	if accessible.UserID != userID {
		return nil, cerrors.Forbidden("only file owner can delete this file")
	}

	now := time.Now().UTC()
	query := `
		UPDATE library_files
		SET deleted_at = $3
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		RETURNING id, user_id, workspace_id, filename, extension, mime_type, size, storage_path, created_at, deleted_at`
	file := entity.LibraryFile{}
	err = r.db.QueryRow(ctx, query, fileID, userID, now).Scan(
		&file.ID,
		&file.UserID,
		&file.WorkspaceID,
		&file.Filename,
		&file.Extension,
		&file.MimeType,
		&file.Size,
		&file.StoragePath,
		&file.CreatedAt,
		&file.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("file not found")
		}
		return nil, fmt.Errorf("postgres: delete library file: %w", err)
	}
	if _, err := r.db.Exec(ctx, `DELETE FROM library_file_shares WHERE file_id = $1`, fileID); err != nil {
		return nil, fmt.Errorf("postgres: revoke deleted file shares: %w", err)
	}
	return &file, nil
}

func (r *FileRepo) ShareFile(ctx context.Context, fileID uuid.UUID, opts repository.FileShareOptions) error {
	file, err := r.GetAccessibleFile(ctx, fileID, opts.ActorID)
	if err != nil {
		return err
	}
	if opts.OwnerOnly && file.UserID != opts.ActorID {
		return cerrors.Forbidden("only file owner can share this file")
	}
	workspaceID := opts.WorkspaceID
	if workspaceID == uuid.Nil {
		workspaceID = file.WorkspaceID
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO library_file_shares (file_id, target_type, target_id, workspace_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (file_id, target_type, target_id) DO NOTHING`,
		fileID,
		opts.TargetType,
		opts.TargetID,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("postgres: share library file: %w", err)
	}
	return nil
}

func (r *FileRepo) RevokeFileShare(ctx context.Context, fileID uuid.UUID, opts repository.FileShareOptions) error {
	file, err := r.GetAccessibleFile(ctx, fileID, opts.ActorID)
	if err != nil {
		return err
	}
	if opts.OwnerOnly && file.UserID != opts.ActorID {
		return cerrors.Forbidden("only file owner can revoke this share")
	}
	_, err = r.db.Exec(ctx, `
		DELETE FROM library_file_shares
		WHERE file_id = $1 AND target_type = $2 AND target_id = $3`,
		fileID,
		opts.TargetType,
		opts.TargetID,
	)
	if err != nil {
		return fmt.Errorf("postgres: revoke library file share: %w", err)
	}
	return nil
}

func (r *FileRepo) ListFileShares(ctx context.Context, fileID, userID uuid.UUID) ([]entity.FileShare, error) {
	file, err := r.GetAccessibleFile(ctx, fileID, userID)
	if err != nil {
		return nil, err
	}
	if file.UserID != userID {
		return nil, cerrors.Forbidden("only file owner can list shares")
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, file_id, target_type, target_id, created_at
		FROM library_file_shares
		WHERE file_id = $1
		ORDER BY created_at ASC, id ASC`, fileID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list library file shares: %w", err)
	}
	defer rows.Close()

	shares := []entity.FileShare{}
	for rows.Next() {
		var share entity.FileShare
		if err := rows.Scan(&share.ID, &share.FileID, &share.Type, &share.TargetID, &share.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: list library file shares scan: %w", err)
		}
		shares = append(shares, share)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list library file shares rows: %w", err)
	}
	return shares, nil
}

func (r *FileRepo) ResolveMessageFiles(ctx context.Context, fileIDs []uuid.UUID) ([]entity.MessageFile, error) {
	if len(fileIDs) == 0 {
		return []entity.MessageFile{}, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, filename, extension, mime_type, size, deleted_at
		FROM library_files
		WHERE id = ANY($1)`, fileIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: resolve message files: %w", err)
	}
	defer rows.Close()

	byID := make(map[uuid.UUID]entity.MessageFile, len(fileIDs))
	for rows.Next() {
		var file entity.MessageFile
		var deletedAt *time.Time
		if err := rows.Scan(&file.ID, &file.Filename, &file.Extension, &file.MimeType, &file.Size, &deletedAt); err != nil {
			return nil, fmt.Errorf("postgres: resolve message files scan: %w", err)
		}
		if deletedAt != nil {
			file = entity.MessageFile{ID: file.ID, Status: "deleted"}
		} else if shouldExposePreviewURL(file.MimeType, file.Extension, true) {
			file.PreviewURL = fileURL(file.ID, "inline")
		}
		byID[file.ID] = file
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: resolve message files rows: %w", err)
	}

	resolved := make([]entity.MessageFile, 0, len(fileIDs))
	for _, id := range fileIDs {
		if file, ok := byID[id]; ok {
			resolved = append(resolved, file)
		}
	}
	return resolved, nil
}

func (r *FileRepo) loadAccessibleFiles(ctx context.Context, userID uuid.UUID, fileID *uuid.UUID, storagePath string) ([]entity.LibraryFile, error) {
	args := []any{userID}
	filters := []string{"f.deleted_at IS NULL"}
	if fileID != nil {
		args = append(args, *fileID)
		filters = append(filters, fmt.Sprintf("f.id = $%d", len(args)))
	}
	if storagePath != "" {
		args = append(args, storagePath)
		filters = append(filters, fmt.Sprintf("f.storage_path = $%d", len(args)))
	}
	where := strings.Join(filters, " AND ")
	query := fmt.Sprintf(`
		SELECT
			f.id, f.user_id, f.workspace_id, f.filename, f.extension, f.mime_type, f.size, f.storage_path, f.created_at, f.deleted_at,
			EXISTS(SELECT 1 FROM file_favorites fav WHERE fav.user_id = $1 AND fav.file_id = f.id) AS starred,
			u.id, u.display_name, COALESCE(u.avatar_url, '')
		FROM library_files f
		INNER JOIN users u ON u.id = f.user_id
		WHERE %s
			AND (
				f.user_id = $1
				OR EXISTS (
					SELECT 1
					FROM library_file_shares s
					LEFT JOIN channel_members cm
						ON s.target_type = 'channel'
						AND cm.channel_id = s.target_id
						AND cm.user_id = $1
					WHERE s.file_id = f.id
						AND (
							(s.target_type = 'user' AND s.target_id = $1)
							OR (s.target_type = 'channel' AND cm.user_id IS NOT NULL)
						)
				)
			)
		ORDER BY f.created_at DESC, f.id DESC`, where)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: load accessible library files: %w", err)
	}
	defer rows.Close()

	files := []entity.LibraryFile{}
	for rows.Next() {
		var file entity.LibraryFile
		if err := rows.Scan(
			&file.ID,
			&file.UserID,
			&file.WorkspaceID,
			&file.Filename,
			&file.Extension,
			&file.MimeType,
			&file.Size,
			&file.StoragePath,
			&file.CreatedAt,
			&file.DeletedAt,
			&file.Starred,
			&file.Owner.ID,
			&file.Owner.DisplayName,
			&file.Owner.AvatarURL,
		); err != nil {
			return nil, fmt.Errorf("postgres: load accessible library files scan: %w", err)
		}
		if shouldExposePreviewURL(file.MimeType, file.Extension, false) {
			file.PreviewURL = fileURL(file.ID, "inline")
		}
		file.SharedWith = []entity.FileSharedTarget{}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: load accessible library files rows: %w", err)
	}
	return files, nil
}

func (r *FileRepo) hydrateSharedTargets(ctx context.Context, viewerID uuid.UUID, files []entity.LibraryFile) error {
	if len(files) == 0 {
		return nil
	}
	fileIDs := make([]uuid.UUID, len(files))
	indexByID := make(map[uuid.UUID]int, len(files))
	for i := range files {
		fileIDs[i] = files[i].ID
		indexByID[files[i].ID] = i
	}

	rows, err := r.db.Query(ctx, `
		SELECT
			s.file_id, s.target_type, s.target_id, s.workspace_id,
			COALESCE(c.type, ''),
			COALESCE(c.name, ''),
			COALESCE(u.display_name, ''),
			COALESCE(u.avatar_url, ''),
			COALESCE((
				SELECT ou.display_name
				FROM channel_members ocm
				INNER JOIN users ou ON ou.id = ocm.user_id
				WHERE ocm.channel_id = s.target_id AND ocm.user_id <> $2
				ORDER BY ou.display_name ASC, ou.id ASC
				LIMIT 1
			), ''),
			COALESCE((
				SELECT ou.avatar_url
				FROM channel_members ocm
				INNER JOIN users ou ON ou.id = ocm.user_id
				WHERE ocm.channel_id = s.target_id AND ocm.user_id <> $2
				ORDER BY ou.display_name ASC, ou.id ASC
				LIMIT 1
			), ''),
			COALESCE((
				SELECT COUNT(*)
				FROM channel_members cm
				WHERE cm.channel_id = s.target_id
			), 0)
		FROM library_file_shares s
		LEFT JOIN channels c ON s.target_type = 'channel' AND c.id = s.target_id
		LEFT JOIN users u ON s.target_type = 'user' AND u.id = s.target_id
		WHERE s.file_id = ANY($1)
		ORDER BY s.created_at ASC, s.id ASC`, fileIDs, viewerID)
	if err != nil {
		return fmt.Errorf("postgres: hydrate file shares: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fileID uuid.UUID
		var targetType entity.FileShareTargetType
		var targetID uuid.UUID
		var workspaceID uuid.UUID
		var channelType string
		var channelName string
		var userName string
		var userAvatar string
		var dmName string
		var dmAvatar string
		var memberCount int
		if err := rows.Scan(
			&fileID,
			&targetType,
			&targetID,
			&workspaceID,
			&channelType,
			&channelName,
			&userName,
			&userAvatar,
			&dmName,
			&dmAvatar,
			&memberCount,
		); err != nil {
			return fmt.Errorf("postgres: hydrate file shares scan: %w", err)
		}
		fileIndex, ok := indexByID[fileID]
		if !ok {
			continue
		}
		target := entity.FileSharedTarget{
			Type:        entity.FileSharedWithChannel,
			TargetID:    targetID,
			WorkspaceID: workspaceID,
			DisplayName: channelName,
		}
		if targetType == entity.FileShareTargetUser || channelType == string(entity.ChannelTypeDM) || channelType == string(entity.ChannelTypeGroupDM) {
			target.Type = entity.FileSharedWithDM
			target.DisplayName = userName
			target.AvatarURL = userAvatar
			if dmName != "" {
				target.DisplayName = dmName
				target.AvatarURL = dmAvatar
			}
		} else {
			count := memberCount
			target.MemberCount = &count
		}
		files[fileIndex].SharedWith = append(files[fileIndex].SharedWith, target)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: hydrate file shares rows: %w", err)
	}
	return nil
}

func filterLibraryFiles(files []entity.LibraryFile, workspaceID uuid.UUID, query string, scope string, chatID *uuid.UUID, category entity.FileCategory, userID uuid.UUID) []entity.LibraryFile {
	lowerQuery := strings.ToLower(query)
	filtered := make([]entity.LibraryFile, 0, len(files))
	for _, file := range files {
		if file.WorkspaceID != workspaceID {
			continue
		}
		if lowerQuery != "" && !strings.Contains(strings.ToLower(file.Filename), lowerQuery) {
			continue
		}
		if scope == "mine" && file.UserID != userID {
			continue
		}
		if scope == "shared" && file.UserID == userID {
			continue
		}
		if scope == "favorites" && !file.Starred {
			continue
		}
		if chatID != nil && !fileSharedWithChat(file, *chatID) {
			continue
		}
		if category != "" && categorizeFile(file.MimeType, file.Extension) != category {
			continue
		}
		filtered = append(filtered, file)
	}
	return filtered
}

func sortLibraryFiles(files []entity.LibraryFile, sortKey string, dir string) {
	sort.SliceStable(files, func(i, j int) bool {
		left := files[i]
		right := files[j]
		less := false
		switch sortKey {
		case "name":
			leftName := strings.ToLower(left.Filename)
			rightName := strings.ToLower(right.Filename)
			if leftName != rightName {
				less = leftName < rightName
				break
			}
			if !left.CreatedAt.Equal(right.CreatedAt) {
				less = left.CreatedAt.Before(right.CreatedAt)
				break
			}
			less = left.ID.String() < right.ID.String()
		case "size":
			if left.Size != right.Size {
				less = left.Size < right.Size
				break
			}
			if !left.CreatedAt.Equal(right.CreatedAt) {
				less = left.CreatedAt.Before(right.CreatedAt)
				break
			}
			less = left.ID.String() < right.ID.String()
		default:
			if !left.CreatedAt.Equal(right.CreatedAt) {
				less = left.CreatedAt.Before(right.CreatedAt)
				break
			}
			less = left.ID.String() < right.ID.String()
		}
		if dir == "desc" {
			return !less && left.ID != right.ID
		}
		return less
	})
}

func buildFileFacets(files []entity.LibraryFile) entity.FileListFacets {
	typeCounts := map[entity.FileCategory]int{}
	chatCounts := map[uuid.UUID]entity.FileChatFacet{}
	for _, file := range files {
		category := categorizeFile(file.MimeType, file.Extension)
		typeCounts[category]++
		seenChats := map[uuid.UUID]struct{}{}
		for _, target := range file.SharedWith {
			if _, ok := seenChats[target.TargetID]; ok {
				continue
			}
			seenChats[target.TargetID] = struct{}{}
			facet := chatCounts[target.TargetID]
			if facet.TargetID == uuid.Nil {
				facet.FileSharedTarget = target
			}
			facet.Count++
			chatCounts[target.TargetID] = facet
		}
	}

	types := make([]entity.FileTypeFacet, 0, len(typeCounts))
	for category, count := range typeCounts {
		types = append(types, entity.FileTypeFacet{Type: category, Count: count})
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i].Type < types[j].Type
	})

	chats := make([]entity.FileChatFacet, 0, len(chatCounts))
	for _, facet := range chatCounts {
		chats = append(chats, facet)
	}
	sort.Slice(chats, func(i, j int) bool {
		if chats[i].Type != chats[j].Type {
			return chats[i].Type < chats[j].Type
		}
		if chats[i].DisplayName != chats[j].DisplayName {
			return chats[i].DisplayName < chats[j].DisplayName
		}
		return chats[i].TargetID.String() < chats[j].TargetID.String()
	})
	return entity.FileListFacets{Types: types, Chats: chats}
}

func fileSharedWithChat(file entity.LibraryFile, chatID uuid.UUID) bool {
	for _, target := range file.SharedWith {
		if target.TargetID == chatID {
			return true
		}
	}
	return false
}

func normalizeFileSort(value string) string {
	switch value {
	case "name", "size":
		return value
	default:
		return "date"
	}
}

func normalizeFileDir(value string, sortKey string) string {
	switch value {
	case "asc", "desc":
		return value
	default:
		if sortKey == "name" {
			return "asc"
		}
		return "desc"
	}
}

func normalizeFileScope(value string) string {
	switch value {
	case "mine", "shared", "favorites":
		return value
	default:
		return "all"
	}
}

func normalizeFileCategory(value string) entity.FileCategory {
	switch entity.FileCategory(value) {
	case entity.FileCategoryImage,
		entity.FileCategoryDocument,
		entity.FileCategoryArchive,
		entity.FileCategoryVideo,
		entity.FileCategoryAudio,
		entity.FileCategoryCode:
		return entity.FileCategory(value)
	default:
		return ""
	}
}

func decodeFileCursor(value string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return uuid.Nil, cerrors.InvalidInput("invalid file cursor")
	}
	var cursor fileCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return uuid.Nil, cerrors.InvalidInput("invalid file cursor")
	}
	id, err := uuid.Parse(cursor.ID)
	if err != nil {
		return uuid.Nil, cerrors.InvalidInput("invalid file cursor")
	}
	return id, nil
}

func encodeFileCursor(id uuid.UUID) string {
	raw, err := json.Marshal(fileCursor{ID: id.String()})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func categorizeFile(mimeType string, extension string) entity.FileCategory {
	lowerMime := strings.ToLower(mimeType)
	lowerExt := strings.TrimPrefix(strings.ToLower(extension), ".")
	switch lowerExt {
	case "avif", "bmp", "gif", "heic", "heif", "jpeg", "jpg", "png", "tif", "tiff", "webp":
		return entity.FileCategoryImage
	case "avi", "m4v", "mkv", "mov", "mp4", "mpeg", "mpg", "webm":
		return entity.FileCategoryVideo
	case "aac", "aiff", "flac", "m4a", "mp3", "ogg", "opus", "wav":
		return entity.FileCategoryAudio
	case "7z", "bz2", "gz", "rar", "tar", "tgz", "zip":
		return entity.FileCategoryArchive
	case "c", "cpp", "cs", "css", "csv", "diff", "doc", "docx", "go", "h", "html", "java", "js", "json", "jsx", "key", "kt", "log", "md", "numbers", "odp", "ods", "odt", "pages", "pdf", "php", "ppt", "pptx", "py", "rb", "rs", "rtf", "sh", "sql", "swift", "toml", "ts", "tsx", "txt", "xls", "xlsx", "xml", "yaml", "yml":
		return entity.FileCategoryDocument
	}
	if strings.HasPrefix(lowerMime, "image/") {
		return entity.FileCategoryImage
	}
	if strings.HasPrefix(lowerMime, "video/") {
		return entity.FileCategoryVideo
	}
	if strings.HasPrefix(lowerMime, "audio/") {
		return entity.FileCategoryAudio
	}
	switch lowerMime {
	case "application/zip", "application/x-7z-compressed", "application/x-rar-compressed", "application/gzip":
		return entity.FileCategoryArchive
	}
	return entity.FileCategoryDocument
}

func shouldExposePreviewURL(mimeType string, extension string, messageCard bool) bool {
	if !isInlinePreviewAllowed(mimeType, extension) {
		return false
	}
	if messageCard {
		return true
	}
	return strings.HasPrefix(strings.ToLower(mimeType), "image/")
}

func isInlinePreviewAllowed(mimeType string, extension string) bool {
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

func fileURL(fileID uuid.UUID, disposition string) string {
	if disposition == "" {
		return "/api/v1/files/" + fileID.String()
	}
	return "/api/v1/files/" + fileID.String() + "?disposition=" + disposition
}
