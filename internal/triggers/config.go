// Copyright (c) 2025 Reliant Labs

// Package triggers converges Temporal Schedules onto the `triggers` table and
// executes a scheduled fire.
//
// The database row is the truth. A Temporal Schedule is a projection of it, so
// every write path calls Sync and startup calls SyncAll: nothing in Temporal
// is authoritative and nothing has to be repaired by hand.
//
// See research/TRIGGERS.md, "Schedules on Temporal".
package triggers

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// MinInterval is the floor on ScheduleConfig.Interval.
//
// Every fire starts a whole agent run — a chat, a root workflow and an LLM
// loop that typically takes minutes. A sub-minute interval would therefore
// queue runs faster than they can finish, and with overlap=skip (the default)
// almost every fire would be recorded as skipped: the schedule would look busy
// and accomplish nothing. One minute is the smallest period where a single run
// has any chance of completing before the next fire.
//
// Tests that need a faster interval override this; see SetMinIntervalForTest.
var MinInterval = time.Minute

func minInterval() time.Duration {
	if MinInterval <= 0 {
		return time.Minute
	}
	return MinInterval
}

// DefaultCatchupWindow bounds how late a fire missed during an outage may
// still run. Temporal's own default is one year, which for us would mean a
// weekend of downtime replaying as a burst of stale agent runs on Monday. Ten
// minutes keeps "the server hiccuped" recoverable and treats anything longer
// as a fire that should simply be skipped.
const DefaultCatchupWindow = 10 * time.Minute

// ConfigError reports a ScheduleConfig that cannot be converted into a
// Temporal Schedule. Handlers map it to InvalidArgument.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	if e.Field == "" {
		return e.Reason
	}
	return e.Field + ": " + e.Reason
}

// Schedule is a validated ScheduleConfig: the Temporal spec and policies the
// row projects to, plus the location its times are expressed in (which the
// fire path needs to render the run's title).
type Schedule struct {
	Spec          client.ScheduleSpec
	Overlap       string // our overlap policy: core.ScheduleOverlapSkip or ...Allow
	CatchupWindow time.Duration
	Location      *time.Location
}

// ParseScheduleConfig validates cfg and converts it to Temporal's shape.
//
// Validation happens here, at the write boundary, rather than being left to
// the Temporal server: the server reports a cron problem as an opaque RPC
// error long after the API has told the user their trigger was created, and by
// then the row exists with no working schedule behind it.
func ParseScheduleConfig(cfg core.ScheduleConfig) (*Schedule, error) {
	loc, err := loadLocation(cfg.Timezone)
	if err != nil {
		return nil, err
	}

	overlap, err := parseOverlap(cfg.Overlap)
	if err != nil {
		return nil, err
	}

	catchup := DefaultCatchupWindow
	if cfg.CatchupWindow != "" {
		d, err := time.ParseDuration(cfg.CatchupWindow)
		if err != nil {
			return nil, &ConfigError{Field: "catchup_window", Reason: "not a Go duration: " + cfg.CatchupWindow}
		}
		if d <= 0 {
			return nil, &ConfigError{Field: "catchup_window", Reason: "must be positive"}
		}
		catchup = d
	}

	spec := client.ScheduleSpec{TimeZoneName: loc.String()}

	for _, expr := range cfg.Cron {
		trimmed := strings.TrimSpace(expr)
		if trimmed == "" {
			return nil, &ConfigError{Field: "cron", Reason: "empty expression"}
		}
		if err := validateCron(trimmed); err != nil {
			return nil, err
		}
		spec.CronExpressions = append(spec.CronExpressions, trimmed)
	}

	if cfg.Interval != "" {
		every, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			return nil, &ConfigError{Field: "interval", Reason: "not a Go duration: " + cfg.Interval}
		}
		if every < minInterval() {
			return nil, &ConfigError{
				Field:  "interval",
				Reason: fmt.Sprintf("must be at least %s, got %s", minInterval(), every),
			}
		}
		spec.Intervals = append(spec.Intervals, client.ScheduleIntervalSpec{Every: every})
	}

	// A trigger with neither is not a schedule at all; accepting it would
	// store a row that can never fire and report success for it.
	if len(spec.CronExpressions) == 0 && len(spec.Intervals) == 0 {
		return nil, &ConfigError{Reason: "schedule needs at least one cron expression or an interval"}
	}

	return &Schedule{Spec: spec, Overlap: overlap, CatchupWindow: catchup, Location: loc}, nil
}

// TemporalOverlap is the overlap policy for the Temporal Schedule itself.
//
// Always ALLOW_ALL, deliberately, whatever OUR overlap policy is. The schedule
// starts TriggerFireWorkflow, a short bookkeeping workflow — not the agent
// run — so two fires overlapping at the Temporal level is harmless. Every
// other policy DROPS or DEFERS an action, and a dropped action leaves no
// trigger_events row at all, so there would be no record that a fire was due
// and declined.
//
// Our skip/allow decision is about the CHAT run and lives in the fire
// activity, where it can read the previous run's state and record a `skipped`
// event. See Fire.
func (s *Schedule) TemporalOverlap() enumspb.ScheduleOverlapPolicy {
	return enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL
}

// SkipOnOverlap reports whether a fire must stand down while this trigger's
// previous run is still live.
func (s *Schedule) SkipOnOverlap() bool { return s.Overlap == core.ScheduleOverlapSkip }

// loadLocation resolves the configured IANA zone; empty means UTC.
func loadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, &ConfigError{Field: "timezone", Reason: "unknown IANA time zone: " + name}
	}
	return loc, nil
}

func parseOverlap(overlap string) (string, error) {
	switch overlap {
	case "", core.ScheduleOverlapSkip:
		return core.ScheduleOverlapSkip, nil
	case core.ScheduleOverlapAllow:
		return core.ScheduleOverlapAllow, nil
	default:
		return "", &ConfigError{Field: "overlap", Reason: `must be "skip" or "allow", got ` + overlap}
	}
}

// validateCron accepts exactly the 5-field form.
//
// Temporal's own parser also takes 6 and 7 fields and @every shorthands, but
// a wider surface here would mean the API accepts expressions whose meaning
// depends on which parser read them — a 6-field string is minute-first to
// Temporal and second-first to most cron implementations people are used to.
// One documented shape, rejected precisely, beats a silent reinterpretation.
func validateCron(expr string) error {
	if n := len(strings.Fields(expr)); n != 5 {
		return &ConfigError{
			Field:  "cron",
			Reason: fmt.Sprintf("want 5 fields (minute hour day-of-month month day-of-week), got %d in %q", n, expr),
		}
	}
	if _, err := cron.ParseStandard(expr); err != nil {
		return &ConfigError{Field: "cron", Reason: fmt.Sprintf("invalid expression %q: %v", expr, err)}
	}
	return nil
}
