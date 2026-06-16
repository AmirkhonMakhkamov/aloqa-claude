package file

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/platform/storage"
	"aloqa/internal/platform/txscope"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/guestaccess"
	searchsvc "aloqa/internal/service/search"
)

// Scanner provides virus scanning for uploaded files.
// Implementations can integrate ClamAV, external APIs, etc.
type Scanner interface {
	// Scan checks the reader for malware. Returns nil if clean, an error
	// describing the threat if infected, and a separate error for scan failures.
	Scan(ctx context.Context, reader io.Reader, filename string) error
}

// noopScanner is used when no virus scanner is configured.
type noopScanner struct{}

func (noopScanner) Scan(context.Context, io.Reader, string) error { return nil }

type SearchIndexer interface {
	IndexFile(ctx context.Context, workspaceID, channelID, attachmentID, messageID uuid.UUID, fileName, mimeType string, createdAt time.Time) error
	DeleteFile(ctx context.Context, workspaceID, attachmentID uuid.UUID) error
}

// Service handles file uploads, downloads, and lifecycle management.
type Service struct {
	store    storage.Storage
	files    repository.FileRepository
	messages repository.MessageRepository
	channels repository.ChannelRepository
	members  repository.WorkspaceRepository
	scanner  Scanner
	guests   *guestaccess.Checker
	access   *accesspolicy.Checker
	search   SearchIndexer
	tx       txscope.Manager

	maxFileSize  int64
	allowedTypes map[string]bool
	signedURLTTL time.Duration
}

// Config holds file service configuration.
type Config struct {
	MaxFileSize  int64    // Maximum upload size in bytes.
	AllowedTypes []string // Allowed MIME types (empty = allow all).
	SignedURLTTL time.Duration
}

// NewService creates a new file service.
func NewService(
	store storage.Storage,
	messages repository.MessageRepository,
	channels repository.ChannelRepository,
	members repository.WorkspaceRepository,
	scanner Scanner,
	search SearchIndexer,
	cfg Config,
	guests *guestaccess.Checker,
) *Service {
	allowed := make(map[string]bool, len(cfg.AllowedTypes))
	for _, t := range cfg.AllowedTypes {
		allowed[t] = true
	}

	if scanner == nil {
		scanner = noopScanner{}
	}

	return &Service{
		store:        store,
		messages:     messages,
		channels:     channels,
		members:      members,
		scanner:      scanner,
		guests:       guests,
		search:       search,
		maxFileSize:  cfg.MaxFileSize,
		allowedTypes: allowed,
		signedURLTTL: cfg.SignedURLTTL,
	}
}

func (s *Service) SetAccessPolicy(access *accesspolicy.Checker) {
	s.access = access
}

func (s *Service) SetFileRepository(files repository.FileRepository) {
	s.files = files
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

// UploadResult contains information about a successfully uploaded file.
type UploadResult struct {
	Attachment *entity.Attachment `json:"attachment"`
}

func (s *Service) canAccessMessage(
	ctx context.Context,
	messageID, expectedChannelID, userID uuid.UUID,
	capability accesspolicy.Capability,
) error {
	msg, err := s.messages.GetByID(ctx, messageID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("message not found")
		}
		return cerrors.Internal("failed to get message", err)
	}
	if msg.DeletedAt != nil {
		return cerrors.NotFound("message has been deleted")
	}
	if expectedChannelID != uuid.Nil && msg.ChannelID != expectedChannelID {
		return cerrors.InvalidInput("message does not belong to this channel")
	}

	if s.access != nil {
		_, err := s.access.Channel(ctx, msg.ChannelID, userID, capability)
		return err
	}

	ch, err := s.channels.GetByID(ctx, msg.ChannelID)
	if err != nil {
		return cerrors.Internal("failed to get channel", err)
	}
	if ch.WorkspaceID == nil {
		return cerrors.NotFound("channel not found")
	}
	workspaceID := *ch.WorkspaceID
	if _, err := s.members.GetMember(ctx, workspaceID, userID); err == nil {
		if ch.Type == entity.ChannelTypePublic && capability == accesspolicy.CapabilityView {
			return nil
		}
		if _, err := s.channels.GetMember(ctx, ch.ID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return cerrors.Forbidden("you do not have access to this channel")
			}
			return cerrors.Internal("failed to verify channel membership", err)
		}
		return nil
	} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return cerrors.Internal("failed to verify workspace membership", err)
	}

	if s.guests != nil {
		allowed, err := s.guests.HasChannelAccess(ctx, workspaceID, ch.ID, userID)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}

	return cerrors.Forbidden("you do not have access to this channel")
}

