package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	calrrule "aloqa/internal/pkg/rrule"
	"aloqa/internal/platform/txscope"
	searchsvc "aloqa/internal/service/search"
)

func TestStartCallFromEventIsIdempotent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	repo := &fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: organizerID,
				Title:       "Planning",
			},
		},
	}
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, calls, noopPublisher{})

	first, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("first StartCallFromEvent error = %v", err)
	}
	second, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("second StartCallFromEvent error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("call IDs differ: first=%s second=%s", first.ID, second.ID)
	}
	if calls.starts != 1 {
		t.Fatalf("starts = %d, want 1", calls.starts)
	}
	if calls.ensureCalls != 1 {
		t.Fatalf("EnsureLiveKitRoomRequired calls = %d, want 1 for existing linked call", calls.ensureCalls)
	}
}

func TestStartCallFromEventRollsBackWhenLinkFails(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
	})
	txManager.linkErr = errors.New("link failed")
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, calls, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if _, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID); err == nil {
		t.Fatalf("StartCallFromEvent error = nil, want link failure")
	}
	if len(txManager.calls.calls) != 0 {
		t.Fatalf("calls persisted after rollback = %d, want 0", len(txManager.calls.calls))
	}
	if got := txManager.calendars.events[eventID].CallID; got != nil {
		t.Fatalf("event call_id after rollback = %s, want nil", *got)
	}
}

func TestStartCallFromEventRollsBackWhenLiveKitPreparationFails(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
	})
	calls := &fakeCallService{
		calls:     map[uuid.UUID]*entity.Call{},
		ensureErr: cerrors.Unavailable("livekit unavailable"),
	}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, calls, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if _, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID); !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("StartCallFromEvent error = %v, want UNAVAILABLE", err)
	}
	if len(txManager.calls.calls) != 0 {
		t.Fatalf("calls persisted after LiveKit failure = %d, want 0", len(txManager.calls.calls))
	}
	if got := txManager.calendars.events[eventID].CallID; got != nil {
		t.Fatalf("event call_id after LiveKit failure = %s, want nil", *got)
	}
	if calls.deleteCalls != 0 {
		t.Fatalf("DeleteLiveKitRoom calls = %d, want 0 before room creation", calls.deleteCalls)
	}
}

func TestStartCallFromEventEnsuresExistingLinkedLiveKitRoom(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	callID := uuid.New()
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
		CallID:      &callID,
	})
	txManager.calls.calls[callID] = &entity.Call{
		ID:          callID,
		WorkspaceID: workspaceID,
		Type:        entity.CallTypeMeeting,
		Status:      entity.CallStatusRinging,
		Title:       "Planning",
		CreatedBy:   organizerID,
	}
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, calls, noopPublisher{})
	svc.SetTransactionManager(txManager)

	callEntity, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("StartCallFromEvent error = %v", err)
	}
	if callEntity.ID != callID {
		t.Fatalf("call ID = %s, want existing %s", callEntity.ID, callID)
	}
	if calls.ensureCalls != 1 {
		t.Fatalf("EnsureLiveKitRoomRequired calls = %d, want 1", calls.ensureCalls)
	}
	if len(txManager.calls.calls) != 1 {
		t.Fatalf("calls persisted = %d, want existing only", len(txManager.calls.calls))
	}
}

func TestStartCallFromEventConcurrentRequestsReturnOneActiveCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
	})
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, calls, noopPublisher{})
	svc.SetTransactionManager(txManager)

	const workers = 8
	var wg sync.WaitGroup
	results := make([]uuid.UUID, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = call.ID
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d StartCallFromEvent error = %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if results[i] != results[0] {
			t.Fatalf("worker %d call ID = %s, want %s", i, results[i], results[0])
		}
	}
	if len(txManager.calls.calls) != 1 {
		t.Fatalf("calls persisted = %d, want 1", len(txManager.calls.calls))
	}
	call := txManager.calls.calls[results[0]]
	if call == nil || call.ScheduledCallID == nil || *call.ScheduledCallID != eventID {
		t.Fatalf("scheduled_call_id was not linked to event")
	}
}

// --- ALK-819 / S11: per-event call-settings preconfig ----------------------

func TestMeetingDefaultCallSettingsTuple(t *testing.T) {
	got := meetingDefaultCallSettings()
	want := entity.CallSettings{
		WaitingRoom:      false,
		MuteOnJoin:       false,
		Recording:        false,
		ScreenSharing:    true,
		Chat:             true,
		BreakoutRooms:    true,
		BreakoutCreation: entity.BreakoutCreationHost,
		MaxBreakoutRooms: 8,
		MaxParticipants:  500,
		E2EE:             false,
		Watermark:        false,
		EntryMode:        entity.EntryModeOpen,
	}
	if got != want {
		t.Fatalf("meetingDefaultCallSettings() = %+v, want %+v", got, want)
	}
}

func eventSettings(mutate func(*entity.EventCallSettings)) *entity.EventCallSettings {
	s := &entity.EventCallSettings{}
	mutate(s)
	return s
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

// baseWithEventOverlay overlays the event preconfig onto the canonical meeting
// defaults WITHOUT finalising (entry_mode/waiting_room/breakout resolution and
// the server invariants are applied later by FinalizeNewCallSettings on both
// start-call paths). These cases assert the overlay step only.
func TestBaseWithEventOverlay(t *testing.T) {
	manualAdmit := entity.EntryModeManualAdmit
	everyone := entity.BreakoutCreationEveryone

	tests := []struct {
		name string
		in   *entity.EventCallSettings
		want entity.CallSettings
	}{
		{
			name: "nil preconfig yields canonical defaults",
			in:   nil,
			want: meetingDefaultCallSettings(),
		},
		{
			name: "entry_mode overlaid (waiting_room still derived later by the finalizer)",
			in:   eventSettings(func(s *entity.EventCallSettings) { s.EntryMode = &manualAdmit }),
			want: func() entity.CallSettings {
				w := meetingDefaultCallSettings()
				w.EntryMode = entity.EntryModeManualAdmit
				return w
			}(),
		},
		{
			name: "partial overlay keeps untouched defaults",
			in: eventSettings(func(s *entity.EventCallSettings) {
				s.MuteOnJoin = boolPtr(true)
				s.BreakoutCreation = &everyone
				s.MaxBreakoutRooms = intPtr(3)
			}),
			want: func() entity.CallSettings {
				w := meetingDefaultCallSettings()
				w.MuteOnJoin = true
				w.BreakoutCreation = entity.BreakoutCreationEveryone
				w.MaxBreakoutRooms = 3
				return w
			}(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := baseWithEventOverlay(tc.in)
			if got != tc.want {
				t.Fatalf("baseWithEventOverlay() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCreateEventPersistsSettingsForMeet(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	repo := &fakeCalendarRepo{}
	svc := NewService(repo, fakeMembers{}, nil, noopPublisher{})

	manualAdmit := entity.EntryModeManualAdmit
	created, err := svc.CreateEvent(ctx, workspaceID, organizerID, CreateEventInput{
		CalendarID:      uuid.New(),
		Title:           "Planning",
		Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt:     time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		DurationMinutes: 30,
		Settings: &entity.EventCallSettings{
			EntryMode:        &manualAdmit,
			MuteOnJoin:       boolPtr(true),
			MaxBreakoutRooms: intPtr(4),
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent error = %v", err)
	}
	if created.Settings == nil {
		t.Fatalf("created.Settings = nil, want preconfig echoed")
	}
	if created.Settings.EntryMode == nil || *created.Settings.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("created.Settings.EntryMode = %v, want manual_admit", created.Settings.EntryMode)
	}
	if created.Settings.MuteOnJoin == nil || !*created.Settings.MuteOnJoin {
		t.Fatalf("created.Settings.MuteOnJoin = %v, want true", created.Settings.MuteOnJoin)
	}
	if created.Settings.MaxBreakoutRooms == nil || *created.Settings.MaxBreakoutRooms != 4 {
		t.Fatalf("created.Settings.MaxBreakoutRooms = %v, want 4", created.Settings.MaxBreakoutRooms)
	}
	// Round-trip from the repo store.
	stored, err := repo.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent error = %v", err)
	}
	if stored.Settings == nil || stored.Settings.EntryMode == nil || *stored.Settings.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("stored.Settings did not round-trip: %+v", stored.Settings)
	}
}

func TestCreateEventDropsSettingsForNonMeet(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	repo := &fakeCalendarRepo{}
	svc := NewService(repo, fakeMembers{}, nil, noopPublisher{})

	link := "https://example.com/meet"
	created, err := svc.CreateEvent(ctx, workspaceID, organizerID, CreateEventInput{
		CalendarID:      uuid.New(),
		Title:           "Sync",
		Location:        entity.EventLocation{Type: entity.EventLocationExternalLink, Value: &link},
		ScheduledAt:     time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		DurationMinutes: 30,
		Settings:        &entity.EventCallSettings{MuteOnJoin: boolPtr(true)},
	})
	if err != nil {
		t.Fatalf("CreateEvent error = %v", err)
	}
	if created.Settings != nil {
		t.Fatalf("created.Settings = %+v, want nil for non-meet location", created.Settings)
	}
}

func TestUpdateEventSettingsTriState(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()

	newSvc := func() (*Service, *fakeCalendarRepo, uuid.UUID) {
		repo := &fakeCalendarRepo{}
		svc := NewService(repo, fakeMembers{}, nil, noopPublisher{})
		created, err := svc.CreateEvent(ctx, workspaceID, organizerID, CreateEventInput{
			CalendarID:      uuid.New(),
			Title:           "Planning",
			Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt:     time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
			DurationMinutes: 30,
			Settings:        &entity.EventCallSettings{MuteOnJoin: boolPtr(true)},
		})
		if err != nil {
			t.Fatalf("CreateEvent error = %v", err)
		}
		return svc, repo, created.ID
	}

	t.Run("absent leaves settings unchanged", func(t *testing.T) {
		svc, _, eventID := newSvc()
		title := "Planning v2"
		updated, err := svc.UpdateEvent(ctx, workspaceID, eventID, organizerID, UpdateEventInput{Title: &title})
		if err != nil {
			t.Fatalf("UpdateEvent error = %v", err)
		}
		if updated.Settings == nil || updated.Settings.MuteOnJoin == nil || !*updated.Settings.MuteOnJoin {
			t.Fatalf("settings cleared on absent key: %+v", updated.Settings)
		}
	})

	t.Run("null clears settings", func(t *testing.T) {
		svc, _, eventID := newSvc()
		updated, err := svc.UpdateEvent(ctx, workspaceID, eventID, organizerID, UpdateEventInput{SettingsSet: true, Settings: nil})
		if err != nil {
			t.Fatalf("UpdateEvent error = %v", err)
		}
		if updated.Settings != nil {
			t.Fatalf("settings not cleared on explicit null: %+v", updated.Settings)
		}
	})

	t.Run("object replaces settings", func(t *testing.T) {
		svc, _, eventID := newSvc()
		updated, err := svc.UpdateEvent(ctx, workspaceID, eventID, organizerID, UpdateEventInput{
			SettingsSet: true,
			Settings:    &entity.EventCallSettings{MaxBreakoutRooms: intPtr(2)},
		})
		if err != nil {
			t.Fatalf("UpdateEvent error = %v", err)
		}
		if updated.Settings == nil || updated.Settings.MaxBreakoutRooms == nil || *updated.Settings.MaxBreakoutRooms != 2 {
			t.Fatalf("settings not replaced: %+v", updated.Settings)
		}
		// MuteOnJoin from the original create is overwritten by the replacement.
		if updated.Settings.MuteOnJoin != nil {
			t.Fatalf("replacement kept stale field MuteOnJoin: %+v", updated.Settings)
		}
	})

	t.Run("relocating away from meet clears settings", func(t *testing.T) {
		svc, _, eventID := newSvc()
		link := "https://example.com/meet"
		updated, err := svc.UpdateEvent(ctx, workspaceID, eventID, organizerID, UpdateEventInput{
			Location: &entity.EventLocation{Type: entity.EventLocationExternalLink, Value: &link},
		})
		if err != nil {
			t.Fatalf("UpdateEvent error = %v", err)
		}
		if updated.Settings != nil {
			t.Fatalf("settings not cleared on relocation to non-meet: %+v", updated.Settings)
		}
	})
}

func TestStartCallFromEventAppliesSettingsNonTxPath(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	manualAdmit := entity.EntryModeManualAdmit
	everyone := entity.BreakoutCreationEveryone
	repo := &fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: organizerID,
				Title:       "Planning",
				Location:    entity.EventLocation{Type: entity.EventLocationAloqaMeet},
				Settings: &entity.EventCallSettings{
					EntryMode:        &manualAdmit,
					BreakoutCreation: &everyone,
					MaxBreakoutRooms: intPtr(5),
				},
			},
		},
	}
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(repo, fakeMembers{}, calls, noopPublisher{})

	call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("StartCallFromEvent error = %v", err)
	}
	if call.Settings.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("EntryMode = %s, want manual_admit", call.Settings.EntryMode)
	}
	if !call.Settings.WaitingRoom {
		t.Fatalf("WaitingRoom = false, want true for manual_admit")
	}
	if call.Settings.BreakoutCreation != entity.BreakoutCreationEveryone {
		t.Fatalf("BreakoutCreation = %s, want everyone", call.Settings.BreakoutCreation)
	}
	if call.Settings.MaxBreakoutRooms != 5 {
		t.Fatalf("MaxBreakoutRooms = %d, want 5", call.Settings.MaxBreakoutRooms)
	}
}

