package triggers

import (
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// QuietWindow is how long an automation must go without a failure for its
// next one to be news. An automation that flaps — fails, succeeds, fails
// again within the hour — begins a new failure streak each time, and each
// streak used to be a new Inbox item and a new notification. Only a failure
// with no other failure of the same automation in the QuietWindow before it
// is a transition into failing; a streak that begins sooner is habitual
// (Habitual): the automation is still unhealthy, and it has already been said.
const QuietWindow = 24 * time.Hour

// Habitual reports whether a failure streak that began at start repeats an
// unhealthy period already reported: the automation's newest failed firing
// before start (priorFailure, nil when there was none) is within QuietWindow
// of it. A prior failure at or after start is not before it, and does not
// count.
func Habitual(start time.Time, priorFailure *time.Time) bool {
	if priorFailure == nil || !priorFailure.Before(start) {
		return false
	}
	return start.Sub(*priorFailure) <= QuietWindow
}

// FiringOf is the firing that launched chatID among a trigger's firings, nil
// when none of them did.
func FiringOf(firings []*core.TriggerEventWithRun, chatID string) *core.TriggerEventWithRun {
	for _, f := range firings {
		if f.Event != nil && f.Event.ChatID != nil && *f.Event.ChatID == chatID {
			return f
		}
	}
	return nil
}
