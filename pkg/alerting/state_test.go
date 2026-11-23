// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func ser(v float64, svc string) series {
	l := map[string]string{"service": svc, "alertname": "slow"}
	return series{fp: fingerprint(RuleKey{"t", "e", "slow"}, l), labels: l, value: v}
}

func rule(forD time.Duration) Rule {
	return Rule{RuleKey: RuleKey{"t", "e", "slow"}, For: forD, Interval: time.Minute}
}

// step advances and applies the result to a map the way a store would.
func step(t *testing.T, r Rule, state *[]Instance, ss []series, at time.Time) Changes {
	t.Helper()
	after, ch := advance(r, *state, ss, at)
	*state = after
	return ch
}

func transitions(ch Changes) []Transition {
	var out []Transition
	for _, e := range ch.Events {
		out = append(out, e.Transition)
	}
	return out
}

func only(t *testing.T, s []Instance) Instance {
	t.Helper()
	if len(s) != 1 {
		t.Fatalf("want 1 instance, have %d: %+v", len(s), s)
	}
	return s[0]
}

func TestPendingThenFiringAfterFor(t *testing.T) {
	var st []Instance
	r := rule(5 * time.Minute)
	ch := step(t, r, &st, []series{ser(900, "api")}, t0)
	if got := only(t, st); got.State != StatePending || !got.ActiveAt.Equal(t0) {
		t.Fatalf("first sight: %+v", got)
	}
	if tr := transitions(ch); len(tr) != 1 || tr[0] != TransitionPending {
		t.Fatalf("events = %v", tr)
	}
	step(t, r, &st, []series{ser(900, "api")}, t0.Add(4*time.Minute))
	if only(t, st).State != StatePending {
		t.Fatal("fired before `for` elapsed")
	}
	ch = step(t, r, &st, []series{ser(950, "api")}, t0.Add(5*time.Minute))
	got := only(t, st)
	if got.State != StateFiring || !got.FiringAt.Equal(t0.Add(5*time.Minute)) || got.LastValue != 950 {
		t.Fatalf("not firing: %+v", got)
	}
	if tr := transitions(ch); len(tr) != 1 || tr[0] != TransitionFiring {
		t.Fatalf("events = %v", tr)
	}
	if ch = step(t, r, &st, []series{ser(950, "api")}, t0.Add(6*time.Minute)); len(ch.Events) != 0 {
		t.Fatalf("a steady firing alert must not write events: %v", transitions(ch))
	}
}

func TestZeroForFiresImmediately(t *testing.T) {
	var st []Instance
	ch := step(t, rule(0), &st, []series{ser(1, "api")}, t0)
	if only(t, st).State != StateFiring {
		t.Fatal("for=0 must fire on first sight")
	}
	if tr := transitions(ch); len(tr) != 1 || tr[0] != TransitionFiring {
		t.Fatalf("events = %v (a for=0 alert has no pending phase to record)", tr)
	}
}

func TestPendingThatClearsIsDroppedSilently(t *testing.T) {
	var st []Instance
	r := rule(5 * time.Minute)
	step(t, r, &st, []series{ser(1, "api")}, t0)
	ch := step(t, r, &st, nil, t0.Add(time.Minute))
	if len(st) != 0 || len(ch.Delete) != 1 {
		t.Fatalf("state %+v delete %v", st, ch.Delete)
	}
	if tr := transitions(ch); len(tr) != 1 || tr[0] != TransitionInactive {
		t.Fatalf("events = %v", tr)
	}
	if alerts, _ := alertsFor(st, time.Minute, t0); len(alerts) != 0 {
		t.Fatal("nothing was ever sent for a pending alert, so nothing may be sent for its clearing")
	}
}