func TestStartCallFromEventNilSettingsUsesDefaultsNonTxPath(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	repo := &fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: organizerID,
				Title:       "Planning",
				Location:    entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			},
		},
	}
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(repo, fakeMembers{}, calls, noopPublisher{})

	call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("StartCallFromEvent error = %v", err)
	}
	// With no preconfig the call carries the FINALISED canonical defaults: the
	// meeting defaults run through FinalizeNewCallSettings (member-permission
	// pointers defaulted to true, entry_mode/breakout resolved). Compare against
	// the finalised tuple, not the raw defaults.
	want := calls.finalize(entity.CallTypeMeeting, meetingDefaultCallSettings())
	if !callSettingsEqual(call.Settings, want) {
		t.Fatalf("call.Settings = %+v, want finalised canonical defaults %+v", call.Settings, want)
	}
}

func TestStartCallFromEventAppliesSettingsTxPath(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	manualAdmit := entity.EntryModeManualAdmit
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
		Location:    entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		Settings: &entity.EventCallSettings{
			EntryMode: &manualAdmit,
		},
	})
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, calls, noopPublisher{})
	svc.SetTransactionManager(txManager)

	call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("StartCallFromEvent error = %v", err)
	}
	stored := txManager.calls.calls[call.ID]
	if stored == nil {
		t.Fatalf("call %s not persisted in tx path", call.ID)
	}
	if stored.Settings.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("tx EntryMode = %s, want manual_admit", stored.Settings.EntryMode)
	}
	if !stored.Settings.WaitingRoom {
		t.Fatalf("tx WaitingRoom = false, want true (finalize REQUIRED on tx path)")
	}
	// breakout enable is a server invariant: meetings always start with it on.
	if !stored.Settings.BreakoutRooms {
		t.Fatalf("tx BreakoutRooms = false, want true (server invariant for meetings)")
	}
	// Breakout fields still resolved even when overlay didn't touch them.
	if stored.Settings.BreakoutCreation != entity.BreakoutCreationHost {
		t.Fatalf("tx BreakoutCreation = %s, want host (resolved default)", stored.Settings.BreakoutCreation)
	}
	if stored.Settings.MaxBreakoutRooms != 8 {
		t.Fatalf("tx MaxBreakoutRooms = %d, want 8 (resolved default)", stored.Settings.MaxBreakoutRooms)
	}
	// Chat / member-permission invariants applied identically by the finalizer.
	if !stored.Settings.Chat {
		t.Fatalf("tx Chat = false, want true (server invariant)")
	}
	if !stored.Settings.ResolvedMembersCanUnmuteMic() || !stored.Settings.ResolvedMembersCanEnableCamera() {
		t.Fatalf("tx member-permission defaults not applied: %+v", stored.Settings)
	}
}

// TestStartCallFromEventTxAndNonTxPathsProduceIdenticalSettings is the core
// regression for the ALK-819 review finding that the two StartCallFromEvent paths
// could persist different settings. Both paths now route through the SAME
// finalizer (CallService.FinalizeNewCallSettings over meetingDefaultCallSettings
// + the event overlay), so for one event preconfig the call.Settings must be
// byte-identical regardless of whether the tx manager is configured.
func TestStartCallFromEventTxAndNonTxPathsProduceIdenticalSettings(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	manualAdmit := entity.EntryModeManualAdmit
	everyone := entity.BreakoutCreationEveryone

	newEvent := func(eventID uuid.UUID) *entity.CalendarEvent {
		return &entity.CalendarEvent{
			ID:          eventID,
			WorkspaceID: workspaceID,
			OrganizerID: organizerID,
			Title:       "Planning",
			Location:    entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			Settings: &entity.EventCallSettings{
				EntryMode:        &manualAdmit,
				MuteOnJoin:       boolPtr(true),
				BreakoutCreation: &everyone,
				MaxBreakoutRooms: intPtr(4),
			},
		}
	}

	// Non-tx path.
	nonTxEventID := uuid.New()
	nonTxRepo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{nonTxEventID: newEvent(nonTxEventID)}}
	nonTxCalls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	nonTxSvc := NewService(nonTxRepo, fakeMembers{}, nonTxCalls, noopPublisher{})
	nonTxCall, err := nonTxSvc.StartCallFromEvent(ctx, workspaceID, nonTxEventID, organizerID)
	if err != nil {
		t.Fatalf("non-tx StartCallFromEvent error = %v", err)
	}

	// Tx path.
	txEventID := uuid.New()
	txManager := newCalendarCallTxManager(newEvent(txEventID))
	txCalls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	txSvc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, txCalls, noopPublisher{})
	txSvc.SetTransactionManager(txManager)
	txCall, err := txSvc.StartCallFromEvent(ctx, workspaceID, txEventID, organizerID)
	if err != nil {
		t.Fatalf("tx StartCallFromEvent error = %v", err)
	}
	txStored := txManager.calls.calls[txCall.ID]
	if txStored == nil {
		t.Fatalf("tx call %s not persisted", txCall.ID)
	}

	if !callSettingsEqual(nonTxCall.Settings, txStored.Settings) {
		t.Fatalf("settings diverge between paths:\n non-tx = %+v\n     tx = %+v", nonTxCall.Settings, txStored.Settings)
	}
}

// TestStartCallFromEvent_PasswordEntryMode_RejectedOnBothPaths guards the
// blocking ALK-819 review finding: the HTTP layer rejects entry_mode=password for
// scheduled events, but a corrupted/legacy event settings row could still carry
// it. A scheduled meeting has no join password available at start, so BOTH the tx
// and non-tx StartCallFromEvent paths must refuse such a row (same InvalidInput
// class) and create NO call, rather than the tx path persisting an unusable
// password-mode call with no JoinPasswordHash.
func TestStartCallFromEvent_PasswordEntryMode_RejectedOnBothPaths(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	password := entity.EntryModePassword

	// newEvent builds an event whose STORED settings carry entry_mode=password,
	// bypassing the HTTP boundary validation by setting the entity field directly.
	newEvent := func(eventID uuid.UUID) *entity.CalendarEvent {
		return &entity.CalendarEvent{
			ID:          eventID,
			WorkspaceID: workspaceID,
			OrganizerID: organizerID,
			Title:       "Planning",
			Location:    entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			Settings:    &entity.EventCallSettings{EntryMode: &password},
		}
	}

	t.Run("non-tx path", func(t *testing.T) {
		eventID := uuid.New()
		repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{eventID: newEvent(eventID)}}
		calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
		svc := NewService(repo, fakeMembers{}, calls, noopPublisher{})

		call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
		if err == nil {
			t.Fatalf("StartCallFromEvent error = nil, want rejection for password entry mode")
		}
		if !hasCode(err, cerrors.CodeInvalidInput) {
			t.Fatalf("error code = %v, want INVALID_INPUT", err)
		}
		if call != nil {
			t.Fatalf("call = %+v, want nil", call)
		}
		if len(calls.calls) != 0 {
			t.Fatalf("calls created = %d, want 0", len(calls.calls))
		}
		if calls.starts != 0 {
			t.Fatalf("StartCall invocations = %d, want 0 (rejected before StartCall)", calls.starts)
		}
	})

	t.Run("tx path", func(t *testing.T) {
		eventID := uuid.New()
		txManager := newCalendarCallTxManager(newEvent(eventID))
		calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
		svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, calls, noopPublisher{})
		svc.SetTransactionManager(txManager)

		call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
		if err == nil {
			t.Fatalf("StartCallFromEvent error = nil, want rejection for password entry mode")
		}
		if !hasCode(err, cerrors.CodeInvalidInput) {
			t.Fatalf("error code = %v, want INVALID_INPUT", err)
		}
		if call != nil {
			t.Fatalf("call = %+v, want nil", call)
		}
		if len(txManager.calls.calls) != 0 {
			t.Fatalf("calls persisted in tx = %d, want 0", len(txManager.calls.calls))
		}
		if got := txManager.calendars.events[eventID].CallID; got != nil {
			t.Fatalf("event call_id after rejection = %s, want nil", *got)
		}
	})
}

