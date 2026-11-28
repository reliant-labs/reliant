package prpolicy

import (
	"testing"
	"time"
)

func TestComputePriority(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	open := &LinkedIncident{Source: IncidentSourceProdAlert, State: IncidentStateOpen, Severity: IncidentSeverityLow}
	resolved := &LinkedIncident{Source: IncidentSourceProdAlert, State: IncidentStateResolved, Severity: IncidentSeverityCritical}
	cases := []struct {
		name   string
		in     PriorityInputs
		want   int
		source PrioritySource
	}{
		{"default, not enqueued", PriorityInputs{Now: now}, 50, PrioritySourceDefault},
		{"default, just enqueued", PriorityInputs{EnqueuedAt: now, Now: now}, 50, PrioritySourceDefault},
		{"under an hour does not age", PriorityInputs{EnqueuedAt: ago(59 * time.Minute), Now: now}, 50, PrioritySourceDefault},
		{"enqueued in the future does not age", PriorityInputs{EnqueuedAt: now.Add(time.Hour), Now: now}, 50, PrioritySourceDefault},
		{"one hour", PriorityInputs{EnqueuedAt: ago(time.Hour), Now: now}, 51, PrioritySourceAging},
		{"whole hours only", PriorityInputs{EnqueuedAt: ago(3*time.Hour + 59*time.Minute), Now: now}, 53, PrioritySourceAging},
		{"aging reaches the cap", PriorityInputs{EnqueuedAt: ago(20 * time.Hour), Now: now}, 70, PrioritySourceAging},
		{"aging stops at the cap", PriorityInputs{EnqueuedAt: ago(500 * time.Hour), Now: now}, 70, PrioritySourceAging},
		{"release", PriorityInputs{Release: true, EnqueuedAt: ago(500 * time.Hour), Now: now}, 80, PrioritySourceRelease},
		{"incident fix at any severity", PriorityInputs{Incident: open, Release: true, Now: now}, 100, PrioritySourceIncidentFix},
		{"resolved incident's fix is ordinary", PriorityInputs{Incident: resolved, EnqueuedAt: ago(2 * time.Hour), Now: now}, 52, PrioritySourceAging},
		{"revert", PriorityInputs{Revert: true, Incident: open, Release: true, Now: now}, 100, PrioritySourceRevert},
		{"decision overrides everything", PriorityInputs{Decision: &PriorityDecision{Value: 95}, Revert: true, Incident: open, Now: now}, 95, PrioritySourceDecision},
		{"decision may lower", PriorityInputs{Decision: &PriorityDecision{Value: 10}, Revert: true, Now: now}, 10, PrioritySourceDecision},
		{"decision beats aging", PriorityInputs{Decision: &PriorityDecision{Value: 50}, EnqueuedAt: ago(10 * time.Hour), Now: now}, 50, PrioritySourceDecision},
		{"decision clamped high", PriorityInputs{Decision: &PriorityDecision{Value: 1000}, Now: now}, 100, PrioritySourceDecision},
		{"decision clamped low", PriorityInputs{Decision: &PriorityDecision{Value: -5}, Now: now}, 0, PrioritySourceDecision},
	}
	for _, tc := range cases {
		got, src := ComputePriority(tc.in)
		if got != tc.want || src != tc.source {
			t.Errorf("%s: ComputePriority = %d (%s), want %d (%s)", tc.name, got, src, tc.want, tc.source)
		}
	}
}

func TestPriorityUrgency(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	if p, _ := ComputePriority(PriorityInputs{EnqueuedAt: now.Add(-1000 * time.Hour), Now: now}); IsUrgent(p) {
		t.Errorf("a long wait made an ordinary PR urgent (%d)", p)
	}
	if p, _ := ComputePriority(PriorityInputs{Release: true, Now: now}); IsUrgent(p) {
		t.Errorf("a release PR is urgent (%d)", p)
	}
	// An emergency-eligible PR (the fix of an open high incident) waits in
	// the normal queue at 100 while its request is decided (design §6.1).
	high := &LinkedIncident{Source: IncidentSourceVuln, State: IncidentStateOpen, Severity: IncidentSeverityHigh}
	if p, _ := ComputePriority(PriorityInputs{Incident: high, Now: now}); p != 100 || !IsUrgent(p) {
		t.Errorf("an incident fix = %d, want 100 and urgent", p)
	}
	if IsUrgent(PriorityUrgent-1) || !IsUrgent(PriorityUrgent) {
		t.Errorf("IsUrgent threshold is not %d", PriorityUrgent)
	}
}
