package recording

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"aloqa/internal/domain/entity"
)

type CompositeLayout string

const (
	CompositeLayoutActiveSpeaker CompositeLayout = "active_speaker"
	CompositeLayoutScreenShare   CompositeLayout = "screen_share_first"
	CompositeLayoutWebinarStage  CompositeLayout = "webinar_stage"
)

type CompositeTrackInput struct {
	LocalPath   string
	UserID      string
	DisplayName string
	Role        entity.CallRole
	Kind        entity.RecordingArtifactKind
	PacketCount int64
}

type CompositeRenderSpec struct {
	WorkDir      string
	Layout       CompositeLayout
	OutputFormat entity.RecordingFormat
	Width        int
	Height       int
	Title        string
	VideoTracks  []CompositeTrackInput
	AudioTracks  []CompositeTrackInput
}

type TranscriptAudioSpec struct {
	WorkDir     string
	SampleRate  int
	AudioTracks []CompositeTrackInput
}

type RenderedAsset struct {
	LocalPath string
	FileName  string
	Format    string
	MimeType  string
	Metadata  map[string]any
}

type CompositeRenderer interface {
	RenderComposite(ctx context.Context, spec CompositeRenderSpec) (*RenderedAsset, error)
	ExtractTranscriptAudio(ctx context.Context, spec TranscriptAudioSpec) (*RenderedAsset, error)
}

type FFmpegRendererConfig struct {
	Binary               string
	Width                int
	Height               int
	OutputFormat         entity.RecordingFormat
	VideoBitrate         string
	AudioBitrate         string
	TranscriptSampleRate int
}

type FFmpegRenderer struct {
	binary               string
	width                int
	height               int
	outputFormat         entity.RecordingFormat
	videoBitrate         string
	audioBitrate         string
	transcriptSampleRate int
}

// Validate checks that the configured FFmpeg binary exists on PATH.
// Call once at startup; fails fast rather than at first recording.
func (r *FFmpegRenderer) Validate() error {
	if _, err := exec.LookPath(r.binary); err != nil {
		return fmt.Errorf("ffmpeg binary %q not found: %w", r.binary, err)
	}
	return nil
}

func NewFFmpegRenderer(cfg FFmpegRendererConfig) *FFmpegRenderer {
	width := cfg.Width
	if width <= 0 {
		width = 1280
	}
	height := cfg.Height
	if height <= 0 {
		height = 720
	}
	format := cfg.OutputFormat
	if format != entity.RecordingFormatMP4 && format != entity.RecordingFormatWebM {
		format = entity.RecordingFormatMP4
	}
	sampleRate := cfg.TranscriptSampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	videoBitrate := strings.TrimSpace(cfg.VideoBitrate)
	if videoBitrate == "" {
		videoBitrate = "3500k"
	}
	audioBitrate := strings.TrimSpace(cfg.AudioBitrate)
	if audioBitrate == "" {
		audioBitrate = "128k"
	}
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "ffmpeg"
	}
	return &FFmpegRenderer{
		binary:               binary,
		width:                width,
		height:               height,
		outputFormat:         format,
		videoBitrate:         videoBitrate,
		audioBitrate:         audioBitrate,
		transcriptSampleRate: sampleRate,
	}
}

func (r *FFmpegRenderer) RenderComposite(ctx context.Context, spec CompositeRenderSpec) (*RenderedAsset, error) {
	if len(spec.VideoTracks) == 0 && len(spec.AudioTracks) == 0 {
		return nil, fmt.Errorf("no renderable tracks for composite recording")
	}
	if spec.Width <= 0 {
		spec.Width = r.width
	}
	if spec.Height <= 0 {
		spec.Height = r.height
	}
	if spec.OutputFormat != entity.RecordingFormatMP4 && spec.OutputFormat != entity.RecordingFormatWebM {
		spec.OutputFormat = r.outputFormat
	}
	outputExt, mimeType := compositeFormatInfo(spec.OutputFormat)
	outputPath := filepath.Join(spec.WorkDir, "composite."+outputExt)
	if err := os.MkdirAll(spec.WorkDir, 0o750); err != nil {
		return nil, err
	}

	args, err := r.buildCompositeArgs(spec, outputPath)
	if err != nil {
		return nil, err
	}
	if err := r.run(ctx, args...); err != nil {
		return nil, err
	}
	return &RenderedAsset{
		LocalPath: outputPath,
		FileName:  filepath.Base(outputPath),
		Format:    outputExt,
		MimeType:  mimeType,
		Metadata: map[string]any{
			"layout":       string(spec.Layout),
			"title":        spec.Title,
			"video_inputs": len(spec.VideoTracks),
			"audio_inputs": len(spec.AudioTracks),
			"width":        spec.Width,
			"height":       spec.Height,
		},
	}, nil
}