// TestCloneEventCallSettings_DeepCopy guards the ALK-819 review fix that derived
// event rows (e.g. a moved recurring occurrence) must own their own settings
// pointers and never alias the parent's. Mutating any cloned pointer field must
// not bleed back into the original.
func TestCloneEventCallSettings_DeepCopy(t *testing.T) {
	t.Run("nil clones to nil", func(t *testing.T) {
		if got := cloneEventCallSettings(nil); got != nil {
			t.Fatalf("cloneEventCallSettings(nil) = %+v, want nil", got)
		}
	})

	t.Run("fully-populated deep copy", func(t *testing.T) {
		entryMode := entity.EntryModeManualAdmit
		breakoutCreation := entity.BreakoutCreationEveryone
		src := &entity.EventCallSettings{
			EntryMode:        &entryMode,
			MuteOnJoin:       boolPtr(true),
			BreakoutCreation: &breakoutCreation,
			MaxBreakoutRooms: intPtr(4),
		}

		clone := cloneEventCallSettings(src)
		if clone == nil {
			t.Fatalf("cloneEventCallSettings returned nil for populated source")
		}
		if !reflect.DeepEqual(src, clone) {
			t.Fatalf("clone not equal by value:\n src = %+v\n clone = %+v", src, clone)
		}

		// No shared pointers: every pointer field must be a distinct allocation.
		if src.EntryMode == clone.EntryMode {
			t.Fatalf("EntryMode pointer aliased")
		}
		if src.MuteOnJoin == clone.MuteOnJoin {
			t.Fatalf("MuteOnJoin pointer aliased")
		}
		if src.BreakoutCreation == clone.BreakoutCreation {
			t.Fatalf("BreakoutCreation pointer aliased")
		}
		if src.MaxBreakoutRooms == clone.MaxBreakoutRooms {
			t.Fatalf("MaxBreakoutRooms pointer aliased")
		}

		// Mutating each cloned field must not affect the original.
		*clone.EntryMode = entity.EntryModeOpen
		*clone.MuteOnJoin = false
		*clone.BreakoutCreation = entity.BreakoutCreationHost
		*clone.MaxBreakoutRooms = 1
		if *src.EntryMode != entity.EntryModeManualAdmit {
			t.Fatalf("src.EntryMode mutated to %v via clone", *src.EntryMode)
		}
		if !*src.MuteOnJoin {
			t.Fatalf("src.MuteOnJoin mutated to %v via clone", *src.MuteOnJoin)
		}
		if *src.BreakoutCreation != entity.BreakoutCreationEveryone {
			t.Fatalf("src.BreakoutCreation mutated to %v via clone", *src.BreakoutCreation)
		}
		if *src.MaxBreakoutRooms != 4 {
			t.Fatalf("src.MaxBreakoutRooms mutated to %v via clone", *src.MaxBreakoutRooms)
		}
	})
}

// callSettingsEqual compares two CallSettings by value, dereferencing the
// member-permission pointer fields so a freshly-allocated *true equals another
// *true (a plain == would compare pointer identity for those fields).
func callSettingsEqual(a, b entity.CallSettings) bool {
	if a.ResolvedMembersCanUnmuteMic() != b.ResolvedMembersCanUnmuteMic() {
		return false
	}
	if a.ResolvedMembersCanEnableCamera() != b.ResolvedMembersCanEnableCamera() {
		return false
	}
	a.MembersCanUnmuteMic, a.MembersCanEnableCamera = nil, nil
	b.MembersCanUnmuteMic, b.MembersCanEnableCamera = nil, nil
	return a == b
}

func TestUpsertRsvpPublishesRsvpEvent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	repo := &fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: organizerID,
				Attendees: []entity.EventAttendee{{
					ID:         uuid.New(),
					EventID:    eventID,
					UserID:     &userID,
					IsRequired: true,
					RsvpStatus: entity.RsvpStatusNoResponse,
				}},
			},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, userID}: true}}, nil, pub)

	if _, err := svc.UpsertRsvp(ctx, workspaceID, eventID, userID, entity.RsvpStatusGoing); err != nil {
		t.Fatalf("UpsertRsvp error = %v", err)
	}
	if len(pub.events) != 2 {
		t.Fatalf("published events = %d, want organizer + attendee user events", len(pub.events))
	}
	if pub.events[0].Type != event.TypeCalendarAttendeeRsvpUpdated {
		t.Fatalf("event type = %s", pub.events[0].Type)
	}
	if pub.subjects[1] != userEventsSubject(userID) {
		t.Fatalf("user subject = %q, want %q", pub.subjects[1], userEventsSubject(userID))
	}
}

func TestUpdateEventPreservesDispatchedReminderState(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	calendarID := uuid.New()
	occurrenceAt := time.Date(2026, 5, 14, 15, 0, 0, 0, time.UTC)
	dispatchedAt := time.Date(2026, 5, 14, 14, 50, 30, 0, time.UTC)
	repo := &fakeCalendarRepo{}
	svc := NewService(repo, fakeMembers{}, nil, noopPublisher{})

	eventEntity, err := svc.CreateEvent(ctx, workspaceID, organizerID, CreateEventInput{
		CalendarID:      calendarID,
		Title:           "Planning",
		Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt:     occurrenceAt,
		DurationMinutes: 30,
		Reminders: []entity.EventReminder{{
			OffsetMinutes: 10,
			Channel:       entity.ReminderChannelInApp,
		}},
	})
	if err != nil {
		t.Fatalf("CreateEvent error = %v", err)
	}
	reminder := mustFindEventReminder(t, repo, eventEntity.ID, organizerID, 10, entity.ReminderChannelInApp)
	dispatchKey := reminderDispatchMapKey(reminder.ID, occurrenceAt)
	repo.dispatches = map[string]time.Time{dispatchKey: dispatchedAt}

	title := "Planning v2"
	if _, err := svc.UpdateEvent(ctx, workspaceID, eventEntity.ID, organizerID, UpdateEventInput{
		Title:        &title,
		RemindersSet: true,
		Reminders: []entity.EventReminder{{
			OffsetMinutes: 10,
			Channel:       entity.ReminderChannelInApp,
		}},
	}); err != nil {
		t.Fatalf("UpdateEvent error = %v", err)
	}

	if _, ok := repo.reminders[reminder.ID]; !ok {
		t.Fatalf("stable reminder row %s was removed", reminder.ID)
	}
	gotDispatchedAt, ok := repo.dispatches[dispatchKey]
	if !ok {
		t.Fatalf("dispatch row for stable reminder %s was removed", reminder.ID)
	}
	if !gotDispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("dispatched_at = %s, want %s", gotDispatchedAt, dispatchedAt)
	}
}

func TestUpdateEventCascadesDispatchOnReminderRemoval(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	calendarID := uuid.New()
	occurrenceAt := time.Date(2026, 5, 14, 16, 0, 0, 0, time.UTC)
	dispatchedAt := time.Date(2026, 5, 14, 15, 30, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{}
	svc := NewService(repo, fakeMembers{}, nil, noopPublisher{})

	eventEntity, err := svc.CreateEvent(ctx, workspaceID, organizerID, CreateEventInput{
		CalendarID:      calendarID,
		Title:           "Planning",
		Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt:     occurrenceAt,
		DurationMinutes: 30,
		Reminders: []entity.EventReminder{
			{OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
			{OffsetMinutes: 30, Channel: entity.ReminderChannelInApp},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent error = %v", err)
	}
	keptReminder := mustFindEventReminder(t, repo, eventEntity.ID, organizerID, 10, entity.ReminderChannelInApp)
	removedReminder := mustFindEventReminder(t, repo, eventEntity.ID, organizerID, 30, entity.ReminderChannelInApp)
	keptDispatchKey := reminderDispatchMapKey(keptReminder.ID, occurrenceAt)
	removedDispatchKey := reminderDispatchMapKey(removedReminder.ID, occurrenceAt)
	repo.dispatches = map[string]time.Time{
		keptDispatchKey:    dispatchedAt,
		removedDispatchKey: dispatchedAt.Add(-20 * time.Minute),
	}

	title := "Planning v2"
	if _, err := svc.UpdateEvent(ctx, workspaceID, eventEntity.ID, organizerID, UpdateEventInput{
		Title:        &title,
		RemindersSet: true,
		Reminders: []entity.EventReminder{{
			OffsetMinutes: 10,
			Channel:       entity.ReminderChannelInApp,
		}},
	}); err != nil {
		t.Fatalf("UpdateEvent error = %v", err)
	}

	if _, ok := repo.reminders[keptReminder.ID]; !ok {
		t.Fatalf("kept reminder row %s was removed", keptReminder.ID)
	}
	if gotDispatchedAt, ok := repo.dispatches[keptDispatchKey]; !ok || !gotDispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("kept dispatch row = %s, present %v; want %s", gotDispatchedAt, ok, dispatchedAt)
	}
	if _, ok := repo.reminders[removedReminder.ID]; ok {
		t.Fatalf("removed reminder row %s is still present", removedReminder.ID)
	}
	if _, ok := repo.dispatches[removedDispatchKey]; ok {
		t.Fatalf("dispatch row for removed reminder %s is still present", removedReminder.ID)
	}
}

func TestListAndDispatchRemindersEnqueuesOutboxAndMarksDispatchedAtomically(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminderTargets: []entity.ReminderTarget{target}})
	pub := &capturingPublisher{}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, pub)
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("first dispatch error = %v", err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("published during dispatch tx = %d, want 0", len(pub.events))
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(txManager.calendars.outbox))
	}
	key := reminderDispatchMapKey(reminderID, target.Occurrence.InstanceAt)
	if _, ok := txManager.calendars.dispatches[key]; !ok {
		t.Fatalf("reminder occurrence was not marked dispatched")
	}

	restarted := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, pub)
	restarted.SetTransactionManager(txManager)
	if err := restarted.ListAndDispatchReminders(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("dispatch after restart error = %v", err)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows after restart = %d, want 1", len(txManager.calendars.outbox))
	}

	publisher := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, pub)
	processed, failed, dead, err := publisher.PublishPendingReminderOutbox(ctx)
	if err != nil {
		t.Fatalf("publish outbox error = %v", err)
	}
	if processed != 1 || failed != 0 || dead != 0 {
		t.Fatalf("outbox result = processed %d failed %d dead %d, want 1/0/0", processed, failed, dead)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published reminder events = %d, want 1", len(pub.events))
	}
	if pub.events[0].Type != event.TypeCalendarEventReminderFired {
		t.Fatalf("event type = %s", pub.events[0].Type)
	}
	if len(txManager.calendars.publishedOutbox) != 1 {
		t.Fatalf("published outbox rows = %d, want 1", len(txManager.calendars.publishedOutbox))
	}
}