// Upload stores a file and creates an attachment record linked to a message.
// It validates file size, MIME type, and optionally scans for viruses.
func (s *Service) Upload(
	ctx context.Context,
	channelID uuid.UUID,
	messageID uuid.UUID,
	userID uuid.UUID,
	filename string,
	reader io.Reader,
	size int64,
	displayMode string,
) (*UploadResult, error) {
	// "photo" (inline) / "file" (download card) / "audio" (voice/waveform) /
	// "" (auto: MIME heuristic). Any other value is rejected (ALK-926).
	if displayMode != "" && displayMode != "photo" && displayMode != "file" && displayMode != "audio" {
		return nil, cerrors.InvalidInput("display_mode must be 'photo', 'file', 'audio', or empty")
	}
	if err := s.canAccessMessage(ctx, messageID, channelID, userID, accesspolicy.CapabilityParticipate); err != nil {
		return nil, err
	}

	// Validate size.
	if s.maxFileSize > 0 && size > s.maxFileSize {
		return nil, cerrors.InvalidInput(fmt.Sprintf(
			"file size %d exceeds maximum of %d bytes", size, s.maxFileSize,
		))
	}

	// Detect MIME type from extension.
	ext := filepath.Ext(filename)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// A voice note signals display_mode='audio'. Go's stdlib maps .webm to
	// video/webm (and may miss other audio containers), so force a correct
	// audio/* type from the extension to keep voice notes classified as audio.
	if displayMode == "audio" {
		mimeType = audioMimeForExt(ext)
	}

	// Validate MIME type.
	if len(s.allowedTypes) > 0 {
		baseType := strings.Split(mimeType, ";")[0]
		if !s.allowedTypes[baseType] {
			return nil, cerrors.InvalidInput(fmt.Sprintf("file type %s is not allowed", baseType))
		}
	}

	tmp, err := os.CreateTemp("", "aloqa-upload-*")
	if err != nil {
		return nil, cerrors.Internal("failed to prepare upload", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, "failed to remove temporary upload file", "path", tmpName, "error", err)
		}
	}()
	defer func() {
		if err := tmp.Close(); err != nil {
			slog.WarnContext(ctx, "failed to close temporary upload file", "path", tmpName, "error", err)
		}
	}()

	written, err := io.Copy(tmp, reader)
	if err != nil {
		return nil, cerrors.Internal("failed to read upload", err)
	}
	if s.maxFileSize > 0 && written > s.maxFileSize {
		return nil, cerrors.InvalidInput(fmt.Sprintf(
			"file size %d exceeds maximum of %d bytes", written, s.maxFileSize,
		))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, cerrors.Internal("failed to prepare upload scan", err)
	}

	if err := s.scanner.Scan(ctx, tmp, filename); err != nil {
		slog.WarnContext(ctx, "virus scan rejected file", "filename", filename, "error", err)
		return nil, cerrors.Forbidden("file rejected by security scan")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, cerrors.Internal("failed to prepare upload storage", err)
	}

	// Generate storage key.
	cleanExt := strings.TrimPrefix(ext, ".")
	if cleanExt == "" {
		cleanExt = "bin"
	}
	key := storage.GenerateKey("attachments", cleanExt)

	// Voice notes from MediaRecorder ship a streaming container with no duration,
	// which makes the browser report Infinity and unreliably decode them. Remux
	// (no re-encode) so the stored clip carries a real duration and is reliably
	// seekable, then probe the duration and waveform server-side so the client
	// never has to decode the file. Best-effort: fall back to the original and
	// nil metadata on any failure so uploads never break.
	storeReader := io.Reader(tmp)
	storeSize := written
	var durationMs *int32
	var waveformPeaks []float32
	if displayMode == "audio" {
		// Isolate ffmpeg/ffprobe from the HTTP request deadline (WriteTimeout) so a
		// slow remux can't be killed mid-write and persist a corrupt file.
		audioCtx, cancelAudio := context.WithTimeout(context.Background(), audioProcessingTimeout)
		defer cancelAudio()

		audioPath := tmpName
		if remuxedPath, remuxedSize, ok := remuxAudioDuration(audioCtx, tmpName, ext); ok {
			if s.maxFileSize > 0 && remuxedSize > s.maxFileSize {
				removeQuietly(ctx, remuxedPath)
			} else {
				remuxedFile, openErr := os.Open(remuxedPath)
				if openErr == nil {
					defer func() {
						if cerr := remuxedFile.Close(); cerr != nil {
							slog.WarnContext(ctx, "failed to close remuxed upload file", "error", cerr)
						}
						removeQuietly(ctx, remuxedPath)
					}()
					storeReader = remuxedFile
					storeSize = remuxedSize
					audioPath = remuxedPath
				} else {
					removeQuietly(ctx, remuxedPath)
				}
			}
		}

		if probed, ok := probeAudioDurationMs(audioCtx, audioPath); ok {
			durationMs = &probed
		}
		waveformPeaks = extractWaveformPeaks(audioCtx, audioPath)
	}

	// Store the file.
	if err := s.store.Put(ctx, key, storeReader, storeSize, mimeType); err != nil {
		slog.ErrorContext(ctx, "failed to store file", "key", key, "error", err)
		return nil, cerrors.Internal("failed to store file", err)
	}

	// Create the attachment record.
	attachment := &entity.Attachment{
		ID:            id.New(),
		MessageID:     messageID,
		FileName:      filename,
		FileSize:      storeSize,
		MimeType:      mimeType,
		DisplayMode:   displayMode,
		DurationMs:    durationMs,
		WaveformPeaks: waveformPeaks,
		StoragePath:   key,
		URL:           "/files/" + key,
		CreatedAt:     time.Now(),
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("file transaction scope is not configured")
			}
			if err := scope.Messages().CreateAttachment(ctx, attachment); err != nil {
				return err
			}
			return s.enqueueFileSearchTx(ctx, scope, attachment, channelID, messageID, filename, mimeType)
		}); err != nil {
			if cleanupErr := s.store.Delete(ctx, key); cleanupErr != nil {
				slog.ErrorContext(ctx, "failed to clean up stored file after attachment transaction error", "key", key, "error", cleanupErr)
			}
			slog.ErrorContext(ctx, "failed to create attachment transaction", "key", key, "error", err)
			return nil, cerrors.Internal("failed to create attachment", err)
		}
	} else {
		if err := s.messages.CreateAttachment(ctx, attachment); err != nil {
			// Clean up the stored file on DB failure.
			if err := s.store.Delete(ctx, key); err != nil {
				slog.ErrorContext(ctx, "failed to clean up stored file after attachment error", "key", key, "error", err)
			}
			slog.ErrorContext(ctx, "failed to create attachment record", "key", key, "error", err)
			return nil, cerrors.Internal("failed to create attachment", err)
		}

		channel, err := s.channels.GetByID(ctx, channelID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to load channel for file search indexing", "channel_id", channelID, "error", err)
		} else if s.search != nil && channel.WorkspaceID != nil {
			if err := s.search.IndexFile(ctx, *channel.WorkspaceID, channelID, attachment.ID, messageID, filename, mimeType, attachment.CreatedAt); err != nil {
				slog.ErrorContext(ctx, "failed to enqueue file search index", "attachment_id", attachment.ID, "error", err)
			}
		}
	}

	slog.InfoContext(ctx, "file uploaded",
		"attachment_id", attachment.ID,
		"message_id", messageID,
		"filename", filename,
		"declared_size", size,
		"stored_size", storeSize,
		"mime_type", mimeType,
	)

	return &UploadResult{Attachment: attachment}, nil
}

