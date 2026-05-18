# ALOQA-239 — Backend Occurrence Move: Implementation Plan

> Branch: `feature/ALOQA-239-occurrence-move` off `origin/develop @ 2d6f955`
> Spec: `docs/superpowers/specs/2026-05-15-calendar-design-parity-design.md §4` (lines 73–272)
> Budget: Tasks 0–11, one commit per task.

## Architecture

New route: `POST /workspaces/{wsID}/events/{eventID}/occurrences/move`

| Layer | Action |
|---|---|
| `internal/pkg/rrule/recurrence_split.go` | NEW — 4 helpers: SetUntil, SetDtstart, IsMember, ShiftBounds |
| `internal/pkg/rrule/recurrence_split_test.go` | NEW — 21 tests |
| `internal/service/calendar/service.go` | MODIFIED — types + MoveEventOccurrence + 3 branch methods |
| `internal/service/calendar/service_test.go` | MODIFIED — 13 new tests |
| `internal/handler/http/calendar.go` | MODIFIED — MoveOccurrence handler |
| `internal/handler/http/calendar_test.go` | NEW — 4 handler tests |
| `internal/handler/http/router.go` | MODIFIED — 1-line wiring |

No migrations (spec §4.9). No new dependencies.

**RRULE round-trip** (teambition/rrule-go v1.8.2):
- Parse: `teambitionrrule.StrToROption(rule)` → `*ROption, error`
- Mutate fields: `opt.Until` (`time.Time`), `opt.Count` (`int`, zero = absent), `opt.Dtstart`
- Serialize: `opt.RRuleString()` — RRULE part only, no DTSTART prefix
- DTSTART lives in `CalendarEvent.ScheduledAt`; rule strings never embed it

**Error constructors** (`cerrors` has no `BadRequest`; HTTP 400 = `cerrors.InvalidInput`):
- `INVALID_INSTANCE` / `INVALID_INSTANCE_EXDATED` → `cerrors.InvalidInput("INVALID_INSTANCE: ...")`
- `INVALID_DURATION` → `cerrors.InvalidInput("INVALID_DURATION: ...")`
- `INVALID_SCOPE` → `cerrors.InvalidInput("INVALID_SCOPE: ...")`
- `FORBIDDEN_NOT_ORGANIZER` → `cerrors.Forbidden("FORBIDDEN_NOT_ORGANIZER: ...")`

**Transaction pattern**: `s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error { ... })` using `scope.Calendars()`, mirroring `ListAndDispatchReminders` (service.go:527). For scope=this and scope=this_and_following: guard `if s.tx == nil { return nil, cerrors.Unavailable("...") }`.

**Import alias needed in service.go**: `calrrule "aloqa/internal/pkg/rrule"` (already present in service_test.go, confirming module path).

---

## Task 0 — Pre-flight

- [ ] `git status` — clean working tree
- [ ] `go test -race -count=1 ./...` — green baseline
- [ ] `go vet ./...` — clean
- [ ] Confirm `internal/pkg/rrule/recurrence_split.go` does not exist

---

## Task 1 — `SetUntil`

**Files:** CREATE `internal/pkg/rrule/recurrence_split.go` + `internal/pkg/rrule/recurrence_split_test.go`

**Goal**: Replace or add `UNTIL=` (UTC) in an RRULE string and strip any pre-existing `COUNT`.

### Step 1 — Failing tests

```go
package rrule

import (
    "strings"
    "testing"
    "time"
)

func containsToken(rule, token string) bool {
    for _, p := range strings.Split(rule, ";") {
        if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(p)), token+"=") {
            return true
        }
    }
    return false
}

func TestSetUntil_ReplacesExistingUNTIL(t *testing.T) {
    until := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
    got, err := SetUntil("FREQ=DAILY;UNTIL=20260501T120000Z", until)
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(got, "20260601T120000Z") {
        t.Fatalf("wrong UNTIL value: %q", got)
    }
    if containsToken(got, "COUNT") {
        t.Fatalf("COUNT must be absent: %q", got)
    }
}

func TestSetUntil_AddsUNTILWhenAbsent(t *testing.T) {
    until := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
    got, err := SetUntil("FREQ=DAILY;INTERVAL=1", until)
    if err != nil || !containsToken(got, "UNTIL") {
        t.Fatalf("err=%v got=%q", err, got)
    }
}

func TestSetUntil_StripsCOUNT(t *testing.T) {
    until := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
    got, err := SetUntil("FREQ=DAILY;COUNT=10", until)
    if err != nil {
        t.Fatal(err)
    }
    if containsToken(got, "COUNT") {
        t.Fatalf("COUNT not stripped: %q", got)
    }
    if !containsToken(got, "UNTIL") {
        t.Fatalf("UNTIL missing: %q", got)
    }
}

func TestSetUntil_ErrorOnMalformed(t *testing.T) {
    if _, err := SetUntil("not-a-rule", time.Now()); err == nil {
        t.Fatal("expected error")
    }
}

func TestSetUntil_Idempotent(t *testing.T) {
    until := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
    first, err := SetUntil("FREQ=WEEKLY;BYDAY=MO,WE;COUNT=20", until)
    if err != nil {
        t.Fatal(err)
    }
    second, err := SetUntil(first, until)
    if err != nil {
        t.Fatal(err)
    }
    if first != second {
        t.Fatalf("not idempotent: %q vs %q", first, second)
    }
}
```

### Step 2 — Implementation

`recurrence_split.go`:

```go
package rrule

import (
    "time"

    teambitionrrule "github.com/teambition/rrule-go"
)

// SetUntil replaces or adds UNTIL= (UTC) and strips COUNT (per spec §4.6).
func SetUntil(rule string, until time.Time) (string, error) {
    opt, err := teambitionrrule.StrToROption(rule)
    if err != nil {
        return "", err
    }
    opt.Until = until.UTC()
    opt.Count = 0
    return opt.RRuleString(), nil
}
```