func TestListAndDispatchRemindersRollsBackWhenOutboxInsertFails(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{
		reminderTargets: []entity.ReminderTarget{reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))},
		enqueueErr:      errors.New("outbox insert failed"),
	}
	txManager := newCalendarReminderTxManager(repo)
	pub := &capturingPublisher{}
	svc := NewService(repo, fakeMembers{}, nil, pub)
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err == nil {
		t.Fatalf("dispatch error = nil, want outbox insert failure")
	}
	if len(pub.events) != 0 {
		t.Fatalf("published events = %d, want 0", len(pub.events))
	}
	if len(txManager.calendars.outbox) != 0 {
		t.Fatalf("outbox rows after rollback = %d, want 0", len(txManager.calendars.outbox))
	}
	if len(txManager.calendars.dispatches) != 0 {
		t.Fatalf("dispatched rows after rollback = %d, want 0", len(txManager.calendars.dispatches))
	}
}

func TestListAndDispatchRemindersRecurringEventFiresEachOccurrence(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	firstFire := time.Date(2026, 5, 13, 9, 50, 0, 0, time.UTC)
	eventEntity := entity.CalendarEvent{
		ID:              eventID,
		WorkspaceID:     workspaceID,
		OrganizerID:     userID,
		Title:           "Daily standup",
		ScheduledAt:     firstFire.Add(10 * time.Minute),
		DurationMinutes: 30,
		Recurrence:      &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"},
	}
	targets := make([]entity.ReminderTarget, 0, 3)
	for i := 0; i < 3; i++ {
		occurrenceAt := eventEntity.ScheduledAt.AddDate(0, 0, i)
		targets = append(targets, entity.ReminderTarget{
			ReminderID:    reminderID,
			UserID:        userID,
			OffsetMinutes: 10,
			Channel:       entity.ReminderChannelInApp,
			FireAt:        occurrenceAt.Add(-10 * time.Minute),
			Occurrence: entity.EventOccurrence{
				CalendarEvent:       eventEntity,
				InstanceAt:          occurrenceAt,
				IsRecurringInstance: true,
			},
		})
	}
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminderTargets: targets})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	for i := 0; i < 3; i++ {
		if err := svc.ListAndDispatchReminders(ctx, firstFire.AddDate(0, 0, i)); err != nil {
			t.Fatalf("dispatch day %d error = %v", i+1, err)
		}
		if got := len(txManager.calendars.outbox); got != i+1 {
			t.Fatalf("outbox rows after day %d = %d, want %d", i+1, got, i+1)
		}
	}
	if len(txManager.calendars.dispatches) != 3 {
		t.Fatalf("dispatched occurrences = %d, want 3", len(txManager.calendars.dispatches))
	}
}

func TestListAndDispatchRemindersSkipsPastOccurrencesOutsideHorizon(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminderTargets: []entity.ReminderTarget{target}})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	if len(txManager.calendars.outbox) != 0 {
		t.Fatalf("outbox rows = %d, want 0", len(txManager.calendars.outbox))
	}
	if len(txManager.calendars.dispatches) != 0 {
		t.Fatalf("dispatched rows = %d, want 0", len(txManager.calendars.dispatches))
	}
}

func TestListAndDispatchRemindersConcurrentWorkersDoNotDoubleDiscover(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{
		reminderTargets: []entity.ReminderTarget{reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))},
	})
	svcA := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svcA.SetTransactionManager(txManager)
	svcB := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svcB.SetTransactionManager(txManager)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, svc := range []*Service{svcA, svcB} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			errs[i] = svc.ListAndDispatchReminders(ctx, now)
		}(i, svc)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d dispatch error = %v", i, err)
		}
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(txManager.calendars.outbox))
	}
	if len(txManager.calendars.dispatches) != 1 {
		t.Fatalf("dispatched rows = %d, want 1", len(txManager.calendars.dispatches))
	}
}

func TestListAndDispatchRemindersDiscoversLongOffsetEventWithinLookahead(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now.Add(-48*time.Hour), now.Add(5*24*time.Hour))
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminderTargets: []entity.ReminderTarget{target}})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	if txManager.calendars.lastReminderHorizon != reminderEventLookaheadHorizon {
		t.Fatalf("horizon = %s, want %s", txManager.calendars.lastReminderHorizon, reminderEventLookaheadHorizon)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(txManager.calendars.outbox))
	}
	if err := svc.ListAndDispatchReminders(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("second dispatch error = %v", err)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows after second run = %d, want 1", len(txManager.calendars.outbox))
	}
}

func TestListAndDispatchRemindersWaitsForLongOffsetFireTime(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	start := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	occurrenceAt := start.Add(14 * 24 * time.Hour)
	fireAt := occurrenceAt.Add(-time.Duration(maxReminderOffsetMinutes) * time.Minute)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, fireAt, occurrenceAt)
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminderTargets: []entity.ReminderTarget{target}})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, start); err != nil {
		t.Fatalf("early dispatch error = %v", err)
	}
	if len(txManager.calendars.outbox) != 0 {
		t.Fatalf("early outbox rows = %d, want 0", len(txManager.calendars.outbox))
	}
	if err := svc.ListAndDispatchReminders(ctx, fireAt); err != nil {
		t.Fatalf("dispatch at fire time error = %v", err)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows at fire time = %d, want 1", len(txManager.calendars.outbox))
	}
}

func TestPublishPendingReminderOutboxConcurrentPublishersDoNotDoublePublish(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))
	payload, err := prepareReminderPayload(target, now)
	if err != nil {
		t.Fatalf("prepare reminder payload error = %v", err)
	}
	repo := &fakeCalendarRepo{outbox: []entity.ReminderOutboxMessage{{
		ID:           uuid.New(),
		ReminderID:   reminderID,
		EventID:      eventID,
		OccurrenceAt: target.Occurrence.InstanceAt,
		UserID:       userID,
		PayloadJSON:  payload,
		EnqueuedAt:   now,
	}}}
	pub := &capturingPublisher{}
	svcA := NewService(repo, fakeMembers{}, nil, pub)
	svcB := NewService(repo, fakeMembers{}, nil, pub)

	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)
	for i, svc := range []*Service{svcA, svcB} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			processed, _, _, err := svc.PublishPendingReminderOutbox(ctx)
			results[i] = processed
			errs[i] = err
		}(i, svc)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("publisher %d error = %v", i, err)
		}
	}
	if results[0]+results[1] != 1 {
		t.Fatalf("processed rows = %d + %d, want 1 total", results[0], results[1])
	}
	if len(pub.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(pub.events))
	}
	if len(repo.publishedOutbox) != 1 {
		t.Fatalf("published outbox rows = %d, want 1", len(repo.publishedOutbox))
	}
}

func TestListEventsKeepsOriginatorTimezoneAcrossBerlinDST(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	calendarID := uuid.New()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	startLocal := time.Date(2026, 3, 28, 9, 30, 0, 0, loc)
	repo := &expandingCalendarRepo{fakeCalendarRepo: fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:              eventID,
				CalendarID:      calendarID,
				WorkspaceID:     workspaceID,
				OrganizerID:     organizerID,
				Title:           "Berlin standup",
				Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
				ScheduledAt:     startLocal.UTC(),
				OriginatorTZ:    "Europe/Berlin",
				DurationMinutes: 30,
				Recurrence:      &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=4"},
			},
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, nil, noopPublisher{})

	occurrences, err := svc.ListEvents(ctx, workspaceID, startLocal.Add(-time.Hour).UTC(), startLocal.AddDate(0, 0, 5).UTC(), organizerID)
	if err != nil {
		t.Fatalf("ListEvents error = %v", err)
	}
	if len(occurrences) != 4 {
		t.Fatalf("occurrences = %v, want 4", occurrences)
	}
	for _, occurrence := range occurrences {
		local := occurrence.InstanceAt.In(loc)
		if local.Hour() != 9 || local.Minute() != 30 {
			t.Fatalf("occurrence shifted from 09:30 Europe/Berlin: %v", occurrences)
		}
	}
}

func TestMoveEventOccurrence_NonRecurring_All_OK(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "S", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30,
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})

	newAt := time.Date(2026, 5, 14, 15, 0, 0, 0, time.UTC)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: now, Scope: MoveScopeAll, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated.ScheduledAt.Equal(newAt.UTC()) {
		t.Fatalf("ScheduledAt=%v want=%v", res.Updated.ScheduledAt, newAt.UTC())
	}
	if res.Updated.DurationMinutes != 30 {
		t.Fatalf("duration not preserved: %d", res.Updated.DurationMinutes)
	}
	if res.Created != nil {
		t.Fatal("scope=all must not create event")
	}
}

func TestMoveEventOccurrence_NonRecurring_This_DegeneratesToAll(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30,
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	newAt := now.Add(48 * time.Hour)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: now, Scope: MoveScopeThis, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated.ScheduledAt.Equal(newAt.UTC()) || res.Created != nil {
		t.Fatalf("degenerate: %+v", res)
	}
}

func TestMoveEventOccurrence_Recurring_All_OK(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=10"},
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	newAt := start.Add(time.Hour)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: start, Scope: MoveScopeAll, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated.ScheduledAt.Equal(newAt.UTC()) {
		t.Fatalf("ScheduledAt=%v", res.Updated.ScheduledAt)
	}
	if res.Updated.Recurrence == nil || res.Updated.Recurrence.RRule != "FREQ=DAILY;COUNT=10" {
		t.Fatalf("recurrence must be preserved: %+v", res.Updated.Recurrence)
	}
	if res.Created != nil {
		t.Fatal("scope=all no new event")
	}
}

func TestMoveEventOccurrence_ScopeAll_OptimisticLock_Conflict(t *testing.T) {
	// R2/M2: scope=all must also honour ExpectedUpdatedAt.
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	storedUpdatedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	staleExpected := storedUpdatedAt.Add(-time.Second)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: storedUpdatedAt, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(newCalendarReminderTxManager(repo))
	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt:        storedUpdatedAt,
		Scope:             MoveScopeAll,
		NewScheduledAt:    storedUpdatedAt.Add(time.Hour),
		ExpectedUpdatedAt: &staleExpected,
	})
	assertConflict(t, err, "CONCURRENT_UPDATE")
}

func TestMoveEventOccurrence_Recurring_All_WithMatchingToken(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	storedUpdatedAt := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
	matchExpected := storedUpdatedAt
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=10"},
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(newCalendarReminderTxManager(repo))
	newAt := start.Add(time.Hour)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt:        start,
		Scope:             MoveScopeAll,
		NewScheduledAt:    newAt,
		ExpectedUpdatedAt: &matchExpected,
	})
	if err != nil {
		t.Fatalf("matching-token recurring scope=all must succeed: %v", err)
	}
	if !res.Updated.ScheduledAt.Equal(newAt.UTC()) {
		t.Fatalf("ScheduledAt=%v want=%v", res.Updated.ScheduledAt, newAt.UTC())
	}
	if res.Updated.Recurrence == nil || res.Updated.Recurrence.RRule != "FREQ=DAILY;COUNT=10" {
		t.Fatalf("recurrence must be preserved through the tx path: %+v", res.Updated.Recurrence)
	}
	if res.Created != nil {
		t.Fatal("scope=all must not create a new event")
	}
}

