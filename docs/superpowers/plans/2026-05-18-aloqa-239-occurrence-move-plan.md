# ALOQA-239 — Backend Occurrence Move: Implementation Plan

> Branch: `feature/ALOQA-239-occurrence-move` off `origin/develop @ 2d6f955`
> Spec: `docs/superpowers/specs/2026-05-15-calendar-design-parity-design.md §4` (lines 73–272)
> Budget: Tasks 0–11 + Task 5a, one commit per task.

## Architecture

New route: `POST /workspaces/{wsID}/events/{eventID}/occurrences/move`

| Layer | Action |
|---|---|
| `internal/pkg/rrule/recurrence_split.go` | NEW — 4 helpers: SetUntil, NormalizeRule, IsMember, ShiftBounds |
| `internal/pkg/rrule/recurrence_split_test.go` | NEW — 22 tests |
| `internal/domain/repository/interfaces.go` | MODIFIED — add CreateEventTx, UpdateEventTx to CalendarRepository |
| `internal/repository/postgres/calendar_repo.go` | MODIFIED — implement CreateEventTx, UpdateEventTx, updateEventRowTx |
| `internal/service/calendar/service.go` | MODIFIED — types + MoveEventOccurrence + 3 branch methods |
| `internal/service/calendar/service_test.go` | MODIFIED — 18 new tests |
| `internal/handler/http/calendar.go` | MODIFIED — MoveOccurrence handler |
| `internal/handler/http/calendar_test.go` | NEW — 7 handler tests |
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
- `CONCURRENT_UPDATE` → `cerrors.Conflict("CONCURRENT_UPDATE: ...")` (HTTP 409)

**Transaction pattern**: `s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error { ... })` using `scope.Calendars()`, mirroring `ListAndDispatchReminders` (service.go:527). For scope=this and scope=this_and_following: guard `if s.tx == nil { return nil, cerrors.Unavailable("...") }` placed **after** the first-occurrence shortcut in `moveOccurrenceThisAndFollowing`.

**`NormalizeRule`** (renamed from `SetDtstart`): validates an RRULE string and returns its canonical form. The `time.Time` parameter is dropped — DTSTART is always derived from `CalendarEvent.ScheduledAt`. All service code and tests use `NormalizeRule`.

**`ShiftBounds` signature**: `ShiftBounds(rule string, parentDtstart, originalInstance, newInstance time.Time)` — the `parentDtstart` parameter is the event's `ScheduledAt.UTC()` (the series root). It is passed to `Expand` in the COUNT case to correctly count remaining occurrences from the original series root, not from `originalInstance` (which would restart the COUNT from a wrong dtstart).

**Tx-aware repo methods** (`CreateEventTx`, `UpdateEventTx`): added to `CalendarRepository` interface and implemented on `*CalendarRepo` using `r.db` directly — no inner `pool.BeginTx`. This ensures atomicity inside `txscope.Manager.WithinTx`. The existing `CreateEvent`/`UpdateEvent` open their own inner transactions and must NOT be called from within `WithinTx`.

**Optimistic concurrency**: `MoveOccurrenceInput.ExpectedUpdatedAt *time.Time` passed through to `UpdateEventTx`. When non-nil, the UPDATE adds `AND updated_at = $16` to WHERE; if 0 rows affected, returns `cerrors.Conflict("CONCURRENT_UPDATE: ...")` (HTTP 409). `moveOccurrenceAll` also forwards `ExpectedUpdatedAt` to `UpdateEventTx` — scope=all is not exempt from optimistic locking (R2/M2).

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

## Task 2 — `NormalizeRule`

**Files:** same

**Goal**: Normalize a rule string. Validates parseability and returns the canonical RRULE string. DTSTART is stored in `CalendarEvent.ScheduledAt`, never in the rule string; this helper does not embed DTSTART.

### Step 1 — Failing tests

```go
func TestNormalizeRule_ReturnsParsedRule(t *testing.T) {
    got, err := NormalizeRule("FREQ=WEEKLY;BYDAY=MO;COUNT=5")
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

func TestNormalizeRule_ErrorOnMalformed(t *testing.T) {
    if _, err := NormalizeRule("GARBAGE"); err == nil {
        t.Fatal("expected error")
    }
}

func TestNormalizeRule_StableOnRepeatCalls(t *testing.T) {
    rule := "FREQ=DAILY;INTERVAL=2;UNTIL=20260601T100000Z"
    first, _ := NormalizeRule(rule)
    second, _ := NormalizeRule(first)
    if first != second {
        t.Fatalf("%q vs %q", first, second)
    }
}

func TestNormalizeRule_PreservesComponents(t *testing.T) {
    rule := "FREQ=MONTHLY;BYDAY=-1MO;COUNT=3"
    got, err := NormalizeRule(rule)
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
// NormalizeRule validates a rule and returns its canonical RRULE string.
// DTSTART is stored in CalendarEvent.ScheduledAt, never in the rule string.
func NormalizeRule(rule string) (string, error) {
    opt, err := teambitionrrule.StrToROption(rule)
    if err != nil {
        return "", err
    }
    opt.Dtstart = time.Time{}
    return opt.RRuleString(), nil
}
```

**Commit:** `feat(rrule): NormalizeRule helper [ALOQA-239]`
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

**Goal**: Compute child rule for a `this_and_following` split. UNTIL → shift by delta. COUNT → count remaining occurrences from `originalInstance` (using `parentDtstart` as series root) via `Expand`. Unbounded → return rule unchanged.

### Step 1 — Failing tests