**Commit:** `feat(rrule): SetUntil helper [ALOQA-239]`
**Compile state:** GREEN

---

## Task 2 — `SetDtstart`

**Files:** same

**Goal**: Normalize a rule string for a new DTSTART. DTSTART is stored in `CalendarEvent.ScheduledAt`, not in the rule string; this helper validates parseability and returns the canonical RRULE string without embedding DTSTART.

### Step 1 — Failing tests

```go
func TestSetDtstart_ReturnsParsedRule(t *testing.T) {
    got, err := SetDtstart("FREQ=WEEKLY;BYDAY=MO;COUNT=5", time.Now())
    if err != nil {
        t.Fatal(err)
    }
    if strings.Contains(got, "DTSTART") {
        t.Fatalf("must not embed DTSTART: %q", got)
    }
    if !strings.HasPrefix(got, "FREQ=") {
        t.Fatalf("unexpected: %q", got)
    }
}

func TestSetDtstart_ErrorOnMalformed(t *testing.T) {
    if _, err := SetDtstart("GARBAGE", time.Now()); err == nil {
        t.Fatal("expected error")
    }
}

func TestSetDtstart_StableOnRepeatCalls(t *testing.T) {
    rule := "FREQ=DAILY;INTERVAL=2;UNTIL=20260601T100000Z"
    dt := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
    first, _ := SetDtstart(rule, dt)
    second, _ := SetDtstart(first, dt.AddDate(0, 0, 1))
    if first != second {
        t.Fatalf("%q vs %q", first, second)
    }
}

func TestSetDtstart_PreservesComponents(t *testing.T) {
    rule := "FREQ=MONTHLY;BYDAY=-1MO;COUNT=3"
    got, err := SetDtstart(rule, time.Now())
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(got, "BYDAY=-1MO") || !strings.Contains(got, "COUNT=3") {
        t.Fatalf("components lost: %q", got)
    }
}
```

### Step 2 — Implementation

```go
// SetDtstart validates a rule and returns its canonical RRULE string.
// DTSTART is stored in CalendarEvent.ScheduledAt, never in the rule string.
func SetDtstart(rule string, _ time.Time) (string, error) {
    opt, err := teambitionrrule.StrToROption(rule)
    if err != nil {
        return "", err
    }
    opt.Dtstart = time.Time{}
    return opt.RRuleString(), nil
}
```

**Commit:** `feat(rrule): SetDtstart helper [ALOQA-239]`
**Compile state:** GREEN

---

## Task 3 — `IsMember`

**Files:** same

**Goal**: Return true when `instance` is in the rrule expansion (excluding exdates). Uses a 1ms window around `instance` via the existing `Expand` function.

### Step 1 — Failing tests

```go
func TestIsMember_InSeries(t *testing.T) {
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    instance := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    ok, err := IsMember("FREQ=DAILY;COUNT=5", start, instance, nil)
    if err != nil || !ok {
        t.Fatalf("err=%v ok=%v", err, ok)
    }
}

func TestIsMember_NotInSeries(t *testing.T) {
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    outside := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
    ok, err := IsMember("FREQ=DAILY;COUNT=5", start, outside, nil)
    if err != nil || ok {
        t.Fatalf("err=%v ok=%v", err, ok)
    }
}

func TestIsMember_ExcludedByExdate(t *testing.T) {
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    instance := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
    ok, err := IsMember("FREQ=DAILY;COUNT=5", start, instance, []time.Time{instance})
    if err != nil || ok {
        t.Fatalf("exdated must not be member: err=%v ok=%v", err, ok)
    }
}

func TestIsMember_BoundaryAtUNTIL(t *testing.T) {
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    until := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    rule := "FREQ=DAILY;UNTIL=" + until.UTC().Format("20060102T150405Z")
    ok, err := IsMember(rule, start, until, nil)
    if err != nil || !ok {
        t.Fatalf("UNTIL boundary must be inclusive: err=%v ok=%v", err, ok)
    }
}

func TestIsMember_UnboundedRule(t *testing.T) {
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    far := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
    ok, err := IsMember("FREQ=DAILY", start, far, nil)
    if err != nil || !ok {
        t.Fatalf("unbounded: err=%v ok=%v", err, ok)
    }
}
```

### Step 2 — Implementation

```go
// IsMember returns true when instance is in the expanded series (excluding exdates).
func IsMember(rule string, dtstart, instance time.Time, exdates []time.Time) (bool, error) {
    from := instance.UTC().Add(-time.Millisecond)
    to := instance.UTC().Add(time.Millisecond)
    occurrences, err := Expand(rule, dtstart, exdates, from, to)
    if err != nil {
        return false, err
    }
    for _, occ := range occurrences {
        if occ.UTC().Equal(instance.UTC()) {
            return true, nil
        }
    }
    return false, nil
}
```

**Commit:** `feat(rrule): IsMember helper [ALOQA-239]`
**Compile state:** GREEN

---

## Task 4 — `ShiftBounds` + `BoundsShiftResult`

**Files:** same

**Goal**: Compute child rule for a `this_and_following` split. UNTIL → shift by delta. COUNT → count remaining occurrences from `originalInstance` via `Expand`. Unbounded → return rule unchanged.

### Step 1 — Failing tests