func TestMoveEventOccurrence_Recurring_All_WithStaleToken(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	storedUpdatedAt := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
	staleExpected := storedUpdatedAt.Add(-time.Second)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=10"},
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(newCalendarReminderTxManager(repo))
	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt:        start,
		Scope:             MoveScopeAll,
		NewScheduledAt:    start.Add(time.Hour),
		ExpectedUpdatedAt: &staleExpected,
	})
	assertConflict(t, err, "CONCURRENT_UPDATE")
}

func TestMoveEventOccurrence_NonOrganizer_Forbidden(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, other, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30,
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{
		{wsID, orgID}: true, {wsID, other}: true,
	}}, nil, noopPublisher{})
	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, other, MoveOccurrenceInput{
		InstanceAt: now, Scope: MoveScopeAll, NewScheduledAt: now.Add(time.Hour),
	})
	assertForbidden(t, err, "FORBIDDEN_NOT_ORGANIZER")
}

func TestMoveEventOccurrence_InvalidScope_BadRequest(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30,
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: now, Scope: "bogus", NewScheduledAt: now.Add(time.Hour),
	})
	assertInvalidInput(t, err, "INVALID_SCOPE")
}

func TestMoveEventOccurrence_DurationOutOfRange_BadRequest(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	bad := 1441
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30,
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: now, Scope: MoveScopeAll,
		NewScheduledAt: now.Add(time.Hour), NewDurationMinutes: &bad,
	})
	assertInvalidInput(t, err, "INVALID_DURATION")
}

func TestCreateEventTx_StoresEvent(t *testing.T) {
	ctx := context.Background()
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{}}
	eventID := uuid.New()
	wsID := uuid.New()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	ev := &entity.CalendarEvent{
		ID: eventID, WorkspaceID: wsID,
		Title: "T", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	got, err := repo.CreateEventTx(ctx, ev)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != eventID {
		t.Fatalf("ID=%v want=%v", got.ID, eventID)
	}
	if _, ok := repo.events[eventID]; !ok {
		t.Fatal("event not stored")
	}
}

func TestUpdateEventTx_UpdatesEvent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	eventID := uuid.New()
	wsID := uuid.New()
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID,
			Title: "Old", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30, UpdatedAt: now,
		},
	}}
	ev := &entity.CalendarEvent{
		ID: eventID, WorkspaceID: wsID,
		Title: "New", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now.Add(time.Hour), DurationMinutes: 45, UpdatedAt: now,
	}
	got, err := repo.UpdateEventTx(ctx, ev, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New" {
		t.Fatalf("Title=%q want=New", got.Title)
	}
}

func TestUpdateEventTx_OptimisticLock_Conflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	stale := now.Add(-time.Second)
	eventID := uuid.New()
	wsID := uuid.New()
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID,
			Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: now, DurationMinutes: 30, UpdatedAt: now,
		},
	}}
	ev := &entity.CalendarEvent{
		ID: eventID, WorkspaceID: wsID,
		Title: "Y", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	_, err := repo.UpdateEventTx(ctx, ev, &stale)
	assertConflict(t, err, "CONCURRENT_UPDATE")
}

func TestMoveEventOccurrence_Recurring_This_OK(t *testing.T) {
	ctx := context.Background()
	wsID, orgID := uuid.New(), uuid.New()
	eventID := uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	instance := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	attendeeUID := uuid.New()
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"},
			Attendees: []entity.EventAttendee{{
				ID: uuid.New(), EventID: eventID, UserID: &attendeeUID,
				IsRequired: true, RsvpStatus: entity.RsvpStatusGoing,
			}},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	newAt := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: instance, Scope: MoveScopeThis, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}

	parent := txMgr.calendars.events[eventID]
	var hasExdate bool
	for _, ex := range parent.Recurrence.Exdates {
		if ex.UTC().Equal(instance.UTC()) {
			hasExdate = true
		}
	}
	if !hasExdate {
		t.Fatalf("parent exdates=%v missing instance", parent.Recurrence.Exdates)
	}
	if res.Created == nil {
		t.Fatal("Created must not be nil")
	}
	if res.Created.ID == eventID {
		t.Fatal("Created must have new ID")
	}
	if !res.Created.ScheduledAt.Equal(newAt.UTC()) {
		t.Fatalf("Created.ScheduledAt=%v", res.Created.ScheduledAt)
	}
	if res.Created.Recurrence != nil {
		t.Fatal("Created.Recurrence must be nil")
	}
	if res.Created.CallID != nil {
		t.Fatal("Created.CallID must be nil")
	}
	if len(res.Created.Attendees) != 1 {
		t.Fatalf("attendees=%d want=1", len(res.Created.Attendees))
	}
	att := res.Created.Attendees[0]
	if att.RsvpStatus != entity.RsvpStatusNoResponse {
		t.Fatalf("RsvpStatus=%q want=no_response", att.RsvpStatus)
	}
	if att.RespondedAt != nil {
		t.Fatal("RespondedAt must be nil")
	}
	if att.ID == repo.events[eventID].Attendees[0].ID {
		t.Fatal("attendee ID must be regenerated")
	}
}

// TestMoveEventOccurrence_Recurring_This_PreservesSettings is the ALK-819 review
// regression: a moved occurrence of an aloqa_meet recurring meeting must carry
// the parent's call-settings preconfig (deep-copied) onto the split-off child,
// not fall back to defaults.
func TestMoveEventOccurrence_Recurring_This_PreservesSettings(t *testing.T) {
	ctx := context.Background()
	wsID, orgID := uuid.New(), uuid.New()
	eventID := uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	instance := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	manualAdmit := entity.EntryModeManualAdmit
	everyone := entity.BreakoutCreationEveryone
	parentSettings := &entity.EventCallSettings{
		EntryMode:        &manualAdmit,
		MuteOnJoin:       boolPtr(true),
		BreakoutCreation: &everyone,
		MaxBreakoutRooms: intPtr(3),
	}
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"},
			Settings:   parentSettings,
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	newAt := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: instance, Scope: MoveScopeThis, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Created == nil || res.Created.Settings == nil {
		t.Fatalf("child settings = %+v, want preserved preconfig", res.Created)
	}
	child := res.Created.Settings
	if child.EntryMode == nil || *child.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("child EntryMode = %v, want manual_admit", child.EntryMode)
	}
	if child.MuteOnJoin == nil || !*child.MuteOnJoin {
		t.Fatalf("child MuteOnJoin = %v, want true", child.MuteOnJoin)
	}
	if child.BreakoutCreation == nil || *child.BreakoutCreation != entity.BreakoutCreationEveryone {
		t.Fatalf("child BreakoutCreation = %v, want everyone", child.BreakoutCreation)
	}
	if child.MaxBreakoutRooms == nil || *child.MaxBreakoutRooms != 3 {
		t.Fatalf("child MaxBreakoutRooms = %v, want 3", child.MaxBreakoutRooms)
	}
	// Deep copy: mutating the child's pointers must not affect the parent's.
	if child.EntryMode == parentSettings.EntryMode {
		t.Fatal("child EntryMode pointer aliases parent (want deep copy)")
	}
	*child.MaxBreakoutRooms = 99
	if parentSettings.MaxBreakoutRooms == nil || *parentSettings.MaxBreakoutRooms != 3 {
		t.Fatalf("mutating child leaked into parent: parent MaxBreakoutRooms = %v", parentSettings.MaxBreakoutRooms)
	}
}

func TestMoveEventOccurrence_InstanceNotInSeries_BadRequest(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt:     time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
		Scope:          MoveScopeThis,
		NewScheduledAt: time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC),
	})
	assertInvalidInput(t, err, "INVALID_INSTANCE")
}

func TestMoveEventOccurrence_InstanceAlreadyExdated_BadRequest(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	exdated := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5", Exdates: []time.Time{exdated}},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: exdated, Scope: MoveScopeThis,
		NewScheduledAt: exdated.Add(24 * time.Hour),
	})
	assertInvalidInput(t, err, "INVALID_INSTANCE_EXDATED")
}

func TestMoveEventOccurrence_ConcurrentUpdate_Conflict_409(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	instance := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	storedUpdatedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	staleExpected := storedUpdatedAt.Add(-time.Second)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt:        instance,
		Scope:             MoveScopeThis,
		NewScheduledAt:    instance.Add(24 * time.Hour),
		ExpectedUpdatedAt: &staleExpected,
	})
	assertConflict(t, err, "CONCURRENT_UPDATE")
}

func TestMoveEventOccurrence_Recurring_ThisAndFollowing_OK(t *testing.T) {
	ctx := context.Background()
	wsID, orgID := uuid.New(), uuid.New()
	eventID := uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	instance := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	futureEx := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	delta := 2 * 24 * time.Hour
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=7", Exdates: []time.Time{futureEx}},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	newAt := instance.Add(delta)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: instance, Scope: MoveScopeThisAndFollowing, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}

	parent := txMgr.calendars.events[eventID]
	if parent.Recurrence == nil || !strings.Contains(parent.Recurrence.RRule, "UNTIL=") {
		t.Fatalf("parent must have UNTIL: %+v", parent.Recurrence)
	}
	for _, ex := range parent.Recurrence.Exdates {
		if !ex.UTC().Before(instance.UTC()) {
			t.Fatalf("parent exdate %v must be < instance", ex)
		}
	}
	if res.Created == nil {
		t.Fatal("Created must not be nil")
	}
	if !res.Created.ScheduledAt.Equal(newAt.UTC()) {
		t.Fatalf("child ScheduledAt=%v", res.Created.ScheduledAt)
	}
	if res.Created.Recurrence == nil || !strings.Contains(res.Created.Recurrence.RRule, "FREQ=DAILY") {
		t.Fatalf("child rrule: %+v", res.Created.Recurrence)
	}
	shiftedEx := futureEx.UTC().Add(delta)
	var found bool
	for _, ex := range res.Created.Recurrence.Exdates {
		if ex.UTC().Equal(shiftedEx) {
			found = true
		}
	}
	if !found {
		t.Fatalf("child exdates %v missing shifted future exdate %v", res.Created.Recurrence.Exdates, shiftedEx)
	}
}

func TestMoveEventOccurrence_Recurring_ThisAndFollowing_FirstOccurrence_DegeneratesToAll(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"},
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})

	newAt := start.Add(time.Hour)
	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: start, Scope: MoveScopeThisAndFollowing, NewScheduledAt: newAt,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !res.Updated.ScheduledAt.Equal(newAt.UTC()) || res.Created != nil {
		t.Fatalf("degenerate: %+v", res)
	}
	if res.Updated.Recurrence == nil {
		t.Fatal("recurrence must be preserved in degenerate path")
	}
}