Note: `ShiftBounds` now takes `parentDtstart` as the second positional parameter (the event's `ScheduledAt`). In UNTIL-bounded and unbounded tests, `parentDtstart` is unused by the logic but must still be passed. Those tests can pass `original` as a convenient placeholder.

```go
func TestShiftBounds_UNTILBounded(t *testing.T) {
    original := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC) // +1d+1h
    until := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    rule := "FREQ=DAILY;UNTIL=" + until.UTC().Format("20060102T150405Z")

    newRule, result, err := ShiftBounds(rule, original, original, newInst)
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
    // Daily COUNT=5 starting May 1. originalInstance = May 3 (3rd occ).
    // Remaining from May 3 inclusive: May3, May4, May5 = 3.
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    original := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

    newRule, result, err := ShiftBounds("FREQ=DAILY;COUNT=5", start, original, newInst)
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
    newRule, result, err := ShiftBounds("FREQ=DAILY", original, original, newInst)
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
    _, result, err := ShiftBounds(rule, original, original, original)
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
    _, result, err := ShiftBounds(rule, original, original, newInst)
    if err != nil {
        t.Fatal(err)
    }
    wantUntil := until.Add(newInst.Sub(original)).UTC()
    if result.NewUntil == nil || !result.NewUntil.Equal(wantUntil) {
        t.Fatalf("NewUntil=%v want=%v", result.NewUntil, wantUntil)
    }
}

func TestShiftBounds_LastOccurrenceCount(t *testing.T) {
    // COUNT=3 starting May 1; split at 3rd (last) occurrence May 3. Remaining=1.
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    original := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    newInst := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
    newRule, result, err := ShiftBounds("FREQ=DAILY;COUNT=3", start, original, newInst)
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
    newRule, result, err := ShiftBounds(rule, original, original, newInst)
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
// parentDtstart is the event's ScheduledAt (series root); it is used as dtstart when
// expanding the COUNT series to correctly count remaining occurrences from originalInstance.
// Returned newRule is a plain RRULE string; caller sets child.ScheduledAt = newInstance.
func ShiftBounds(rule string, parentDtstart, originalInstance, newInstance time.Time) (string, BoundsShiftResult, error) {
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
        // Use parentDtstart (not originalInstance) so Expand walks the series from
        // its root and COUNT is consumed correctly before the from window.
        remaining, err := Expand(rule, parentDtstart.UTC(), nil, originalInstance.UTC().Add(-time.Millisecond), far)
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

**Goal**: Add `MoveOccurrenceScope` constants, `MoveOccurrenceInput`, `MoveOccurrenceResult`, and `MoveEventOccurrence` with `scope=all` path plus non-recurring degenerate. `moveOccurrenceAll` uses `UpdateEventTx` and forwards `ExpectedUpdatedAt` so that scope=all participates in optimistic locking (R2/M2).

### Step 1 — Failing tests

Add to `service_test.go` (also add helpers `assertForbidden`, `assertInvalidInput`, and `assertConflict` at the bottom):

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

func TestMoveEventOccurrence_ScopeAll_OptimisticLock_Conflict(t *testing.T) {
    // R2/M2: scope=all must also honour ExpectedUpdatedAt.
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    storedUpdatedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
    staleExpected := storedUpdatedAt.Add(-time.Second)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: storedUpdatedAt, DurationMinutes: 30, UpdatedAt: storedUpdatedAt},
    }}
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
        InstanceAt:        storedUpdatedAt,
        Scope:             MoveScopeAll,
        NewScheduledAt:    storedUpdatedAt.Add(time.Hour),
        ExpectedUpdatedAt: &staleExpected,
    })
    assertConflict(t, err, "CONCURRENT_UPDATE")
}

// R4/M2 fix: explicit named tests for the recurring scope=all path with
// matching and stale ExpectedUpdatedAt tokens. These complement the
// non-recurring ScopeAll_OptimisticLock_Conflict test above and exercise
// the same WithinTx + UpdateEventTx code path on a recurring event so we
// also confirm recurrence preservation under the optimistic-lock branch.
func TestMoveEventOccurrence_Recurring_All_WithMatchingToken(t *testing.T) {
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    storedUpdatedAt := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
    matchExpected := storedUpdatedAt // exact match
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=10"}},
    }}
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    // R4/M2: wire WithinTx so the non-nil token routes through tx scope.
    // newCalendarReminderTxManager is defined in Task 6 — it satisfies
    // txscope.Manager and exposes scope.Calendars() returning the same repo.
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
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "Daily", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=10"}},
    }}
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    svc.SetTransactionManager(&fakeTxManager{calendars: repo})
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
    assertForbidden(t, err, "FORBIDDEN_NOT_ORGANIZER")
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
    ExpectedUpdatedAt  *time.Time
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

// moveOccurrenceAll updates the event's scheduled_at (and optionally duration) in place.
// When ExpectedUpdatedAt is nil, calls the non-tx UpdateEvent (best-effort write).
// When non-nil, wraps in WithinTx + UpdateEventTx so the optimistic lock is honoured
// inside a single transaction and a 409 surfaces cleanly (R2/M2 v2).
func (s *Service) moveOccurrenceAll(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
    existing.ScheduledAt = input.NewScheduledAt.UTC()
    existing.DurationMinutes = deriveDuration(existing, input)
    existing.UpdatedAt = time.Now().UTC()

    var updated *entity.CalendarEvent
    if input.ExpectedUpdatedAt == nil {
        u, err := s.calendars.UpdateEvent(ctx, existing)
        if err != nil {
            return nil, err
        }
        updated = u
    } else {
        if s.tx == nil {
            return nil, cerrors.Unavailable("transaction manager not configured")
        }
        if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
            if scope.Calendars() == nil {
                return cerrors.Unavailable("calendars repository not bound to tx")
            }
            u, err := scope.Calendars().UpdateEventTx(ctx, existing, input.ExpectedUpdatedAt)
            if err != nil {
                return err
            }
            updated = u
            return nil
        }); err != nil {
            return nil, err
        }
    }
    s.publishEvent(ctx, event.TypeCalendarEventUpdated, *updated, existing.OrganizerID)
    return &MoveOccurrenceResult{Updated: updated}, nil
}
```

Stub the other two methods returning `cerrors.Unavailable("not yet implemented")` to keep compile green. They will be replaced in Tasks 6 and 7.

**Commit:** `feat(calendar): MoveEventOccurrence types + scope=all [ALOQA-239]`
**Compile state:** GREEN (Tasks 6–7 tests not yet written; stubs keep compiler happy)

---

## Task 5a — `CreateEventTx` / `UpdateEventTx` — tx-aware repository methods

**Files:** MODIFY `internal/domain/repository/interfaces.go`, MODIFY `internal/repository/postgres/calendar_repo.go`, MODIFY `internal/repository/postgres/calendar_repo_test.go`, MODIFY `internal/service/calendar/service_test.go`

**Goal**: Add `CreateEventTx` and `UpdateEventTx` to `CalendarRepository` so that callers inside `txscope.Manager.WithinTx` use the outer transaction's connection (`r.db`) directly rather than opening a nested `pool.BeginTx` (which would break atomicity). `UpdateEventTx` supports optional optimistic concurrency via `expectedUpdatedAt`.

**Concurrency token precision contract (R3 new)**: `ExpectedUpdatedAt` is compared with `updated_at` via exact SQL `AND updated_at = $N` (microsecond precision in Postgres). The HTTP layer accepts RFC3339Nano (full nanosecond fidelity) — clients are expected to round-trip the exact `event.updated_at` they received from the previous GET or mutation response. The 409 test (`TestUpdateEventTx_StaleExpected_ReturnsConflict`) deliberately constructs a stale token by `Add(-time.Second)` — any sub-second mismatch is treated as stale on purpose. A future iteration may switch to opaque versioning (`int64` etag), but for v1 the timestamp policy is **exact equality, RFC3339Nano on the wire**.

### Step 1 — Failing tests

Add to `service_test.go` after the Task 5 helpers:

```go
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
        eventID: {ID: eventID, WorkspaceID: wsID,
            Title: "Old", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30, UpdatedAt: now},
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
    stale := now.Add(-time.Second) // wrong expected updated_at
    eventID := uuid.New()
    wsID := uuid.New()
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID,
            Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: now, DurationMinutes: 30, UpdatedAt: now},
    }}
    ev := &entity.CalendarEvent{
        ID: eventID, WorkspaceID: wsID,
        Title: "Y", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: now, DurationMinutes: 30,
    }
    _, err := repo.UpdateEventTx(ctx, ev, &stale)
    assertConflict(t, err, "CONCURRENT_UPDATE")
}
```

**Postgres-level integration tests** (R2/B1, R3/B1 v2): Add to `internal/repository/postgres/calendar_repo_test.go`. Reuse the existing harness present in that file: `setupCalendarRepoPostgresTest(t) (ctx, *pgxpool.Pool)`, `setupCalendarTestEnv(t, ctx, pool) calendarRepoTestEnv`, `newCalendarRepoTestEvent(env, title, reminders) *entity.CalendarEvent`, and `NewCalendarRepo(pool)`. The harness honours `ALOQA_POSTGRES_TEST_DSN` and skips when unset.

Five tests, exactly:

```go
func TestCreateEventTx_InsertsEvent(t *testing.T) {
    ctx, pool := setupCalendarRepoPostgresTest(t)
    defer pool.Close()
    env := setupCalendarTestEnv(t, ctx, pool)
    repo := NewCalendarRepo(pool)

    tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        t.Fatalf("BeginTx: %v", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()
    txRepo := repo.withTx(tx)

    ev := newCalendarRepoTestEvent(env, "tx-create", nil)
    inserted, err := txRepo.CreateEventTx(ctx, ev)
    if err != nil {
        t.Fatalf("CreateEventTx: %v", err)
    }
    if inserted.ID != ev.ID {
        t.Fatalf("ID=%v want=%v", inserted.ID, ev.ID)
    }
    if err := tx.Commit(ctx); err != nil {
        t.Fatalf("Commit: %v", err)
    }
    fetched, err := repo.GetEvent(ctx, ev.ID)
    if err != nil {
        t.Fatalf("GetEvent after commit: %v", err)
    }
    if fetched.Title != "tx-create" {
        t.Fatalf("Title=%q want=tx-create", fetched.Title)
    }
}

func TestUpdateEventTx_NilExpected_UpdatesEvent(t *testing.T) {
    ctx, pool := setupCalendarRepoPostgresTest(t)
    defer pool.Close()
    env := setupCalendarTestEnv(t, ctx, pool)
    repo := NewCalendarRepo(pool)

    // Seed via the non-tx path (its own internal BeginTx).
    seed := newCalendarRepoTestEvent(env, "seed", nil)
    if _, err := repo.CreateEvent(ctx, seed); err != nil {
        t.Fatalf("seed CreateEvent: %v", err)
    }

    // Update inside an outer tx with nil expected → no lock check.
    tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        t.Fatalf("BeginTx: %v", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()
    txRepo := repo.withTx(tx)

    seed.Title = "updated-nil"
    seed.UpdatedAt = time.Now().UTC()
    got, err := txRepo.UpdateEventTx(ctx, seed, nil)
    if err != nil {
        t.Fatalf("UpdateEventTx nil-expected: %v", err)
    }
    if got.Title != "updated-nil" {
        t.Fatalf("Title=%q want=updated-nil", got.Title)
    }
    if err := tx.Commit(ctx); err != nil {
        t.Fatalf("Commit: %v", err)
    }
}

func TestUpdateEventTx_MatchingExpected_UpdatesEvent(t *testing.T) {
    ctx, pool := setupCalendarRepoPostgresTest(t)
    defer pool.Close()
    env := setupCalendarTestEnv(t, ctx, pool)
    repo := NewCalendarRepo(pool)

    seed := newCalendarRepoTestEvent(env, "seed", nil)
    inserted, err := repo.CreateEvent(ctx, seed)
    if err != nil {
        t.Fatalf("seed: %v", err)
    }
    expected := inserted.UpdatedAt // exact stored value

    tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        t.Fatalf("BeginTx: %v", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()
    txRepo := repo.withTx(tx)

    inserted.Title = "matching-token"
    inserted.UpdatedAt = time.Now().UTC()
    got, err := txRepo.UpdateEventTx(ctx, inserted, &expected)
    if err != nil {
        t.Fatalf("UpdateEventTx matching-expected: %v", err)
    }
    if got.Title != "matching-token" {
        t.Fatalf("Title=%q want=matching-token", got.Title)
    }
    if err := tx.Commit(ctx); err != nil {
        t.Fatalf("Commit: %v", err)
    }
}

func TestUpdateEventTx_StaleExpected_ReturnsConflict(t *testing.T) {
    ctx, pool := setupCalendarRepoPostgresTest(t)
    defer pool.Close()
    env := setupCalendarTestEnv(t, ctx, pool)
    repo := NewCalendarRepo(pool)

    seed := newCalendarRepoTestEvent(env, "seed", nil)
    inserted, err := repo.CreateEvent(ctx, seed)
    if err != nil {
        t.Fatalf("seed: %v", err)
    }
    stale := inserted.UpdatedAt.Add(-time.Second) // wrong token

    tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        t.Fatalf("BeginTx: %v", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()
    txRepo := repo.withTx(tx)

    inserted.Title = "should-fail"
    inserted.UpdatedAt = time.Now().UTC()
    _, err = txRepo.UpdateEventTx(ctx, inserted, &stale)
    if err == nil {
        t.Fatal("expected conflict error")
    }
    ae, ok := cerrors.AsAppError(err)
    if !ok || ae.Code != cerrors.CodeConflict {
        t.Fatalf("want Conflict, got %v", err)
    }
    if !strings.Contains(ae.Message, "CONCURRENT_UPDATE") {
        t.Fatalf("message=%q missing CONCURRENT_UPDATE", ae.Message)
    }
}

// TestUpdateEventTx_PropagatesOuterTx exercises the exact boundary
// TxManager.WithinTx creates: an outer pool.BeginTx → repo.withTx(tx).
// Uncommitted writes inside the outer tx MUST be invisible to a fresh
// pool connection until Commit succeeds, and the inserted row MUST become
// visible after Commit. Verifying both halves proves that updateEventRowTx
// truly uses r.db (the outer tx) and not r.pool.
func TestUpdateEventTx_PropagatesOuterTx(t *testing.T) {
    ctx, pool := setupCalendarRepoPostgresTest(t)
    defer pool.Close()
    env := setupCalendarTestEnv(t, ctx, pool)
    repo := NewCalendarRepo(pool)

    seed := newCalendarRepoTestEvent(env, "seed", nil)
    inserted, err := repo.CreateEvent(ctx, seed)
    if err != nil {
        t.Fatalf("seed: %v", err)
    }

    tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        t.Fatalf("BeginTx: %v", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()
    txRepo := repo.withTx(tx)

    inserted.Title = "outer-tx-mutate"
    inserted.UpdatedAt = time.Now().UTC()
    if _, err := txRepo.UpdateEventTx(ctx, inserted, nil); err != nil {
        t.Fatalf("UpdateEventTx: %v", err)
    }

    // Read from a NEW pool connection: must still see the OLD title (uncommitted).
    snapshot, err := repo.GetEvent(ctx, inserted.ID)
    if err != nil {
        t.Fatalf("GetEvent pre-commit: %v", err)
    }
    if snapshot.Title != "seed" {
        t.Fatalf("pre-commit Title=%q want=seed (outer tx leaked)", snapshot.Title)
    }

    if err := tx.Commit(ctx); err != nil {
        t.Fatalf("Commit: %v", err)
    }

    // After commit the new title is visible.
    after, err := repo.GetEvent(ctx, inserted.ID)
    if err != nil {
        t.Fatalf("GetEvent post-commit: %v", err)
    }
    if after.Title != "outer-tx-mutate" {
        t.Fatalf("post-commit Title=%q want=outer-tx-mutate", after.Title)
    }
}
```

Imports to add to `calendar_repo_test.go`: `"strings"`, `"github.com/jackc/pgx/v5"`, and `"aloqa/internal/pkg/cerrors"`.

### Step 2 — Implementation

**`internal/domain/repository/interfaces.go`** — add two methods to `CalendarRepository` after `UpdateEvent`:

```go
// CreateEventTx inserts an event using the current connection (r.db).
// Must be called from within txscope.Manager.WithinTx; does not open a nested transaction.
CreateEventTx(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error)

// UpdateEventTx updates an event using the current connection (r.db).
// If expectedUpdatedAt is non-nil, adds AND updated_at = expectedUpdatedAt to WHERE;
// returns cerrors.Conflict("CONCURRENT_UPDATE: ...") if 0 rows were affected.
UpdateEventTx(ctx context.Context, event *entity.CalendarEvent, expectedUpdatedAt *time.Time) (*entity.CalendarEvent, error)
```

**`internal/repository/postgres/calendar_repo.go`** — add three methods:

```go
// updateEventRowTx updates the event row using r.db (no inner BeginTx).
// When expectedUpdatedAt is non-nil, appends AND updated_at = $16 to WHERE.
// Returns cerrors.Conflict("CONCURRENT_UPDATE: ...") when 0 rows affected with expected timestamp.
func (r *CalendarRepo) updateEventRowTx(ctx context.Context, event *entity.CalendarEvent, expectedUpdatedAt *time.Time) error {
    rrule, exdates := recurrenceColumns(event.Recurrence)
    args := []any{
        event.ID, event.CalendarID, event.ChannelID, event.Title, event.Description,
        event.Location.Type, event.Location.Value, event.ScheduledAt,
        originatorTZOrDefault(event.OriginatorTZ), event.DurationMinutes,
        event.AllDay, rrule, exdates, event.CallID, event.WorkspaceID,
    }
    sql := `
        UPDATE calendar_events
        SET calendar_id=$2, channel_id=$3, title=$4, description=$5,
            location_type=$6, location_value=$7, scheduled_at=$8, originator_tz=$9,
            duration_minutes=$10, all_day=$11, recurrence_rrule=$12,
            recurrence_exdates=$13, call_id=$14, updated_at=NOW()
        WHERE id=$1 AND workspace_id=$15`
    if expectedUpdatedAt != nil {
        sql += " AND updated_at=$16"
        args = append(args, expectedUpdatedAt.UTC())
    }
    sql += `
        RETURNING id, calendar_id, workspace_id, channel_id, organizer_id, title,
                  description, location_type, location_value, scheduled_at, originator_tz,
                  duration_minutes, all_day, recurrence_rrule, recurrence_exdates,
                  call_id, created_at, updated_at`
    row := r.db.QueryRow(ctx, sql, args...)
    updated, err := scanCalendarEvent(row)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            if expectedUpdatedAt != nil {
                return cerrors.Conflict("CONCURRENT_UPDATE: event was modified concurrently")
            }
            return cerrors.NotFound("event not found")
        }
        return err
    }
    event.CreatedAt = updated.CreatedAt
    event.UpdatedAt = updated.UpdatedAt
    return nil
}

// CreateEventTx inserts event + attendees + reminders using r.db (no nested BeginTx).
func (r *CalendarRepo) CreateEventTx(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error) {
    if err := r.insertEvent(ctx, event); err != nil {
        return nil, err
    }
    if err := r.replaceAttendees(ctx, event.ID, event.Attendees); err != nil {
        return nil, err
    }
    if err := r.replaceReminders(ctx, event.ID, event.Reminders); err != nil {
        return nil, err
    }
    return r.GetEvent(ctx, event.ID)
}

// UpdateEventTx updates event + attendees + reminders using r.db (no nested BeginTx).
func (r *CalendarRepo) UpdateEventTx(ctx context.Context, event *entity.CalendarEvent, expectedUpdatedAt *time.Time) (*entity.CalendarEvent, error) {
    if err := r.updateEventRowTx(ctx, event, expectedUpdatedAt); err != nil {
        return nil, err
    }
    if err := r.replaceAttendees(ctx, event.ID, event.Attendees); err != nil {
        return nil, err
    }
    if err := r.replaceReminders(ctx, event.ID, event.Reminders); err != nil {
        return nil, err
    }
    return r.GetEvent(ctx, event.ID)
}
```

**`internal/service/calendar/service_test.go`** — add to `fakeCalendarRepo` (auto-promoted to `txCalendarRepo` via embedding):

```go
func (r *fakeCalendarRepo) CreateEventTx(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
    return r.CreateEvent(ctx, eventEntity)
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
```

Note: `txCalendarRepo` embeds `*fakeCalendarRepo`, so both new methods are automatically available on `txCalendarRepo` without any additional code.

**Commit:** `feat(calendar): CreateEventTx/UpdateEventTx tx-aware repo methods [ALOQA-239]`
**Compile state:** GREEN

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

func TestMoveEventOccurrence_ConcurrentUpdate_Conflict_409(t *testing.T) {
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    instance := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
    storedUpdatedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
    staleExpected := storedUpdatedAt.Add(-time.Second) // wrong — triggers conflict
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30, UpdatedAt: storedUpdatedAt,
            Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=5"}},
    }}
    txMgr := newCalendarReminderTxManager(repo)
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    svc.SetTransactionManager(txMgr)

    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
        InstanceAt: instance, Scope: MoveScopeThis,
        NewScheduledAt: instance.Add(24 * time.Hour),
        ExpectedUpdatedAt: &staleExpected,
    })
    assertConflict(t, err, "CONCURRENT_UPDATE")
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
        updated, err := scope.Calendars().UpdateEventTx(ctx, parent, input.ExpectedUpdatedAt)
        if err != nil {
            return err
        }
        parentAfter = updated
        child := buildOccurrenceChild(existing, input.NewScheduledAt, deriveDuration(existing, input))
        inserted, err := scope.Calendars().CreateEventTx(ctx, child)
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

