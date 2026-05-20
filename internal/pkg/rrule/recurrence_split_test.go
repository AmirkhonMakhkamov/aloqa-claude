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

func TestShiftBounds_COUNTBounded_LongRunningSeries(t *testing.T) {
	// R6 regression: 50-year window truncated YEARLY series. Verify exact count.
	parentDtstart := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	originalInstance := time.Date(2028, 5, 1, 10, 0, 0, 0, time.UTC)
	newInstance := time.Date(2028, 5, 2, 10, 0, 0, 0, time.UTC)
	_, result, err := ShiftBounds("FREQ=YEARLY;COUNT=100", parentDtstart, originalInstance, newInstance)
	if err != nil {
		t.Fatalf("ShiftBounds: %v", err)
	}
	if !result.HadCount {
		t.Fatal("HadCount must be true")
	}
	if result.NewCount == nil {
		t.Fatal("NewCount must be non-nil")
	}
	if *result.NewCount != 98 {
		t.Fatalf("NewCount=%d want=98", *result.NewCount)
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
