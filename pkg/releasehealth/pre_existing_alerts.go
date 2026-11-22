package releasehealth

// ALERTS ON AN ISSUE THAT PREDATES THE RELEASE. An alert that names an issue
// (label issue_id) and is not one of the platform's NewIssue or
// IssueRegressed alerts (issue_alerts.go) — IssueSpike, or an env's own rule
// over an issue — is about that issue's condition, and the condition can be
// older than the alert instance. control-plane v1.23.0-rc.9 (2026-11-19)
// failed prod on one alert_firing finding: IssueSpike for an issue first seen
// at 08:43 in v1.22.0-rc.2, which fired ~28 times an hour all morning, 3.5h
// before the promotion, and kept the same rate under rc.9. The IssueSpike
// instance first fired at 12:15, after the noon promotion, because the
// spike detector compares the last one to two hours with the 24h before
// them, and that ratio crossed its factor then; it had never fired before,
// so the baseline-alert rule (alert_baseline.go) had nothing to discount.
//
// So such an alert counts against the release only when its condition is
// new. Its issue is read ([AlertIssue]), and the alert is PRE-EXISTING when:
//
//   - the issue was first seen before the promotion, in a version other than
//     the release (first_build), and
//   - under the release it does not fire materially faster than it did
//     before: the release's version fired at least one and fewer than
//     new_issue_min_events of its events in the after window, or its rate there is at most
//     [PreExistingIssueMaxRateRatio] times the other versions' rate in the
//     baseline window, or the events above that rate number fewer than
//     new_issue_min_events. The two windows are read apart ([AlertIssue]
//     Before and After): the baseline is the other versions' events before
//     the promotion, the release's rate its own events from the promotion to
//     the after window's end, each over its first and last event in its
//     window. A window whose counted events reach outside it (a backend that
//     counts by the hour answers for whole hours) cannot tell the issue
//     before the release from the issue under it, so the alert counts.
//
// The alert is matched to its own finding: Evaluate names the alert on its
// alert_firing finding ([Finding.Alert]). A finding that names none (a
// judged report from a control plane that predates the field) is matched
// only when it is the one counted finding of its rule and the report itself
// lists exactly one alert of that rule; anything less exact leaves the
// finding counted.
//
// A pre-existing alert's alert_firing finding is kept with policy warn,
// pre_existing set and a reason in [Finding.Unattributed], and the alert
// moves from FiringAlerts to [Report.PreExistingAlerts], so it is visible
// and never fails the release. It still fails, as before:
//
//   - an alert whose issue was first seen after the promotion, or in the
//     release (the issue rule's new_issue judges it as well);
//   - an issue the issue rule already judged (a new or regressed issue);
//   - an issue that fires faster under the release, beyond the ratio and by
//     at least new_issue_min_events events;
//   - an issue with no events of another version in the range, so no rate
//     before the release to compare with, when the release fired at least
//     new_issue_min_events of them (a dormant issue the release woke up);
//   - a critical alert (severity=critical): an env that made a rule critical
//     means it to page;
//   - an alert whose issue could not be read: without the issue nothing
//     proves the condition is old.
//
// An alert that names no issue is judged by the baseline-alert rule alone.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// PreExistingIssueMaxRateRatio is how many times faster than before the
// release an issue may fire under it while an alert on it is still the
// issue's old condition rather than the release's doing. Event counts over
// minutes are noisy, so the bar is a doubling, and the excess must also reach
// new_issue_min_events events.
const PreExistingIssueMaxRateRatio = 2.0

// AlertIssue is the issue an alert names (label issue_id), as the
// pre-existing-alert rule reads it.
type AlertIssue struct {
	ID string
	// FirstSeenAt and FirstVersion are the issue's first event ever and the
	// version it carried; an empty FirstVersion is read from the alert's
	// first_build label.
	FirstSeenAt  time.Time
	FirstVersion string
	// Before is the issue's events by version over the baseline window
	// before the promotion, [P - baseline_window, P) or a range inside it;
	// After over [P, the after window's end) or a range inside it. Each is
	// read on its own, so neither counts the other's events.
	Before IssueWindow
	After  IssueWindow
}