**Goal**: Clamp parent RRULE `UNTIL = instanceAt - 1ms`; partition exdates; build child with `ShiftBounds` rrule + shifted future exdates. First-occurrence degenerates to `scope=all`. `IsMember` check occurs before the first-occurrence shortcut.

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

func TestMoveEventOccurrence_Recurring_ThisAndFollowing_FirstOccurrenceAlreadyExdated_BadRequest(t *testing.T) {
    // IsMember check on start occurrence (exdated) returns false → INVALID_INSTANCE.
    // This test proves IsMember fires BEFORE the first-occurrence shortcut.
    ctx := context.Background()
    wsID, orgID, eventID := uuid.New(), uuid.New(), uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{
        eventID: {ID: eventID, WorkspaceID: wsID, OrganizerID: orgID,
            Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
            ScheduledAt: start, DurationMinutes: 30,
            Recurrence: &entity.RecurrenceRule{
                RRule:   "FREQ=DAILY;COUNT=5",
                Exdates: []time.Time{start}, // start occurrence is exdated
            }},
    }}
    txMgr := newCalendarReminderTxManager(repo)
    svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{wsID, orgID}: true}}, nil, noopPublisher{})
    svc.SetTransactionManager(txMgr)

    _, err := svc.MoveEventOccurrence(ctx, wsID, eventID, orgID, MoveOccurrenceInput{
        InstanceAt: start, Scope: MoveScopeThisAndFollowing,
        NewScheduledAt: start.Add(time.Hour),
    })
    assertInvalidInput(t, err, "INVALID_INSTANCE")
}
```

### Step 2 — Implement (replace stub)

```go
func (s *Service) moveOccurrenceThisAndFollowing(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
    loc, _ := time.LoadLocation(existing.OriginatorTZ)
    if loc == nil {
        loc = time.UTC
    }
    // IsMember check BEFORE first-occurrence shortcut (B3 fix).
    ok, err := calrrule.IsMember(existing.Recurrence.RRule, existing.ScheduledAt.In(loc), input.InstanceAt.UTC(), existing.Recurrence.Exdates)
    if err != nil {
        return nil, err
    }
    if !ok {
        return nil, cerrors.InvalidInput("INVALID_INSTANCE: instance_at is not a member of the series")
    }
    // Degenerate: first occurrence → scope=all (spec §4.5.this_and_following).
    if input.InstanceAt.UTC().Equal(existing.ScheduledAt.UTC()) {
        return s.moveOccurrenceAll(ctx, existing, input)
    }
    // tx guard is after shortcut because scope=all does not need a tx manager.
    if s.tx == nil {
        return nil, cerrors.Unavailable("transaction manager required for scope=this_and_following")
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

    childRule, _, err := calrrule.ShiftBounds(existing.Recurrence.RRule, existing.ScheduledAt.UTC(), input.InstanceAt.UTC(), input.NewScheduledAt.UTC())
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
        updated, err := scope.Calendars().UpdateEventTx(ctx, parent, input.ExpectedUpdatedAt)
        if err != nil {
            return err
        }
        parentAfter = updated
        child := buildOccurrenceChild(existing, input.NewScheduledAt, deriveDuration(existing, input))
        child.Recurrence = &entity.RecurrenceRule{RRule: childRule, Exdates: childExdates}
        inserted, err := scope.Calendars().CreateEventTx(ctx, child)
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

**Goal**: Decode `instance_at`, `scope`, `new_scheduled_at`, `new_duration_minutes`, `expected_updated_at`; call `MoveEventOccurrence`; return `{ updated, created }`. Handler tests decode the `{"error":{"code":"...","message":"..."}}` error body and assert both the HTTP status and message fragment.

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
    "aloqa/internal/platform/txscope"
    calendarservice "aloqa/internal/service/calendar"
)

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
    // R4/M3 fix: wire fakeMoveCalHTTPTxManager so scope=all + non-nil ExpectedUpdatedAt
    // routes through WithinTx → UpdateEventTx (otherwise s.tx == nil short-circuits to
    // cerrors.Unavailable and the 409 path never reaches the repo).
    svc.SetTransactionManager(&fakeMoveCalHTTPTxManager{repo: repo})
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

// decodeErrBody decodes {"error":{"code":"...","message":"..."}} response shapes.
func decodeErrBody(t *testing.T, res *httptest.ResponseRecorder) (code, message string) {
    t.Helper()
    var body struct {
        Error struct {
            Code    string `json:"code"`
            Message string `json:"message"`
        } `json:"error"`
    }
    if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode error body: %v (raw=%s)", err, res.Body.String())
    }
    return body.Error.Code, body.Error.Message
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
    _, msg := decodeErrBody(t, res)
    if !strings.Contains(msg, "INVALID_SCOPE") {
        t.Fatalf("message=%q missing INVALID_SCOPE", msg)
    }
}

