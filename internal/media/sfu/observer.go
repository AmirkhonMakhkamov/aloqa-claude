package sfu

import (
	"github.com/pion/rtp"
)

type ObservedTrack struct {
	TrackID    string
	StreamID   string
	SourcePeer string
	MimeType   string
	Layer      string
}

type PacketSink interface {
	WriteRTP(packet *rtp.Packet) error
	Close() error
}

type TrackObserver interface {
	OnTrack(track ObservedTrack) (PacketSink, error)
}