// IssueWindow is an issue's events by version over [From, To).
type IssueWindow struct {
	From, To time.Time
	Versions []IssueVersionEvents
}

// IssueVersionEvents is one version's events of an issue over a range.
type IssueVersionEvents struct {
	Version     string
	Events      int64
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// sum is the events of the versions keep selects over w, and whether they
// are w's alone: a window that is unset, or a selected version whose events
// reach outside it, is not evidence of what the issue did in w.
func (w IssueWindow) sum(keep func(version string) bool) (IssueVersionEvents, bool) {
	if w.From.IsZero() || !w.From.Before(w.To) {
		return IssueVersionEvents{}, false
	}
	var out IssueVersionEvents
	for _, v := range w.Versions {
		if !keep(v.Version) || v.Events <= 0 {
			continue
		}
		if v.FirstSeenAt.IsZero() || v.LastSeenAt.IsZero() || v.FirstSeenAt.Before(w.From) || !v.LastSeenAt.Before(w.To) {
			return IssueVersionEvents{}, false
		}
		out.Events += v.Events
		if out.FirstSeenAt.IsZero() || v.FirstSeenAt.Before(out.FirstSeenAt) {
			out.FirstSeenAt = v.FirstSeenAt
		}
		if v.LastSeenAt.After(out.LastSeenAt) {
			out.LastSeenAt = v.LastSeenAt
		}
	}
	return out, true
}

// PreExistingIssue is set on an alert of [Report.PreExistingAlerts]: the
// issue it fired for, when it was first seen, and its rate under the release
// and before it (0 when there were too few events to measure one).
type PreExistingIssue struct {
	ID              string    `json:"id"`
	FirstSeenAt     time.Time `json:"first_seen_at,omitzero"`
	FirstVersion    string    `json:"first_version,omitempty"`
	ReleaseEvents   int64     `json:"release_events"`
	ReleasePerHour  float64   `json:"release_per_hour,omitempty"`
	BaselineEvents  int64     `json:"baseline_events"`
	BaselinePerHour float64   `json:"baseline_per_hour,omitempty"`
}

// WithPreExistingAlerts applies the pre-existing-alert rule (above) to a
// judged report: each alert_firing finding of an alert whose issue predates
// the release and fires no faster under it becomes a warn finding saying so,
// and the alert moves from FiringAlerts to PreExistingAlerts. issues are the
// issues the alerts name, read by the caller; nil applies nothing. live is the
// env's alert state read now, or nil: it names the alerts behind a finding
// whose answer lists none. new_issue_min_events is read from the report, an
// override (the request's --threshold pairs; nil for none) winning. The
// verdict is recomputed when a finding changed, so it can improve: an issue's
// old condition was never the release's evidence. [Evaluate] judges by the
// same rule when given [Input.AlertIssues].
func WithPreExistingAlerts(r Report, issues []AlertIssue, live []Alert, overrides map[string]string) Report {
	minEvents, _, _ := issueMinEvents(withOverrides(r.Thresholds, overrides))
	return discountPreExistingAlerts(r, issues, live, minEvents)
}

// AlertIssueIDs are the issues the rule would read for r: the issue each
// counted alert_firing alert names (of r's FiringAlerts, then live), once
// each, in order. A caller reads these and passes them to
// [WithPreExistingAlerts].
func AlertIssueIDs(r Report, live []Alert) []string {
	if !slices.ContainsFunc(r.Findings, countedAlertFinding) {
		return nil
	}
	var out []string
	for _, a := range slices.Concat(r.FiringAlerts, live) {
		if id := issueOf(a); id != "" && !a.Critical() && !a.Excluded() && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func countedAlertFinding(f Finding) bool {
	return f.Code == release.HealthAlertFiring && f.Unattributed == ""
}

// issueOf is the issue an alert names, or "" for none or for a platform issue
// alert, which the issue rule judges (issue_alerts.go).
func issueOf(a Alert) string {
	if a.IssueCode() != "" {
		return ""
	}
	return strings.TrimSpace(a.Labels[LabelIssueID])
}

// discountPreExistingAlerts is the rule itself, with new_issue_min_events
// given.
func discountPreExistingAlerts(r Report, issues []AlertIssue, live []Alert, minEvents int) Report {
	p := r.Subject.PromotedAt
	if len(issues) == 0 || p.IsZero() {
		return r
	}
	byID := make(map[string]AlertIssue, len(issues))
	for _, is := range issues {
		if is.ID != "" {
			byID[is.ID] = is
		}
	}
	judged := map[string]bool{}
	for _, is := range slices.Concat(r.NewIssues, r.RegressedIssues) {
		judged[is.ID] = true
	}

	// The alerts that could be behind a counted alert_firing finding, by
	// rule; the baseline-alert rule's already discounted.
	type candidate struct {
		a          Alert
		fromReport bool // listed in r.FiringAlerts, not only in live
		why        string
		pre        bool
	}
	rules := map[string][]candidate{}
	var order []string
	seen := map[string]bool{}
	for _, a := range slices.Concat(r.BaselineAlerts, r.PreExistingAlerts) {
		seen[alertKey(a)] = true
	}
	consider := func(a Alert, fromReport bool) {
		k := alertKey(a)
		if seen[k] || a.Excluded() || mirrorsJudgedIssue(a, r.NotEvaluated) || afterWindow(a, r.After.To) ||
			(!a.FiringAt.IsZero() && a.FiringAt.Before(p)) {
			return
		}
		seen[k] = true
		if _, ok := rules[a.Rule]; !ok {
			order = append(order, a.Rule)
		}
		c := candidate{a: a, fromReport: fromReport}
		id := issueOf(a)
		if is, read := byID[id]; id != "" && read && !judged[id] && !a.Critical() {
			if pe, why, ok := preExisting(is, a, r.Subject, minEvents); ok {
				c.a.Issue, c.why, c.pre = &pe, why, true
			}
		}
		rules[a.Rule] = append(rules[a.Rule], c)
	}
	for _, a := range r.FiringAlerts {
		consider(a, true)
	}
	for _, a := range live {
		consider(a, false)
	}

	out := r
	out.Findings = slices.Clone(r.Findings)
	moved := map[string]bool{}
	for _, rule := range order {
		cs := rules[rule]
		var idx []int
		named := false
		for i, f := range out.Findings {
			if countedAlertFinding(f) && findingNamesRule(f, rule) {
				idx = append(idx, i)
				named = named || f.Alert != ""
			}
		}
		for _, c := range cs {
			if !c.pre {
				continue
			}
			// The finding this alert is behind, exactly, or none.
			i := -1
			for _, j := range idx {
				if out.Findings[j].Alert == alertKey(c.a) {
					i = j
				}
			}
			if i < 0 && !named && len(idx) == 1 && len(cs) == 1 && c.fromReport {
				i = idx[0]
			}
			if i < 0 || !countedAlertFinding(out.Findings[i]) {
				continue
			}
			out.Findings[i].Policy = PolicyWarn
			out.Findings[i].PreExisting = true
			out.Findings[i].Unattributed = c.why
			out.Findings[i].Message += fmt.Sprintf("; its issue %s predates the release, so it is not counted against the release", c.a.Issue.ID)
			out.PreExistingAlerts = append(slices.Clip(out.PreExistingAlerts), c.a)
			moved[alertKey(c.a)] = true
		}
	}
	if len(moved) == 0 {
		return r
	}
	out.FiringAlerts = slices.DeleteFunc(slices.Clone(r.FiringAlerts), func(a Alert) bool { return moved[alertKey(a)] })
	if len(out.FiringAlerts) == 0 {
		out.FiringAlerts = nil
	}
	sort.SliceStable(out.PreExistingAlerts, func(i, j int) bool { return out.PreExistingAlerts[i].Rule < out.PreExistingAlerts[j].Rule })
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, gaugeOf(out.Thresholds), out.unproven())
	return out
}

// preExisting reports whether an alert's issue predates the release and fires
// no faster under it (above), with the figures and the sentence that say so.
func preExisting(is AlertIssue, a Alert, s Subject, minEvents int) (PreExistingIssue, string, bool) {
	firstVersion := firstNonEmpty(is.FirstVersion, strings.TrimSpace(a.Labels["first_build"]))
	pe := PreExistingIssue{ID: is.ID, FirstSeenAt: is.FirstSeenAt, FirstVersion: firstVersion}
	if s.Release == "" || is.FirstSeenAt.IsZero() || !is.FirstSeenAt.Before(s.PromotedAt) || firstVersion == s.Release {
		return pe, "", false // new with the release, or nothing to tell its version by
	}
	// Each side read in its own window, on its own side of the promotion:
	// the other versions before it, the release's version after it.
	if is.Before.To.After(s.PromotedAt) || is.After.From.Before(s.PromotedAt) {
		return pe, "", false
	}
	rel, relOK := is.After.sum(func(v string) bool { return v == s.Release })
	if !relOK {
		return pe, "", false // the release's events cannot be told from what came after its window
	}
	// A baseline that cannot be told from the release's window is none: no
	// rate before the release, so only too few events under it can clear it.
	base, _ := is.Before.sum(func(v string) bool { return v != s.Release })
	pe.ReleaseEvents, pe.BaselineEvents = rel.Events, base.Events
	baseRate, baseRated := perHour(base)
	pe.BaselinePerHour = baseRate
	first := firstNonEmpty(firstVersion, is.FirstSeenAt.UTC().Format(time.RFC3339))
	since := fmt.Sprintf("issue %s was first seen %s in %s, before the promotion", is.ID, is.FirstSeenAt.UTC().Format(time.RFC3339), firstNonEmpty(firstVersion, "an earlier version"))
	if rel.Events == 0 {
		// No event of the release's version in its window says nothing
		// about what the release did to the issue: no evidence clears it.
		return pe, "", false
	}
	if rel.Events < int64(minEvents) {
		return pe, fmt.Sprintf("pre-existing alert (first seen %s, too few events under the release to have risen): %s, and fired %d time(s) under %s (new_issue_min_events %d)",
			first, since, rel.Events, s.Release, minEvents), true
	}
	relRate, relRated := perHour(rel)
	pe.ReleasePerHour = relRate
	if !baseRated || !relRated {
		return pe, "", false // no rate before it to compare with, or a burst too short to measure
	}
	excess := float64(rel.Events) - baseRate*rel.LastSeenAt.Sub(rel.FirstSeenAt).Hours()
	if relRate > PreExistingIssueMaxRateRatio*baseRate && excess >= float64(minEvents) {
		return pe, "", false // it got worse under the release
	}
	return pe, fmt.Sprintf("pre-existing alert (first seen %s, rate unchanged): %s, and fires %.1f/h under %s against %.1f/h before it (limit %.2gx)",
		first, since, relRate, s.Release, baseRate, PreExistingIssueMaxRateRatio), true
}

// perHour is a version's events per hour over its first to last event: n
// events span n-1 intervals. Fewer than two events, or no time between them,
// measure no rate.
func perHour(v IssueVersionEvents) (float64, bool) {
	span := v.LastSeenAt.Sub(v.FirstSeenAt).Hours()
	if v.Events < 2 || span <= 0 {
		return 0, false
	}
	return float64(v.Events-1) / span, true
}
