package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MinScheduleInterval is the shortest `every` interval: the engine's
// schedules fire at most once a minute.
const MinScheduleInterval = time.Minute

const ifMainMovedSuffix = " if main moved"

// Schedule is a parsed schedule field: exactly one of Cron and Every is set.
type Schedule struct {
	// Cron is a 5-field cron line (minute hour day-of-month month
	// day-of-week), evaluated in UTC.
	Cron string
	// Every is a fixed interval.
	Every time.Duration
	// IfMainMoved skips a firing when the base branch has not moved since
	// the last run. Release schedules only.
	IfMainMoved bool
}

// String renders s in the form ParseSchedule reads.
func (s Schedule) String() string {
	out := s.Cron
	if s.Every > 0 {
		out = "every " + formatDuration(s.Every)
	}
	if s.IfMainMoved {
		out += ifMainMovedSuffix
	}
	return out
}

// ParseSchedule reads `<cron>` or `every <duration>`, either optionally
// followed by ` if main moved`. The cron form takes exactly five fields, the
// shape the workflow engine's schedule triggers accept (no seconds field, no
// @descriptors, no CRON_TZ prefix): one documented shape whose meaning does
// not depend on which parser reads it.
func ParseSchedule(s string) (Schedule, error) {
	text := strings.Join(strings.Fields(s), " ")
	if text == "" {
		return Schedule{}, fmt.Errorf("is empty: write a 5-field cron line such as \"0 6 * * *\", or \"every 2h\"")
	}
	var out Schedule
	if rest, ok := strings.CutSuffix(text, ifMainMovedSuffix); ok {
		out.IfMainMoved = true
		text = rest
	}
	if rest, ok := strings.CutPrefix(text, "every "); ok {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return Schedule{}, fmt.Errorf("%q: `every` takes a duration with a unit, such as 2h or 30m", s)
		}
		if d < MinScheduleInterval {
			return Schedule{}, fmt.Errorf("%q: the shortest interval is %s", s, formatDuration(MinScheduleInterval))
		}
		out.Every = d
		return out, nil
	}
	if n := len(strings.Fields(text)); n != 5 {
		return Schedule{}, fmt.Errorf("%q: want a 5-field cron line (minute hour day-of-month month day-of-week) or \"every <duration>\", got %d fields", s, n)
	}
	if _, err := cron.ParseStandard(text); err != nil {
		return Schedule{}, fmt.Errorf("%q: invalid cron line: %v", s, err)
	}
	out.Cron = text
	return out, nil
}

// formatDuration drops the zero units time.Duration.String keeps ("2h0m0s"
// becomes "2h").
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
