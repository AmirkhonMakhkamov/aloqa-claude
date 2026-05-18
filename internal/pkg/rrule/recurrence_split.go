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