func TestMoveOccurrenceHandler_400MissingInstanceAt(t *testing.T) {
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: now, DurationMinutes: 30,
    }
    res := f.serve(eventID, `{"scope":"all","new_scheduled_at":"2026-05-05T10:00:00Z"}`)
    if res.Code != http.StatusBadRequest {
        t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
    }
    _, msg := decodeErrBody(t, res)
    if !strings.Contains(msg, "instance_at") {
        t.Fatalf("message=%q must name the missing field instance_at", msg)
    }
}

func TestMoveOccurrenceHandler_400MissingNewScheduledAt(t *testing.T) {
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: now, DurationMinutes: 30,
    }
    res := f.serve(eventID, `{"scope":"all","instance_at":"2026-05-03T10:00:00Z"}`)
    if res.Code != http.StatusBadRequest {
        t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
    }
    _, msg := decodeErrBody(t, res)
    if !strings.Contains(msg, "new_scheduled_at") {
        t.Fatalf("message=%q must name the missing field new_scheduled_at", msg)
    }
}

func TestMoveOccurrenceHandler_400InvalidInstance(t *testing.T) {
    // R2/M4: handler must propagate INVALID_INSTANCE from service as 400.
    f := newCalendarHTTPFixture()
    eventID := uuid.New()
    start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: start, DurationMinutes: 30,
        Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"},
    }
    // instance_at is outside the COUNT=3 series (May 1–3); May 10 is invalid.
    body := `{"instance_at":"2026-05-10T10:00:00Z","scope":"this","new_scheduled_at":"2026-05-11T10:00:00Z"}`
    res := f.serve(eventID, body)
    if res.Code != http.StatusBadRequest {
        t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
    }
    _, msg := decodeErrBody(t, res)
    if !strings.Contains(msg, "INVALID_INSTANCE") {
        t.Fatalf("message=%q missing INVALID_INSTANCE", msg)
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
    _, msg := decodeErrBody(t, res)
    if !strings.Contains(msg, "FORBIDDEN_NOT_ORGANIZER") {
        t.Fatalf("message=%q missing FORBIDDEN_NOT_ORGANIZER", msg)
    }
}

