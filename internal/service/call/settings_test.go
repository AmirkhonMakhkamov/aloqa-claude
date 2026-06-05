package call

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

func TestUpdateCallSettingsRejectsNonHost(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	actorID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: breakoutCall(callID, workspaceID, hostID),
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, actorID}: connectedParticipant(callID, actorID, entity.CallRoleParticipant),
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	_, err := svc.UpdateCallSettings(ctx, callID, actorID, CallSettingsPatch{BreakoutRooms: boolPtr(false)})
	if !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("UpdateCallSettings non-host error = %v, want FORBIDDEN", err)
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0", calls.settingsUpdates)
	}
}

func TestUpdateCallSettingsEnableBreakoutRoomsPersistsAndPublishes(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	callEntity := breakoutCall(callID, workspaceID, hostID)
	callEntity.Settings.BreakoutRooms = false
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: callEntity},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{BreakoutRooms: boolPtr(true)})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if !updated.Settings.BreakoutRooms || !calls.calls[callID].Settings.BreakoutRooms {
		t.Fatalf("breakout_rooms was not persisted true: updated=%v stored=%v", updated.Settings.BreakoutRooms, calls.calls[callID].Settings.BreakoutRooms)
	}
	if calls.settingsUpdates != 1 {
		t.Fatalf("settingsUpdates = %d, want 1", calls.settingsUpdates)
	}
	assertSettingsChangedEvent(t, pub, callID, true)
}

func TestUpdateCallSettingsDisableBreakoutRoomsClosesActiveRoomsFirst(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	breakouts := newStubBreakoutRepo()
	roomID := uuid.New()
	breakouts.rooms[roomID] = &entity.BreakoutRoom{
		ID:     roomID,
		CallID: callID,
		Name:   "A",
		Status: entity.BreakoutRoomStatusActive,
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, breakouts, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, newBreakoutSFU(t), mediaTestConfig(), nil, nil)

	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{BreakoutRooms: boolPtr(false)})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if updated.Settings.BreakoutRooms || calls.calls[callID].Settings.BreakoutRooms {
		t.Fatalf("breakout_rooms was not persisted false: updated=%v stored=%v", updated.Settings.BreakoutRooms, calls.calls[callID].Settings.BreakoutRooms)
	}
	if breakouts.closeAllByCallCount != 1 {
		t.Fatalf("CloseAllByCall count = %d, want 1", breakouts.closeAllByCallCount)
	}
	if breakouts.rooms[roomID].Status != entity.BreakoutRoomStatusClosed {
		t.Fatalf("breakout room status = %s, want closed", breakouts.rooms[roomID].Status)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutRoomsAllClosed); got != 1 {
		t.Fatalf("breakout all_closed events = %d, want 1", got)
	}
	assertSettingsChangedEvent(t, pub, callID, false)
}

func TestUpdateCallSettingsNilPatchLeavesSettingsUntouched(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if !updated.Settings.BreakoutRooms {
		t.Fatalf("BreakoutRooms = false, want original true")
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0", calls.settingsUpdates)
	}
	if pub.called {
		t.Fatalf("published event for nil patch, want no-op")
	}
}

func TestUpdateCallSettingsMuteOnJoinPersistsAndPublishes(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{MuteOnJoin: boolPtr(true)})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if !updated.Settings.MuteOnJoin || !calls.calls[callID].Settings.MuteOnJoin {
		t.Fatalf("mute_on_join not persisted: updated=%v stored=%v", updated.Settings.MuteOnJoin, calls.calls[callID].Settings.MuteOnJoin)
	}
	if calls.settingsUpdates != 1 {
		t.Fatalf("settingsUpdates = %d, want 1", calls.settingsUpdates)
	}
	// breakout_rooms is unchanged (still the fixture default true).
	assertSettingsChangedEvent(t, pub, callID, true)
}

func TestUpdateCallSettingsEntryModeManualAdmitSetsWaitingRoom(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	callEntity := breakoutCall(callID, workspaceID, hostID)
	callEntity.Settings.EntryMode = entity.EntryModeOpen
	callEntity.Settings.WaitingRoom = false
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: callEntity},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	mode := entity.EntryModeManualAdmit
	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{EntryMode: &mode})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if updated.Settings.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("entry_mode = %s, want manual_admit", updated.Settings.EntryMode)
	}
	if !updated.Settings.WaitingRoom || !calls.calls[callID].Settings.WaitingRoom {
		t.Fatalf("waiting_room not derived true for manual_admit: updated=%v stored=%v", updated.Settings.WaitingRoom, calls.calls[callID].Settings.WaitingRoom)
	}
	assertSettingsChangedEvent(t, pub, callID, true)
}

