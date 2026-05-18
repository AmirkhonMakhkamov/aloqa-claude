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