func TestMoveOccurrenceHandler_409ConcurrentUpdate_WithToken(t *testing.T) {
    // R3/M3: verifies the full JSON → decode → service → UpdateEventTx → 409
    // chain. After R2/M2, scope=all with non-nil ExpectedUpdatedAt routes
    // through WithinTx → UpdateEventTx, so the fixture MUST wire
    // fakeMoveCalHTTPTxManager (which actually invokes fn, not short-circuits)
    // and the fake repo MUST return Conflict ONLY on stale token. If the field
    // were silently dropped during JSON decode or service dispatch, the fake
    // repo would receive nil and succeed — the assertion below would fail.
    f := newCalendarHTTPFixture() // wires fakeMoveCalHTTPTxManager into svc.tx
    eventID := uuid.New()
    storedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
    f.repo.events[eventID] = &entity.CalendarEvent{
        ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
        Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
        ScheduledAt: storedAt, DurationMinutes: 30, UpdatedAt: storedAt,
    }
    // expected_updated_at is 1 second before the stored updated_at → triggers 409.
    staleTs := storedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)
    body := `{"instance_at":"2026-05-01T08:00:00Z","scope":"all","new_scheduled_at":"2026-05-02T10:00:00Z","expected_updated_at":"` + staleTs + `"}`
    res := f.serve(eventID, body)
    if res.Code != http.StatusConflict {
        t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
    }
    _, msg := decodeErrBody(t, res)
    if !strings.Contains(msg, "CONCURRENT_UPDATE") {
        t.Fatalf("message=%q missing CONCURRENT_UPDATE", msg)
    }
    // Sanity: also verify the fake repo's UpdateEventTx was invoked with a
    // non-nil expected_updated_at matching the JSON-supplied value, proving
    // the token threaded all the way through.
    if !f.repo.lastUpdateTxCalled {
        t.Fatal("UpdateEventTx not invoked — token was dropped before reaching repo")
    }
    if f.repo.lastUpdateTxExpected == nil {
        t.Fatal("UpdateEventTx received nil expected_updated_at — token was dropped")
    }
}

// fakeMoveCalHTTPTxManager satisfies txscope.Manager. WithinTx ACTUALLY invokes
// fn with a scope wrapping the fake repo — NOT a short-circuit. This is the
// minimum required to verify that scope=all + non-nil ExpectedUpdatedAt
// reaches UpdateEventTx in the repo (R3/M3).
type fakeMoveCalHTTPTxManager struct {
    repo *fakeMoveCalendarRepo
}

func (m *fakeMoveCalHTTPTxManager) WithinTx(ctx context.Context, fn func(ctx context.Context, scope txscope.Scope) error) error {
    return fn(ctx, &fakeMoveCalHTTPScope{calendars: m.repo})
}

type fakeMoveCalHTTPScope struct{ calendars *fakeMoveCalendarRepo }

func (s *fakeMoveCalHTTPScope) Calendars() repository.CalendarRepository { return s.calendars }
func (s *fakeMoveCalHTTPScope) Calls() repository.CallRepository         { return nil }