func TestMoveEventOccurrence_Recurring_ThisAndFollowing_LastOccurrence_OK(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	last := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	res, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: last, Scope: MoveScopeThisAndFollowing,
		NewScheduledAt: last.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Created == nil {
		t.Fatal("last occurrence must still produce child event")
	}
	if res.Created.Recurrence == nil || !strings.Contains(res.Created.Recurrence.RRule, "COUNT=1") {
		t.Fatalf("child rrule must be COUNT=1: %+v", res.Created.Recurrence)
	}
}

func TestMoveEventOccurrence_Recurring_ThisAndFollowing_FirstOccurrenceAlreadyExdated_BadRequest(t *testing.T) {
	ctx := context.Background()
	wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{
				RRule:   "FREQ=DAILY;COUNT=5",
				Exdates: []time.Time{start},
			},
		},
	}}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txMgr)

	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: start, Scope: MoveScopeThisAndFollowing,
		NewScheduledAt: start.Add(time.Hour),
	})
	assertInvalidInput(t, err, "INVALID_INSTANCE_EXDATED")
}

func TestMoveEventOccurrence_RealtimePublishedCorrectly(t *testing.T) {
	ctx := context.Background()
	wsID, orgID := uuid.New(), uuid.New()
	eventID := uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	instance := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
			Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"},
		},
	}}
	pub := &capturingPublisher{}
	txMgr := newCalendarReminderTxManager(repo)
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, pub)
	svc.SetTransactionManager(txMgr)

	_, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: instance, Scope: MoveScopeThis,
		NewScheduledAt: time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}

	counts := map[event.Type]int{}
	for _, e := range pub.events {
		counts[e.Type]++
	}
	if counts[event.TypeCalendarEventUpdated] != 1 {
		t.Fatalf("Updated count=%d want=1", counts[event.TypeCalendarEventUpdated])
	}
	if counts[event.TypeCalendarEventCreated] != 1 {
		t.Fatalf("Created count=%d want=1", counts[event.TypeCalendarEventCreated])
	}

	repo2 := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
		eventID: {
			ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
			Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt: start, DurationMinutes: 30,
		},
	}}
	pub2 := &capturingPublisher{}
	svc2 := NewService(repo2, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, pub2)
	if _, err := svc2.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
		InstanceAt: start, Scope: MoveScopeAll, NewScheduledAt: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("scope=all err=%v", err)
	}
	if len(pub2.events) != 1 || pub2.events[0].Type != event.TypeCalendarEventUpdated {
		t.Fatalf("scope=all events=%v", pub2.events)
	}
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

type fakeCalendarRepo struct {
	mu                  sync.Mutex
	calendars           []entity.UserCalendar
	events              map[uuid.UUID]*entity.CalendarEvent
	reminders           map[uuid.UUID]entity.EventReminder
	reminderTargets     []entity.ReminderTarget
	dispatches          map[string]time.Time
	outbox              []entity.ReminderOutboxMessage
	publishedOutbox     map[uuid.UUID]time.Time
	enqueueErr          error
	lastReminderHorizon time.Duration
	lastReminderLimit   int
}

func reminderTarget(workspaceID, userID, eventID, reminderID uuid.UUID, fireAt, occurrenceAt time.Time) entity.ReminderTarget {
	return entity.ReminderTarget{
		ReminderID:    reminderID,
		UserID:        userID,
		OffsetMinutes: int(occurrenceAt.Sub(fireAt) / time.Minute),
		Channel:       entity.ReminderChannelInApp,
		FireAt:        fireAt.UTC(),
		Occurrence: entity.EventOccurrence{
			CalendarEvent: entity.CalendarEvent{
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: userID,
				Title:       "Standup",
				ScheduledAt: occurrenceAt.UTC(),
			},
			InstanceAt: occurrenceAt.UTC(),
		},
	}
}

func reminderDispatchMapKey(reminderID uuid.UUID, occurrenceAt time.Time) string {
	return reminderID.String() + "|" + occurrenceAt.UTC().Format(time.RFC3339Nano)
}

func mustFindEventReminder(t *testing.T, repo *fakeCalendarRepo, eventID, userID uuid.UUID, offsetMinutes int, channel entity.ReminderChannel) entity.EventReminder {
	t.Helper()
	for _, reminder := range repo.reminders {
		if reminder.EventID == eventID && reminder.UserID == userID && reminder.OffsetMinutes == offsetMinutes && reminder.Channel == channel {
			return reminder
		}
	}
	t.Fatalf("reminder not found for event=%s user=%s offset=%d channel=%s", eventID, userID, offsetMinutes, channel)
	return entity.EventReminder{}
}

func (r *fakeCalendarRepo) ListUserCalendars(context.Context, uuid.UUID, uuid.UUID) ([]entity.UserCalendar, error) {
	return r.calendars, nil
}

func (r *fakeCalendarRepo) GetUserCalendar(_ context.Context, workspaceID, calendarID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	for _, calendar := range r.calendars {
		if calendar.ID == calendarID && calendar.WorkspaceID == workspaceID && calendar.OwnerID == ownerID {
			copy := calendar
			return &copy, nil
		}
	}
	return &entity.UserCalendar{ID: calendarID, WorkspaceID: workspaceID, OwnerID: ownerID}, nil
}

func (r *fakeCalendarRepo) CreateUserCalendar(_ context.Context, calendar *entity.UserCalendar) error {
	r.calendars = append(r.calendars, *calendar)
	return nil
}

func (r *fakeCalendarRepo) UpdateUserCalendar(context.Context, *entity.UserCalendar) error {
	return nil
}

func (r *fakeCalendarRepo) DeleteUserCalendar(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *fakeCalendarRepo) SetCalendarVisibility(_ context.Context, workspaceID, calendarID, ownerID uuid.UUID, visible bool) (*entity.UserCalendar, error) {
	return &entity.UserCalendar{ID: calendarID, WorkspaceID: workspaceID, OwnerID: ownerID, IsVisible: visible}, nil
}

func (r *fakeCalendarRepo) EnsureDefaultCalendar(_ context.Context, workspaceID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	return &entity.UserCalendar{ID: uuid.New(), WorkspaceID: workspaceID, OwnerID: ownerID, IsDefault: true}, nil
}

func (r *fakeCalendarRepo) ListEvents(context.Context, uuid.UUID, time.Time, time.Time, uuid.UUID) ([]entity.EventOccurrence, error) {
	return nil, nil
}

func (r *fakeCalendarRepo) ListUpcoming(context.Context, uuid.UUID, uuid.UUID, int) ([]entity.EventOccurrence, error) {
	return nil, nil
}

func (r *fakeCalendarRepo) ListDueReminderTargets(_ context.Context, now time.Time, horizon time.Duration, limit int) ([]entity.ReminderTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastReminderHorizon = horizon
	r.lastReminderLimit = limit
	if limit <= 0 {
		limit = len(r.reminderTargets)
	}
	windowStart := now.UTC().Add(-30 * time.Second)
	windowEnd := now.UTC().Add(horizon)
	var targets []entity.ReminderTarget
	for _, reminder := range r.reminderTargets {
		if len(targets) >= limit {
			break
		}
		if reminder.FireAt.After(now.UTC()) {
			continue
		}
		if reminder.Occurrence.InstanceAt.Before(windowStart) || reminder.Occurrence.InstanceAt.After(windowEnd) {
			continue
		}
		if r.dispatches != nil {
			key := reminderDispatchMapKey(reminder.ReminderID, reminder.Occurrence.InstanceAt)
			if _, ok := r.dispatches[key]; ok {
				continue
			}
		}
		targets = append(targets, reminder)
	}
	return targets, nil
}

func (r *fakeCalendarRepo) EnqueueReminderOutbox(_ context.Context, target entity.ReminderTarget, payloadJSON []byte, enqueuedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enqueueErr != nil {
		return r.enqueueErr
	}
	r.outbox = append(r.outbox, entity.ReminderOutboxMessage{
		ID:           uuid.New(),
		ReminderID:   target.ReminderID,
		EventID:      target.Occurrence.ID,
		OccurrenceAt: target.Occurrence.InstanceAt.UTC(),
		UserID:       target.UserID,
		PayloadJSON:  append([]byte(nil), payloadJSON...),
		EnqueuedAt:   enqueuedAt.UTC(),
	})
	return nil
}

func (r *fakeCalendarRepo) MarkReminderDispatched(_ context.Context, reminderID uuid.UUID, occurrenceAt, dispatchedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatches == nil {
		r.dispatches = map[string]time.Time{}
	}
	r.dispatches[reminderDispatchMapKey(reminderID, occurrenceAt)] = dispatchedAt
	return nil
}

func (r *fakeCalendarRepo) PublishReminderOutbox(ctx context.Context, limit, maxAttempts int, publish func(context.Context, entity.ReminderOutboxMessage) error) (processed, failed, dead int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = len(r.outbox)
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	if r.publishedOutbox == nil {
		r.publishedOutbox = map[uuid.UUID]time.Time{}
	}
	for i := range r.outbox {
		if processed+failed+dead >= limit {
			break
		}
		msg := r.outbox[i]
		if _, ok := r.publishedOutbox[msg.ID]; ok {
			continue
		}
		if msg.Attempts >= maxAttempts {
			continue
		}
		if publish != nil {
			if err := publish(ctx, msg); err != nil {
				r.outbox[i].Attempts++
				if r.outbox[i].Attempts >= maxAttempts {
					dead++
				} else {
					failed++
				}
				continue
			}
		}
		r.outbox[i].Attempts++
		r.publishedOutbox[msg.ID] = time.Now().UTC()
		processed++
	}
	return processed, failed, dead, nil
}

type expandingCalendarRepo struct {
	fakeCalendarRepo
}

func (r *expandingCalendarRepo) ListEvents(_ context.Context, workspaceID uuid.UUID, fromTS, toTS time.Time, viewerID uuid.UUID) ([]entity.EventOccurrence, error) {
	var occurrences []entity.EventOccurrence
	for _, eventEntity := range r.events {
		if eventEntity.WorkspaceID != workspaceID || !canAccessEvent(eventEntity, viewerID) {
			continue
		}
		if eventEntity.Recurrence == nil {
			occurrences = append(occurrences, entity.EventOccurrence{CalendarEvent: *eventEntity, InstanceAt: eventEntity.ScheduledAt})
			continue
		}
		loc, err := time.LoadLocation(eventEntity.OriginatorTZ)
		if err != nil {
			loc = time.UTC
		}
		expanded, err := calrrule.Expand(eventEntity.Recurrence.RRule, eventEntity.ScheduledAt.In(loc), eventEntity.Recurrence.Exdates, fromTS.In(loc), toTS.In(loc))
		if err != nil {
			return nil, err
		}
		for _, instanceAt := range expanded {
			occurrences = append(occurrences, entity.EventOccurrence{
				CalendarEvent:       *eventEntity,
				InstanceAt:          instanceAt.UTC(),
				IsRecurringInstance: true,
			})
		}
	}
	return occurrences, nil
}

