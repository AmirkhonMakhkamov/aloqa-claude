package rrule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Frequency string

const (
	Daily   Frequency = "DAILY"
	Weekly  Frequency = "WEEKLY"
	Monthly Frequency = "MONTHLY"
	Yearly  Frequency = "YEARLY"
)

type weekdayRule struct {
	Ordinal int
	Day     time.Weekday
}

type RRule struct {
	freq       Frequency
	interval   int
	until      *time.Time
	count      int
	byDay      []weekdayRule
	byMonth    []time.Month
	byMonthDay []int
	dtstart    time.Time
}

func StrToRRule(raw string) (*RRule, error) {
	r := &RRule{interval: 1}
	for _, part := range strings.Split(raw, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("rrule: invalid component %q", part)
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "FREQ":
			r.freq = Frequency(strings.ToUpper(value))
		case "INTERVAL":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("rrule: invalid interval %q", value)
			}
			r.interval = n
		case "UNTIL":
			t, err := parseUntil(value, time.UTC)
			if err != nil {
				return nil, err
			}
			r.until = &t
		case "COUNT":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("rrule: invalid count %q", value)
			}
			r.count = n
		case "BYDAY":
			days, err := parseByDay(value)
			if err != nil {
				return nil, err
			}
			r.byDay = days
		case "BYMONTH":
			months, err := parseIntList(value, 1, 12)
			if err != nil {
				return nil, fmt.Errorf("rrule: invalid bymonth: %w", err)
			}
			for _, month := range months {
				r.byMonth = append(r.byMonth, time.Month(month))
			}
		case "BYMONTHDAY":
			days, err := parseIntList(value, -31, 31)
			if err != nil {
				return nil, fmt.Errorf("rrule: invalid bymonthday: %w", err)
			}
			r.byMonthDay = days
		case "WKST":
			// The helper uses Monday week starts, which matches the frontend presets.
		default:
			// Preserve compatibility with common RRULE strings by ignoring
			// components this backend foundation does not need yet.
		}
	}
	if r.freq == "" {
		return nil, fmt.Errorf("rrule: FREQ is required")
	}
	switch r.freq {
	case Daily, Weekly, Monthly, Yearly:
	default:
		return nil, fmt.Errorf("rrule: unsupported frequency %q", r.freq)
	}
	return r, nil
}

func (r *RRule) DTStart(dtstart time.Time) {
	r.dtstart = dtstart
	if r.until != nil && !strings.HasSuffix(r.until.Format(time.RFC3339), "Z") {
		until := r.until.In(dtstart.Location())
		r.until = &until
	}
}

func (r *RRule) Between(after, before time.Time, inc bool) []time.Time {
	if r == nil || r.dtstart.IsZero() || before.Before(after) {
		return nil
	}
	if r.until != nil && r.until.Before(after) {
		return nil
	}
	limit := before
	if r.until != nil && r.until.Before(limit) {
		limit = *r.until
	}

	var out []time.Time
	generated := 0
	emit := func(t time.Time) bool {
		if t.Before(r.dtstart) {
			return true
		}
		if r.until != nil && t.After(*r.until) {
			return false
		}
		generated++
		if (inc && !t.Before(after) && !t.After(before)) || (!inc && t.After(after) && t.Before(before)) {
			out = append(out, t)
		}
		return r.count <= 0 || generated < r.count
	}

	switch r.freq {
	case Daily:
		for t := r.dtstart; !t.After(limit); t = t.AddDate(0, 0, r.interval) {
			if r.matchesDateFilters(t) && !emit(t) {
				break
			}
		}
	case Weekly:
		r.betweenWeekly(limit, emit)
	case Monthly:
		r.betweenMonthly(limit, emit)
	case Yearly:
		r.betweenYearly(limit, emit)
	}
	return out
}