```go
func TestShiftBounds_UNTILBounded(t *testing.T) {
    original := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC) // +1d+1h
    until := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    rule := "FREQ=DAILY;UNTIL=" + until.UTC().Format("20060102T150405Z")

    newRule, result, err := ShiftBounds(rule, original, newInst)
    if err != nil {
        t.Fatal(err)
    }
    if !result.HadUntil || result.HadCount {
        t.Fatalf("flags: %+v", result)
    }
    delta := newInst.Sub(original)
    wantUntil := until.Add(delta).UTC()
    if result.NewUntil == nil || !result.NewUntil.Equal(wantUntil) {
        t.Fatalf("NewUntil=%v want=%v", result.NewUntil, wantUntil)
    }
    if !containsToken(newRule, "UNTIL") || containsToken(newRule, "COUNT") {
        t.Fatalf("newRule: %q", newRule)
    }
}

func TestShiftBounds_COUNTBounded(t *testing.T) {
    // Daily COUNT=5. originalInstance = May 3 (3rd occ). Remaining: May3,4,5 = 3.
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    original := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

    newRule, result, err := ShiftBounds("FREQ=DAILY;COUNT=5", original, newInst)
    _ = start
    if err != nil {
        t.Fatal(err)
    }
    if !result.HadCount || result.HadUntil {
        t.Fatalf("flags: %+v", result)
    }
    if result.NewCount == nil || *result.NewCount != 3 {
        t.Fatalf("NewCount=%v want=3", result.NewCount)
    }
    if !strings.Contains(newRule, "COUNT=3") {
        t.Fatalf("newRule: %q", newRule)
    }
}

func TestShiftBounds_Unbounded(t *testing.T) {
    original := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
    newRule, result, err := ShiftBounds("FREQ=DAILY", original, newInst)
    if err != nil {
        t.Fatal(err)
    }
    if result.HadUntil || result.HadCount {
        t.Fatalf("unbounded must have no bounds: %+v", result)
    }
    if containsToken(newRule, "UNTIL") || containsToken(newRule, "COUNT") {
        t.Fatalf("newRule must stay unbounded: %q", newRule)
    }
}

func TestShiftBounds_ZeroDelta(t *testing.T) {
    original := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    until := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    rule := "FREQ=DAILY;UNTIL=" + until.UTC().Format("20060102T150405Z")
    _, result, err := ShiftBounds(rule, original, original)
    if err != nil {
        t.Fatal(err)
    }
    if result.NewUntil == nil || !result.NewUntil.Equal(until.UTC()) {
        t.Fatalf("zero delta must preserve UNTIL: %v", result.NewUntil)
    }
}

func TestShiftBounds_NegativeDelta(t *testing.T) {
    original := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC) // 1 day earlier
    until := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    rule := "FREQ=DAILY;UNTIL=" + until.UTC().Format("20060102T150405Z")
    _, result, err := ShiftBounds(rule, original, newInst)
    if err != nil {
        t.Fatal(err)
    }
    wantUntil := until.Add(newInst.Sub(original)).UTC()
    if result.NewUntil == nil || !result.NewUntil.Equal(wantUntil) {
        t.Fatalf("NewUntil=%v want=%v", result.NewUntil, wantUntil)
    }
}

func TestShiftBounds_LastOccurrenceCount(t *testing.T) {
    // COUNT=3, split at 3rd (last). Remaining=1.
    original := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    newRule, result, err := ShiftBounds("FREQ=DAILY;COUNT=3", original, newInst)
    if err != nil {
        t.Fatal(err)
    }
    if result.NewCount == nil || *result.NewCount != 1 {
        t.Fatalf("NewCount=%v want=1", result.NewCount)
    }
    if !strings.Contains(newRule, "COUNT=1") {
        t.Fatalf("newRule: %q", newRule)
    }
}

func TestShiftBounds_UNTILWinsOverCOUNT(t *testing.T) {
    // Malformed: both UNTIL and COUNT present. UNTIL must win, COUNT stripped.
    original := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
    until := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    rule := "FREQ=DAILY;COUNT=20;UNTIL=" + until.UTC().Format("20060102T150405Z")
    newRule, result, err := ShiftBounds(rule, original, newInst)
    if err != nil {
        t.Fatal(err)
    }
    if !result.HadUntil {
        t.Fatal("HadUntil must be true")
    }
    if containsToken(newRule, "COUNT") {
        t.Fatalf("COUNT must be stripped: %q", newRule)
    }
}
```

### Step 2 — Implementation

```go
// BoundsShiftResult describes what ShiftBounds changed, for diagnostics and tests.
type BoundsShiftResult struct {
    HadUntil        bool
    HadCount        bool
    NewUntil        *time.Time
    NewCount        *int
    OccurrenceIndex int // 1-based index of originalInstance in parent series (approximate)
}

// ShiftBounds computes the child RRULE for a this_and_following split (spec §4.6).
// Returned newRule is a plain RRULE string; caller sets child.ScheduledAt = newInstance.
func ShiftBounds(rule string, originalInstance, newInstance time.Time) (string, BoundsShiftResult, error) {
    opt, err := teambitionrrule.StrToROption(rule)
    if err != nil {
        return "", BoundsShiftResult{}, err
    }
    result := BoundsShiftResult{
        HadUntil: !opt.Until.IsZero(),
        HadCount: opt.Count != 0,
    }
    delta := newInstance.UTC().Sub(originalInstance.UTC())

    switch {
    case result.HadUntil:
        shifted := opt.Until.UTC().Add(delta)
        result.NewUntil = &shifted
        opt.Until = shifted
        opt.Count = 0

    case result.HadCount:
        far := originalInstance.UTC().AddDate(50, 0, 0)
        remaining, err := Expand(rule, originalInstance.UTC(), nil, originalInstance.UTC().Add(-time.Millisecond), far)
        if err != nil {
            return "", BoundsShiftResult{}, err
        }
        n := len(remaining)
        result.NewCount = &n
        result.OccurrenceIndex = opt.Count - n + 1
        opt.Count = n
        opt.Until = time.Time{}

    default:
        // Unbounded: no changes.
    }

    return opt.RRuleString(), result, nil
}
```

**Commit:** `feat(rrule): ShiftBounds + BoundsShiftResult [ALOQA-239]`
**Compile state:** GREEN

---