// fakeMoveCalendarRepo satisfies repository.CalendarRepository.
// GetEvent, CreateEvent, UpdateEvent, CreateEventTx, UpdateEventTx have real behaviour.
// UpdateEventTx returns cerrors.Conflict("CONCURRENT_UPDATE: ...") ONLY when
// expectedUpdatedAt is non-nil AND does not equal the stored UpdatedAt.
// Records the last call's expected token in lastUpdateTxExpected for assertions.
// All other methods are stubs returning zero values.
type fakeMoveCalendarRepo struct {
    events               map[uuid.UUID]*entity.CalendarEvent
    lastUpdateTxCalled   bool
    lastUpdateTxExpected *time.Time
}
// (full method stubs follow the pattern of fakeCalendarRepo in service_test.go)
```

At the bottom of `calendar_test.go`, implement the supporting fakes:

```go
// fakeCalHTTPMembers implements repository.WorkspaceRepository.
// Only GetMember has real behaviour (checks wsID+userID match).
// All other methods panic with "not implemented" or return nil.
type fakeCalHTTPMembers struct {
    wsID   uuid.UUID
    userID uuid.UUID
}

// noopCalHTTPPublisher implements calendarservice.EventPublisher.
type noopCalHTTPPublisher struct{}

func (noopCalHTTPPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
```

The full method stubs for `fakeMoveCalendarRepo` (implementing all `CalendarRepository` methods) and `fakeCalHTTPMembers` (implementing all `WorkspaceRepository` methods with only `GetMember` returning real data) follow the established pattern from `service_test.go`. `fakeMoveCalendarRepo` must implement both `CreateEventTx` and `UpdateEventTx` — `CreateEventTx` delegates to `CreateEvent`; `UpdateEventTx` checks optimistic lock when `expectedUpdatedAt` is non-nil and delegates to `UpdateEvent`.

### Step 2 — Implement handler

In `calendar.go`:

```go
type moveOccurrenceRequest struct {
    InstanceAt         time.Time                           `json:"instance_at"`
    Scope              calendarservice.MoveOccurrenceScope `json:"scope"`
    NewScheduledAt     time.Time                           `json:"new_scheduled_at"`
    NewDurationMinutes *int                                `json:"new_duration_minutes"`
    ExpectedUpdatedAt  *time.Time                          `json:"expected_updated_at"`
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
        ExpectedUpdatedAt:  req.ExpectedUpdatedAt,
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

---

## Task 9 — Router wiring

**Files:** MODIFY `internal/handler/http/router.go`

**Goal**: Register the route inside the `r.Route("/{eventID}", ...)` block at line 358.

### Step 1 — Smoke test

In `calendar_test.go`, add:

```go
func TestMoveOccurrenceRouteRegistered(t *testing.T) {
    router := NewRouter(RouterDeps{
        Calendar:  &CalendarHandler{},
        Validator: fakeTokenValidator{userID: uuid.New()},
    })
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

Note: `fakeTokenValidator` is already defined in `router_personal_test.go` (same package `http`) with the signature `ValidateToken(string) (uuid.UUID, string, error)`. It is reused here to satisfy `RouterDeps.Validator` so that the auth middleware can pass the request through.

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
| 2 `NormalizeRule` | same | GREEN |
| 3 `IsMember` | same | GREEN |
| 4 `ShiftBounds` | same | GREEN |
| 5 types + scope=all | `service.go` + test | GREEN |
| 5a `CreateEventTx`/`UpdateEventTx` | `interfaces.go` + `calendar_repo.go` + test | GREEN |
| 6 scope=this | `service.go` + test | GREEN |
| 7 scope=this_and_following | `service.go` + test | GREEN |
| 8 handler | `calendar.go` + `calendar_test.go` | GREEN |
| 9 router | `router.go` | GREEN |
| 10 realtime | `service_test.go` | GREEN |
| 11 verify | — | GREEN |

---

## Test Plan Checklist

**rrule helpers** (22 tests):
- [ ] SetUntil: replaces existing UNTIL
- [ ] SetUntil: adds UNTIL when absent
- [ ] SetUntil: strips COUNT
- [ ] SetUntil: error on malformed
- [ ] SetUntil: idempotent
- [ ] NormalizeRule: returns parsed rule without DTSTART
- [ ] NormalizeRule: error on malformed
- [ ] NormalizeRule: stable on repeat calls
- [ ] NormalizeRule: preserves all components
- [ ] IsMember: in series
- [ ] IsMember: not in series
- [ ] IsMember: excluded by exdate
- [ ] IsMember: boundary at UNTIL (inclusive)
- [ ] IsMember: unbounded rule
- [ ] ShiftBounds: UNTIL-bounded shifts by delta
- [ ] ShiftBounds: COUNT-bounded counts remaining (uses parentDtstart)
- [ ] ShiftBounds: unbounded unchanged
- [ ] ShiftBounds: zero delta idempotent
- [ ] ShiftBounds: negative delta
- [ ] ShiftBounds: last occurrence COUNT=1
- [ ] ShiftBounds: UNTIL wins over COUNT
- [ ] (NormalizeRule replaces SetDtstart; same 4 test cases, renamed)

**Service** (15 tests):
- [ ] NonRecurring_All_OK (duration preserved)
- [ ] NonRecurring_This_DegeneratesToAll
- [ ] Recurring_All_OK (recurrence preserved)
- [ ] Recurring_This_OK (exdate + child + RSVP reset + new IDs)
- [ ] InstanceNotInSeries_BadRequest
- [ ] InstanceAlreadyExdated_BadRequest
- [ ] ConcurrentUpdate_Conflict_409
- [ ] CreateEventTx_StoresEvent
- [ ] UpdateEventTx_UpdatesEvent
- [ ] UpdateEventTx_OptimisticLock_Conflict
- [ ] Recurring_ThisAndFollowing_OK (UNTIL + shifted rrule + shifted exdates)
- [ ] ThisAndFollowing_FirstOccurrence_DegeneratesToAll
- [ ] ThisAndFollowing_LastOccurrence_OK (child COUNT=1)
- [ ] ThisAndFollowing_FirstOccurrenceAlreadyExdated_BadRequest
- [ ] NonOrganizer_Forbidden (asserts FORBIDDEN_NOT_ORGANIZER fragment)
- [ ] InvalidScope_BadRequest
- [ ] DurationOutOfRange_BadRequest
- [ ] RealtimePublishedCorrectly

**HTTP** (6 tests):
- [ ] 200 OK response shape (Updated non-nil)
- [ ] 400 invalid scope (error body contains INVALID_SCOPE)
- [ ] 400 missing required fields (error body non-empty message)
- [ ] 403 non-organizer (error body contains FORBIDDEN_NOT_ORGANIZER)
- [ ] 409 concurrent update (error body contains CONCURRENT_UPDATE)
- [ ] Route registered (smoke, with fakeTokenValidator)

---

## Self-Review Checklist

- [ ] All new helpers in `internal/pkg/rrule/recurrence_split.go`; `expand.go` untouched
- [ ] Helper is `NormalizeRule(rule string) (string, error)` — no `time.Time` parameter (m8)
- [ ] `ShiftBounds` signature: `(rule string, parentDtstart, originalInstance, newInstance time.Time)` (B1)
- [ ] COUNT case uses `Expand(rule, parentDtstart.UTC(), nil, originalInstance.UTC().Add(-time.Millisecond), far)` (B1)
- [ ] `CreateEventTx`/`UpdateEventTx` added to `CalendarRepository` interface (B2)
- [ ] `CalendarRepo.CreateEventTx`/`UpdateEventTx` use `r.db` directly — no inner `pool.BeginTx` (B2)
- [ ] `fakeCalendarRepo` implements `CreateEventTx`/`UpdateEventTx`; auto-promoted to `txCalendarRepo` (B2)
- [ ] `moveOccurrenceThisAndFollowing`: `IsMember` check is BEFORE first-occurrence shortcut (B3)
- [ ] `s.tx == nil` guard is AFTER first-occurrence shortcut (B3)
- [ ] `MoveOccurrenceInput.ExpectedUpdatedAt *time.Time` field present (B4)
- [ ] `moveOccurrenceRequest.ExpectedUpdatedAt *time.Time` JSON field present (B4)
- [ ] `UpdateEventTx` uses optimistic lock when `expectedUpdatedAt != nil`; returns `cerrors.Conflict("CONCURRENT_UPDATE: ...")` on mismatch (B4)
- [ ] `assertForbidden` checks both `CodeForbidden` and `FORBIDDEN_NOT_ORGANIZER` message fragment (M5)
- [ ] `assertConflict` helper added (M5)
- [ ] All handler tests call `decodeErrBody` and assert message fragment (M6)
- [ ] Router smoke test provides `fakeTokenValidator{userID: uuid.New()}` in `RouterDeps.Validator` (m7)
- [ ] `cerrors.InvalidInput` for all 400s; no invented constructor
- [ ] `cerrors.Forbidden` for non-organizer; `cerrors.Conflict` for concurrent update
- [ ] `s.tx.WithinTx` for scope=this and scope=this_and_following; scope=all does NOT use WithinTx
- [ ] scope=this first-occurrence NOT degenerated (parent gets exdate + child created)
- [ ] scope=this_and_following first-occurrence degenerates to scope=all
- [ ] `buildOccurrenceChild`: `call_id=nil`, `recurrence=nil`, fresh timestamps
- [ ] `resetAttendees`: new IDs, `rsvp_status=no_response`, `responded_at=nil`
- [ ] `copyReminders`: rebinds `EventID`; does NOT call `buildReminderRows`
- [ ] Exdate partition: `< instanceAt` to parent, `> instanceAt` shifted by delta to child, `== instanceAt` discarded
- [ ] `ShiftBounds` UNTIL takes priority when both UNTIL and COUNT present
- [ ] `IsMember` uses 1ms window (not full-series expand)
- [ ] scope=all path: `ExpectedUpdatedAt == nil` uses direct `UpdateEvent`; non-nil uses `WithinTx` + `UpdateEventTx` (M2)
- [ ] Service tests `Recurring_All_WithMatchingToken` and `Recurring_All_WithStaleToken` present (M2)
- [ ] Postgres tests in `calendar_repo_test.go`: CreateEventTx_InsertsEvent, UpdateEventTx_NilExpected, UpdateEventTx_MatchingExpected, UpdateEventTx_StaleExpected_ReturnsConflict, UpdateEventTx_PropagatesOuterTx (B1)
- [ ] Postgres isolation test uses `pool.BeginTx` + `repo.withTx(tx)`, NOT direct pool calls (B1)
- [ ] Handler test `TestMoveOccurrenceHandler_409ConcurrentUpdate_WithToken`: request JSON includes `expected_updated_at` + fake tx manager actually calls fn + fake repo returns Conflict on stale token (M3)
- [ ] Handler test `TestMoveOccurrenceHandler_400InvalidInstance`: real handler+service path, asserts HTTP 400 + body contains `INVALID_INSTANCE` (M4)
- [ ] Handler test `TestMoveOccurrenceHandler_400MissingFields`: body contains `"instance_at"` or `"new_scheduled_at"` (M4)
- [ ] No migration; no new go.mod dependencies
- [ ] Handler validates `instance_at` and `new_scheduled_at` non-zero before calling service
- [ ] Route wired inside `r.Route("/{eventID}", ...)` block, not at events-list level
- [ ] `go mod tidy` no diff

---

## Spec Gaps

1. **`NormalizeRule` semantics**: Spec §4.6 references a "SetDtstart" helper. Renamed to `NormalizeRule` throughout; DTSTART is never embedded in the rule string in this codebase (stored in `CalendarEvent.ScheduledAt`). The `time.Time` parameter is dropped — the function is purely a canonicalization validator.

2. **`IsMember` 1ms window vs full expand**: A 1ms precision window may produce false-negatives for occurrences whose computed time differs by sub-millisecond due to DST arithmetic edge cases. The window is sufficient for the UTC timestamps this codebase stores; a full-series expand fallback is not needed.

3. **`OccurrenceIndex` for COUNT series**: Approximated via `opt.Count - len(remaining) + 1`. Used only in diagnostics/logs; callers do not depend on it for correctness.

4. **`notify` flag** (mentioned in spec §4.5 prose): Not present in the §4.2 request schema. Out of scope this iteration.

5. **Concurrency token precision (R3)**: `ExpectedUpdatedAt` uses exact RFC3339Nano equality. Clients must round-trip the `updated_at` from the previous response without truncation. Future iteration may switch to opaque integer etag versioning.

---

## Codex Plan Review R4 — applied

All 2 findings from the R4 review (2026-05-18) have been incorporated. Summary:

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| R4/B1 | BLOCKING | R3/M3 incomplete: `newCalendarHTTPFixture` constructed `svc := NewService(..., nil, ...)` and never called `svc.SetTransactionManager(&fakeMoveCalHTTPTxManager{...})`. The comment claimed wiring but body did not, so `s.tx == nil` short-circuited to `cerrors.Unavailable` before any 409 path could fire | Added `svc.SetTransactionManager(&fakeMoveCalHTTPTxManager{repo: repo})` to `newCalendarHTTPFixture` body with explanatory comment. The handler 409 test now actually reaches `WithinTx → UpdateEventTx → Conflict` |
| R4/M2 | MAJOR | R3/M2 incomplete: `moveOccurrenceAll` branching correct but the exact named recurring service tests `TestMoveEventOccurrence_Recurring_All_WithMatchingToken` and `_WithStaleToken` did not exist in the plan body | Added both tests in Task 5 service_test.go block after `TestMoveEventOccurrence_ScopeAll_OptimisticLock_Conflict`. Both use a recurring `FREQ=DAILY;COUNT=10` event, set `svc.SetTransactionManager(newCalendarReminderTxManager(repo))` to wire WithinTx, then verify success (matching token preserves recurrence) or Conflict (stale token returns `CONCURRENT_UPDATE`) |

---

## Codex Plan Review R3 — applied

All 5 findings from the R3 review (2026-05-18) have been incorporated into the plan above. R3 was the FOURTH iteration (after R1 → v2, R2 → v3, and a partial v3 reassembly that left some claims unbacked). Summary:

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| B1 | BLOCKING | R2/B1 partially fixed: postgres tests used non-existent helpers (`newTestCalendarRepo`, `seedWorkspace`, `seedEvent`, `buildTestEvent`); Task 5a Files list omitted `calendar_repo_test.go`; test names did not match the applied-table claims | Task 5a Files block now includes `internal/repository/postgres/calendar_repo_test.go`. Five postgres tests rewritten verbatim using actual helpers: `setupCalendarRepoPostgresTest`, `setupCalendarTestEnv`, `newCalendarRepoTestEvent`, `NewCalendarRepo(pool)`. Test names exactly match the claimed set: `TestCreateEventTx_InsertsEvent`, `TestUpdateEventTx_NilExpected_UpdatesEvent`, `TestUpdateEventTx_MatchingExpected_UpdatesEvent`, `TestUpdateEventTx_StaleExpected_ReturnsConflict`, `TestUpdateEventTx_PropagatesOuterTx`. `PropagatesOuterTx` uses `pool.BeginTx → repo.withTx(tx)` + asserts pre-commit invisibility on a fresh pool connection AND post-commit visibility |
| M2 | MAJOR | R2/M2 was FALSE: `moveOccurrenceAll` always called `UpdateEventTx` (no nil/non-nil branching); claimed matching/stale tests absent | `moveOccurrenceAll` rewritten with explicit branch: `ExpectedUpdatedAt == nil` → `s.calendars.UpdateEvent(ctx, existing)`; non-nil → `s.tx.WithinTx(ctx, fn)` + `scope.Calendars().UpdateEventTx(ctx, existing, input.ExpectedUpdatedAt)`. `publishEvent` fires only after the tx commits successfully |
| M3 | MAJOR | R2/M3 partially fixed: test had stale token in JSON but used wrong name and did not exercise tx path; no `fakeMoveCalHTTPTxManager` in plan | Test renamed `TestMoveOccurrenceHandler_409ConcurrentUpdate_WithToken`. Comment rewritten to document the post-M2 routing (scope=all + non-nil token DOES require tx). Added `fakeMoveCalHTTPTxManager` and `fakeMoveCalHTTPScope` types — `WithinTx` actually invokes fn with a scope wrapping the fake repo. Added `fakeMoveCalendarRepo` instrumentation (`lastUpdateTxCalled`, `lastUpdateTxExpected`) so the test can ALSO assert the token reached the repo, not just that 409 was returned |
| M4 | MINOR | R2/M4 mostly fixed but missing-field test allowed generic "required" via `!Contains(msg, "instance_at") && Contains(msg, "required")` permissive branch | Split into two tests: `TestMoveOccurrenceHandler_400MissingInstanceAt` asserts body contains `instance_at`; `TestMoveOccurrenceHandler_400MissingNewScheduledAt` asserts body contains `new_scheduled_at`. No generic fallback |
| New | MAJOR | Optimistic concurrency had no precision policy or regression test for sub-second mismatch (RFC3339 seconds vs RFC3339Nano) | Added explicit "Concurrency token precision contract" to Task 5a goal section. Policy: exact RFC3339Nano equality on the wire; SQL `AND updated_at = $N` (postgres microsecond precision). Stale-token test uses `Add(-time.Second)` to encode the intent that ANY mismatch is treated as stale. Future iteration may switch to opaque etag |

---

## Codex Plan Review R2 — applied

All 4 findings from the R2 review (2026-05-18) have been incorporated into the plan above. Summary:

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| B1 | BLOCKING | Task 5a adds runtime-critical SQL paths (CreateEventTx, UpdateEventTx, updateEventRowTx WHERE/conflict logic) but plan only tested these against fakeCalendarRepo in service_test.go; real Postgres SQL was untested | Added 5 postgres-level tests to `calendar_repo_test.go` using the existing `setupCalendarRepoPostgresTest`/`setupCalendarTestEnv`/`pgxpool.New` pattern (ALOQA_POSTGRES_TEST_DSN skip guard). Tests: CreateEventTx_InsertsEvent, UpdateEventTx_NilExpected, UpdateEventTx_MatchingExpected, UpdateEventTx_StaleExpected_ReturnsConflict, UpdateEventTx_PropagatesOuterTx. Isolation test uses `pool.BeginTx` + `repo.withTx(tx)` to simulate the exact boundary TxManager.WithinTx creates |
| M2 | MAJOR | Optimistic concurrency coverage incomplete: (a) no matching-token happy-path test at service level; (b) scope=all silently ignored ExpectedUpdatedAt — called UpdateEvent unconditionally regardless of token | Added `TestMoveEventOccurrence_Recurring_All_WithMatchingToken` and `TestMoveEventOccurrence_Recurring_All_WithStaleToken` at service level. Rewrote `moveOccurrenceAll`: when `ExpectedUpdatedAt == nil` calls `UpdateEvent` directly; when non-nil wraps in `WithinTx` + `UpdateEventTx` with the token. Postgres level adds `TestUpdateEventTx_MatchingExpected_UpdatesEvent` covering the non-nil equal-token path |
| M3 | MAJOR | 409 handler test used `fakeConflictTxManager` that short-circuits `WithinTx` without executing fn; request body lacked `expected_updated_at`; field could be silently dropped during JSON decode or service dispatch without the test catching it | Replaced `TestMoveOccurrenceHandler_409ConcurrentUpdate` with `TestMoveOccurrenceHandler_409ConcurrentUpdate_WithToken`: request body includes `expected_updated_at` as a stale RFC3339 timestamp; uses `fakeMoveCalHTTPTxManager` that actually calls fn (not short-circuit); `fakeMoveCalendarRepo.UpdateEventTx` returns Conflict only when token mismatches; this verifies the full JSON→decode→service→repo→409 chain |
| M4 | MAJOR | No `TestMoveOccurrenceHandler_InvalidInstance*` handler test; INVALID_INSTANCE only in service tests; missing-field test asserted only `msg != ""` | Added `TestMoveOccurrenceHandler_400InvalidInstance`: real handler + `fakeMoveCalHTTPTxManager` + recurring event with COUNT=3 where instance_at is outside the series → asserts HTTP 400 + body contains `INVALID_INSTANCE`. Strengthened `TestMoveOccurrenceHandler_400MissingFields`: asserts body contains `"instance_at"` or `"new_scheduled_at"` (not just non-empty) |

---

## Codex Plan Review R1 — applied

All 8 findings from the R1 review (2026-05-18) have been incorporated into the plan above. Summary:

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| B1 | HIGH | `ShiftBounds` COUNT case uses `originalInstance` as `Expand` dtstart, producing wrong remaining count because COUNT is consumed from a non-root start | Added `parentDtstart time.Time` as 2nd param; COUNT `Expand` call uses `parentDtstart.UTC()` as dtstart; all 7 test call sites updated |
| B2 | HIGH | `CreateEvent`/`UpdateEvent` open an inner `pool.BeginTx` internally; calling them from within `WithinTx` breaks atomicity (nested non-joined transaction) | Added `CreateEventTx`/`UpdateEventTx` to `CalendarRepository` interface and `CalendarRepo` using `r.db` directly; Tasks 6 and 7 use `*Tx` variants inside `WithinTx` |
| B3 | HIGH | `moveOccurrenceThisAndFollowing` performs `IsMember` check AFTER the first-occurrence shortcut; an exdated first occurrence silently degenerates to scope=all instead of returning 400 | Reordered: `IsMember` check is now BEFORE the shortcut; `s.tx == nil` guard is AFTER the shortcut |
| B4 | MEDIUM | No optimistic concurrency — concurrent DnD moves on the same event silently overwrite each other | Added `ExpectedUpdatedAt *time.Time` to `MoveOccurrenceInput` and `moveOccurrenceRequest`; `updateEventRowTx` adds `AND updated_at=$16` when non-nil; returns `cerrors.Conflict("CONCURRENT_UPDATE: ...")` on mismatch |
| M5 | MEDIUM | `assertForbidden` only checks error code, not message fragment; `FORBIDDEN_NOT_ORGANIZER` marker can silently drift | Updated `assertForbidden` to accept and assert a message fragment; added `assertConflict` helper with same pattern |
| M6 | MEDIUM | Handler tests check HTTP status only; `writeErr` shape `{"error":{"code":"...","message":"..."}}` is never decoded in tests | Added `decodeErrBody` helper; all error-path handler tests now assert both status and message fragment; added 409 test with `fakeConflictTxManager` |
| m7 | LOW | Router smoke test provides no `Validator` in `RouterDeps`; auth middleware short-circuits with 401, making the route registration check a false positive | Updated smoke test to provide `Validator: fakeTokenValidator{userID: uuid.New()}` |
| m8 | LOW | `SetDtstart` name misleads — it does not embed DTSTART; the `time.Time` parameter is silently ignored | Renamed to `NormalizeRule`; dropped `time.Time` parameter from signature, tests, and all references in Architecture section |

---

*End of plan. Estimated test counts: 22 rrule + 22 service + 5 postgres + 7 handler = 56 new tests. Line count: ~2340.*