func (r *fakeCalendarRepo) GetEvent(_ context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return nil, cerrors.NotFound("event not found")
	}
	copy := cloneCalendarEvent(eventEntity)
	if r.reminders != nil {
		copy.Reminders = r.eventReminders(eventID)
	}
	return copy, nil
}

func (r *fakeCalendarRepo) LockEventForStartCall(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	return r.GetEvent(ctx, eventID)
}

func (r *fakeCalendarRepo) CreateEvent(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.events == nil {
		r.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	stored := cloneCalendarEvent(eventEntity)
	stored.Reminders = nil
	r.events[eventEntity.ID] = stored
	r.replaceEventReminders(eventEntity.ID, eventEntity.Reminders)
	return r.GetEvent(ctx, eventEntity.ID)
}

func (r *fakeCalendarRepo) CreateEventTx(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	return r.CreateEvent(ctx, eventEntity)
}

func (r *fakeCalendarRepo) UpdateEvent(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.events == nil {
		r.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	stored := cloneCalendarEvent(eventEntity)
	stored.Reminders = nil
	r.events[eventEntity.ID] = stored
	r.replaceEventReminders(eventEntity.ID, eventEntity.Reminders)
	return r.GetEvent(ctx, eventEntity.ID)
}

func (r *fakeCalendarRepo) UpdateEventTx(ctx context.Context, eventEntity *entity.CalendarEvent, expectedUpdatedAt *time.Time) (*entity.CalendarEvent, error) {
	if expectedUpdatedAt != nil {
		existing, err := r.GetEvent(ctx, eventEntity.ID)
		if err != nil {
			return nil, err
		}
		if !existing.UpdatedAt.UTC().Equal(expectedUpdatedAt.UTC()) {
			return nil, cerrors.Conflict("CONCURRENT_UPDATE: event was modified concurrently")
		}
	}
	return r.UpdateEvent(ctx, eventEntity)
}

type fakeReminderDefinitionKey struct {
	userID        uuid.UUID
	offsetMinutes int
	channel       entity.ReminderChannel
}

func (r *fakeCalendarRepo) replaceEventReminders(eventID uuid.UUID, reminders []entity.EventReminder) {
	if r.reminders == nil {
		r.reminders = map[uuid.UUID]entity.EventReminder{}
	}
	existing := map[fakeReminderDefinitionKey]uuid.UUID{}
	for reminderID, reminder := range r.reminders {
		if reminder.EventID != eventID || reminder.UserID == uuid.Nil {
			continue
		}
		key := fakeReminderDefinitionKey{
			userID:        reminder.UserID,
			offsetMinutes: reminder.OffsetMinutes,
			channel:       reminder.Channel,
		}
		if _, ok := existing[key]; !ok {
			existing[key] = reminderID
		}
	}

	desired := make(map[fakeReminderDefinitionKey]entity.EventReminder, len(reminders))
	orderedDesiredKeys := make([]fakeReminderDefinitionKey, 0, len(reminders))
	for _, reminder := range reminders {
		if reminder.UserID == uuid.Nil {
			continue
		}
		key := fakeReminderDefinitionKey{
			userID:        reminder.UserID,
			offsetMinutes: reminder.OffsetMinutes,
			channel:       reminder.Channel,
		}
		if _, ok := desired[key]; ok {
			continue
		}
		desired[key] = reminder
		orderedDesiredKeys = append(orderedDesiredKeys, key)
	}

	for key, reminderID := range existing {
		if _, ok := desired[key]; ok {
			continue
		}
		delete(r.reminders, reminderID)
		r.deleteReminderDispatches(reminderID)
	}
	for _, key := range orderedDesiredKeys {
		if _, ok := existing[key]; ok {
			continue
		}
		reminder := desired[key]
		if reminder.ID == uuid.Nil {
			reminder.ID = id.New()
		}
		reminder.EventID = eventID
		r.reminders[reminder.ID] = reminder
	}
}

func (r *fakeCalendarRepo) deleteReminderDispatches(reminderID uuid.UUID) {
	prefix := reminderID.String() + "|"
	for key := range r.dispatches {
		if strings.HasPrefix(key, prefix) {
			delete(r.dispatches, key)
		}
	}
}

func (r *fakeCalendarRepo) eventReminders(eventID uuid.UUID) []entity.EventReminder {
	var reminders []entity.EventReminder
	for _, reminder := range r.reminders {
		if reminder.EventID == eventID {
			reminders = append(reminders, reminder)
		}
	}
	return reminders
}

func (r *fakeCalendarRepo) DeleteEvent(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeCalendarRepo) SetEventCallIDIfUnset(_ context.Context, eventID, callID uuid.UUID) (*entity.CalendarEvent, error) {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return nil, cerrors.NotFound("event not found")
	}
	if eventEntity.CallID == nil {
		eventEntity.CallID = &callID
	}
	return eventEntity, nil
}

func (r *fakeCalendarRepo) LinkEventAndCall(_ context.Context, eventID, callID uuid.UUID) error {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return cerrors.NotFound("event not found")
	}
	eventEntity.CallID = &callID
	return nil
}

func (r *fakeCalendarRepo) UpsertRsvp(_ context.Context, eventID, userID uuid.UUID, status entity.RsvpStatus) (*entity.EventAttendee, error) {
	now := time.Now().UTC()
	attendee := &entity.EventAttendee{
		ID:          uuid.New(),
		EventID:     eventID,
		UserID:      &userID,
		IsRequired:  true,
		RsvpStatus:  status,
		RespondedAt: &now,
	}
	return attendee, nil
}

type fakeMembers struct {
	members map[[2]uuid.UUID]bool
}

func (m fakeMembers) Create(context.Context, *entity.Workspace) error { return nil }
func (m fakeMembers) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}

func (m fakeMembers) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}

func (m fakeMembers) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (m fakeMembers) Update(context.Context, *entity.Workspace) error          { return nil }
func (m fakeMembers) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
func (m fakeMembers) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if m.members == nil || m.members[[2]uuid.UUID{workspaceID, userID}] {
		return &entity.WorkspaceMember{ID: uuid.New(), WorkspaceID: workspaceID, UserID: userID}, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}

func (m fakeMembers) ListMembers(context.Context, uuid.UUID, pagination.Params, string) ([]entity.WorkspaceMember, error) {
	return nil, nil
}

func (m fakeMembers) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (m fakeMembers) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeCallService struct {
	mu          sync.Mutex
	starts      int
	deleteCalls int
	ensureCalls int
	ensureErr   error
	calls       map[uuid.UUID]*entity.Call
}

func (s *fakeCallService) StartCall(_ context.Context, workspaceID, userID uuid.UUID, callType entity.CallType, title string, channelID *uuid.UUID, settings entity.CallSettings, _ string) (*entity.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	call := &entity.Call{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		Type:        callType,
		Status:      entity.CallStatusRinging,
		Title:       title,
		CreatedBy:   userID,
		// Mirror call.Service.StartCall: the non-tx start-call path finalises the
		// caller-supplied settings before persisting, so the fake must too — this
		// is what makes the non-tx path comparable to the tx path (which calls
		// FinalizeNewCallSettings directly) in the equivalence test. (ALK-819 review.)
		Settings:  s.finalize(callType, settings),
		CreatedAt: time.Now().UTC(),
	}
	s.calls[call.ID] = call
	return call, nil
}

// finalize mirrors call.Service.FinalizeNewCallSettings so the fake produces the
// same persisted settings the real call service would (ALK-819 review). It is a
// pure function (no shared state) and safe to call without the mutex.
func (s *fakeCallService) finalize(callType entity.CallType, settings entity.CallSettings) entity.CallSettings {
	if callType == entity.CallTypeGroup || callType == entity.CallTypeMeeting {
		settings.BreakoutRooms = true
	}
	settings.Recording = false // fake has no egress configured (s.recordingEnabled == false)
	settings.Chat = true
	if settings.MembersCanUnmuteMic == nil {
		settings.MembersCanUnmuteMic = boolPtr(true)
	}
	if settings.MembersCanEnableCamera == nil {
		settings.MembersCanEnableCamera = boolPtr(true)
	}
	entryMode := settings.ResolvedEntryMode()
	settings.EntryMode = entryMode
	settings.WaitingRoom = entryMode == entity.EntryModeManualAdmit
	settings.BreakoutCreation = settings.ResolvedBreakoutCreation()
	settings.MaxBreakoutRooms = settings.ResolvedMaxBreakoutRooms()
	return settings
}

func (s *fakeCallService) FinalizeNewCallSettings(callType entity.CallType, settings entity.CallSettings) entity.CallSettings {
	return s.finalize(callType, settings)
}

// ValidateNewCallSettings mirrors call.Service.ValidateNewCallSettings (a thin
// wrapper over the package-private validateCallSettings) so the calendar service's
// scheduled-settings chokepoint runs the SAME field validation against the fake
// that it would against the real call service (ALK-819 review). It is a pure
// function (no shared state) and safe to call without the mutex.
func (s *fakeCallService) ValidateNewCallSettings(callType entity.CallType, settings entity.CallSettings) error {
	switch callType {
	case entity.CallTypeOneToOne, entity.CallTypeGroup, entity.CallTypeMeeting, entity.CallTypeWebinar, entity.CallTypeSelector:
	default:
		return cerrors.InvalidInput("invalid call type")
	}
	if settings.MaxParticipants < 0 {
		return cerrors.InvalidInput("max_participants cannot be negative")
	}
	if settings.EntryMode != "" && !settings.EntryMode.Valid() {
		return cerrors.InvalidInput("invalid entry_mode")
	}
	if settings.BreakoutCreation != "" && !settings.BreakoutCreation.Valid() {
		return cerrors.InvalidInput("invalid breakout_creation")
	}
	if settings.MaxBreakoutRooms != 0 && (settings.MaxBreakoutRooms < 1 || settings.MaxBreakoutRooms > 8) {
		return cerrors.InvalidInput("max_breakout_rooms must be between 1 and 8")
	}
	return nil
}

func (s *fakeCallService) GetCall(_ context.Context, _ uuid.UUID, callID, _ uuid.UUID) (*entity.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[callID]
	if call == nil {
		return nil, cerrors.NotFound("call not found")
	}
	return call, nil
}

func (s *fakeCallService) EnsureLiveKitRoomRequired(context.Context, *entity.Call) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCalls++
	return s.ensureErr
}

func (s *fakeCallService) DeleteLiveKitRoom(context.Context, uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls++
	return nil
}