## Task 5 — Service types + `scope=all` branch + degenerate non-recurring

**Files:** MODIFY `internal/service/calendar/service.go` + `internal/service/calendar/service_test.go`

**Goal**: Add `MoveOccurrenceScope` constants, `MoveOccurrenceInput`, `MoveOccurrenceResult`, and `MoveEventOccurrence` with `scope=all` path plus non-recurring degenerate.

### Step 1 — Failing tests

Add to `service_test.go` (also add helpers `assertForbidden` and `assertInvalidInput` at the bottom):

```go
func TestMoveEventOccurrence_NonRecurring_All_OK(t *testing.T) {
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    now := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "S", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30},
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
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30},
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
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=10"}},
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

func TestMoveEventOccurrence_NonOrganizer_Forbidden(t *testing.T) {
    ctx := context.Background()
    wsID, orgID, other, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
    now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30},
    }}
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{
        {wsID, orgID}: true, {wsID, other}: true,
    }}, nil, noopPublisher{})
    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, other, MoveOccurrenceInput{
        InstanceAt: now, Scope: MoveScopeAll, NewScheduledAt: now.Add(time.Hour),
    })
    assertForbidden(t, err)
}

func TestMoveEventOccurrence_InvalidScope_BadRequest(t *testing.T) {
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30},
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
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30},
    }}
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
        InstanceAt: now, Scope: MoveScopeAll,
        NewScheduledAt: now.Add(time.Hour), NewDurationMinutes: &bad,
    })
    assertInvalidInput(t, err, "INVALID_DURATION")
}

// helpers — add at bottom of service_test.go
func assertForbidden(t *testing.T, err error) {
    t.Helper()
    ae, ok := cerrors.AsAppError(err)
    if !ok || ae.Code != cerrors.CodeForbidden {
        t.Fatalf("want Forbidden, got %v", err)
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
```

### Step 2 — Implement types + method + scope=all

Add to `service.go` after `UpdateEventInput` (~line 85):

```go
type MoveOccurrenceScope string

const (
    MoveScopeThis             MoveOccurrenceScope = "this"
    MoveScopeThisAndFollowing MoveOccurrenceScope = "this_and_following"
    MoveScopeAll              MoveOccurrenceScope = "all"
)

type MoveOccurrenceInput struct {
    InstanceAt         time.Time
    Scope              MoveOccurrenceScope
    NewScheduledAt     time.Time
    NewDurationMinutes *int
}

type MoveOccurrenceResult struct {
    Updated *entity.CalendarEvent
    Created *entity.CalendarEvent
}
```

Add method (below `DeleteEvent`):

```go
func (s *Service) MoveEventOccurrence(
    ctx context.Context,
    workspaceID, eventID, actorID uuid.UUID,
    input MoveOccurrenceInput,
) (*MoveOccurrenceResult, error) {
    if err := s.requireWorkspaceMember(ctx, workspaceID, actorID); err != nil {
        return nil, err
    }
    switch input.Scope {
    case MoveScopeThis, MoveScopeThisAndFollowing, MoveScopeAll:
    default:
        return nil, cerrors.InvalidInput("INVALID_SCOPE: must be this, this_and_following, or all")
    }
    if input.NewDurationMinutes != nil && (*input.NewDurationMinutes < 0 || *input.NewDurationMinutes > 24*60) {
        return nil, cerrors.InvalidInput("INVALID_DURATION: duration_minutes must be 0–1440")
    }
    existing, err := s.calendars.GetEvent(ctx, eventID)
    if err != nil {
        return nil, err
    }
    if existing.WorkspaceID != workspaceID {
        return nil, cerrors.NotFound("event not found")
    }
    if existing.OrganizerID != actorID {
        return nil, cerrors.Forbidden("FORBIDDEN_NOT_ORGANIZER: only the organizer can move occurrences")
    }
    if existing.Recurrence == nil {
        return s.moveOccurrenceAll(ctx, existing, input)
    }
    switch input.Scope {
    case MoveScopeAll:
        return s.moveOccurrenceAll(ctx, existing, input)
    case MoveScopeThis:
        return s.moveOccurrenceThis(ctx, existing, input)
    default:
        return s.moveOccurrenceThisAndFollowing(ctx, existing, input)
    }
}

func deriveDuration(existing *entity.CalendarEvent, input MoveOccurrenceInput) int {
    if input.NewDurationMinutes != nil {
        return *input.NewDurationMinutes
    }
    return existing.DurationMinutes
}

func (s *Service) moveOccurrenceAll(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
    existing.ScheduledAt = input.NewScheduledAt.UTC()
    existing.DurationMinutes = deriveDuration(existing, input)
    existing.UpdatedAt = time.Now().UTC()
    updated, err := s.calendars.UpdateEvent(ctx, existing)
    if err != nil {
        return nil, err
    }
    s.publishEvent(ctx, event.TypeCalendarEventUpdated, *updated, existing.OrganizerID)
    return &MoveOccurrenceResult{Updated: updated}, nil
}
```

Stub the other two methods returning `cerrors.Unavailable("not yet implemented")` to keep compile green. They will be replaced in Tasks 6 and 7.

**Commit:** `feat(calendar): MoveEventOccurrence types + scope=all [ALOQA-239]`
**Compile state:** GREEN (Tasks 6–7 tests not yet written; stubs keep compiler happy)

---

## Task 6 — `scope=this` branch

**Files:** same

**Goal**: Validate `instance_at` is in series (not exdated), atomically append exdate to parent, INSERT new single-occurrence child event. Attendees regenerated with new IDs and `rsvp_status=no_response`. Reminders verbatim (per spec §4.5.this).

### Step 1 — Failing tests

