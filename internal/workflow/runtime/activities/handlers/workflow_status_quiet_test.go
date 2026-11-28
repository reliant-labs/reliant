package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// An automation that flaps — fails, succeeds, fails again — pages for each
// new unhealthy period. Recovery ends the earlier episode even when its
// failure is still within triggers.QuietWindow.
func TestWorkflowStatus_FlappingAutomationNotifiesOncePerQuietWindow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		gap        time.Duration // between the success and the next failure
		wantUnread bool
	}{
		{"a failure an hour after a success that followed a failure", 0, true},
		{"a failure after a quiet day", triggers.QuietWindow, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStreakFixture(t, false)
			unread, _ := f.finish(f.launch(core.TriggerEventKindSchedule, db.Active()), "failed")
			assert.True(t, unread, "the first failure notifies")
			f.finish(f.launch(core.TriggerEventKindSchedule, db.Active()), "completed")

			f.clock = f.clock.Add(tc.gap)
			unread, reason := f.finish(f.launch(core.TriggerEventKindSchedule, db.Active()), "failed")
			assert.Equal(t, tc.wantUnread, unread)
			if tc.wantUnread {
				assert.Equal(t, "workflow_failed", reason)
			}
		})
	}
}