// Download returns a reader for a stored attachment.
func (s *Service) Download(ctx context.Context, attachmentID uuid.UUID) (io.ReadCloser, *entity.Attachment, error) {
	// Look up attachment in all messages (we need the storage path).
	// For now we'll search by querying the attachment directly.
	// This requires a GetAttachment method - we'll use the message repo's list.
	return nil, nil, cerrors.Unavailable("download by attachment ID is not available")
}

// DownloadByKey returns a reader for a file stored at the given key after authorization.
func (s *Service) DownloadByKey(ctx context.Context, key string, userID uuid.UUID) (io.ReadCloser, *storage.FileInfo, error) {
	attachment, err := s.messages.GetAttachmentByStoragePath(ctx, key)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, nil, cerrors.NotFound("file not found")
		}
		return nil, nil, cerrors.Internal("failed to look up file", err)
	}
	if err := s.canAccessMessage(ctx, attachment.MessageID, uuid.Nil, userID, accesspolicy.CapabilityView); err != nil {
		return nil, nil, err
	}

	reader, info, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, cerrors.NotFound("file not found")
		}
		return nil, nil, cerrors.Internal("failed to read file", err)
	}
	info.MimeType = attachment.MimeType
	info.Size = attachment.FileSize
	return reader, info, nil
}