func (r *RRule) betweenWeekly(limit time.Time, emit func(time.Time) bool) {
	days := r.byDay
	if len(days) == 0 {
		days = []weekdayRule{{Day: r.dtstart.Weekday()}}
	}
	allowed := map[time.Weekday]struct{}{}
	for _, d := range days {
		if d.Ordinal == 0 {
			allowed[d.Day] = struct{}{}
		}
	}
	startWeek := startOfWeek(r.dtstart)
	for date := midnight(r.dtstart); !date.After(limit); date = date.AddDate(0, 0, 1) {
		if _, ok := allowed[date.Weekday()]; !ok {
			continue
		}
		weeks := int(startOfWeek(date).Sub(startWeek).Hours() / (24 * 7))
		if weeks < 0 || weeks%r.interval != 0 {
			continue
		}
		occ := withClock(date, r.dtstart)
		if occ.Before(r.dtstart) {
			continue
		}
		if r.matchesDateFilters(occ) && !emit(occ) {
			break
		}
	}
}

func (r *RRule) betweenMonthly(limit time.Time, emit func(time.Time) bool) {
	year, month, _ := r.dtstart.Date()
	for cursor := time.Date(year, month, 1, r.dtstart.Hour(), r.dtstart.Minute(), r.dtstart.Second(), r.dtstart.Nanosecond(), r.dtstart.Location()); !cursor.After(limit); cursor = cursor.AddDate(0, r.interval, 0) {
		for _, occ := range r.monthlyCandidates(cursor.Year(), cursor.Month()) {
			if occ.Before(r.dtstart) || occ.After(limit) {
				continue
			}
			if r.matchesDateFilters(occ) && !emit(occ) {
				return
			}
		}
	}
}

func (r *RRule) betweenYearly(limit time.Time, emit func(time.Time) bool) {
	for year := r.dtstart.Year(); year <= limit.In(r.dtstart.Location()).Year(); year += r.interval {
		months := r.byMonth
		if len(months) == 0 {
			months = []time.Month{r.dtstart.Month()}
		}
		for _, month := range months {
			for _, occ := range r.yearlyCandidates(year, month) {
				if occ.Before(r.dtstart) || occ.After(limit) {
					continue
				}
				if r.matchesDateFilters(occ) && !emit(occ) {
					return
				}
			}
		}
	}
}

func (r *RRule) monthlyCandidates(year int, month time.Month) []time.Time {
	var out []time.Time
	if len(r.byDay) > 0 {
		for _, d := range r.byDay {
			if d.Ordinal == 0 {
				for day := 1; day <= daysInMonth(year, month); day++ {
					t := withClock(time.Date(year, month, day, 0, 0, 0, 0, r.dtstart.Location()), r.dtstart)
					if t.Weekday() == d.Day {
						out = append(out, t)
					}
				}
				continue
			}
			if t, ok := nthWeekdayOfMonth(year, month, d.Ordinal, d.Day, r.dtstart); ok {
				out = append(out, t)
			}
		}
		return out
	}
	if len(r.byMonthDay) > 0 {
		for _, day := range r.byMonthDay {
			if t, ok := monthDay(year, month, day, r.dtstart); ok {
				out = append(out, t)
			}
		}
		return out
	}
	if t, ok := monthDay(year, month, r.dtstart.Day(), r.dtstart); ok {
		out = append(out, t)
	}
	return out
}

func (r *RRule) yearlyCandidates(year int, month time.Month) []time.Time {
	var out []time.Time
	if len(r.byMonthDay) > 0 {
		for _, day := range r.byMonthDay {
			if t, ok := monthDay(year, month, day, r.dtstart); ok {
				out = append(out, t)
			}
		}
		return out
	}
	if len(r.byDay) > 0 {
		for _, d := range r.byDay {
			if d.Ordinal != 0 {
				if t, ok := nthWeekdayOfMonth(year, month, d.Ordinal, d.Day, r.dtstart); ok {
					out = append(out, t)
				}
			}
		}
		return out
	}
	if t, ok := monthDay(year, month, r.dtstart.Day(), r.dtstart); ok {
		out = append(out, t)
	}
	return out
}