```go
func TestMoveEventOccurrence_Recurring_This_OK(t *testing.T) {
    ctx := context.Background()
    wsID, orgID := uuid.New(), uuid.New()
    eventID := uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    instance := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    attendeeUID := uuid.New()
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
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

func TestMoveEventOccurrence_InstanceNotInSeries_BadRequest(t *testing.T) {
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"}},
    }}
    txMgr := newCalendarReminderTxManager(repo)
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    svc.SetTransactionManager(txMgr)

    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
        InstanceAt: time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC), // past COUNT=3
        Scope: MoveScopeThis,
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
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5", Exdates: []time.Time{exdated}}},
    }}
    txMgr := newCalendarReminderTxManager(repo)
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    svc.SetTransactionManager(txMgr)

    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
        InstanceAt: exdated, Scope: MoveScopeThis,
        NewScheduledAt: exdated.Add(24 * time.Hour),
    })
    assertInvalidInput(t, err, "INVALID_INSTANCE")
}
```

### Step 2 — Implement (replace stub)

Add to `service.go`:

```go
func cloneEvent(src *entity.CalendarEvent) *entity.CalendarEvent {
    c := *src
    c.Attendees = append([]entity.EventAttendee(nil), src.Attendees...)
    c.Reminders = append([]entity.EventReminder(nil), src.Reminders...)
    return &c
}

func resetAttendees(src []entity.EventAttendee, newEventID uuid.UUID) []entity.EventAttendee {
    out := make([]entity.EventAttendee, len(src))
    for i, a := range src {
        uid := a.UserID
        out[i] = entity.EventAttendee{
            ID:         id.New(),
            EventID:    newEventID,
            UserID:     uid,
            Email:      a.Email,
            IsRequired: a.IsRequired,
            RsvpStatus: entity.RsvpStatusNoResponse,
        }
    }
    return out
}

func copyReminders(src []entity.EventReminder, newEventID uuid.UUID) []entity.EventReminder {
    out := make([]entity.EventReminder, len(src))
    for i, r := range src {
        out[i] = entity.EventReminder{
            ID:            id.New(),
            EventID:       newEventID,
            UserID:        r.UserID,
            OffsetMinutes: r.OffsetMinutes,
            Channel:       r.Channel,
        }
    }
    return out
}

func buildOccurrenceChild(parent *entity.CalendarEvent, scheduledAt time.Time, durationMinutes int) *entity.CalendarEvent {
    now := time.Now().UTC()
    child := &entity.CalendarEvent{
        ID:              id.New(),
        CalendarID:      parent.CalendarID,
        WorkspaceID:     parent.WorkspaceID,
        ChannelID:       parent.ChannelID,
        OrganizerID:     parent.OrganizerID,
        Title:           parent.Title,
        Description:     parent.Description,
        Location:        parent.Location,
        ScheduledAt:     scheduledAt.UTC(),
        OriginatorTZ:    parent.OriginatorTZ,
        DurationMinutes: durationMinutes,
        AllDay:          parent.AllDay,
        Recurrence:      nil,
        CallID:          nil,
        CreatedAt:       now,
        UpdatedAt:       now,
    }
    child.Attendees = resetAttendees(parent.Attendees, child.ID)
    child.Reminders = copyReminders(parent.Reminders, child.ID)
    return child
}

func (s *Service) moveOccurrenceThis(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
    if s.tx == nil {
        return nil, cerrors.Unavailable("transaction manager required for scope=this")
    }
    loc, _ := time.LoadLocation(existing.OriginatorTZ)
    if loc == nil {
        loc = time.UTC
    }
    ok, err := calrrule.IsMember(existing.Recurrence.RRule, existing.ScheduledAt.In(loc), input.InstanceAt.UTC(), existing.Recurrence.Exdates)
    if err != nil {
        return nil, err
    }
    if !ok {
        return nil, cerrors.InvalidInput("INVALID_INSTANCE: instance_at is not a member of the series")
    }

    var parentAfter, created *entity.CalendarEvent
    if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
        if scope.Calendars() == nil {
            return cerrors.Unavailable("calendar transaction scope not configured")
        }
        parent := cloneEvent(existing)
        parent.Recurrence = &entity.RecurrenceRule{
            RRule:   existing.Recurrence.RRule,
            Exdates: append(append([]time.Time(nil), existing.Recurrence.Exdates...), input.InstanceAt.UTC()),
        }
        parent.UpdatedAt = time.Now().UTC()
        updated, err := scope.Calendars().UpdateEvent(ctx, parent)
        if err != nil {
            return err
        }
        parentAfter = updated
        child := buildOccurrenceChild(existing, input.NewScheduledAt, deriveDuration(existing, input))
        inserted, err := scope.Calendars().CreateEvent(ctx, child)
        if err != nil {
            return err
        }
        created = inserted
        return nil
    }); err != nil {
        return nil, err
    }
    s.publishEvent(ctx, event.TypeCalendarEventUpdated, *parentAfter, existing.OrganizerID)
    s.publishEvent(ctx, event.TypeCalendarEventCreated, *created, existing.OrganizerID)
    return &MoveOccurrenceResult{Updated: parentAfter, Created: created}, nil
}
```

**Commit:** `feat(calendar): MoveEventOccurrence scope=this [ALOQA-239]`
**Compile state:** GREEN

---

## Task 7 — `scope=this_and_following` branch

**Files:** same

**Goal**: Clamp parent RRULE `UNTIL = instanceAt - 1ms`; partition exdates; build child with `ShiftBounds` rrule + shifted future exdates. First-occurrence degenerates to `scope=all`.

### Step 1 — Failing tests

```go
func TestMoveEventOccurrence_Recurring_ThisAndFollowing_OK(t *testing.T) {
    ctx := context.Background()
    wsID, orgID := uuid.New(), uuid.New()
    eventID := uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    instance := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
    futureEx := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
    delta := 2 * 24 * time.Hour
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=7", Exdates: []time.Time{futureEx}}},
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
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"}},
    }}
    // No tx manager needed: first-occurrence path calls moveOccurrenceAll directly.
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
    last := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC) // 3rd and last in COUNT=3
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"}},
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
```

