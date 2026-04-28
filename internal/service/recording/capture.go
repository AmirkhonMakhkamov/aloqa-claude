package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
	"github.com/pion/webrtc/v4/pkg/media/rtpdump"

	"aloqa/internal/domain/entity"
	"aloqa/internal/media/sfu"
)

type CaptureManager struct {
	baseDir  string
	mu       sync.Mutex
	sessions map[uuid.UUID]*captureSession
}

type captureSession struct {
	recording  entity.Recording
	workDir    string
	room       *sfu.Room
	observerID string

	mu      sync.Mutex
	stopped bool
	tracks  map[string]*captureTrack
}

type captureTrack struct {
	state *SpoolTrackState
	sink  *captureSink
}

type captureSink struct {
	mu          sync.Mutex
	closed      bool
	packetCount int64
	writer      packetWriter
	state       *SpoolTrackState
}

type packetWriter interface {
	WriteRTP(packet *rtp.Packet) error
	Close() error
}

type rtpDumpPacketWriter struct {
	file   *os.File
	writer *rtpdump.Writer
	start  time.Time
}

type SpoolState struct {
	RecordingID uuid.UUID                `json:"recording_id"`
	WorkspaceID uuid.UUID                `json:"workspace_id"`
	CallID      uuid.UUID                `json:"call_id"`
	StartedBy   uuid.UUID                `json:"started_by"`
	Strategy    entity.RecordingStrategy `json:"strategy"`
	StartedAt   time.Time                `json:"started_at"`
	StoppedAt   *time.Time               `json:"stopped_at,omitempty"`
	Tracks      []SpoolTrackState        `json:"tracks"`
}

type SpoolTrackState struct {
	Kind         entity.RecordingArtifactKind `json:"kind"`
	SourceUserID *uuid.UUID                   `json:"source_user_id,omitempty"`
	TrackID      string                       `json:"track_id"`
	StreamID     string                       `json:"stream_id"`
	Layer        string                       `json:"layer,omitempty"`
	Codec        string                       `json:"codec"`
	MimeType     string                       `json:"mime_type"`
	Format       string                       `json:"format"`
	LocalPath    string                       `json:"local_path"`
	FileName     string                       `json:"file_name"`
	PacketCount  int64                        `json:"packet_count"`
	StartedAt    time.Time                    `json:"started_at"`
	StoppedAt    *time.Time                   `json:"stopped_at,omitempty"`
}

func NewCaptureManager(baseDir string) (*CaptureManager, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, errors.New("recording capture base dir is required")
	}
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		return nil, fmt.Errorf("create recording capture dir: %w", err)
	}
	return &CaptureManager{
		baseDir:  baseDir,
		sessions: map[uuid.UUID]*captureSession{},
	}, nil
}

func (m *CaptureManager) WorkDir(recordingID uuid.UUID) string {
	return filepath.Join(m.baseDir, recordingID.String())
}

func (m *CaptureManager) Start(_ context.Context, rec entity.Recording, room *sfu.Room) error {
	if room == nil {
		return errors.New("media room is not available")
	}

	workDir := m.WorkDir(rec.ID)
	tracksDir := filepath.Join(workDir, "tracks")
	if err := os.MkdirAll(tracksDir, 0o750); err != nil {
		return fmt.Errorf("create recording work dir: %w", err)
	}

	session := &captureSession{
		recording: rec,
		workDir:   workDir,
		room:      room,
		tracks:    map[string]*captureTrack{},
	}
	observerID := room.AddObserver(session)
	if observerID == "" {
		return errors.New("failed to attach recording observer")
	}
	session.observerID = observerID

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[rec.ID]; exists {
		room.RemoveObserver(observerID)
		return errors.New("recording capture session already active")
	}
	m.sessions[rec.ID] = session
	return nil
}

func (m *CaptureManager) Stop(recordingID uuid.UUID) error {
	m.mu.Lock()
	session, ok := m.sessions[recordingID]
	if ok {
		delete(m.sessions, recordingID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return session.stop()
}

func (m *CaptureManager) IsActive(recordingID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[recordingID]
	return ok
}

func (s *captureSession) OnTrack(track sfu.ObservedTrack) (sfu.PacketSink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil, nil
	}

	key := track.SourcePeer + ":" + track.StreamID + ":" + track.TrackID + ":" + track.Layer
	if existing := s.tracks[key]; existing != nil {
		return nil, nil
	}

	state, writer, err := buildTrackWriter(s.workDir, track)
	if err != nil {
		return nil, err
	}
	sink := &captureSink{
		writer: writer,
		state:  state,
	}
	s.tracks[key] = &captureTrack{state: state, sink: sink}
	return sink, nil
}

func (s *captureSession) stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	room := s.room
	observerID := s.observerID
	tracks := make([]*captureTrack, 0, len(s.tracks))
	for _, track := range s.tracks {
		tracks = append(tracks, track)
	}
	s.mu.Unlock()

	if room != nil && observerID != "" {
		room.RemoveObserver(observerID)
	}
	for _, track := range tracks {
		if track == nil || track.sink == nil {
			continue
		}
		if err := track.sink.Close(); err != nil {
			return err
		}
	}
	return s.persistState()
}

