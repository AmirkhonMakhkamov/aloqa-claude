package event

import "testing"

// TestCallInvitedIsEphemeral guards the zombie-ring regression: call.invited
// rings a specific user into an ongoing call and must NEVER be persisted to the
// durable outbox or replayed to a freshly-subscribed socket. Replayable=true
// here would let a stale invite pop an incoming-call ring for a long-ended call.
func TestCallInvitedIsEphemeral(t *testing.T) {
	def := DefinitionForType(TypeCallInvited)
	if def.DeliverySemantic != DeliveryEphemeral {
		t.Fatalf("call.invited delivery semantic = %q, want %q", def.DeliverySemantic, DeliveryEphemeral)
	}
	if def.Replayable {
		t.Fatal("call.invited must not be replayable (durable replay = zombie ring)")
	}
}

func TestCallControlEventsAreEphemeral(t *testing.T) {
	for _, typ := range []Type{TypeCallPinnedChanged, TypeCallParticipantMuted, TypeCallAskUnmute} {
		def := DefinitionForType(typ)
		if def.DeliverySemantic != DeliveryEphemeral {
			t.Fatalf("%s delivery semantic = %q, want %q", typ, def.DeliverySemantic, DeliveryEphemeral)
		}
		if def.Replayable {
			t.Fatalf("%s must not be replayable", typ)
		}
	}
}