### Step 2 — Implement (replace stub)

```go
func (s *Service) moveOccurrenceThisAndFollowing(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
    // Degenerate: first occurrence → scope=all (spec §4.5.this_and_following).
    if input.InstanceAt.UTC().Equal(existing.ScheduledAt.UTC()) {
        return s.moveOccurrenceAll(ctx, existing, input)
    }
    if s.tx == nil {
        return nil, cerrors.Unavailable("transaction manager required for scope=this_and_following")
    }
    loc, _ := time.LoadLocation(existing.OriginatorTZ)
    if loc == nil {
        loc = time.UTC
    }
    ok, err := calrrule.IsMember(existing.Recurrence.RRule, existing.ScheduledAt.In(loc), input.InstanceAt.UTC(), existing.Recurrence.Exdates)
    if err != nil {
        return nil, err
    }
    if !ok {
        return nil, cerrors.InvalidInput("INVALID_INSTANCE: instance_at is not a member of the series")
    }

    delta := input.NewScheduledAt.UTC().Sub(input.InstanceAt.UTC())
    parentUntil := input.InstanceAt.UTC().Add(-time.Millisecond)

    var parentExdates, childExdates []time.Time
    for _, ex := range existing.Recurrence.Exdates {
        switch {
        case ex.UTC().Before(input.InstanceAt.UTC()):
            parentExdates = append(parentExdates, ex)
        case ex.UTC().After(input.InstanceAt.UTC()):
            childExdates = append(childExdates, ex.UTC().Add(delta))
        }
    }

    childRule, _, err := calrrule.ShiftBounds(existing.Recurrence.RRule, input.InstanceAt.UTC(), input.NewScheduledAt.UTC())
    if err != nil {
        return nil, err
    }

    var parentAfter, created *entity.CalendarEvent
    if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
        if scope.Calendars() == nil {
            return cerrors.Unavailable("calendar transaction scope not configured")
        }
        parentRRule, err := calrrule.SetUntil(existing.Recurrence.RRule, parentUntil)
        if err != nil {
            return err
        }
        parent := cloneEvent(existing)
        parent.Recurrence = &entity.RecurrenceRule{RRule: parentRRule, Exdates: parentExdates}
        parent.UpdatedAt = time.Now().UTC()
        updated, err := scope.Calendars().UpdateEvent(ctx, parent)
        if err != nil {
            return err
        }
        parentAfter = updated
        child := buildOccurrenceChild(existing, input.NewScheduledAt, deriveDuration(existing, input))
        child.Recurrence = &entity.RecurrenceRule{RRule: childRule, Exdates: childExdates}
        inserted, err := scope.Calendars().CreateEvent(ctx, child)
        if err != nil {
            return err
        }
        created = inserted
        return nil
    }); err != nil {
        return nil, err
    }
    s.publishEvent(ctx, event.TypeCalendarEventUpdated, *parentAfter, existing.OrganizerID)
    s.publishEvent(ctx, event.TypeCalendarEventCreated, *created, existing.OrganizerID)
    return &MoveOccurrenceResult{Updated: parentAfter, Created: created}, nil
}
```

**Commit:** `feat(calendar): MoveEventOccurrence scope=this_and_following [ALOQA-239]`
**Compile state:** GREEN

---

## Task 8 — HTTP handler

**Files:** MODIFY `internal/handler/http/calendar.go`, CREATE `internal/handler/http/calendar_test.go`

**Goal**: Decode `instance_at`, `scope`, `new_scheduled_at`, `new_duration_minutes`; call `MoveEventOccurrence`; return `{ updated, created }`.

### Step 1 — Failing tests

`internal/handler/http/calendar_test.go`:

```go
package http

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/google/uuid"

    "aloqa/internal/domain/entity"
    "aloqa/internal/middleware"
    "aloqa/internal/pkg/cerrors"
    calendarservice "aloqa/internal/service/calendar"
)

// fakeCalendarService wraps a fakeCalendarRepo for handler tests.
type calendarHTTPFixture struct {
    wsID   uuid.UUID
    userID uuid.UUID
    repo   *fakeMoveCalendarRepo
    router *chi.Mux
}

type fakeMoveCalendarRepo struct {
    events map[uuid.UUID]*entity.CalendarEvent
}

func newCalendarHTTPFixture() calendarHTTPFixture {
    wsID := uuid.New()
    userID := uuid.New()
    repo := &fakeMoveCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{}}
    svc := calendarservice.NewService(repo, fakeCalHTTPMembers{wsID: wsID, userID: userID}, nil, noopCalHTTPPublisher{})
    handler := NewCalendarHandler(svc)
    router := chi.NewRouter()
    router.Use(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx := context.WithValue(r.Context(), middleware.WorkspaceIDKey, wsID)
            ctx = context.WithValue(ctx, middleware.UserIDKey, userID)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    })
    router.Post("/events/{eventID}/occurrences/move", handler.MoveOccurrence)
    return calendarHTTPFixture{wsID: wsID, userID: userID, repo: repo, router: router}
}

func (f calendarHTTPFixture) serve(eventID uuid.UUID, body string) *httptest.ResponseRecorder {
    req := httptest.NewRequest(http.MethodPost, "/events/"+eventID.String()+"/occurrences/move", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    res := httptest.NewRecorder()
    f.router.ServeHTTP(res, req)
    return res
}

func TestMoveOccurrenceHandler_200OK(t *testing.T) {
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: now, DurationMinutes: 30,
    }
    body := `{"instance_at":"2026-05-03T10:00:00Z","scope":"all","new_scheduled_at":"2026-05-05T10:00:00Z"}`
    res := f.serve(eventID, body)
    if res.Code != http.StatusOK {
        t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
    }
    var resp moveOccurrenceResponse
    if err := json.Unmarshal(res.Body.Bytes(), &resp); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if resp.Updated == nil {
        t.Fatal("updated must not be nil")
    }
}

func TestMoveOccurrenceHandler_400InvalidScope(t *testing.T) {
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: now, DurationMinutes: 30,
    }
    body := `{"instance_at":"2026-05-03T10:00:00Z","scope":"bad","new_scheduled_at":"2026-05-05T10:00:00Z"}`
    res := f.serve(eventID, body)
    if res.Code != http.StatusBadRequest {
        t.Fatalf("status=%d", res.Code)
    }
}

func TestMoveOccurrenceHandler_400MissingFields(t *testing.T) {
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: time.Now(), DurationMinutes: 30,
    }
    res := f.serve(eventID, `{}`)
    if res.Code != http.StatusBadRequest {
        t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
    }
}

func TestMoveOccurrenceHandler_403NonOrganizer(t *testing.T) {
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    otherOrg := uuid.New()
    now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: otherOrg,
        Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: now, DurationMinutes: 30,
    }
    body := `{"instance_at":"2026-05-03T10:00:00Z","scope":"all","new_scheduled_at":"2026-05-05T10:00:00Z"}`
    res := f.serve(eventID, body)
    if res.Code != http.StatusForbidden {
        t.Fatalf("status=%d", res.Code)
    }
}
```

