package releasehealth

// ALERTS THAT WERE ALREADY FLAPPING. An alert that opens after a promotion is
// evidence against the release only if the release could have caused it.
// control-plane v1.21.0-rc.6 (2026-11-17) failed prod on one alert_firing
// finding: IdentityLatency, a warning that fired 2-27 times a day all week
// and twice that day before any rc.6 pod existed, on a sign-in path the
// release did not touch. An alert that fires that often opens in most
// 15-minute windows, so read as new it fails releases at random.
//
// So an alert opened after the promotion that ALSO fired at least
// alert_baseline_min_firings times (default 1) in the baseline_window before
// it (default 24h; the window error_rate's sparse endpoints are compared
// against) was flapping before the release: it is listed in
// [Report.BaselineAlerts], and its alert_firing finding is kept with policy
// warn and a reason in [Finding.Unattributed], so it is visible and never
// fails the release. An alert that never fired in the baseline window still
// fails as before, as does every critical one (severity=critical): an env
// that made a rule critical means it to page, however noisy it is.
//
// SAME ALERT. Identity is the rule plus its labels, NOT the fingerprint: a
// rule's query may return measurements as columns, and every column but the
// value becomes a label (forge/pkg/alerting labels.go), so one condition
// fingerprints as a new instance each time a sample count moves
// (IdentityLatency's slow=4 and slow=6 were two instances). A label whose
// value is a number is therefore read as a measurement and left out; the
// rest (service, uri, pod, severity, ...) name what the alert is about, and a
// baseline firing counts only when its identity is EXACTLY the alert's: the
// same non-numeric labels with the same values, no more and no fewer. A
// subset test would let an alert with no identifying label (a rule whose only
// labels are measurements) match every instance of its rule, so a service
// that flapped would discount another service's new alert; exact, an empty
// identity matches only the same, label-less series. The
// trade-off is a rule grouped by a numeric label (code="500"): its values are
// one identity here, so a baseline firing for one value discounts another.
// alert_baseline_min_firings = 0 is the rule before this one, every alert
// counts; raising it asks for a flap rate rather than one prior firing.
//
// THE EVIDENCE. The history is the env's alert state transitions
// ([AlertEvent]): a firing transition in [P-baseline_window, P) is one
// firing, and an instance whose first transition there is a resolve was
// firing when the window opened, which is one more. Without a history (nil:
// not read, or a backend that keeps none) nothing is discounted, so a
// history that could not be read can only leave the rule as strict as it
// was, never looser; a truncated one likewise only misses discounts.
//
// `forge obs check` reads the history itself and applies
// [WithAlertBaseline], so the rule reaches a control plane's answer judged
// without it. A control plane's own judge applies it only when it passes the
// history in [Input.AlertHistory] to [Evaluate]; one that does not yet (the
// stored verdict and promotion health gate at this release) counts every
// alert, as before.

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Alert state transitions, as an [AlertEvent] names them (forge/pkg/alerting
// Transition).
const (
	TransitionPending  = "pending"
	TransitionFiring   = "firing"
	TransitionResolved = "resolved"
	TransitionInactive = "inactive"
	TransitionRemoved  = "removed"
)

// AlertEvent is one state transition of an alert instance, from the env's
// alert history.
type AlertEvent struct {
	Rule        string            `json:"rule"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	// Transition is one of the Transition* values.
	Transition string    `json:"transition"`
	At         time.Time `json:"at"`
}

// identityLabels are the labels that say which thing an alert is about: all
// of them but alertname (the rule) and the labels whose value is a number,
// which a rule's query returns as measurements (above).
func identityLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k == "alertname" || isMeasurement(v) {
			continue
		}
		out[k] = v
	}
	return out
}

func isMeasurement(v string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil
}

// baselineFirings is how many times an alert of a's rule and identity fired
// in [from, to), by history: each firing transition, plus one for each
// instance that was already firing at from (its first transition in the
// range resolves it).
func baselineFirings(a Alert, history []AlertEvent, from, to time.Time) int {
	id := identityLabels(a.Labels)
	type instance struct {
		first      time.Time
		firstState string
		firings    int
	}
	instances := map[string]*instance{}
	for _, e := range history {
		if e.Rule != a.Rule || e.At.Before(from) || !e.At.Before(to) || !sameIdentity(identityLabels(e.Labels), id) {
			continue
		}
		key := e.Fingerprint
		if key == "" {
			key = alertKey(Alert{Rule: e.Rule, Labels: e.Labels})
		}
		in := instances[key]
		if in == nil {
			in = &instance{}
			instances[key] = in
		}
		if in.first.IsZero() || e.At.Before(in.first) {
			in.first, in.firstState = e.At, e.Transition
		}
		if e.Transition == TransitionFiring {
			in.firings++
		}
	}
	n := 0
	for _, in := range instances {
		n += in.firings
		if in.firstState == TransitionResolved || in.firstState == TransitionRemoved {
			n++ // firing when the range opened
		}
	}
	return n
}

// sameIdentity reports whether two identities (identityLabels) are the same
// label set: equal keys and values, so an empty identity matches only an
// empty one.
func sameIdentity(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// WithAlertBaseline applies the baseline-alert rule (above) to a judged
// report: each alert_firing finding of an alert that also fired
// alert_baseline_min_firings times in the baseline window before the
// promotion becomes a warn finding saying so, and the alert moves from
// FiringAlerts to BaselineAlerts. history is the env's alert transitions
// since at least the baseline window's start; nil applies nothing. live is
// the env's alert state read now, or nil: it names the alerts behind a
// finding whose answer lists none (a control plane that omits its alert
// arrays). The threshold and the baseline window are read from the report,
// except that an override (the request's --threshold pairs; nil for none)
// wins, and a report that does not list the threshold is held to the
// default and lists it. The verdict is recomputed when a finding changed, so
// it can improve: a flapping alert was never the release's evidence.
// [Evaluate] judges by the same rule, so over its report this changes
// nothing.
func WithAlertBaseline(r Report, history []AlertEvent, live []Alert, overrides map[string]string) Report {
	minFirings, ths := alertBaselineMinFirings(r.Thresholds, overrides)
	out := discountBaselineAlerts(r, history, live, minFirings, baselineWindowOf(ths))
	out.Thresholds = ths
	return out
}

// discountBaselineAlerts is the rule itself, over the report's alerts and
// live, with the threshold and baseline window given.
func discountBaselineAlerts(r Report, history []AlertEvent, live []Alert, minFirings int, window time.Duration) Report {
	p := r.Subject.PromotedAt
	if minFirings < 1 || history == nil || p.IsZero() {
		return r
	}
	from := p.Add(-window)

	// Each rule's alerts behind its alert_firing findings, as Evaluate
	// counts them: opened in [P, after.To), not excluded, not judged as
	// their issue. counted ones keep failing; flapping ones are discounted.
	type tally struct {
		counted  int
		flapping []Alert
	}
	rules := map[string]*tally{}
	var order []string
	seen := map[string]bool{}
	for _, a := range r.BaselineAlerts {
		seen[alertKey(a)] = true
	}
	consider := func(a Alert) {
		k := alertKey(a)
		if seen[k] || a.Excluded() || mirrorsJudgedIssue(a, r.NotEvaluated) || afterWindow(a, r.After.To) ||
			(!a.FiringAt.IsZero() && a.FiringAt.Before(p)) {
			return
		}
		seen[k] = true
		t := rules[a.Rule]
		if t == nil {
			t = &tally{}
			rules[a.Rule] = t
			order = append(order, a.Rule)
		}
		n := 0
		if !a.Critical() {
			n = baselineFirings(a, history, from, p)
		}
		if n < minFirings {
			t.counted++
			return
		}
		a.BaselineFirings = n
		t.flapping = append(t.flapping, a)
	}
	for _, a := range r.FiringAlerts {
		consider(a)
	}
	for _, a := range live {
		consider(a)
	}

	out := r
	out.Findings = slices.Clone(r.Findings)
	moved := map[string]bool{}
	for _, rule := range order {
		t := rules[rule]
		// The rule's findings still counted. One per alert (Evaluate), and
		// every one says the same, so which is discounted does not matter;
		// a finding the counted alerts account for keeps failing.
		var idx []int
		for i, f := range out.Findings {
			if f.Code == release.HealthAlertFiring && f.Unattributed == "" && findingNamesRule(f, rule) {
				idx = append(idx, i)
			}
		}
		take := min(len(t.flapping), len(idx)-t.counted)
		for j := 0; j < take; j++ {
			a := t.flapping[j]
			i := idx[len(idx)-1-j]
			out.Findings[i].Policy = PolicyWarn
			out.Findings[i].Unattributed = fmt.Sprintf("the same alert fired %d time(s) in the %s before the promotion (alert_baseline_min_firings %d): it was flapping before this release",
				a.BaselineFirings, durationText(window), minFirings)
			out.Findings[i].Message += fmt.Sprintf("; it also fired %d time(s) in the %s before the promotion, so it is not counted against the release", a.BaselineFirings, durationText(window))
			out.BaselineAlerts = append(slices.Clip(out.BaselineAlerts), a)
			moved[alertKey(a)] = true
		}
	}
	if len(moved) == 0 {
		return r
	}
	out.FiringAlerts = slices.DeleteFunc(slices.Clone(r.FiringAlerts), func(a Alert) bool { return moved[alertKey(a)] })
	if len(out.FiringAlerts) == 0 {
		out.FiringAlerts = nil
	}
	sort.SliceStable(out.BaselineAlerts, func(i, j int) bool { return out.BaselineAlerts[i].Rule < out.BaselineAlerts[j].Rule })
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, gaugeOf(out.Thresholds), out.unproven())
	return out
}

// findingNamesRule reports whether an alert_firing finding is rule's. A
// finding that crossed a wire without its ref is matched by its message,
// which names the rule.
func findingNamesRule(f Finding, rule string) bool {
	return f.Ref == rule || (f.Ref == "" && strings.Contains(f.Message, "alert "+rule+" "))
}

// alertBaselineMinFirings reads alert_baseline_min_firings from a report's
// thresholds, an override winning, and returns the thresholds with it listed:
// a report judged before it existed is held to the default.
func alertBaselineMinFirings(ths []Threshold, overrides map[string]string) (int, []Threshold) {
	n, src, at := release.DefaultAlertBaselineMinFirings, SourceDefault, -1
	for i, th := range ths {
		if th.Name != ThresholdAlertBaselineMinFirings {
			continue
		}
		at = i
		if v, err := strconv.Atoi(th.Value); err == nil && v >= 0 {
			n, src = v, nameOr(th.Source, SourceDefault)
		}
	}
	if v, ok := overrides[ThresholdAlertBaselineMinFirings]; ok {
		if o, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && o >= 0 {
			n, src = o, SourceRequest
		}
	}
	th := Threshold{ThresholdAlertBaselineMinFirings, strconv.Itoa(n), src}
	switch {
	case at >= 0 && ths[at] == th:
		return n, ths
	case at >= 0:
		out := slices.Clone(ths)
		out[at] = th
		return n, out
	}
	pos := slices.IndexFunc(ths, func(t Threshold) bool { return t.Name == ThresholdRegressedIssueMinEvents }) + 1
	if pos == 0 {
		pos = len(ths)
	}
	return n, slices.Insert(slices.Clone(ths), pos, th)
}

// baselineWindowOf reads baseline_window from a report's thresholds; a
// report that does not list it was judged with the default.
func baselineWindowOf(ths []Threshold) time.Duration {
	for _, th := range ths {
		if th.Name == ThresholdBaselineWindow {
			if d, err := time.ParseDuration(th.Value); err == nil && d > 0 {
				return d
			}
		}
	}
	return release.DefaultHealthBaselineWindow
}
