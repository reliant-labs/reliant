package testreport

import (
	"math"
	"sort"
	"time"
)

// State is a test's flake state.
type State string

// The states.
const (
	// StateHealthy: no confirmed event, or a suspected test's events have
	// gone quiet.
	StateHealthy State = "healthy"
	// StateSuspected: one confirmed event. The actionable state where a
	// flaky test is deleted on its first flake.
	StateSuspected State = "suspected"
	// StateFlaky: confirmed events on two or more distinct commits within
	// 30 days.
	StateFlaky State = "flaky"
	// StateFixed: a flaky test whose file changed after its last event,
	// followed by 14 days with no event.
	StateFixed State = "fixed"
	// StateQuarantined: a quarantine entry for the test exists on the
	// default branch.
	StateQuarantined State = "quarantined"
	// StateRemoved: a suspected or flaky test that no longer appears in its
	// lane's runs (deleted, as the policy asks).
	StateRemoved State = "removed"
)

// The flake state machine's constants.
const (
	// FlakeWindow is how far back distinct-commit events are counted.
	FlakeWindow = 30 * 24 * time.Hour
	// QuietPeriod is how long a test must go without an event to leave
	// suspected (to healthy) or, after its file changed, flaky (to fixed).
	QuietPeriod = 14 * 24 * time.Hour
	// FlakyCommits is how many distinct commits with a confirmed event
	// within FlakeWindow make a test flaky.
	FlakyCommits = 2
)

// History is everything [FlakeState] reads about one top-level test.
type History struct {
	// Events are the test's events. Only VerdictConfirmed ones count, and a
	// commit counts once however many runs on it flaked (a same-commit
	// burst is one event).
	Events []FlakeEvent
	// FileChanges are the times the test's file changed on the default
	// branch.
	FileChanges []time.Time
	// Quarantined is set when a quarantine entry for the test exists on the
	// default branch.
	Quarantined bool
	// Absent is set when the test did not appear in the most recent
	// completed default-branch run of its lane.
	Absent bool
}

// FlakeStatus is a test's state at a point in time.
type FlakeStatus struct {
	State State `json:"state"`
	// Since is when the test entered State; zero for a test that was never
	// anything but healthy, and for quarantined and removed (which the
	// history does not date).
	Since time.Time `json:"since"`
	// Events30d counts distinct commits with a confirmed event in the
	// FlakeWindow before now (since the test was last fixed).
	Events30d int       `json:"events_30d"`
	LastEvent time.Time `json:"last_event"`
}

// FlakeState computes a test's state at now from its history, replaying the
// events in time order:
//
//   - an event makes a test suspected, or flaky when the FlakeWindow ending
//     at it holds events on FlakyCommits distinct commits;
//   - suspected becomes healthy QuietPeriod after its last event;
//   - flaky stays flaky until the test's file changes after its last event
//     and QuietPeriod passes from that change with no event: then fixed, and
//     events before the fix no longer count;
//   - a quarantine entry makes it quarantined, whatever else is true; a
//     suspected or flaky test that is absent from its lane is removed.
//
// It is a pure function: the same history and now give the same state.
func FlakeState(h History, now time.Time) FlakeStatus {
	evs := confirmedByCommit(h.Events, now)
	fcs := make([]time.Time, 0, len(h.FileChanges))
	for _, t := range h.FileChanges {
		if !t.After(now) {
			fcs = append(fcs, t)
		}
	}
	sort.Slice(fcs, func(i, j int) bool { return fcs[i].Before(fcs[j]) })

	st := FlakeStatus{State: StateHealthy}
	var epoch time.Time // events at or before it no longer count (last fix)
	var counted []FlakeEvent
	decay := func(t time.Time) {
		switch st.State {
		case StateSuspected:
			if quiet := st.LastEvent.Add(QuietPeriod); !t.Before(quiet) {
				st.State, st.Since = StateHealthy, quiet
			}
		case StateFlaky:
			for _, fc := range fcs {
				if !fc.After(st.LastEvent) {
					continue
				}
				if fixed := fc.Add(QuietPeriod); !t.Before(fixed) {
					st.State, st.Since = StateFixed, fixed
					epoch, counted = fixed, nil
				}
				break
			}
		}
	}
	for _, e := range evs {
		decay(e.At)
		if !e.At.After(epoch) && !epoch.IsZero() {
			continue
		}
		counted = append(counted, e)
		st.LastEvent = e.At
		switch {
		case st.State == StateFlaky:
		case distinctCommits(counted, e.At) >= FlakyCommits:
			st.State, st.Since = StateFlaky, e.At
		case st.State != StateSuspected:
			st.State, st.Since = StateSuspected, e.At
		}
	}
	decay(now)
	st.Events30d = distinctCommits(counted, now)
	switch {
	case h.Quarantined:
		st.State, st.Since = StateQuarantined, time.Time{}
	case h.Absent && (st.State == StateSuspected || st.State == StateFlaky):
		st.State, st.Since = StateRemoved, time.Time{}
	}
	return st
}

// confirmedByCommit keeps the confirmed events at or before now, one per
// commit (the earliest), in time order.
func confirmedByCommit(events []FlakeEvent, now time.Time) []FlakeEvent {
	first := map[string]FlakeEvent{}
	for _, e := range events {
		if e.Verdict != VerdictConfirmed || e.At.After(now) {
			continue
		}
		k := e.Commit
		if k == "" {
			k = "run:" + e.Run
		}
		if have, ok := first[k]; !ok || e.At.Before(have.At) {
			first[k] = e
		}
	}
	out := make([]FlakeEvent, 0, len(first))
	for _, e := range first {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Commit < out[j].Commit
	})
	return out
}

// distinctCommits counts the events in the FlakeWindow ending at t. The
// events are already one per commit.
func distinctCommits(events []FlakeEvent, t time.Time) int {
	n := 0
	for _, e := range events {
		if !e.At.After(t) && t.Sub(e.At) < FlakeWindow {
			n++
		}
	}
	return n
}

// WilsonLowerBound is the lower bound of the 95% Wilson score interval for k
// successes in n trials: the flake rate shown with its n, which keeps one
// event in two runs from reading as 50%. 0 when n is not positive.
func WilsonLowerBound(k, n int) float64 {
	if n <= 0 {
		return 0
	}
	k = min(max(k, 0), n)
	const z = 1.959963984540054
	p := float64(k) / float64(n)
	nf := float64(n)
	den := 1 + z*z/nf
	centre := p + z*z/(2*nf)
	margin := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return math.Max(0, (centre-margin)/den)
}