func TestFlapDampingHoldsResolutionOneEvaluation(t *testing.T) {
	var st []Instance
	r := rule(0)
	step(t, r, &st, []series{ser(1, "api")}, t0)

	ch := step(t, r, &st, nil, t0.Add(time.Minute))
	got := only(t, st)
	if got.State != StateFiring || got.ClearedAt.IsZero() {
		t.Fatalf("first miss must hold the alert firing: %+v", got)
	}
	if len(ch.Events) != 0 {
		t.Fatalf("first miss must record no transition: %v", transitions(ch))
	}
	if alerts, _ := alertsFor(st, time.Minute, t0.Add(time.Minute)); len(alerts) != 1 || alerts[0].EndsAt.Before(t0.Add(time.Minute)) {
		t.Fatal("a held alert must keep being re-sent")
	}

	// it comes back: the hold is cleared and nothing resolved.
	ch = step(t, r, &st, []series{ser(2, "api")}, t0.Add(2*time.Minute))
	got = only(t, st)
	if got.State != StateFiring || !got.ClearedAt.IsZero() || len(ch.Events) != 0 {
		t.Fatalf("a flap must be invisible: %+v %v", got, transitions(ch))
	}
	if !got.FiringAt.Equal(t0) {
		t.Fatalf("a flap must not restart the firing clock: %v", got.FiringAt)
	}
}

func TestResolvesOnSecondConsecutiveMissAndIsSentOnce(t *testing.T) {
	var st []Instance
	r := rule(0)
	step(t, r, &st, []series{ser(1, "api")}, t0)
	step(t, r, &st, nil, t0.Add(1*time.Minute))
	ch := step(t, r, &st, nil, t0.Add(2*time.Minute))

	got := only(t, st)
	if got.State != StateResolved || !got.ResolvedAt.Equal(t0.Add(2*time.Minute)) || got.ResolveSent {
		t.Fatalf("not resolved: %+v", got)
	}
	if tr := transitions(ch); len(tr) != 1 || tr[0] != TransitionResolved {
		t.Fatalf("events = %v", tr)
	}

	now := t0.Add(2 * time.Minute)
	alerts, delivered := alertsFor(st, time.Minute, now)
	if len(alerts) != 1 || !alerts[0].EndsAt.Equal(now) || len(delivered) != 1 {
		t.Fatalf("resolution must be sent once, ending now: %+v", alerts)
	}
	if !alerts[0].StartsAt.Equal(t0) {
		t.Fatalf("StartsAt = %v, want the firing time", alerts[0].StartsAt)
	}

	// delivered: next evaluation drops the row and sends nothing.
	st[0].ResolveSent = true
	ch = step(t, r, &st, nil, t0.Add(3*time.Minute))
	if len(st) != 0 || len(ch.Delete) != 1 {
		t.Fatalf("a delivered resolution must be deleted: %+v", st)
	}
}

func TestUndeliveredResolutionIsRetried(t *testing.T) {
	var st []Instance
	r := rule(0)
	step(t, r, &st, []series{ser(1, "api")}, t0)
	step(t, r, &st, nil, t0.Add(time.Minute))
	step(t, r, &st, nil, t0.Add(2*time.Minute))
	step(t, r, &st, nil, t0.Add(3*time.Minute)) // Alertmanager was down: ResolveSent never set
	if got := only(t, st); got.State != StateResolved {
		t.Fatalf("state = %v", got.State)
	}
	if alerts, _ := alertsFor(st, time.Minute, t0.Add(3*time.Minute)); len(alerts) != 1 {
		t.Fatal("an undelivered resolution must be retried")
	}
}

func TestRefiringAfterResolutionStartsOver(t *testing.T) {
	var st []Instance
	r := rule(2 * time.Minute)
	step(t, r, &st, []series{ser(1, "api")}, t0)
	step(t, r, &st, []series{ser(1, "api")}, t0.Add(2*time.Minute))
	step(t, r, &st, nil, t0.Add(3*time.Minute))
	step(t, r, &st, nil, t0.Add(4*time.Minute))
	st[0].ResolveSent = true
	// the condition returns while the resolved row still exists
	ch := step(t, r, &st, []series{ser(1, "api")}, t0.Add(5*time.Minute))
	got := only(t, st)
	if got.State != StatePending || !got.ActiveAt.Equal(t0.Add(5*time.Minute)) || !got.ResolvedAt.IsZero() || got.ResolveSent {
		t.Fatalf("a recurrence must start a fresh pending run: %+v", got)
	}
	if tr := transitions(ch); len(tr) != 1 || tr[0] != TransitionPending {
		t.Fatalf("events = %v", tr)
	}
}

