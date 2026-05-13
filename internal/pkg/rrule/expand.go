package rrule

import (
	"time"

	teambitionrrule "github.com/teambition/rrule-go"
)

func Expand(rule string, dtstart time.Time, exdates []time.Time, from, to time.Time) ([]time.Time, error) {
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