At the bottom of `calendar_test.go`, add minimal fakes that satisfy `calendarservice.NewService` interfaces (they can delegate to the `fakeCalendarRepo` pattern from `service_test.go` — copy the necessary method stubs). Also add:

```go
// fakeMoveCalendarRepo satisfies repository.CalendarRepository.
// Embed fakeCalendarRepo and override as needed.
// Add all required methods delegating to an internal fakeCalendarRepo.
```

The full fake implementation follows the pattern of `fakeCalendarRepo` in `service_test.go` — delegate every required interface method; only `GetEvent`, `UpdateEvent`, `CreateEvent` need real behaviour.

### Step 2 — Implement handler

In `calendar.go`:

```go
type moveOccurrenceRequest struct {
    InstanceAt         time.Time                           `json:"instance_at"`
    Scope              calendarservice.MoveOccurrenceScope `json:"scope"`
    NewScheduledAt     time.Time                           `json:"new_scheduled_at"`
    NewDurationMinutes *int                                `json:"new_duration_minutes"`
}

type moveOccurrenceResponse struct {
    Updated *entity.CalendarEvent `json:"updated"`
    Created *entity.CalendarEvent `json:"created"`
}

func (h *CalendarHandler) MoveOccurrence(w http.ResponseWriter, r *http.Request) {
    eventID, err := id.Parse(chi.URLParam(r, "eventID"))
    if err != nil {
        writeErr(w, err)
        return
    }
    var req moveOccurrenceRequest
    if err := decodeJSON(r, &req); err != nil {
        writeErr(w, err)
        return
    }
    if req.InstanceAt.IsZero() {
        writeErr(w, cerrors.InvalidInput("instance_at is required"))
        return
    }
    if req.NewScheduledAt.IsZero() {
        writeErr(w, cerrors.InvalidInput("new_scheduled_at is required"))
        return
    }
    wsID := middleware.WorkspaceIDFromContext(r.Context())
    userID := middleware.UserIDFromContext(r.Context())
    result, err := h.svc.MoveEventOccurrence(r.Context(), wsID, eventID, userID, calendarservice.MoveOccurrenceInput{
        InstanceAt:         req.InstanceAt.UTC(),
        Scope:              req.Scope,
        NewScheduledAt:     req.NewScheduledAt.UTC(),
        NewDurationMinutes: req.NewDurationMinutes,
    })
    if err != nil {
        writeErr(w, err)
        return
    }
    writeOK(w, moveOccurrenceResponse{Updated: result.Updated, Created: result.Created})
}
```

**Commit:** `feat(calendar): MoveOccurrence HTTP handler [ALOQA-239]`
**Compile state:** GREEN

---

## Task 9 — Router wiring

**Files:** MODIFY `internal/handler/http/router.go`

**Goal**: Register the route inside the `r.Route("/{eventID}", ...)` block at line 358.

### Step 1 — Smoke test

In `calendar_test.go`, add:

```go
func TestMoveOccurrenceRouteRegistered(t *testing.T) {
    router := NewRouter(RouterDeps{Calendar: &CalendarHandler{}})
    req := httptest.NewRequest(http.MethodPost,
        "/api/v1/workspaces/"+uuid.NewString()+"/events/"+uuid.NewString()+"/occurrences/move",
        strings.NewReader(`{}`))
    req.Header.Set("Content-Type", "application/json")
    res := httptest.NewRecorder()
    router.ServeHTTP(res, req)
    if res.Code == http.StatusNotFound {
        t.Fatal("route not registered: 404")
    }
}
```

### Step 2 — Wire route

In `internal/handler/http/router.go`, inside `r.Route("/{eventID}", func(r chi.Router) { ... })` block (after `r.Post("/start-call", ...)`):

```go
r.Post("/occurrences/move", deps.Calendar.MoveOccurrence)
```

**Commit:** `feat(calendar): wire MoveOccurrence route [ALOQA-239]`
**Compile state:** GREEN

---

## Task 10 — Realtime assertion test

**Files:** MODIFY `internal/service/calendar/service_test.go`

**Goal**: Verify `TypeCalendarEventUpdated` published for parent and `TypeCalendarEventCreated` for new event (scope=this); only `TypeCalendarEventUpdated` for scope=all.

```go
func TestMoveEventOccurrence_RealtimePublishedCorrectly(t *testing.T) {
    ctx := context.Background()
    wsID, orgID := uuid.New(), uuid.New()
    eventID := uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    instance := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"}},
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

    // scope=all: only Updated.
    repo2 := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30},
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
```

