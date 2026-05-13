package rrule

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, layout, value string, loc *time.Location) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(layout, value, loc)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestExpandDailySimple(t *testing.T) {
	loc := time.UTC
	start := mustTime(t, time.RFC3339, "2026-05-01T09:00:00Z", loc)

	got, err := Expand("FREQ=DAILY;COUNT=3", start, nil, start, start.AddDate(0, 0, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[2].Equal(start.AddDate(0, 0, 2)) {
		t.Fatalf("occurrences = %v", got)
	}
}

func TestExpandDailyUntil(t *testing.T) {
	start := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	got, err := Expand("FREQ=DAILY;UNTIL=20260503T090000Z", start, nil, start, start.AddDate(0, 0, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (%v)", len(got), got)
	}
}

func TestExpandWeeklyByDay(t *testing.T) {
	start := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC) // Monday.
	got, err := Expand("FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=6", start, nil, start, start.AddDate(0, 0, 14))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6 (%v)", len(got), got)
	}
	for _, occurrence := range got {
		switch occurrence.Weekday() {
		case time.Monday, time.Wednesday, time.Friday:
		default:
			t.Fatalf("unexpected weekday %s in %v", occurrence.Weekday(), got)
		}
	}
}

func TestExpandWeeklyAcrossISOWeekYearBoundary(t *testing.T) {
	start := time.Date(2020, 12, 28, 9, 0, 0, 0, time.UTC) // Monday in ISO week 53.
	got, err := Expand("FREQ=WEEKLY;COUNT=3", start, nil, start, start.AddDate(0, 0, 21))
	if err != nil {
		t.Fatal(err)
	}
	wantWeeks := []int{53, 1, 2}
	if len(got) != len(wantWeeks) {
		t.Fatalf("got %v, want %d occurrences", got, len(wantWeeks))
	}
	for i, occurrence := range got {
		_, week := occurrence.ISOWeek()
		if week != wantWeeks[i] {
			t.Fatalf("occurrence %d ISO week = %d, want %d (%v)", i, week, wantWeeks[i], got)
		}
	}
}

func TestExpandMonthlyLastMonday(t *testing.T) {
	start := time.Date(2026, 1, 26, 9, 0, 0, 0, time.UTC)
	got, err := Expand("FREQ=MONTHLY;BYDAY=-1MO;COUNT=3", start, nil, start, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{
		time.Date(2026, 1, 26, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 23, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC),
	}
	for i := range want {
		if i >= len(got) || !got[i].Equal(want[i]) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExpandMonthlyOn31stSkipsShortMonths(t *testing.T) {
	start := time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)
	got, err := Expand("FREQ=MONTHLY;COUNT=4", start, nil, start, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{
		time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExpandDSTForwardAndBack(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		start time.Time
	}{
		{name: "forward", start: time.Date(2026, 3, 28, 9, 30, 0, 0, loc)},
		{name: "back", start: time.Date(2026, 10, 24, 9, 30, 0, 0, loc)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand("FREQ=DAILY;COUNT=3", tc.start, nil, tc.start, tc.start.AddDate(0, 0, 4))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Fatalf("len = %d, want 3", len(got))
			}
			for _, occurrence := range got {
				local := occurrence.In(loc)
				if local.Hour() != 9 || local.Minute() != 30 {
					t.Fatalf("wall clock shifted: %v", got)
				}
			}
		})
	}
}

func TestExpandUntilUsesLocalTimezoneBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 28, 9, 30, 0, 0, loc)
	got, err := Expand("FREQ=DAILY;UNTIL=20260330T080000", start, nil, start, start.AddDate(0, 0, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want two local occurrences before 2026-03-30 08:00 Europe/Berlin", got)
	}
	for _, occurrence := range got {
		local := occurrence.In(loc)
		if local.Hour() != 9 || local.Minute() != 30 {
			t.Fatalf("wall clock shifted: %v", got)
		}
		if !local.Before(time.Date(2026, 3, 30, 8, 0, 0, 0, loc)) {
			t.Fatalf("occurrence crossed local UNTIL boundary: %v", got)
		}
	}
}

func TestExpandLeapYearFeb29(t *testing.T) {
	start := time.Date(2024, 2, 29, 8, 0, 0, 0, time.UTC)
	got, err := Expand("FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;COUNT=3", start, nil, start, time.Date(2033, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	wantYears := []int{2024, 2028, 2032}
	if len(got) != len(wantYears) {
		t.Fatalf("got %v", got)
	}
	for i, year := range wantYears {
		if got[i].Year() != year || got[i].Month() != time.February || got[i].Day() != 29 {
			t.Fatalf("got %v, want Feb 29 in %v", got[i], wantYears)
		}
	}
}

func TestExpandExdate(t *testing.T) {
	start := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	exdate := start.AddDate(0, 0, 1)
	got, err := Expand("FREQ=DAILY;COUNT=3", start, []time.Time{exdate}, start, start.AddDate(0, 0, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Equal(exdate) || got[1].Equal(exdate) {
		t.Fatalf("exdate not applied: %v", got)
	}
}

func TestExpandYearlyStopsAtUntil(t *testing.T) {
	start := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	got, err := Expand("FREQ=YEARLY;UNTIL=20220101T120000Z", start, nil, start, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[len(got)-1].Year() != 2022 {
		t.Fatalf("got %v, want years 2020..2022", got)
	}
}
