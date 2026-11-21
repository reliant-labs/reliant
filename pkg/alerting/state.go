// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"sort"
	"time"
)

// advance moves one rule's instances one evaluation forward.
//
//	inactive ─returns→ pending ─held for `for`→ firing ─stops returning→ (held) → resolved
//
// FLAP DAMPING. A firing instance that stops returning is not resolved at once:
// the first miss only records ClearedAt, and it resolves on a LATER evaluation
// that also misses and is at least rule.KeepFiringFor after that first miss.
// With no KeepFiringFor that is the next evaluation: one interval of grace, so
// a single empty result (a gap in ingestion, a late batch) does not
// page-resolve-page. A return in between clears ClearedAt and nothing was ever
// sent.
//
// A pending instance that stops returning is dropped without ceremony: nothing
// was sent for it, so there is nothing to resolve.
//
// prev holds every stored instance of THIS rule; ss the series the query
// returned. The result is the instances that remain and what to persist.
func advance(rule Rule, prev []Instance, ss []series, now time.Time) ([]Instance, Changes) {
	prevByFP := make(map[string]Instance, len(prev))
	for _, in := range prev {
		prevByFP[in.Fingerprint] = in
	}
	var (
		after []Instance
		ch    Changes
		seen  = make(map[string]bool, len(ss))
	)
	for _, s := range ss {
		seen[s.fp] = true
		in, ok := prevByFP[s.fp]
		fresh := !ok || in.State == StateResolved
		if fresh {
			in = Instance{RuleKey: rule.RuleKey, Fingerprint: s.fp, State: StatePending, ActiveAt: now}
		}
		in.Labels, in.Annotations, in.Why = s.labels, s.annotations, s.why
		in.LastEvalAt, in.LastValue = now, s.value
		in.ClearedAt = time.Time{}

		switch {
		case in.State == StatePending && now.Sub(in.ActiveAt) >= rule.For:
			in.State, in.FiringAt = StateFiring, now
			ch.Events = append(ch.Events, event(in, TransitionFiring, now))
		case fresh:
			ch.Events = append(ch.Events, event(in, TransitionPending, now))
		}
		ch.Upsert = append(ch.Upsert, in)
		after = append(after, in)
	}

	for _, fp := range sortedInstanceKeys(prevByFP) {
		if seen[fp] {
			continue
		}
		in := prevByFP[fp]
		switch in.State {
		case StatePending:
			ch.Delete = append(ch.Delete, ref(in))
			ch.Events = append(ch.Events, event(in, TransitionInactive, now))
		case StateFiring:
			in.LastEvalAt = now
			if in.ClearedAt.IsZero() {
				in.ClearedAt = now
			} else if now.Sub(in.ClearedAt) >= rule.KeepFiringFor {
				in.State, in.ResolvedAt, in.ResolveSent = StateResolved, now, false
				ch.Events = append(ch.Events, event(in, TransitionResolved, now))
			}
			ch.Upsert = append(ch.Upsert, in)
			after = append(after, in)
		case StateResolved:
			if in.ResolveSent {
				ch.Delete = append(ch.Delete, ref(in))
			} else {
				after = append(after, in)
			}
		}
	}
	return after, ch
}

// retire is advance for a rule that no longer exists: nothing damps, because
// the author withdrew the rule rather than the condition clearing. A firing
// instance resolves now; a pending one is dropped.
func retire(prev []Instance, now time.Time) ([]Instance, Changes) {
	var (
		after []Instance
		ch    Changes
	)
	for _, in := range prev {
		switch in.State {
		case StatePending:
			ch.Delete = append(ch.Delete, ref(in))
			ch.Events = append(ch.Events, event(in, TransitionRemoved, now))
		case StateFiring:
			in.State, in.ResolvedAt, in.ResolveSent, in.LastEvalAt = StateResolved, now, false, now
			ch.Events = append(ch.Events, event(in, TransitionResolved, now))
			ch.Upsert = append(ch.Upsert, in)
			after = append(after, in)
		case StateResolved:
			if in.ResolveSent {
				ch.Delete = append(ch.Delete, ref(in))
			} else {
				after = append(after, in)
			}
		}
	}
	return after, ch
}

// alertsFor is what to post for a rule's instances: every firing instance,
// re-sent with an end three intervals out so that a missed evaluation lets
// Alertmanager expire it, and every resolved instance not yet delivered, once,
// ending now.
func alertsFor(after []Instance, interval time.Duration, now time.Time) (alerts []Alert, delivered []Instance) {
	for _, in := range after {
		switch in.State {
		case StateFiring:
			alerts = append(alerts, Alert{Labels: in.Labels, Annotations: in.Annotations, StartsAt: in.FiringAt, EndsAt: now.Add(3 * interval), Why: in.Why})
		case StateResolved:
			if !in.ResolveSent {
				start := in.FiringAt
				if start.IsZero() {
					start = in.ActiveAt
				}
				alerts = append(alerts, Alert{Labels: in.Labels, Annotations: in.Annotations, StartsAt: start, EndsAt: now, Why: in.Why})
				delivered = append(delivered, in)
			}
		}
	}
	return alerts, delivered
}

func event(in Instance, t Transition, at time.Time) Event {
	return Event{RuleKey: in.RuleKey, Fingerprint: in.Fingerprint, Labels: in.Labels, Transition: t, Value: in.LastValue, At: at, Why: in.Why}
}

func ref(in Instance) InstanceRef {
	return InstanceRef{RuleKey: in.RuleKey, Fingerprint: in.Fingerprint}
}

func sortedInstanceKeys(m map[string]Instance) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