func (r *FFmpegRenderer) ExtractTranscriptAudio(ctx context.Context, spec TranscriptAudioSpec) (*RenderedAsset, error) {
	if len(spec.AudioTracks) == 0 {
		return nil, fmt.Errorf("no audio tracks available for transcript extraction")
	}
	if spec.SampleRate <= 0 {
		spec.SampleRate = r.transcriptSampleRate
	}
	outputPath := filepath.Join(spec.WorkDir, "transcript_audio.wav")
	if err := os.MkdirAll(spec.WorkDir, 0o750); err != nil {
		return nil, err
	}

	args := []string{"-y"}
	for _, track := range spec.AudioTracks {
		args = append(args, "-i", track.LocalPath)
	}
	filter, audioLabel := buildAudioMixFilter(0, len(spec.AudioTracks), spec.SampleRate)
	args = append(args,
		"-filter_complex", filter,
		"-map", audioLabel,
		"-ac", "1",
		"-ar", strconv.Itoa(spec.SampleRate),
		"-c:a", "pcm_s16le",
		outputPath,
	)
	if err := r.run(ctx, args...); err != nil {
		return nil, err
	}
	return &RenderedAsset{
		LocalPath: outputPath,
		FileName:  filepath.Base(outputPath),
		Format:    "wav",
		MimeType:  "audio/wav",
		Metadata: map[string]any{
			"sample_rate":  spec.SampleRate,
			"channels":     1,
			"mix_strategy": "amix",
			"input_tracks": len(spec.AudioTracks),
		},
	}, nil
}

func (r *FFmpegRenderer) buildCompositeArgs(spec CompositeRenderSpec, outputPath string) ([]string, error) {
	args := []string{"-y"}
	for _, track := range spec.VideoTracks {
		args = append(args, "-i", track.LocalPath)
	}
	audioStart := len(spec.VideoTracks)
	for _, track := range spec.AudioTracks {
		args = append(args, "-i", track.LocalPath)
	}

	filter, videoLabel, audioLabel, err := buildCompositeFilter(spec, audioStart)
	if err != nil {
		return nil, err
	}
	args = append(args, "-filter_complex", filter, "-map", videoLabel)
	if audioLabel != "" {
		args = append(args, "-map", audioLabel)
	}
	switch spec.OutputFormat {
	case entity.RecordingFormatWebM:
		args = append(args, "-c:v", "libvpx-vp9", "-b:v", r.videoBitrate)
		if audioLabel != "" {
			args = append(args, "-c:a", "libopus", "-b:a", r.audioBitrate)
		}
	default:
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-b:v", r.videoBitrate)
		if audioLabel != "" {
			args = append(args, "-c:a", "aac", "-b:a", r.audioBitrate, "-movflags", "+faststart")
		} else {
			args = append(args, "-movflags", "+faststart")
		}
	}
	args = append(args, "-shortest", outputPath)
	return args, nil
}