**Commit:** `test(calendar): realtime assertions for MoveEventOccurrence [ALOQA-239]`
**Compile state:** GREEN

---

## Task 11 — Final verification

```bash
go test -race -count=1 ./...
go vet ./...
make lint
go mod tidy   # must produce no diff
go build ./cmd/server
```

Fix any findings (lint, vet, formatting). If changes required:

**Commit:** `chore(calendar): verify gate fixes [ALOQA-239]`

---

## Summary Table

| Task | Files | State after commit |
|---|---|---|
| 0 | — | GREEN baseline |
| 1 `SetUntil` | `recurrence_split.go` + test | GREEN |
| 2 `SetDtstart` | same | GREEN |
| 3 `IsMember` | same | GREEN |
| 4 `ShiftBounds` | same | GREEN |
| 5 types + scope=all | `service.go` + test | GREEN |
| 6 scope=this | `service.go` + test | GREEN |
| 7 scope=this_and_following | `service.go` + test | GREEN |
| 8 handler | `calendar.go` + `calendar_test.go` | GREEN |
| 9 router | `router.go` | GREEN |
| 10 realtime | `service_test.go` | GREEN |
| 11 verify | — | GREEN |

---

## Test Plan Checklist

**rrule helpers** (21 tests):
- [ ] SetUntil: replaces existing UNTIL
- [ ] SetUntil: adds UNTIL when absent
- [ ] SetUntil: strips COUNT
- [ ] SetUntil: error on malformed
- [ ] SetUntil: idempotent
- [ ] SetDtstart: returns parsed rule without DTSTART
- [ ] SetDtstart: error on malformed
- [ ] SetDtstart: stable on repeat calls
- [ ] SetDtstart: preserves all components
- [ ] IsMember: in series
- [ ] IsMember: not in series
- [ ] IsMember: excluded by exdate
- [ ] IsMember: boundary at UNTIL (inclusive)
- [ ] IsMember: unbounded rule
- [ ] ShiftBounds: UNTIL-bounded shifts by delta
- [ ] ShiftBounds: COUNT-bounded counts remaining
- [ ] ShiftBounds: unbounded unchanged
- [ ] ShiftBounds: zero delta idempotent
- [ ] ShiftBounds: negative delta
- [ ] ShiftBounds: last occurrence COUNT=1
- [ ] ShiftBounds: UNTIL wins over COUNT

**Service** (13 tests):
- [ ] NonRecurring_All_OK (duration preserved)
- [ ] NonRecurring_This_DegeneratesToAll
- [ ] Recurring_All_OK (recurrence preserved)
- [ ] Recurring_This_OK (exdate + child + RSVP reset + new IDs)
- [ ] InstanceNotInSeries_BadRequest
- [ ] InstanceAlreadyExdated_BadRequest
- [ ] Recurring_ThisAndFollowing_OK (UNTIL + shifted rrule + shifted exdates)
- [ ] ThisAndFollowing_FirstOccurrence_DegeneratesToAll
- [ ] ThisAndFollowing_LastOccurrence_OK (child COUNT=1)
- [ ] NonOrganizer_Forbidden
- [ ] InvalidScope_BadRequest
- [ ] DurationOutOfRange_BadRequest
- [ ] RealtimePublishedCorrectly

**HTTP** (5 tests):
- [ ] 200 OK response shape
- [ ] 400 invalid scope
- [ ] 400 missing required fields
- [ ] 403 non-organizer
- [ ] Route registered (smoke)

---

## Self-Review Checklist

- [ ] All new helpers in `internal/pkg/rrule/recurrence_split.go`; `expand.go` untouched
- [ ] `cerrors.InvalidInput` for all 400s; no invented constructor
- [ ] `cerrors.Forbidden` for non-organizer
- [ ] `s.tx.WithinTx` for scope=this and scope=this_and_following; `if s.tx == nil` guard
- [ ] scope=this first-occurrence NOT degenerated (parent gets exdate + child created)
- [ ] scope=this_and_following first-occurrence degenerates to scope=all
- [ ] `buildOccurrenceChild`: `call_id=nil`, `recurrence=nil`, fresh timestamps
- [ ] `resetAttendees`: new IDs, `rsvp_status=no_response`, `responded_at=nil`
- [ ] `copyReminders`: rebinds `EventID`; does NOT call `buildReminderRows`
- [ ] Exdate partition: `< instanceAt` to parent, `> instanceAt` shifted by delta to child, `== instanceAt` discarded
- [ ] `ShiftBounds` UNTIL takes priority when both UNTIL and COUNT present
- [ ] `IsMember` uses 1ms window (not full-series expand)
- [ ] No migration; no new go.mod dependencies
- [ ] Handler validates `instance_at` and `new_scheduled_at` non-zero before calling service
- [ ] Route wired inside `r.Route("/{eventID}", ...)` block, not at events-list level
- [ ] `go mod tidy` no diff

---

## Spec Gaps

1. **`SetDtstart` semantics**: Spec §4.6 says "replaces or adds DTSTART= and rebuilds the rrule string." In this codebase DTSTART never lives in the rule string (stored in `ScheduledAt`). `SetDtstart` therefore normalizes without embedding DTSTART — intentional divergence consistent with how `expand.go` operates.

2. **`IsMember` 1ms window vs full expand**: A 1ms precision window may produce false-negatives for occurrences whose computed time differs by sub-millisecond due to DST arithmetic edge cases. The window is sufficient for the UTC timestamps this codebase stores; a full-series expand fallback is not needed.

3. **`OccurrenceIndex` for COUNT series**: Approximated via `opt.Count - len(remaining) + 1`. Used only in diagnostics/logs; callers do not depend on it for correctness.

4. **`notify` flag** (mentioned in spec §4.5 prose): Not present in the §4.2 request schema. Out of scope this iteration.
