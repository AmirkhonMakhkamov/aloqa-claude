package sfu

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/interceptor/pkg/report"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/webrtc/v4"
)

// Config holds the SFU server configuration.
type Config struct {
	// ICEServers is the list of STUN/TURN servers for ICE negotiation.
	ICEServers []webrtc.ICEServer
	// ListenPort is the UDP port used for WebRTC media traffic. When zero,
	// Pion picks an ephemeral port.
	ListenPort int
	// PortMin and PortMax define a UDP port range for WebRTC media traffic.
	// Useful for firewall configuration. When both are zero, Pion uses
	// ephemeral ports.
	PortMin uint16
	PortMax uint16
	// MaxRoomsPerNode limits the number of concurrent rooms on this SFU
	// instance. Zero means unlimited.
	MaxRoomsPerNode int
	// EnableExperimentalLyra registers a custom audio/LYRA RTP codec for
	// native clients or gateways. Browser WebRTC clients should continue to
	// negotiate Opus unless they explicitly support Lyra.
	EnableExperimentalLyra bool
}

// SFUStats contains aggregate statistics for the SFU instance.
type SFUStats struct {
	Rooms           int // Number of active rooms.
	Presenters      int // Total presenters across all rooms.
	Viewers         int // Total viewers across all rooms.
	Tracks          int // Total active track routers.
	SimulcastTracks int // Total active simulcast tracks.
}

// SFU is the top-level Selective Forwarding Unit. It manages rooms and
// provides a pre-configured Pion WebRTC API with the correct codecs and
// interceptors for media forwarding.
type SFU struct {
	config Config
	rooms  map[string]*Room
	mu     sync.RWMutex
	api    *webrtc.API
}

// NewSFU creates an SFU with a MediaEngine configured for VP8, VP9, AV1
// (video) and Opus (audio), plus optional experimental Lyra for native
// clients/gateways. It also enables interceptors for NACK retransmission,
// RTCP sender/receiver reports, and TWCC bandwidth estimation.
func NewSFU(cfg Config) (*SFU, error) {
	me := &webrtc.MediaEngine{}

	// Register audio codecs.
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register Opus codec: %w", err)
	}
	if cfg.EnableExperimentalLyra {
		if err := me.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  "audio/LYRA",
				ClockRate: 48000,
				Channels:  1,
			},
			PayloadType: 112,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			return nil, fmt.Errorf("register Lyra codec: %w", err)
		}
	}

	// Register video codecs.
	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeVP8,
				ClockRate:   90000,
				SDPFmtpLine: "",
			},
			PayloadType: 96,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeVP9,
				ClockRate:   90000,
				SDPFmtpLine: "profile-id=0",
			},
			PayloadType: 98,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeAV1,
				ClockRate:   90000,
				SDPFmtpLine: "",
			},
			PayloadType: 35,
		},
	} {
		if err := me.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, fmt.Errorf("register %s codec: %w", codec.MimeType, err)
		}
	}

	// Register header extensions for transport-wide congestion control.
	if err := me.RegisterHeaderExtension(
		webrtc.RTPHeaderExtensionCapability{URI: "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"},
		webrtc.RTPCodecTypeVideo,
	); err != nil {
		return nil, fmt.Errorf("register TWCC header extension: %w", err)
	}

	// Register simulcast header extensions (RTP Stream ID for rid-based simulcast).
	for _, ext := range []string{
		"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
		"urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id",
		"urn:ietf:params:rtp-hdrext:sdes:mid",
	} {
		if err := me.RegisterHeaderExtension(
			webrtc.RTPHeaderExtensionCapability{URI: ext},
			webrtc.RTPCodecTypeVideo,
		); err != nil {
			return nil, fmt.Errorf("register %s header extension: %w", ext, err)
		}
	}

	// Register audio header extension for mid.
	if err := me.RegisterHeaderExtension(
		webrtc.RTPHeaderExtensionCapability{URI: "urn:ietf:params:rtp-hdrext:sdes:mid"},
		webrtc.RTPCodecTypeAudio,
	); err != nil {
		return nil, fmt.Errorf("register audio mid header extension: %w", err)
	}

	// Build the interceptor registry with NACK, RTCP reports, and TWCC.
	ir := &interceptor.Registry{}

	// NACK responder: retransmits packets when a receiver reports loss.
	nackResponder, err := nack.NewResponderInterceptor()
	if err != nil {
		return nil, fmt.Errorf("create NACK responder: %w", err)
	}
	ir.Add(nackResponder)

	// NACK generator: requests retransmission of lost packets.
	nackGenerator, err := nack.NewGeneratorInterceptor()
	if err != nil {
		return nil, fmt.Errorf("create NACK generator: %w", err)
	}
	ir.Add(nackGenerator)

	// Sender reports for RTCP.
	senderReport, err := report.NewSenderInterceptor()
	if err != nil {
		return nil, fmt.Errorf("create sender report interceptor: %w", err)
	}
	ir.Add(senderReport)

	// Receiver reports for RTCP.
	receiverReport, err := report.NewReceiverInterceptor()
	if err != nil {
		return nil, fmt.Errorf("create receiver report interceptor: %w", err)
	}
	ir.Add(receiverReport)

	// Transport-wide congestion control.
	twccInterceptor, err := twcc.NewSenderInterceptor()
	if err != nil {
		return nil, fmt.Errorf("create TWCC sender interceptor: %w", err)
	}
	ir.Add(twccInterceptor)

	// Configure the setting engine for port binding.
	se := webrtc.SettingEngine{}
	if cfg.PortMin > 0 && cfg.PortMax > 0 {
		se.SetEphemeralUDPPortRange(cfg.PortMin, cfg.PortMax)
		slog.Info("SFU configured with UDP port range", "min", cfg.PortMin, "max", cfg.PortMax)
	} else if cfg.ListenPort > 0 {
		se.SetEphemeralUDPPortRange(uint16(cfg.ListenPort), uint16(cfg.ListenPort))
		slog.Info("SFU configured with fixed UDP port", "port", cfg.ListenPort)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(me),
		webrtc.WithInterceptorRegistry(ir),
		webrtc.WithSettingEngine(se),
	)

	sfu := &SFU{
		config: cfg,
		rooms:  make(map[string]*Room),
		api:    api,
	}

	slog.Info("SFU initialized",
		"ice_servers", len(cfg.ICEServers),
		"listen_port", cfg.ListenPort,
		"max_rooms", cfg.MaxRoomsPerNode,
		"experimental_lyra", cfg.EnableExperimentalLyra,
	)

	return sfu, nil
}

