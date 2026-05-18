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