func TestUpdateCallSettingsEntryModeOpenClearsWaitingRoom(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	callEntity := breakoutCall(callID, workspaceID, hostID)
	callEntity.Settings.EntryMode = entity.EntryModeManualAdmit
	callEntity.Settings.WaitingRoom = true
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: callEntity},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	mode := entity.EntryModeOpen
	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{EntryMode: &mode})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if updated.Settings.EntryMode != entity.EntryModeOpen {
		t.Fatalf("entry_mode = %s, want open", updated.Settings.EntryMode)
	}
	if updated.Settings.WaitingRoom || calls.calls[callID].Settings.WaitingRoom {
		t.Fatalf("waiting_room not cleared for open: updated=%v stored=%v", updated.Settings.WaitingRoom, calls.calls[callID].Settings.WaitingRoom)
	}
}

func TestUpdateCallSettingsEntryModePasswordWithoutHashRejected(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	callEntity := breakoutCall(callID, workspaceID, hostID)
	callEntity.JoinPasswordHash = ""
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: callEntity},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	mode := entity.EntryModePassword
	_, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{EntryMode: &mode})
	if !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("entry_mode=password without hash error = %v, want INVALID_INPUT", err)
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0", calls.settingsUpdates)
	}
	if pub.called {
		t.Fatalf("published event on rejected patch, want no-op")
	}
}

func TestUpdateCallSettingsEntryModePasswordWithHashAccepted(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	callEntity := breakoutCall(callID, workspaceID, hostID)
	callEntity.JoinPasswordHash = "bcrypt-hash"
	callEntity.Settings.WaitingRoom = true
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: callEntity},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	mode := entity.EntryModePassword
	updated, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{EntryMode: &mode})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if updated.Settings.EntryMode != entity.EntryModePassword {
		t.Fatalf("entry_mode = %s, want password", updated.Settings.EntryMode)
	}
	// password mode is not the lobby mode → waiting_room derives false.
	if updated.Settings.WaitingRoom {
		t.Fatalf("waiting_room = true, want false for password mode")
	}
	if calls.calls[callID].JoinPasswordHash != "bcrypt-hash" {
		t.Fatalf("join password hash mutated: %q", calls.calls[callID].JoinPasswordHash)
	}
}

func TestUpdateCallSettingsInvalidEntryModeRejected(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	mode := entity.EntryMode("bogus")
	_, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{EntryMode: &mode})
	if !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("invalid entry_mode error = %v, want INVALID_INPUT", err)
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0", calls.settingsUpdates)
	}
}

func TestUpdateCallSettingsRejectedEntryModeLeavesOtherFieldsUntouched(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	mode := entity.EntryMode("bogus")
	_, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{
		MuteOnJoin: boolPtr(true),
		EntryMode:  &mode,
	})
	if !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("error = %v, want INVALID_INPUT", err)
	}
	// The invalid entry_mode aborts before persist — the same-request mute_on_join
	// must not leak into stored settings.
	if calls.calls[callID].Settings.MuteOnJoin {
		t.Fatalf("mute_on_join leaked despite the rejected entry_mode")
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0", calls.settingsUpdates)
	}
	if pub.called {
		t.Fatalf("published event on rejected patch, want no-op")
	}
}

func assertSettingsChangedEvent(t *testing.T, pub *capturingPublisher, callID uuid.UUID, breakoutRooms bool) {
	t.Helper()
	if !pub.called {
		t.Fatalf("settings changed event was not published")
	}
	var env struct {
		Type    event.Type                       `json:"type"`
		Payload event.CallSettingsChangedPayload `json:"payload"`
	}
	if err := json.Unmarshal(pub.body, &env); err != nil {
		t.Fatalf("unmarshal settings event: %v", err)
	}
	if env.Type != event.TypeCallSettingsChanged {
		t.Fatalf("event type = %s, want %s", env.Type, event.TypeCallSettingsChanged)
	}
	if env.Payload.CallID != callID {
		t.Fatalf("payload call_id = %s, want %s", env.Payload.CallID, callID)
	}
	if env.Payload.Settings.BreakoutRooms != breakoutRooms {
		t.Fatalf("payload breakout_rooms = %v, want %v", env.Payload.Settings.BreakoutRooms, breakoutRooms)
	}
}
