// Copyright (c) 2025 Reliant Labs

// Package triggerspec validates what a trigger DECLARES — its source arm, its
// schedule, its CEL filter and its inputs mapping — independently of any
// store, scheduler or launcher.
//
// It is a leaf on purpose. The same rules are enforced in three places that
// cannot import each other: the TriggerService write path (internal/triggers
// and the gRPC handler), workflow validation of a `triggers:` block
// (internal/workflow/validation, which the launcher imports), and the agent's
// authoring tools (internal/llm/tools, which the LLM drivers import). Each
// imports this package, so a cron or filter rule has exactly one
// implementation and "valid in the editor" means "accepted by the API".
//
// It deliberately knows nothing of Temporal: internal/triggers converts a
// validated ScheduleSpec into a Temporal Schedule.
//
//forge:exclude-contract: pure validation functions over proto and db/core types, shared by packages that cannot import each other; no state, no I/O, no substitution point
package triggerspec

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// minScheduleInterval is the floor on ScheduleConfig.Interval.
//
// Every fire starts a whole agent run — a chat, a root workflow and an LLM
// loop that typically takes minutes. A sub-minute interval would therefore
// queue runs faster than they can finish, and with overlap=skip (the default)
// almost every fire would be recorded as skipped: the schedule would look busy
// and accomplish nothing. One minute is the smallest period where a single run
// has any chance of completing before the next fire.
//
// Tests that need a faster interval override it with SetMinIntervalForTest.
var minScheduleInterval = time.Minute

func minInterval() time.Duration {
	if minScheduleInterval <= 0 {
		return time.Minute
	}
	return minScheduleInterval
}

// SetMinIntervalForTest lowers the interval floor so tests can use a schedule
// that fires within a bounded wait. It returns a restore func.
func SetMinIntervalForTest(d time.Duration) func() {
	prev := minScheduleInterval
	minScheduleInterval = d
	return func() { minScheduleInterval = prev }
}

// DefaultCatchupWindow bounds how late a fire missed during an outage may
// still run. Temporal's own default is one year, which for us would mean a
// weekend of downtime replaying as a burst of stale agent runs on Monday. Ten
// minutes keeps "the server hiccuped" recoverable and treats anything longer
// as a fire that should simply be skipped.
const DefaultCatchupWindow = 10 * time.Minute

// ConfigError reports a source config that cannot be stored or turned into a
// working schedule. Handlers map it to InvalidArgument.
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

// ScheduleSpec is a validated ScheduleConfig with every default applied.
type ScheduleSpec struct {
	Cron          []string // trimmed 5-field expressions
	Intervals     []time.Duration
	Location      *time.Location
	Overlap       string // core.ScheduleOverlapSkip or core.ScheduleOverlapAllow
	CatchupWindow time.Duration
}

// ValidateSchedule checks cfg and applies its defaults.
//
// Validation happens at the write boundary rather than being left to the
// Temporal server: the server reports a cron problem as an opaque RPC error
// long after the API has told the user their trigger was created, and by then
// the row exists with no working schedule behind it.
func ValidateSchedule(cfg core.ScheduleConfig) (*ScheduleSpec, error) {
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

	spec := &ScheduleSpec{Location: loc, Overlap: overlap, CatchupWindow: catchup}

	for _, expr := range cfg.Cron {
		trimmed := strings.TrimSpace(expr)
		if trimmed == "" {
			return nil, &ConfigError{Field: "cron", Reason: "empty expression"}
		}
		if err := validateCron(trimmed); err != nil {
			return nil, err
		}
		spec.Cron = append(spec.Cron, trimmed)
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
		spec.Intervals = append(spec.Intervals, every)
	}

	// A trigger with neither is not a schedule at all; accepting it would
	// store a row that can never fire and report success for it.
	if len(spec.Cron) == 0 && len(spec.Intervals) == 0 {
		return nil, &ConfigError{Reason: "schedule needs at least one cron expression or an interval"}
	}
	return spec, nil
}

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