func (r *RRule) matchesDateFilters(t time.Time) bool {
	if len(r.byMonth) > 0 {
		found := false
		for _, month := range r.byMonth {
			if t.Month() == month {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(r.byMonthDay) > 0 {
		found := false
		for _, day := range r.byMonthDay {
			actual := t.Day()
			if day < 0 {
				actual = actual - daysInMonth(t.Year(), t.Month()) - 1
			}
			if actual == day {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(r.byDay) > 0 && r.freq != Weekly && r.freq != Monthly {
		found := false
		for _, d := range r.byDay {
			if d.Ordinal == 0 && t.Weekday() == d.Day {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func parseUntil(value string, loc *time.Location) (time.Time, error) {
	layouts := []string{"20060102T150405Z", "20060102T150405", "20060102"}
	for _, layout := range layouts {
		if strings.HasSuffix(layout, "Z") {
			if t, err := time.Parse(layout, value); err == nil {
				return t, nil
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("rrule: invalid until %q", value)
}

func parseByDay(value string) ([]weekdayRule, error) {
	var out []weekdayRule
	for _, token := range strings.Split(value, ",") {
		token = strings.ToUpper(strings.TrimSpace(token))
		if len(token) < 2 {
			return nil, fmt.Errorf("invalid BYDAY token %q", token)
		}
		dayText := token[len(token)-2:]
		day, ok := parseWeekday(dayText)
		if !ok {
			return nil, fmt.Errorf("invalid BYDAY weekday %q", token)
		}
		ordinal := 0
		if prefix := token[:len(token)-2]; prefix != "" {
			n, err := strconv.Atoi(prefix)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("invalid BYDAY ordinal %q", token)
			}
			ordinal = n
		}
		out = append(out, weekdayRule{Ordinal: ordinal, Day: day})
	}
	return out, nil
}

func parseWeekday(day string) (time.Weekday, bool) {
	switch day {
	case "SU":
		return time.Sunday, true
	case "MO":
		return time.Monday, true
	case "TU":
		return time.Tuesday, true
	case "WE":
		return time.Wednesday, true
	case "TH":
		return time.Thursday, true
	case "FR":
		return time.Friday, true
	case "SA":
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func parseIntList(value string, min, max int) ([]int, error) {
	var out []int
	for _, token := range strings.Split(value, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(token))
		if err != nil || n < min || n > max || n == 0 {
			return nil, fmt.Errorf("invalid integer %q", token)
		}
		out = append(out, n)
	}
	return out, nil
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func withClock(date, clock time.Time) time.Time {
	y, m, d := date.Date()
	return time.Date(y, m, d, clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(), clock.Location())
}

func startOfWeek(t time.Time) time.Time {
	mid := midnight(t)
	offset := (int(mid.Weekday()) + 6) % 7
	return mid.AddDate(0, 0, -offset)
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func monthDay(year int, month time.Month, day int, clock time.Time) (time.Time, bool) {
	days := daysInMonth(year, month)
	if day < 0 {
		day = days + day + 1
	}
	if day < 1 || day > days {
		return time.Time{}, false
	}
	return time.Date(year, month, day, clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(), clock.Location()), true
}

func nthWeekdayOfMonth(year int, month time.Month, ordinal int, weekday time.Weekday, clock time.Time) (time.Time, bool) {
	if ordinal > 0 {
		seen := 0
		for day := 1; day <= daysInMonth(year, month); day++ {
			t := time.Date(year, month, day, clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(), clock.Location())
			if t.Weekday() != weekday {
				continue
			}
			seen++
			if seen == ordinal {
				return t, true
			}
		}
		return time.Time{}, false
	}
	seen := 0
	target := -ordinal
	for day := daysInMonth(year, month); day >= 1; day-- {
		t := time.Date(year, month, day, clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(), clock.Location())
		if t.Weekday() != weekday {
			continue
		}
		seen++
		if seen == target {
			return t, true
		}
	}
	return time.Time{}, false
}