func TestInstancesAreIndependent(t *testing.T) {
	var st []Instance
	r := rule(0)
	step(t, r, &st, []series{ser(1, "api"), ser(1, "web")}, t0)
	step(t, r, &st, []series{ser(1, "web")}, t0.Add(time.Minute))
	step(t, r, &st, []series{ser(1, "web")}, t0.Add(2*time.Minute))
	var api, web Instance
	for _, in := range st {
		if in.Labels == nil {
			continue
		}
		switch in.Fingerprint {
		case ser(0, "api").fp:
			api = in
		case ser(0, "web").fp:
			web = in
		}
	}
	if api.State != StateResolved || web.State != StateFiring {
		t.Fatalf("api=%v web=%v", api.State, web.State)
	}
}

func TestFiringAlertEndsThreeIntervalsOut(t *testing.T) {
	var st []Instance
	step(t, rule(0), &st, []series{ser(1, "api")}, t0)
	alerts, _ := alertsFor(st, 2*time.Minute, t0)
	if len(alerts) != 1 || !alerts[0].EndsAt.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestRetireResolvesFiringImmediately(t *testing.T) {
	var st []Instance
	step(t, rule(0), &st, []series{ser(1, "api")}, t0)
	pend := rule(time.Hour)
	pend.Name = "other"
	st = append(st, Instance{RuleKey: pend.RuleKey, Fingerprint: "p", State: StatePending})
	after, ch := retire(st, t0.Add(time.Minute))
	var states []State
	for _, in := range after {
		states = append(states, in.State)
	}
	if len(after) != 1 || after[0].State != StateResolved || len(ch.Delete) != 1 {
		t.Fatalf("after = %v delete = %v", states, ch.Delete)
	}
	got := map[Transition]int{}
	for _, tr := range transitions(ch) {
		got[tr]++
	}
	if got[TransitionResolved] != 1 || got[TransitionRemoved] != 1 {
		t.Fatalf("events = %v", got)
	}
}

func TestKeepFiringForHoldsResolutionPastTheNextMiss(t *testing.T) {
	var st []Instance
	r := rule(0)
	r.KeepFiringFor = 5 * time.Minute
	step(t, r, &st, []series{ser(1, "api")}, t0)
	step(t, r, &st, nil, t0.Add(time.Minute)) // first miss: held
	for _, m := range []int{2, 3, 4, 5} {
		ch := step(t, r, &st, nil, t0.Add(time.Duration(m)*time.Minute))
		if got := only(t, st); got.State != StateFiring || len(ch.Events) != 0 {
			t.Fatalf("minute %d: resolved inside keep_firing_for: %+v", m, got)
		}
	}
	if alerts, _ := alertsFor(st, time.Minute, t0.Add(5*time.Minute)); len(alerts) != 1 || !alerts[0].EndsAt.After(t0.Add(5*time.Minute)) {
		t.Fatal("a held alert must keep being re-sent")
	}
	// the hold ends 5m after the FIRST miss (t0+1m).
	ch := step(t, r, &st, nil, t0.Add(6*time.Minute))
	if got := only(t, st); got.State != StateResolved || len(ch.Events) != 1 {
		t.Fatalf("not resolved after keep_firing_for: %+v", got)
	}
}

func TestKeepFiringForIsClearedByAReturn(t *testing.T) {
	var st []Instance
	r := rule(0)
	r.KeepFiringFor = 5 * time.Minute
	step(t, r, &st, []series{ser(1, "api")}, t0)
	step(t, r, &st, nil, t0.Add(time.Minute))
	step(t, r, &st, []series{ser(1, "api")}, t0.Add(2*time.Minute))
	step(t, r, &st, nil, t0.Add(3*time.Minute)) // a NEW first miss
	step(t, r, &st, nil, t0.Add(7*time.Minute)) // only 4m after it
	if got := only(t, st); got.State != StateFiring {
		t.Fatalf("the hold restarts when the series returns: %+v", got)
	}
}
