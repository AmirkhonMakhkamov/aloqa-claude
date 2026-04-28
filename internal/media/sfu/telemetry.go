package sfu

import (
	"math"
	"sort"
	"time"

	"github.com/pion/webrtc/v4"
)

type PeerMediaTelemetry struct {
	StreamID                     string
	MediaKind                    string
	PacketLossPct                float64
	JitterMs                     float64
	RoundTripTimeMs              float64
	AvailableOutgoingBitrateKbps int
	AvailableIncomingBitrateKbps int
	BytesSent                    int64
	BytesReceived                int64
	Metadata                     map[string]any
}

type PeerTelemetry struct {
	UserID             string
	Role               PeerRole
	ConnectionState    string
	ICEConnectionState string
	SampledAt          time.Time
	Samples            []PeerMediaTelemetry
}

type RoomTelemetry struct {
	RoomID       string
	Presenters   int
	Viewers      int
	SampledAt    time.Time
	Participants []PeerTelemetry
}

func (s *SFU) Rooms() []*Room {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rooms := make([]*Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	return rooms
}

func (r *Room) TelemetrySnapshot() RoomTelemetry {
	r.mu.RLock()
	peers := make([]*Peer, 0, len(r.presenters)+len(r.viewers))
	for _, peer := range r.presenters {
		peers = append(peers, peer)
	}
	for _, peer := range r.viewers {
		peers = append(peers, peer)
	}
	presenters := len(r.presenters)
	viewers := len(r.viewers)
	r.mu.RUnlock()

	participants := make([]PeerTelemetry, 0, len(peers))
	for _, peer := range peers {
		participants = append(participants, peer.TelemetrySnapshot())
	}

	return RoomTelemetry{
		RoomID:       r.ID,
		Presenters:   presenters,
		Viewers:      viewers,
		SampledAt:    time.Now().UTC(),
		Participants: participants,
	}
}

func (p *Peer) TelemetrySnapshot() PeerTelemetry {
	snapshot := PeerTelemetry{
		UserID:             p.UserID,
		Role:               p.Role,
		SampledAt:          time.Now().UTC(),
		ConnectionState:    p.PC.ConnectionState().String(),
		ICEConnectionState: p.PC.ICEConnectionState().String(),
	}
	if p.PC == nil {
		return snapshot
	}

	report := p.PC.GetStats()
	selectedPair := selectedCandidatePair(report)
	accs := map[string]*telemetryAccumulator{}

	for _, stat := range report {
		switch v := stat.(type) {
		case webrtc.InboundRTPStreamStats:
			acc := ensureTelemetryAccumulator(accs, v.Kind, v.ID)
			acc.PacketsReceived += int64(v.PacketsReceived)
			acc.PacketsLost += int64(v.PacketsLost)
			acc.JitterMs = maxFloat(acc.JitterMs, v.Jitter*1000)
			acc.Metadata["nack_count"] = int(v.NACKCount)
			acc.Metadata["pli_count"] = int(v.PLICount)
			acc.Metadata["fir_count"] = int(v.FIRCount)
			acc.Metadata["freeze_count"] = int(v.FreezeCount)
			acc.Metadata["pause_count"] = int(v.PauseCount)
			acc.Metadata["frames_received"] = int(v.FramesReceived)
			acc.Metadata["frames_decoded"] = int(v.FramesDecoded)
			acc.Metadata["frames_rendered"] = int(v.FramesRendered)
			acc.Metadata["frames_dropped"] = int(v.FramesDropped)
			acc.Metadata["total_freezes_duration_ms"] = v.TotalFreezesDuration * 1000
			acc.Metadata["total_pauses_duration_ms"] = v.TotalPausesDuration * 1000
			acc.Metadata["jitter_buffer_delay_ms"] = v.JitterBufferDelay * 1000
			acc.Metadata["jitter_buffer_target_delay_ms"] = v.JitterBufferTargetDelay * 1000
			acc.Metadata["jitter_buffer_emitted_count"] = int(v.JitterBufferEmittedCount)
			acc.Metadata["power_efficient_decoder"] = v.PowerEfficientDecoder
			if v.TotalDecodeTime > 0 && v.FramesDecoded > 0 {
				acc.Metadata["avg_decode_time_ms"] = (v.TotalDecodeTime / float64(v.FramesDecoded)) * 1000
			}
			acc.Metadata["total_decode_time_ms"] = v.TotalDecodeTime * 1000
			acc.Metadata["transport_id"] = v.TransportID
		case webrtc.RemoteInboundRTPStreamStats:
			acc := ensureTelemetryAccumulator(accs, v.Kind, v.ID)
			acc.PacketsReceived += int64(v.PacketsReceived)
			acc.PacketsLost += int64(v.PacketsLost)
			acc.JitterMs = maxFloat(acc.JitterMs, v.Jitter*1000)
			acc.RoundTripTimeMs = maxFloat(acc.RoundTripTimeMs, v.RoundTripTime*1000)
			acc.Metadata["burst_loss_rate"] = v.BurstLossRate
			acc.Metadata["burst_discard_rate"] = v.BurstDiscardRate
		}
	}

	if selectedPair != nil {
		for _, acc := range accs {
			acc.RoundTripTimeMs = maxFloat(acc.RoundTripTimeMs, selectedPair.CurrentRoundTripTime*1000)
			acc.AvailableOutgoingBitrateKbps = int(math.Round(selectedPair.AvailableOutgoingBitrate / 1000))
			acc.AvailableIncomingBitrateKbps = int(math.Round(selectedPair.AvailableIncomingBitrate / 1000))
			acc.BytesSent = clampInt64(selectedPair.BytesSent)
			acc.BytesReceived = clampInt64(selectedPair.BytesReceived)
		}
	}

	kinds := make([]string, 0, len(accs))
	for kind := range accs {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		acc := accs[kind]
		snapshot.Samples = append(snapshot.Samples, acc.toPeerMediaTelemetry())
	}

	return snapshot
}

type telemetryAccumulator struct {
	StreamID                     string
	MediaKind                    string
	PacketsReceived              int64
	PacketsLost                  int64
	JitterMs                     float64
	RoundTripTimeMs              float64
	AvailableOutgoingBitrateKbps int
	AvailableIncomingBitrateKbps int
	BytesSent                    int64
	BytesReceived                int64
	Metadata                     map[string]any
}

func ensureTelemetryAccumulator(accs map[string]*telemetryAccumulator, kind, streamID string) *telemetryAccumulator {
	acc := accs[kind]
	if acc != nil {
		return acc
	}
	acc = &telemetryAccumulator{
		StreamID:  streamID,
		MediaKind: kind,
		Metadata:  map[string]any{},
	}
	accs[kind] = acc
	return acc
}

func (a *telemetryAccumulator) toPeerMediaTelemetry() PeerMediaTelemetry {
	total := a.PacketsReceived + maxInt64(a.PacketsLost, 0)
	packetLossPct := 0.0
	if total > 0 {
		packetLossPct = float64(maxInt64(a.PacketsLost, 0)) * 100 / float64(total)
	}
	return PeerMediaTelemetry{
		StreamID:                     a.StreamID,
		MediaKind:                    a.MediaKind,
		PacketLossPct:                packetLossPct,
		JitterMs:                     a.JitterMs,
		RoundTripTimeMs:              a.RoundTripTimeMs,
		AvailableOutgoingBitrateKbps: a.AvailableOutgoingBitrateKbps,
		AvailableIncomingBitrateKbps: a.AvailableIncomingBitrateKbps,
		BytesSent:                    a.BytesSent,
		BytesReceived:                a.BytesReceived,
		Metadata:                     a.Metadata,
	}
}

func selectedCandidatePair(report webrtc.StatsReport) *webrtc.ICECandidatePairStats {
	var chosen *webrtc.ICECandidatePairStats
	for _, stat := range report {
		pair, ok := stat.(webrtc.ICECandidatePairStats)
		if !ok {
			continue
		}
		if pair.Nominated && pair.State == webrtc.StatsICECandidatePairStateSucceeded {
			cp := pair
			return &cp
		}
		if chosen == nil || pair.BytesSent+pair.BytesReceived > chosen.BytesSent+chosen.BytesReceived {
			cp := pair
			chosen = &cp
		}
	}
	return chosen
}

func maxFloat(values ...float64) float64 {
	maxValue := 0.0
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func clampInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}