func (s *Service) PresignDownloadByKey(ctx context.Context, key string, userID uuid.UUID) (string, error) {
	signer, ok := s.store.(storage.DownloadSigner)
	if !ok || signer == nil {
		return "", nil
	}
	attachment, err := s.messages.GetAttachmentByStoragePath(ctx, key)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return "", cerrors.NotFound("file not found")
		}
		return "", cerrors.Internal("failed to look up file", err)
	}
	if err := s.canAccessMessage(ctx, attachment.MessageID, uuid.Nil, userID, accesspolicy.CapabilityView); err != nil {
		return "", err
	}
	url, err := signer.SignedDownloadURL(ctx, key, storage.SignedURLOptions{
		Filename:    attachment.FileName,
		ContentType: attachment.MimeType,
		ExpiresIn:   s.signedURLTTL,
		Attachment:  attachmentDownloadDisposition(attachment.MimeType) == "attachment",
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			return "", nil
		}
		return "", cerrors.Internal("failed to sign file download", err)
	}
	return url, nil
}

func attachmentDownloadDisposition(mimeType string) string {
	mimeType = strings.ToLower(mimeType)
	if mimeType == "image/svg+xml" {
		return "attachment"
	}
	// Audio is served inline so voice notes stream through an <audio> element
	// instead of triggering a file download.
	if strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "audio/") {
		return "inline"
	}
	return "attachment"
}

// audioProcessingTimeout caps server-side ffmpeg/ffprobe work per upload. It is
// applied on a background context so it is independent of the HTTP request
// deadline (WriteTimeout), which would otherwise kill ffmpeg mid-write.
const audioProcessingTimeout = 30 * time.Second

// waveformBarCount is how many amplitude bars the server precomputes for a voice
// note's waveform.
const waveformBarCount = 64

// removeQuietly deletes a temp file, logging (not failing) if removal errors.
func removeQuietly(ctx context.Context, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, "failed to remove temporary audio file", "path", path, "error", err)
	}
}

