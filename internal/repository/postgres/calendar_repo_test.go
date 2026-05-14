package postgres

import (
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
)

func TestDiffReminderDefinitionsPreservesStableRowsAndDeletesOnlyRemoved(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	userC := uuid.New()
	idA := uuid.New()
	idB := uuid.New()
	incomingA := uuid.New()
	incomingC := uuid.New()

	keyA := reminderDefinitionKey{userID: userA, offsetMinutes: 10, channel: entity.ReminderChannelInApp}
	keyB := reminderDefinitionKey{userID: userB, offsetMinutes: 30, channel: entity.ReminderChannelOS}

	removed, added := diffReminderDefinitions(map[reminderDefinitionKey]uuid.UUID{
		keyA: idA,
		keyB: idB,
	}, []entity.EventReminder{
		{ID: incomingA, UserID: userA, OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
		{ID: incomingC, UserID: userC, OffsetMinutes: 60, Channel: entity.ReminderChannelInApp},
		{ID: uuid.New(), UserID: userC, OffsetMinutes: 60, Channel: entity.ReminderChannelInApp},
		{ID: uuid.New(), UserID: uuid.Nil, OffsetMinutes: 5, Channel: entity.ReminderChannelInApp},
	})

	if len(removed) != 1 || removed[0] != idB {
		t.Fatalf("removed reminder ids = %v, want only %s", removed, idB)
	}
	if len(added) != 1 {
		t.Fatalf("added reminders = %v, want exactly one", added)
	}
	if added[0].ID != incomingC || added[0].UserID != userC || added[0].OffsetMinutes != 60 || added[0].Channel != entity.ReminderChannelInApp {
		t.Fatalf("added reminder = %+v, want new user C definition", added[0])
	}
	if added[0].ID == incomingA {
		t.Fatalf("unchanged reminder was re-added with incoming id %s; existing id %s must remain stable", incomingA, idA)
	}
}
