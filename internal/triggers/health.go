// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// HealthWindow is how many of a trigger's newest firings health is read from.
const HealthWindow = 10

// EpisodeFirings bounds how many firings are read to size a failure episode.
const EpisodeFirings = 500

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
			skipStreak = false
		case resultSuccess:
			anySuccess = true
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
	health.ConsecutiveFailures = FailureStreak(firings).Count

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

// GapWindow is how long a polled source's lost place (a re-baseline that
// skipped what arrived meanwhile) keeps the trigger DEGRADED.
const GapWindow = 24 * time.Hour

// WithSource folds a polled trigger's source state into health computed from
// its firings. A source that cannot deliver produces no firings at all, so
// firings alone would read a dead source as healthy (or never-fired) forever:
//
//   - needs_reauth: FAILING. The connection must be reconnected before any
//     event can arrive; the detail says so. Nothing else is worth reading.
//   - error: at least DEGRADED, with the poll's error as the detail. Polls
//     are retried on their schedule; a transient failure clears itself.
//   - a gap within GapWindow: at least DEGRADED, detail naming what was lost.
//
// A worse status from the firings stands, and so does its detail. reg nil
// (not polled, or never polled) leaves health unchanged.
func WithSource(health *reliantv1.TriggerHealth, reg *core.TriggerRegistration, now time.Time) *reliantv1.TriggerHealth {
	if health == nil || reg == nil {
		return health
	}
	degrade := func(detail string) {
		if health.Status == reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING {
			return
		}
		health.Status = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED
		if health.LastFailureDetail == "" {
			health.LastFailureDetail = detail
		}
	}
	switch reg.Status {
	case core.TriggerRegistrationNeedsReauth:
		health.Status = reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING
		detail := reg.StatusDetail
		if detail == "" {
			detail = "the trigger's connection needs to be reconnected"
		}
		health.LastFailureDetail = "Not receiving events: " + detail
		return health
	case core.TriggerRegistrationError:
		detail := reg.StatusDetail
		if detail == "" {
			detail = "the last poll failed"
		}
		degrade("Polling failed: " + detail)
	}
	if reg.LastGapAt != nil && now.Sub(*reg.LastGapAt) < GapWindow {
		detail := reg.LastGapDetail
		if detail == "" {
			detail = "the source lost its place and restarted from the present"
		}
		degrade(fmt.Sprintf("Events were missed (%s): %s", reg.LastGapAt.UTC().Format("2006-01-02 15:04 MST"), detail))
	}
	return health
}

// resolveFiring reduces one firing to a result, with a description when it is
// a failure. A launched run that ended FAILED is a failure: the launch worked
// and the automation still did not.
//
// The run is the one the trigger FIRED. Once that run has ended its result is
// recorded on the event (RunStatus) and wins over the chat's live state, which
// a person replying in the chat moves on to their own turns: their failure is
// not the automation failing, and their success does not end its streak. The
// live state is read only while the fired run is still going, or for an event
// recorded before RunStatus existed.
func resolveFiring(f *core.TriggerEventWithRun) (firingResult, string) {
	if f.Event.Outcome == core.TriggerEventLaunched && f.Event.RunStatus != "" {
		return resolveRecordedRun(f)
	}
	switch f.Event.Outcome {
	case core.TriggerEventSkipped:
		if f.Event.Kind.IsInbound() {
			// An event trigger's skip is its filter declining an event that
			// was not for it — the trigger working, not standing down.
			return resultUnresolved, ""
		}
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

// resolveRecordedRun reads a launched firing whose fired run has ended.
func resolveRecordedRun(f *core.TriggerEventWithRun) (firingResult, string) {
	switch f.Event.RunStatus {
	case core.TriggerRunCompleted:
		return resultSuccess, ""
	case core.TriggerRunFailed:
		if f.Run == nil || f.Run.Title == "" {
			return resultFailure, "the launched run failed"
		}
		return resultFailure, fmt.Sprintf("the launched run %q failed", f.Run.Title)
	}
	return resultUnresolved, ""
}

// Streak is a trigger's current run of consecutive failures, newest-first
// input, ended by the first success. Skipped and still-running firings neither
// extend nor end it, exactly as in ComputeHealth.
type Streak struct {
	// Count is how many failures the streak holds.
	Count int32
	// First is the OLDEST failure of the streak: the episode's identity. A
	// later failure of the same streak leaves it unchanged, which is what keeps
	// one Inbox item (and one dismissal) per episode. Nil when Count is 0.
	First *core.TriggerEventWithRun
	// Newest is the most recent failure. Nil when Count is 0.
	Newest *core.TriggerEventWithRun
}

// FailureStreak reads the failure streak from newest-first firings. Unlike
// ComputeHealth it is NOT limited to HealthWindow: callers pass the firings
// since the last success, so a long outage is one episode, not a sliding one.
func FailureStreak(firings []*core.TriggerEventWithRun) Streak {
	var streak Streak
	for _, f := range firings {
		result, _ := resolveFiring(f)
		if result == resultSuccess {
			break
		}
		if result != resultFailure {
			continue
		}
		streak.Count++
		streak.First = f
		if streak.Newest == nil {
			streak.Newest = f
		}
	}
	return streak
}

// PriorFailureStreak is the streak of firings OLDER than the one launching
// chatID, and reports whether chatID's firing was found. It answers "was the
// run that just failed the first of its streak?" without counting itself.
func PriorFailureStreak(firings []*core.TriggerEventWithRun, chatID string) (Streak, bool) {
	for i, f := range firings {
		if f.Event != nil && f.Event.ChatID != nil && *f.Event.ChatID == chatID {
			return FailureStreak(firings[i+1:]), true
		}
	}
	return Streak{}, false
}