// probeAudioDurationMs reads the exact duration of an audio file via ffprobe.
// Best-effort: returns ok=false if ffprobe is missing or the probe fails.
func probeAudioDurationMs(ctx context.Context, path string) (int32, bool) {
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, false
	}

	out, err := exec.CommandContext(ctx, ffprobeBin,
		"-v", "quiet", "-print_format", "json", "-show_format", path,
	).Output()
	if err != nil {
		return 0, false
	}

	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if jsonErr := json.Unmarshal(out, &parsed); jsonErr != nil {
		return 0, false
	}

	seconds, parseErr := strconv.ParseFloat(parsed.Format.Duration, 64)
	if parseErr != nil || seconds <= 0 || math.IsInf(seconds, 0) || math.IsNaN(seconds) {
		return 0, false
	}

	return int32(seconds * 1000), true
}

// extractWaveformPeaks decodes the audio to mono 8 kHz PCM via ffmpeg and
// downsamples it into `waveformBarCount` normalized amplitude bars (0..1) for
// the client to draw without decoding the file. Best-effort: returns nil if
// ffmpeg is missing or extraction fails.
func extractWaveformPeaks(ctx context.Context, path string) []float32 {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil
	}

	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-v", "error", "-i", path, "-ac", "1", "-ar", "8000", "-f", "s16le", "-",
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if runErr := cmd.Run(); runErr != nil {
		return nil
	}

	return downsampleWaveform(stdout.Bytes(), waveformBarCount)
}

// downsampleWaveform buckets raw little-endian s16 mono PCM into `bars` peak
// amplitudes normalized against the loudest bucket.
func downsampleWaveform(pcm []byte, bars int) []float32 {
	sampleCount := len(pcm) / 2
	if sampleCount == 0 || bars <= 0 {
		return nil
	}

	peaks := make([]float32, bars)
	bucketSize := sampleCount / bars
	if bucketSize == 0 {
		bucketSize = 1
	}

	var loudest float32
	for bar := 0; bar < bars; bar++ {
		start := bar * bucketSize
		if start >= sampleCount {
			break
		}
		end := start + bucketSize
		if end > sampleCount || bar == bars-1 {
			end = sampleCount
		}

		var maxAmp float32
		for i := start; i < end; i++ {
			raw := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
			amp := float32(raw)
			if amp < 0 {
				amp = -amp
			}
			if amp > maxAmp {
				maxAmp = amp
			}
		}
		peaks[bar] = maxAmp
		if maxAmp > loudest {
			loudest = maxAmp
		}
	}

	if loudest == 0 {
		return peaks
	}
	for i := range peaks {
		peaks[i] /= loudest
	}
	return peaks
}

// remuxAudioDuration rewrites a voice-note container so it carries a real
// duration (MediaRecorder webm/ogg omit it). Uses `-c copy` (no re-encode, so
// it is cheap and lossless) and returns the path + size of the rewritten file.
// Best-effort: returns ok=false if ffmpeg is missing or the remux fails, and
// the caller stores the original upload unchanged.
func remuxAudioDuration(ctx context.Context, inputPath, ext string) (string, int64, bool) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		slog.WarnContext(ctx, "ffmpeg not found; storing voice note without server-side duration/waveform")
		return "", 0, false
	}

	cleanExt := strings.TrimPrefix(strings.ToLower(ext), ".")
	if cleanExt == "" {
		cleanExt = "webm"
	}
	outPath := inputPath + ".remux." + cleanExt

	cmd := exec.CommandContext(ctx, ffmpegBin, "-v", "error", "-i", inputPath, "-c", "copy", "-y", outPath)
	if runErr := cmd.Run(); runErr != nil {
		if rmErr := os.Remove(outPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.WarnContext(ctx, "failed to remove failed remux output", "path", outPath, "error", rmErr)
		}
		return "", 0, false
	}

	info, statErr := os.Stat(outPath)
	if statErr != nil || info.Size() == 0 {
		if rmErr := os.Remove(outPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.WarnContext(ctx, "failed to remove invalid remux output", "path", outPath, "error", rmErr)
		}
		return "", 0, false
	}

	return outPath, info.Size(), true
}

