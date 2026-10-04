// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// HealthWindow is how many of a trigger's newest firings health is read from.
const HealthWindow = 10

// skipStreakDegraded is the run of consecutive skipped firings that marks a
// trigger DEGRADED: it is firing but never launching.
const skipStreakDegraded = 3

type firingResult int

const (
	resultUnresolved firingResult = iota // run still going, cancelled, or chat gone
	resultSuccess
	resultFailure
	resultSkipped
)

// ComputeHealth derives a trigger's health from its newest-first firings. The
// rule is documented on reliantv1.TriggerHealthStatus; this is its only
// implementation.
func ComputeHealth(firings []*core.TriggerEventWithRun) *reliantv1.TriggerHealth {
	if len(firings) > HealthWindow {
		firings = firings[:HealthWindow]
	}

	health := &reliantv1.TriggerHealth{Status: reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_UNKNOWN}
	var (
		anyFailure  bool
		anySuccess  bool
		failStreak  = true // still counting consecutive failures
		skipStreak  = true
		lastFailure string
		haveFailure bool
	)
	for _, f := range firings {
		result, detail := resolveFiring(f)
		switch result {
		case resultFailure:
			anyFailure = true
			if !haveFailure {
				lastFailure, haveFailure = detail, true
			}
			if failStreak {
				health.ConsecutiveFailures++
			}
			skipStreak = false
		case resultSuccess:
			anySuccess = true
			failStreak = false
			skipStreak = false
		case resultSkipped:
			if skipStreak {
				health.ConsecutiveSkips++
			}
		case resultUnresolved:
			skipStreak = false
		}
	}
	health.LastFailureDetail = lastFailure

	switch {
	case health.ConsecutiveFailures >= 2:
		health.Status = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING
	case anyFailure || health.ConsecutiveSkips >= skipStreakDegraded:
		health.Status = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED
	case anySuccess:
		health.Status = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_HEALTHY
	}
	return health
}

// resolveFiring reduces one firing to a result, with a description when it is
// a failure. A launched run that ended FAILED is a failure: the launch worked
// and the automation still did not.
func resolveFiring(f *core.TriggerEventWithRun) (firingResult, string) {
	switch f.Event.Outcome {
	case core.TriggerEventSkipped:
		return resultSkipped, ""
	case core.TriggerEventFailed:
		detail := f.Event.OutcomeDetail
		if detail == "" {
			detail = "the run could not be launched"
		}
		return resultFailure, detail
	case core.TriggerEventLaunched:
		if f.Run == nil {
			return resultUnresolved, ""
		}
		switch f.Run.DisplayState {
		case core.RunDisplayFailed:
			title := f.Run.Title
			if title == "" {
				return resultFailure, "the launched run failed"
			}
			return resultFailure, fmt.Sprintf("the launched run %q failed", title)
		case core.RunDisplayCompleted:
			return resultSuccess, ""
		}
	}
	return resultUnresolved, ""
}

// NewestFailure returns the newest firing in the window that counts as a
// failure under ComputeHealth's rules, or nil when none does. Callers key
// "this failure" on its event id, so a newer failure is a different one.
func NewestFailure(firings []*core.TriggerEventWithRun) *core.TriggerEventWithRun {
	if len(firings) > HealthWindow {
		firings = firings[:HealthWindow]
	}
	for _, f := range firings {
		if result, _ := resolveFiring(f); result == resultFailure {
			return f
		}
	}
	return nil
}
