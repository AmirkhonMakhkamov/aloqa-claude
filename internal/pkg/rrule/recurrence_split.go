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