// audioMimeForExt returns a correct audio/* MIME type for common audio
// container extensions. Go's mime package maps .webm to video/webm, which
// misclassifies audio-only voice recordings.
func audioMimeForExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "webm":
		return "audio/webm"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "m4a", "mp4":
		return "audio/mp4"
	case "mp3", "mpeg":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "aac":
		return "audio/aac"
	default:
		return "audio/webm"
	}
}

// Delete removes a file from storage and its attachment record.
func (s *Service) Delete(ctx context.Context, key string, userID uuid.UUID) error {
	attachment, err := s.messages.GetAttachmentByStoragePath(ctx, key)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("file not found")
		}
		return cerrors.Internal("failed to look up file", err)
	}

	msg, err := s.messages.GetByID(ctx, attachment.MessageID)
	if err != nil {
		return cerrors.Internal("failed to load file message", err)
	}
	channel, err := s.channels.GetByID(ctx, msg.ChannelID)
	if err != nil {
		return cerrors.Internal("failed to load file channel", err)
	}
	if err := s.canAccessMessage(ctx, attachment.MessageID, uuid.Nil, userID, accesspolicy.CapabilityParticipate); err != nil {
		return err
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("file transaction scope is not configured")
			}
			if err := scope.Messages().DeleteAttachment(ctx, attachment.ID); err != nil {
				return err
			}
			if channel.WorkspaceID == nil {
				return nil
			}
			return s.enqueueFileDeleteSearchTx(ctx, scope, *channel.WorkspaceID, attachment.ID)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to delete attachment transaction", "attachment_id", attachment.ID, "error", err)
			return cerrors.Internal("failed to delete attachment record", err)
		}
		if err := s.store.Delete(ctx, key); err != nil {
			slog.ErrorContext(ctx, "failed to delete file after metadata transaction", "key", key, "error", err)
			return cerrors.Internal("failed to delete file", err)
		}
		return nil
	}
	if err := s.store.Delete(ctx, key); err != nil {
		slog.ErrorContext(ctx, "failed to delete file", "key", key, "error", err)
		return cerrors.Internal("failed to delete file", err)
	}
	if err := s.messages.DeleteAttachment(ctx, attachment.ID); err != nil {
		slog.ErrorContext(ctx, "failed to delete attachment record", "attachment_id", attachment.ID, "error", err)
		return cerrors.Internal("failed to delete attachment record", err)
	}
	if s.search != nil && channel.WorkspaceID != nil {
		if err := s.search.DeleteFile(ctx, *channel.WorkspaceID, attachment.ID); err != nil {
			slog.ErrorContext(ctx, "failed to enqueue file search delete", "attachment_id", attachment.ID, "error", err)
		}
	}
	return nil
}

func (s *Service) enqueueFileSearchTx(ctx context.Context, scope txscope.Scope, attachment *entity.Attachment, channelID, messageID uuid.UUID, fileName, mimeType string) error {
	if scope == nil || scope.SearchIndexer() == nil || attachment == nil {
		return nil
	}
	channelRepo := s.channels
	if scope.Channels() != nil {
		channelRepo = scope.Channels()
	}
	channel, err := channelRepo.GetByID(ctx, channelID)
	if err != nil {
		return err
	}
	return scope.SearchIndexer().EnqueueUpsert(ctx, searchsvc.Document{
		WorkspaceID: func() uuid.UUID {
			if channel.WorkspaceID == nil {
				return uuid.Nil
			}
			return *channel.WorkspaceID
		}(),
		ResourceID: attachment.ID,
		ChannelID:  &channelID,
		Type:       searchsvc.ResourceTypeFile,
		Title:      fileName,
		Content:    strings.TrimSpace(fileName + " " + mimeType),
		Metadata: map[string]any{
			"message_id": messageID.String(),
			"mime_type":  mimeType,
		},
		CreatedAt: attachment.CreatedAt,
		UpdatedAt: attachment.CreatedAt,
	})
}

func (s *Service) enqueueFileDeleteSearchTx(ctx context.Context, scope txscope.Scope, workspaceID, attachmentID uuid.UUID) error {
	if scope == nil || scope.SearchIndexer() == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeFile, attachmentID)
}
