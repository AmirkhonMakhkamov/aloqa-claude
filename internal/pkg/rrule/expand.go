package rrule

import (
	"strings"
	"time"

	teambitionrrule "github.com/teambition/rrule-go"
)

func Expand(rule string, dtstart time.Time, exdates []time.Time, from, to time.Time) ([]time.Time, error) {
	rule, err := normalizeFloatingUntil(rule, dtstart.Location())
	if err != nil {
		return nil, err
	}
	parsed, err := teambitionrrule.StrToRRule(rule)
	if err != nil {
		return nil, err
	}
	parsed.DTStart(dtstart)

	occurrences := parsed.Between(from, to, true)
	if len(occurrences) == 0 || len(exdates) == 0 {
		return occurrences, nil
	}

	excluded := make(map[int64]struct{}, len(exdates))
	for _, exdate := range exdates {
		excluded[exdate.UTC().UnixNano()] = struct{}{}
	}

	filtered := occurrences[:0]
	for _, occurrence := range occurrences {
		if _, ok := excluded[occurrence.UTC().UnixNano()]; ok {
			continue
		}
		filtered = append(filtered, occurrence)
	}
	return filtered, nil
}

func normalizeFloatingUntil(rule string, loc *time.Location) (string, error) {
	parts := strings.Split(rule, ";")
	for i, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "UNTIL") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasSuffix(strings.ToUpper(value), "Z") || !strings.Contains(value, "T") {
			continue
		}
		until, err := time.ParseInLocation("20060102T150405", value, loc)
		if err != nil {
			return "", err
		}
		parts[i] = strings.TrimSpace(key) + "=" + until.UTC().Format("20060102T150405Z")
	}
	return strings.Join(parts, ";"), nil
}