type calendarCallTxManager struct {
	mu        sync.Mutex
	calendars *txCalendarRepo
	calls     *txCallRepo
	events    []event.Event
	linkErr   error
}

func newCalendarCallTxManager(eventEntity *entity.CalendarEvent) *calendarCallTxManager {
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{}}
	copy := *eventEntity
	repo.events[eventEntity.ID] = &copy
	return newCalendarReminderTxManager(repo)
}

func newCalendarReminderTxManager(repo *fakeCalendarRepo) *calendarCallTxManager {
	if repo.events == nil {
		repo.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	return &calendarCallTxManager{
		calendars: &txCalendarRepo{fakeCalendarRepo: repo},
		calls:     &txCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[uuid.UUID][]entity.CallParticipant{}},
	}
}

func (m *calendarCallTxManager) WithinTx(ctx context.Context, fn func(context.Context, txscope.Scope) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	calendars := m.calendars.clone()
	calls := m.calls.clone()
	calendars.calls = calls
	calendars.linkErr = m.linkErr
	scope := &fakeCalendarTxScope{calendars: calendars, calls: calls}
	if err := fn(ctx, scope); err != nil {
		return err
	}
	m.calendars = calendars
	m.calls = calls
	m.events = append(m.events, scope.events...)
	return nil
}

type txCalendarRepo struct {
	*fakeCalendarRepo
	calls   *txCallRepo
	linkErr error
}

func (r *txCalendarRepo) clone() *txCalendarRepo {
	events := map[uuid.UUID]*entity.CalendarEvent{}
	for eventID, eventEntity := range r.events {
		events[eventID] = cloneCalendarEvent(eventEntity)
	}
	return &txCalendarRepo{fakeCalendarRepo: &fakeCalendarRepo{
		calendars:           append([]entity.UserCalendar(nil), r.calendars...),
		events:              events,
		reminders:           cloneEventReminderMap(r.reminders),
		reminderTargets:     append([]entity.ReminderTarget(nil), r.reminderTargets...),
		dispatches:          cloneReminderDispatchMap(r.dispatches),
		outbox:              cloneReminderOutbox(r.outbox),
		publishedOutbox:     cloneUUIDTimeMap(r.publishedOutbox),
		enqueueErr:          r.enqueueErr,
		lastReminderHorizon: r.lastReminderHorizon,
		lastReminderLimit:   r.lastReminderLimit,
	}}
}

func (r *txCalendarRepo) LinkEventAndCall(_ context.Context, eventID, callID uuid.UUID) error {
	if r.linkErr != nil {
		return r.linkErr
	}
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return cerrors.NotFound("event not found")
	}
	callEntity := r.calls.calls[callID]
	if callEntity == nil {
		return cerrors.NotFound("call not found")
	}
	eventEntity.CallID = &callID
	scheduledCallID := eventID
	callEntity.ScheduledCallID = &scheduledCallID
	return nil
}

type txCallRepo struct {
	calls        map[uuid.UUID]*entity.Call
	participants map[uuid.UUID][]entity.CallParticipant
}

func (r *txCallRepo) clone() *txCallRepo {
	calls := map[uuid.UUID]*entity.Call{}
	for callID, callEntity := range r.calls {
		copy := *callEntity
		calls[callID] = &copy
	}
	participants := map[uuid.UUID][]entity.CallParticipant{}
	for callID, existing := range r.participants {
		participants[callID] = append([]entity.CallParticipant(nil), existing...)
	}
	return &txCallRepo{calls: calls, participants: participants}
}

func (r *txCallRepo) Create(_ context.Context, call *entity.Call) error {
	if r.calls == nil {
		r.calls = map[uuid.UUID]*entity.Call{}
	}
	copy := *call
	r.calls[call.ID] = &copy
	return nil
}

func (r *txCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	call := r.calls[id]
	if call == nil {
		return nil, cerrors.NotFound("call not found")
	}
	copy := *call
	return &copy, nil
}

func (r *txCallRepo) ListActiveByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]entity.Call, error) {
	var calls []entity.Call
	for _, call := range r.calls {
		if call.WorkspaceID == workspaceID && call.Status != entity.CallStatusEnded {
			calls = append(calls, *call)
		}
	}
	return calls, nil
}

func (r *txCallRepo) UpdateStatus(_ context.Context, id uuid.UUID, status entity.CallStatus) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	call.Status = status
	return nil
}

func (r *txCallRepo) UpdateSettings(_ context.Context, id uuid.UUID, settings entity.CallSettings) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	call.Settings = settings
	return nil
}

func (r *txCallRepo) UpdateAccessLevel(context.Context, uuid.UUID, entity.AccessLevel) error {
	return nil
}

func (r *txCallRepo) AddInvitedMembers(context.Context, uuid.UUID, []uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *txCallRepo) IsInvited(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (r *txCallRepo) ListInvitedMembers(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (r *txCallRepo) SnapshotConnectedIntoInvited(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *txCallRepo) End(_ context.Context, id uuid.UUID) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now
	return nil
}

func (r *txCallRepo) AddParticipant(_ context.Context, p *entity.CallParticipant) error {
	if r.participants == nil {
		r.participants = map[uuid.UUID][]entity.CallParticipant{}
	}
	r.participants[p.CallID] = append(r.participants[p.CallID], *p)
	return nil
}

func (r *txCallRepo) AddParticipantIfCapacity(ctx context.Context, p *entity.CallParticipant, _ int) error {
	return r.AddParticipant(ctx, p)
}

func (r *txCallRepo) GetParticipant(context.Context, uuid.UUID, uuid.UUID) (*entity.CallParticipant, error) {
	return nil, cerrors.NotFound("participant not found")
}

func (r *txCallRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}

func (r *txCallRepo) UpdateParticipantStatus(context.Context, uuid.UUID, entity.ParticipantStatus) error {
	return nil
}

func (r *txCallRepo) TransferHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (r *txCallRepo) UpdateParticipantRole(context.Context, uuid.UUID, entity.CallRole) error {
	return nil
}

func (r *txCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}
func (r *txCallRepo) SetCanScreenShare(context.Context, uuid.UUID, bool) error { return nil }
func (r *txCallRepo) SetFeaturedShareUserID(context.Context, uuid.UUID, *uuid.UUID) error {
	return nil
}
func (r *txCallRepo) SetPinnedParticipantUserID(context.Context, uuid.UUID, *uuid.UUID) error {
	return nil
}
func (r *txCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeCalendarTxScope struct {
	calendars repository.CalendarRepository
	calls     repository.CallRepository
	events    []event.Event
}

func (s *fakeCalendarTxScope) Users() repository.UserRepository                       { return nil }
func (s *fakeCalendarTxScope) Workspaces() repository.WorkspaceRepository             { return nil }
func (s *fakeCalendarTxScope) Messages() repository.MessageRepository                 { return nil }
func (s *fakeCalendarTxScope) Channels() repository.ChannelRepository                 { return nil }
func (s *fakeCalendarTxScope) ChannelGrants() repository.ChannelAccessGrantRepository { return nil }
func (s *fakeCalendarTxScope) Calls() repository.CallRepository                       { return s.calls }
func (s *fakeCalendarTxScope) CallMessages() repository.CallMessageRepository         { return nil }
func (s *fakeCalendarTxScope) Calendars() repository.CalendarRepository               { return s.calendars }
func (s *fakeCalendarTxScope) Recordings() repository.RecordingRepository             { return nil }
func (s *fakeCalendarTxScope) Invites() repository.GuestInviteRepository              { return nil }
func (s *fakeCalendarTxScope) GuestGrants() repository.GuestAccessRepository          { return nil }
func (s *fakeCalendarTxScope) Roles() repository.WorkspaceRoleRepository              { return nil }
func (s *fakeCalendarTxScope) Audit() repository.AuditRepository                      { return nil }
func (s *fakeCalendarTxScope) SearchIndexer() searchsvc.Indexer                       { return nil }
func (s *fakeCalendarTxScope) EnqueueRealtime(_ context.Context, evt event.Event, _ []byte) error {
	s.events = append(s.events, evt)
	return nil
}

func cloneCalendarEvent(eventEntity *entity.CalendarEvent) *entity.CalendarEvent {
	if eventEntity == nil {
		return nil
	}
	copy := *eventEntity
	copy.Attendees = append([]entity.EventAttendee(nil), eventEntity.Attendees...)
	copy.Reminders = append([]entity.EventReminder(nil), eventEntity.Reminders...)
	return &copy
}

func cloneEventReminderMap(in map[uuid.UUID]entity.EventReminder) map[uuid.UUID]entity.EventReminder {
	if in == nil {
		return nil
	}
	out := map[uuid.UUID]entity.EventReminder{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneReminderDispatchMap(in map[string]time.Time) map[string]time.Time {
	if in == nil {
		return nil
	}
	out := map[string]time.Time{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneUUIDTimeMap(in map[uuid.UUID]time.Time) map[uuid.UUID]time.Time {
	if in == nil {
		return nil
	}
	out := map[uuid.UUID]time.Time{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneReminderOutbox(in []entity.ReminderOutboxMessage) []entity.ReminderOutboxMessage {
	out := make([]entity.ReminderOutboxMessage, len(in))
	for i, msg := range in {
		out[i] = msg
		out[i].PayloadJSON = append([]byte(nil), msg.PayloadJSON...)
	}
	return out
}

func assertForbidden(t *testing.T, err error, fragment string) {
	t.Helper()
	ae, ok := cerrors.AsAppError(err)
	if !ok || ae.Code != cerrors.CodeForbidden {
		t.Fatalf("want Forbidden, got %v", err)
	}
	if !strings.Contains(ae.Message, fragment) {
		t.Fatalf("message %q missing fragment %q", ae.Message, fragment)
	}
}

func assertInvalidInput(t *testing.T, err error, fragment string) {
	t.Helper()
	ae, ok := cerrors.AsAppError(err)
	if !ok || ae.Code != cerrors.CodeInvalidInput {
		t.Fatalf("want InvalidInput, got %v", err)
	}
	if !strings.Contains(ae.Message, fragment) {
		t.Fatalf("message %q missing %q", ae.Message, fragment)
	}
}

func assertConflict(t *testing.T, err error, fragment string) {
	t.Helper()
	ae, ok := cerrors.AsAppError(err)
	if !ok || ae.Code != cerrors.CodeConflict {
		t.Fatalf("want Conflict, got %v", err)
	}
	if !strings.Contains(ae.Message, fragment) {
		t.Fatalf("message %q missing fragment %q", ae.Message, fragment)
	}
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type capturingPublisher struct {
	mu       sync.Mutex
	subjects []string
	events   []event.Event
}

func (p *capturingPublisher) Publish(_ context.Context, subject string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subject)
	var evt event.Event
	if err := json.Unmarshal(data, &evt); err == nil {
		p.events = append(p.events, evt)
	}
	return nil
}
