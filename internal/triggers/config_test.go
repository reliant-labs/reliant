// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestParseScheduleConfigCron(t *testing.T) {
	sched, err := ParseScheduleConfig(core.ScheduleConfig{
		Cron:     []string{"0 9 * * 1-5", " 30 6 * * * "},
		Timezone: "America/New_York",
	})
	if err != nil {
		t.Fatalf("ParseScheduleConfig: %v", err)
	}
	if got := sched.Spec.CronExpressions; len(got) != 2 || got[0] != "0 9 * * 1-5" || got[1] != "30 6 * * *" {
		t.Fatalf("CronExpressions = %q, want the two trimmed expressions", got)
	}
	if sched.Spec.TimeZoneName != "America/New_York" {
		t.Errorf("TimeZoneName = %q", sched.Spec.TimeZoneName)
	}
	if sched.Location.String() != "America/New_York" {
		t.Errorf("Location = %v", sched.Location)
	}
	if !sched.SkipOnOverlap() {
		t.Error("empty overlap should default to skip")
	}
	if sched.CatchupWindow != DefaultCatchupWindow {
		t.Errorf("CatchupWindow = %v, want %v", sched.CatchupWindow, DefaultCatchupWindow)
	}
}

func TestParseScheduleConfigInterval(t *testing.T) {
	sched, err := ParseScheduleConfig(core.ScheduleConfig{
		Interval:      "15m",
		Overlap:       core.ScheduleOverlapAllow,
		CatchupWindow: "2m",
	})
	if err != nil {
		t.Fatalf("ParseScheduleConfig: %v", err)
	}
	if len(sched.Spec.Intervals) != 1 || sched.Spec.Intervals[0].Every != 15*time.Minute {
		t.Fatalf("Intervals = %+v", sched.Spec.Intervals)
	}
	if sched.Spec.TimeZoneName != "UTC" {
		t.Errorf("empty timezone should mean UTC, got %q", sched.Spec.TimeZoneName)
	}
	if sched.SkipOnOverlap() {
		t.Error("overlap=allow should not skip")
	}
	if sched.CatchupWindow != 2*time.Minute {
		t.Errorf("CatchupWindow = %v", sched.CatchupWindow)
	}
}

func TestParseScheduleConfigRejects(t *testing.T) {
	tests := []struct {
		name  string
		cfg   core.ScheduleConfig
		field string
	}{
		{"no cron and no interval", core.ScheduleConfig{}, ""},
		{"empty cron entry", core.ScheduleConfig{Cron: []string{"  "}}, "cron"},
		{"six-field cron", core.ScheduleConfig{Cron: []string{"0 0 9 * * 1"}}, "cron"},
		{"cron shorthand", core.ScheduleConfig{Cron: []string{"@daily"}}, "cron"},
		{"garbage cron", core.ScheduleConfig{Cron: []string{"99 9 * * *"}}, "cron"},
		{"interval below the floor", core.ScheduleConfig{Interval: "30s"}, "interval"},
		{"interval not a duration", core.ScheduleConfig{Interval: "every hour"}, "interval"},
		{"unknown timezone", core.ScheduleConfig{Interval: "5m", Timezone: "Mars/Olympus"}, "timezone"},
		{"bad overlap", core.ScheduleConfig{Interval: "5m", Overlap: "queue"}, "overlap"},
		{"bad catchup", core.ScheduleConfig{Interval: "5m", CatchupWindow: "soon"}, "catchup_window"},
		{"negative catchup", core.ScheduleConfig{Interval: "5m", CatchupWindow: "-5m"}, "catchup_window"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseScheduleConfig(tc.cfg)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			cfgErr, ok := err.(*ConfigError)
			if !ok {
				t.Fatalf("error is %T, want *ConfigError: %v", err, err)
			}
			if cfgErr.Field != tc.field {
				t.Errorf("Field = %q, want %q (%v)", cfgErr.Field, tc.field, err)
			}
		})
	}
}

// The Temporal-level overlap policy must never drop an action: a dropped
// action leaves no trigger_events row, so there would be no record that a fire
// was due and declined. Our own skip lives in the fire activity instead.
func TestTemporalOverlapNeverDropsAnAction(t *testing.T) {
	for _, overlap := range []string{"", core.ScheduleOverlapSkip, core.ScheduleOverlapAllow} {
		sched, err := ParseScheduleConfig(core.ScheduleConfig{Interval: "5m", Overlap: overlap})
		if err != nil {
			t.Fatalf("overlap %q: %v", overlap, err)
		}
		if got := sched.TemporalOverlap(); got != enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL {
			t.Errorf("overlap %q: TemporalOverlap = %v, want ALLOW_ALL", overlap, got)
		}
	}
}

func TestSetMinIntervalForTest(t *testing.T) {
	restore := SetMinIntervalForTest(time.Second)
	if _, err := ParseScheduleConfig(core.ScheduleConfig{Interval: "2s"}); err != nil {
		t.Fatalf("2s should be accepted while the floor is lowered: %v", err)
	}
	restore()
	if _, err := ParseScheduleConfig(core.ScheduleConfig{Interval: "2s"}); err == nil {
		t.Fatal("floor was not restored")
	}
}