func (s *captureSession) persistState() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	state := SpoolState{
		RecordingID: s.recording.ID,
		WorkspaceID: s.recording.WorkspaceID,
		CallID:      s.recording.CallID,
		StartedBy:   s.recording.StartedBy,
		Strategy:    s.recording.Strategy,
		StartedAt:   s.recording.StartedAt,
		StoppedAt:   &now,
		Tracks:      make([]SpoolTrackState, 0, len(s.tracks)),
	}
	for _, track := range s.tracks {
		if track == nil || track.state == nil {
			continue
		}
		state.Tracks = append(state.Tracks, *track.state)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal recording spool state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.workDir, "state.json"), data, 0o640); err != nil {
		return fmt.Errorf("write recording spool state: %w", err)
	}
	return nil
}

func buildTrackWriter(workDir string, track sfu.ObservedTrack) (*SpoolTrackState, packetWriter, error) {
	sourceUserID, _ := uuid.Parse(track.SourcePeer)
	var sourceUserPtr *uuid.UUID
	if sourceUserID != uuid.Nil {
		sourceUserPtr = &sourceUserID
	}

	kind := classifyTrackKind(track)
	format, ext := formatForTrack(track.MimeType)
	filename := safeTrackFileName(track, ext)
	localPath := filepath.Join(workDir, "tracks", filename)

	state := &SpoolTrackState{
		Kind:         kind,
		SourceUserID: sourceUserPtr,
		TrackID:      track.TrackID,
		StreamID:     track.StreamID,
		Layer:        track.Layer,
		Codec:        track.MimeType,
		MimeType:     track.MimeType,
		Format:       format,
		LocalPath:    localPath,
		FileName:     filename,
		StartedAt:    time.Now().UTC(),
	}

	writer, err := newPacketWriter(localPath, track.MimeType)
	if err != nil {
		return nil, nil, err
	}
	return state, writer, nil
}

func newPacketWriter(localPath, mimeType string) (packetWriter, error) {
	switch strings.ToLower(mimeType) {
	case "audio/opus":
		return oggwriter.New(localPath, 48000, 2)
	case "video/vp8", "video/vp9", "video/av1":
		return ivfwriter.New(localPath, ivfwriter.WithCodec(mimeType))
	default:
		return newRTPDumpPacketWriter(localPath)
	}
}

func newRTPDumpPacketWriter(localPath string) (packetWriter, error) {
	file, err := os.Create(localPath)
	if err != nil {
		return nil, err
	}
	writer, err := rtpdump.NewWriter(file, rtpdump.Header{
		Start:  time.Now().UTC(),
		Source: net.IPv4(127, 0, 0, 1),
		Port:   0,
	})
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &rtpDumpPacketWriter{
		file:   file,
		writer: writer,
		start:  time.Now().UTC(),
	}, nil
}

func (w *rtpDumpPacketWriter) WriteRTP(packet *rtp.Packet) error {
	if packet == nil {
		return nil
	}
	payload, err := packet.Marshal()
	if err != nil {
		return err
	}
	return w.writer.WritePacket(rtpdump.Packet{
		Offset:  time.Since(w.start),
		Payload: payload,
	})
}

func (w *rtpDumpPacketWriter) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (s *captureSink) WriteRTP(packet *rtp.Packet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if err := s.writer.WriteRTP(packet); err != nil {
		return err
	}
	s.packetCount++
	s.state.PacketCount = s.packetCount
	return nil
}

func (s *captureSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	now := time.Now().UTC()
	s.state.StoppedAt = &now
	s.state.PacketCount = s.packetCount
	return s.writer.Close()
}

func LoadSpoolState(baseDir string, recordingID uuid.UUID) (*SpoolState, error) {
	data, err := os.ReadFile(filepath.Join(baseDir, recordingID.String(), "state.json"))
	if err != nil {
		return nil, err
	}
	var state SpoolState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func RemoveSpoolDir(baseDir string, recordingID uuid.UUID) error {
	if strings.TrimSpace(baseDir) == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(baseDir, recordingID.String()))
}

func classifyTrackKind(track sfu.ObservedTrack) entity.RecordingArtifactKind {
	candidate := strings.ToLower(track.TrackID + ":" + track.StreamID)
	if strings.HasPrefix(strings.ToLower(track.MimeType), "audio/") {
		return entity.RecordingArtifactKindAudioTrack
	}
	if strings.Contains(candidate, "screen") || strings.Contains(candidate, "share") {
		return entity.RecordingArtifactKindScreenTrack
	}
	return entity.RecordingArtifactKindVideoTrack
}

func formatForTrack(mimeType string) (format, ext string) {
	switch strings.ToLower(mimeType) {
	case "audio/opus":
		return "ogg", "ogg"
	case "video/vp8", "video/vp9", "video/av1":
		return "ivf", "ivf"
	default:
		return "rtpdump", "rtpdump"
	}
}

func safeTrackFileName(track sfu.ObservedTrack, ext string) string {
	name := strings.ToLower(strings.TrimSpace(track.SourcePeer + "-" + track.StreamID + "-" + track.TrackID + "-" + track.Layer))
	name = strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-").Replace(name)
	name = strings.Trim(name, "-")
	if name == "" {
		name = uuid.NewString()
	}
	return name + "." + ext
}
