package sfu

import "testing"

func TestNewSFUAllowsExperimentalLyraRegistration(t *testing.T) {
	server, err := NewSFU(Config{EnableExperimentalLyra: true, MaxRoomsPerNode: 1})
	if err != nil {
		t.Fatalf("NewSFU returned error: %v", err)
	}
	defer server.Close()
}

func TestRoomOptionsDefaultTrackBudget(t *testing.T) {
	room := newRoom("room", RoomOptions{})
	if room.options.MaxTracksPerPresenter != DefaultMaxTracks {
		t.Fatalf("MaxTracksPerPresenter = %d, want %d", room.options.MaxTracksPerPresenter, DefaultMaxTracks)
	}
}
