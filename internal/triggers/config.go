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
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// The rules for what a schedule may declare live in triggerspec, which
// workflow validation and the agent's tools share with this package. What
// stays here is the Temporal half: turning a validated spec into a Schedule.

// DefaultCatchupWindow bounds how late a fire missed during an outage may
// still run; see triggerspec.DefaultCatchupWindow.
const DefaultCatchupWindow = triggerspec.DefaultCatchupWindow

// ConfigError reports a ScheduleConfig that cannot be converted into a
// Temporal Schedule. Handlers map it to InvalidArgument.
type ConfigError = triggerspec.ConfigError

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
func ParseScheduleConfig(cfg core.ScheduleConfig) (*Schedule, error) {
	valid, err := triggerspec.ValidateSchedule(cfg)
	if err != nil {
		return nil, err
	}
	spec := client.ScheduleSpec{TimeZoneName: valid.Location.String(), CronExpressions: valid.Cron}
	for _, every := range valid.Intervals {
		spec.Intervals = append(spec.Intervals, client.ScheduleIntervalSpec{Every: every})
	}
	return &Schedule{Spec: spec, Overlap: valid.Overlap, CatchupWindow: valid.CatchupWindow, Location: valid.Location}, nil
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