// API returns the pre-configured Pion WebRTC API. Use this to create
// PeerConnections that share the SFU's codec and interceptor configuration.
func (s *SFU) API() *webrtc.API {
	return s.api
}

// NewPeerConnection creates a PeerConnection using the SFU's API and the
// configured ICE servers. This is a convenience method so callers don't
// need to build the configuration themselves.
func (s *SFU) NewPeerConnection() (*webrtc.PeerConnection, error) {
	return s.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: s.config.ICEServers,
	})
}

// CreateRoom creates a new room with the given ID and options. Returns an
// error if a room with the same ID already exists or if the node has reached
// its room limit.
func (s *SFU) CreateRoom(id string, opts RoomOptions) (*Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.rooms[id]; exists {
		return nil, fmt.Errorf("room %s already exists", id)
	}

	if s.config.MaxRoomsPerNode > 0 && len(s.rooms) >= s.config.MaxRoomsPerNode {
		return nil, fmt.Errorf("node has reached the room limit (%d)", s.config.MaxRoomsPerNode)
	}

	room := newRoom(id, opts)
	s.rooms[id] = room

	slog.Info("room created",
		"room_id", id,
		"max_presenters", opts.MaxPresenters,
		"max_viewers", opts.MaxViewers,
		"max_tracks_per_presenter", opts.MaxTracksPerPresenter,
	)

	return room, nil
}

// GetRoom returns the room with the given ID, or false if it doesn't exist.
func (s *SFU) GetRoom(id string) (*Room, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	room, ok := s.rooms[id]
	return room, ok
}

// CloseRoom closes a room and removes it from the SFU. All peers in the
// room are disconnected and their tracks are cleaned up.
func (s *SFU) CloseRoom(id string) {
	s.mu.Lock()
	room, exists := s.rooms[id]
	if !exists {
		s.mu.Unlock()
		return
	}
	delete(s.rooms, id)
	s.mu.Unlock()

	room.Close()

	slog.Info("room closed and removed from SFU", "room_id", id)
}

// Stats returns aggregate statistics across all rooms.
func (s *SFU) Stats() SFUStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stats SFUStats
	stats.Rooms = len(s.rooms)

	for _, room := range s.rooms {
		p, v := room.PeerCount()
		stats.Presenters += p
		stats.Viewers += v

		room.mu.RLock()
		stats.Tracks += len(room.tracks)
		stats.SimulcastTracks += len(room.simulcastTracks)
		room.mu.RUnlock()
	}

	return stats
}

// Close shuts down the SFU, closing all rooms and disconnecting all peers.
func (s *SFU) Close() {
	s.mu.Lock()
	rooms := make(map[string]*Room, len(s.rooms))
	for k, v := range s.rooms {
		rooms[k] = v
	}
	s.rooms = make(map[string]*Room)
	s.mu.Unlock()

	for _, room := range rooms {
		room.Close()
	}

	slog.Info("SFU shut down", "rooms_closed", len(rooms))
}
