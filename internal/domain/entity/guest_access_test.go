package entity

import (
	"testing"

	"github.com/google/uuid"
)

func TestGuestAccessGrantAllowsChannelLegacy(t *testing.T) {
	chA := uuid.New()
	chB := uuid.New()

	// Empty channel list (legacy, no call scope) → all channels allowed.
	all := GuestAccessGrant{}
	if !all.AllowsChannel(chA) || !all.AllowsChannel(chB) {
		t.Fatalf("empty-channel legacy grant should allow any channel")
	}

	// Listed channels → only those.
	scoped := GuestAccessGrant{ChannelIDs: []uuid.UUID{chA}}
	if !scoped.AllowsChannel(chA) {
		t.Fatalf("listed channel should be allowed")
	}
	if scoped.AllowsChannel(chB) {
		t.Fatalf("unlisted channel should be denied")
	}
}

func TestGuestAccessGrantCallScopedNeverAllowsChannel(t *testing.T) {
	callID := uuid.New()
	channelID := uuid.New()

	// A call-scoped grant confers NO channel access, even with an empty channel
	// list (which would mean "all channels" for a legacy grant).
	grant := GuestAccessGrant{CallID: &callID}
	if grant.AllowsChannel(channelID) {
		t.Fatalf("call-scoped grant must not confer channel access")
	}

	// Even if channel IDs happen to be listed, the call scope wins.
	grantWithChannels := GuestAccessGrant{CallID: &callID, ChannelIDs: []uuid.UUID{channelID}}
	if grantWithChannels.AllowsChannel(channelID) {
		t.Fatalf("call-scoped grant must deny channels regardless of listed channel IDs")
	}
}

func TestGuestAccessGrantAllowsCall(t *testing.T) {
	callID := uuid.New()
	otherCall := uuid.New()

	grant := GuestAccessGrant{CallID: &callID}
	if !grant.AllowsCall(callID) {
		t.Fatalf("call-scoped grant should allow its own call")
	}
	if grant.AllowsCall(otherCall) {
		t.Fatalf("call-scoped grant should not allow a different call")
	}

	// A legacy (non-call-scoped) grant allows no call.
	legacy := GuestAccessGrant{}
	if legacy.AllowsCall(callID) {
		t.Fatalf("legacy grant should not allow any call")
	}
}