func (r *FFmpegRenderer) run(ctx context.Context, args ...string) error {
	if _, err := exec.LookPath(r.binary); err != nil {
		return fmt.Errorf("recording renderer binary %q is not available: %w", r.binary, err)
	}
	cmd := exec.CommandContext(ctx, r.binary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func buildCompositeFilter(spec CompositeRenderSpec, audioStart int) (string, string, string, error) {
	var parts []string
	audioLabel := ""
	if len(spec.AudioTracks) > 0 {
		audioFilter, mixedAudio := buildAudioMixFilter(audioStart, len(spec.AudioTracks), 48000)
		parts = append(parts, audioFilter)
		audioLabel = mixedAudio
	}
	if len(spec.VideoTracks) == 0 {
		if audioLabel == "" {
			return "", "", "", fmt.Errorf("no audio available to render waveform composite")
		}
		parts = append(parts, fmt.Sprintf("%sshowwaves=s=%dx%d:mode=line:colors=0x00d084[vout]", audioLabel, spec.Width, spec.Height))
		return strings.Join(parts, ";"), "[vout]", audioLabel, nil
	}
	visualFilter, videoLabel, err := buildVisualLayout(spec)
	if err != nil {
		return "", "", "", err
	}
	parts = append(parts, visualFilter)
	return strings.Join(parts, ";"), videoLabel, audioLabel, nil
}

func buildVisualLayout(spec CompositeRenderSpec) (string, string, error) {
	visualTracks := spec.VideoTracks
	switch spec.Layout {
	case CompositeLayoutScreenShare:
		return buildScreenShareLayout(spec, visualTracks), "[vout]", nil
	case CompositeLayoutWebinarStage:
		return buildWebinarLayout(spec, visualTracks), "[vout]", nil
	default:
		return buildActiveSpeakerLayout(spec, visualTracks), "[vout]", nil
	}
}

func buildActiveSpeakerLayout(spec CompositeRenderSpec, tracks []CompositeTrackInput) string {
	primaryWidth := int(float64(spec.Width) * 0.75)
	thumbWidth := spec.Width - primaryWidth
	thumbHeight := spec.Height / maxInt(minInt(len(tracks), 4), 1)
	parts := make([]string, 0, len(tracks)+1)
	layouts := make([]string, 0, minInt(len(tracks), 5))
	for i, track := range tracks {
		if i == 0 {
			parts = append(parts, scaledFilter(i, primaryWidth, spec.Height))
			layouts = append(layouts, "0_0")
			continue
		}
		if i > 4 {
			break
		}
		_ = track
		parts = append(parts, scaledFilter(i, thumbWidth, thumbHeight))
		layouts = append(layouts, fmt.Sprintf("%d_%d", primaryWidth, (i-1)*thumbHeight))
	}
	inputs := minInt(len(tracks), 5)
	parts = append(parts, xstackFilter(inputs, layouts))
	return strings.Join(parts, ";")
}

func buildScreenShareLayout(spec CompositeRenderSpec, tracks []CompositeTrackInput) string {
	screenHeight := int(float64(spec.Height) * 0.75)
	thumbHeight := spec.Height - screenHeight
	thumbWidth := spec.Width / maxInt(minInt(len(tracks)-1, 4), 1)
	parts := make([]string, 0, len(tracks)+1)
	layouts := []string{"0_0"}
	for i := range tracks {
		if i == 0 {
			parts = append(parts, scaledFilter(i, spec.Width, screenHeight))
			continue
		}
		if i > 4 {
			break
		}
		parts = append(parts, scaledFilter(i, thumbWidth, thumbHeight))
		layouts = append(layouts, fmt.Sprintf("%d_%d", (i-1)*thumbWidth, screenHeight))
	}
	inputs := minInt(len(tracks), 5)
	parts = append(parts, xstackFilter(inputs, layouts))
	return strings.Join(parts, ";")
}

func buildWebinarLayout(spec CompositeRenderSpec, tracks []CompositeTrackInput) string {
	count := minInt(len(tracks), 4)
	parts := make([]string, 0, count+1)
	layouts := make([]string, 0, count)
	switch count {
	case 1:
		parts = append(parts, scaledFilter(0, spec.Width, spec.Height))
		layouts = append(layouts, "0_0")
	case 2:
		for i := 0; i < count; i++ {
			parts = append(parts, scaledFilter(i, spec.Width/2, spec.Height))
			layouts = append(layouts, fmt.Sprintf("%d_0", i*(spec.Width/2)))
		}
	default:
		cellW := spec.Width / 2
		cellH := spec.Height / 2
		for i := 0; i < count; i++ {
			parts = append(parts, scaledFilter(i, cellW, cellH))
			layouts = append(layouts, fmt.Sprintf("%d_%d", (i%2)*cellW, (i/2)*cellH))
		}
	}
	parts = append(parts, xstackFilter(count, layouts))
	return strings.Join(parts, ";")
}

func scaledFilter(inputIdx, width, height int) string {
	return fmt.Sprintf("[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black[v%d]", inputIdx, width, height, width, height, inputIdx)
}

func xstackFilter(inputs int, layouts []string) string {
	labels := make([]string, 0, inputs)
	for i := 0; i < inputs; i++ {
		labels = append(labels, fmt.Sprintf("[v%d]", i))
	}
	return fmt.Sprintf("%sxstack=inputs=%d:layout=%s:fill=black[vout]", strings.Join(labels, ""), inputs, strings.Join(layouts[:inputs], "|"))
}

func buildAudioMixFilter(startIdx, count, sampleRate int) (string, string) {
	labels := make([]string, 0, count)
	for i := 0; i < count; i++ {
		labels = append(labels, fmt.Sprintf("[%d:a]", startIdx+i))
	}
	if count == 1 {
		return fmt.Sprintf("%saresample=%d,aformat=sample_rates=%d:sample_fmts=s16:channel_layouts=mono[aout]", labels[0], sampleRate, sampleRate), "[aout]"
	}
	return fmt.Sprintf("%samix=inputs=%d:dropout_transition=2:normalize=0,aresample=%d,aformat=sample_rates=%d:sample_fmts=s16:channel_layouts=mono[aout]", strings.Join(labels, ""), count, sampleRate, sampleRate), "[aout]"
}

func compositeFormatInfo(format entity.RecordingFormat) (string, string) {
	switch format {
	case entity.RecordingFormatWebM:
		return "webm", "video/webm"
	default:
		return "mp4", "video/mp4"
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
