package triggers

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestHabitual(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := start.Add(d); return &v }
	for _, tc := range []struct {
		name  string
		prior *time.Time
		want  bool
	}{
		{"no failure before: the automation was healthy", nil, false},
		{"a failure an hour before: it flaps", at(-time.Hour), true},
		{"a failure exactly a quiet window before", at(-QuietWindow), true},
		{"a failure longer ago than the quiet window", at(-QuietWindow - time.Second), false},
		{"a failure at the start is the streak's own", at(0), false},
		{"a failure after the start is not before it", at(time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Habitual(start, tc.prior); got != tc.want {
				t.Errorf("Habitual = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFiringOf(t *testing.T) {
	chat := func(id string) *string { return &id }
	firings := []*core.TriggerEventWithRun{
		{Event: &core.TriggerEvent{ID: "ev-3", ChatID: chat("c-3")}},
		{Event: &core.TriggerEvent{ID: "ev-2"}},
		{Event: &core.TriggerEvent{ID: "ev-1", ChatID: chat("c-1")}},
	}
	if got := FiringOf(firings, "c-1"); got == nil || got.Event.ID != "ev-1" {
		t.Errorf("FiringOf(c-1) = %v, want ev-1", got)
	}
	if got := FiringOf(firings, "c-9"); got != nil {
		t.Errorf("FiringOf(c-9) = %v, want nil", got.Event.ID)
	}
}
