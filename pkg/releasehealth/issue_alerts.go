package releasehealth

// ISSUE THRESHOLDS AND ISSUE ALERTS. A release answers for an issue through
// the issue rule: new_issue (first seen in the release, at least
// new_issue_min_events after-window events) and regressed_issue (regressed at
// or after the promotion, at least regressed_issue_min_events of them). The
// platform's NewIssue and IssueRegressed alerts fire on the same issues with
// no threshold and no version: one event of an old, transient signature, or
// of a client build that is not the release, raises one. Counted as
// alert_firing findings of their own, they failed a release the issue rule
// had already judged and let pass (control-plane v1.20.0-rc.4, 2026-11-14:
// a 1-event regression, two 1-event issues of an older client build, and the
// alerts those raised).
//
// So an issue alert — a NewIssue or IssueRegressed alert that names its issue
// (label issue_id) — is judged as its issue: it is listed in
// [Report.IssueAlerts] and is no finding of its own. Three things keep that
// from hiding a real failure:
//
//   - A critical one (severity=critical) is never an issue alert here: an
//     env that raised the rule to critical means it to page, and it fails the
//     release as every critical alert does.
//   - When the issue rule was not evaluated (the backend could not read
//     issues: not_evaluated names new_issue or regressed_issue), the alert is
//     the only evidence, and it counts as before.
//   - Any other rule — IssueSpike, a rule of the env's own, an alert with no
//     issue_id — counts as before.

import (
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// The platform's issue alerts, by alertname, and the label that names the
// issue each fired for (forge internal/obsdash alerts.go; the control plane's
// alerting/issues.go writes the label).
const (
	AlertNewIssue       = "NewIssue"
	AlertIssueRegressed = "IssueRegressed"
	LabelIssueID        = "issue_id"
)

// IssueCode is the finding code that judges the issue the alert fired for
// (release.HealthNewIssue for NewIssue, release.HealthRegressedIssue for
// IssueRegressed), or "" when the alert is no issue alert: another rule, or
// one that names no issue.
func (a Alert) IssueCode() string {
	if strings.TrimSpace(a.Labels[LabelIssueID]) == "" {
		return ""
	}
	switch a.Rule {
	case AlertNewIssue:
		return release.HealthNewIssue
	case AlertIssueRegressed:
		return release.HealthRegressedIssue
	}
	return ""
}

// mirrorsJudgedIssue reports whether a is an issue alert whose issue the issue
// rule judged, so it adds nothing of its own (above).
func mirrorsJudgedIssue(a Alert, notEvaluated []string) bool {
	code := a.IssueCode()
	return code != "" && !a.Critical() && !a.Excluded() && !slices.Contains(notEvaluated, code)
}

// WithIssueThresholds applies the issue thresholds and the issue-alert rule
// (above) to a judged report: a new or regressed issue below its min-events
// threshold is no issue and no finding, and an issue alert moves from
// FiringAlerts to IssueAlerts, taking the alert_firing finding of its rule
// with it when no other alert of that rule is counted. The thresholds are
// read from the report, except that overrides (the request's --threshold
// pairs, by name; nil for none) win, so an answer that does not echo them is
// still judged by them; a report judged before regressed_issue_min_events
// existed gets it from new_issue_min_events, and lists it. The verdict is
// recomputed when anything changed, so it can improve: what is taken out was
// never the release's evidence. [Evaluate] judges by the same rule, so over
// its report this changes nothing; it is how the rule reaches a control
// plane's answer judged without it (`forge obs check` applies it before
// attribution).
func WithIssueThresholds(r Report, overrides map[string]string) Report {
	newMin, regressedMin, ths := issueMinEvents(withOverrides(r.Thresholds, overrides))
	out := r
	out.Thresholds = ths
	out.NewIssues = keepIssues(r.NewIssues, newMin)
	out.RegressedIssues = keepIssues(r.RegressedIssues, regressedMin)

	mirrored, counted := map[string]bool{}, map[string]bool{}
	out.FiringAlerts, out.IssueAlerts = nil, slices.Clone(r.IssueAlerts)
	for _, a := range r.FiringAlerts {
		if !mirrorsJudgedIssue(a, r.NotEvaluated) {
			out.FiringAlerts = append(out.FiringAlerts, a)
			counted[a.Rule] = true
			continue
		}
		mirrored[a.Rule] = true
		if !slices.ContainsFunc(out.IssueAlerts, func(b Alert) bool { return alertKey(b) == alertKey(a) }) {
			out.IssueAlerts = append(out.IssueAlerts, a)
		}
	}
	// A finding's Value is its issue's after-window events (Evaluate, and a
	// control plane that predates the threshold), so it is judged by it.
	out.Findings = slices.DeleteFunc(slices.Clone(r.Findings), func(f Finding) bool {
		switch f.Code {
		case release.HealthNewIssue:
			return f.Value < float64(newMin)
		case release.HealthRegressedIssue:
			return f.Value < float64(regressedMin)
		case release.HealthAlertFiring:
			for rule := range mirrored {
				if !counted[rule] && (f.Ref == rule || (f.Ref == "" && strings.Contains(f.Message, "alert "+rule+" "))) {
					return true
				}
			}
		}
		return false
	})
	if len(out.Findings) == len(r.Findings) && len(out.NewIssues) == len(r.NewIssues) &&
		len(out.RegressedIssues) == len(r.RegressedIssues) && len(mirrored) == 0 {
		out.FiringAlerts, out.IssueAlerts = r.FiringAlerts, r.IssueAlerts
		return out // nothing the rule removes: the report as judged, with the threshold listed
	}
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, gaugeOf(out.Thresholds), out.unproven())
	return out
}

// issueMinEvents reads new_issue_min_events and regressed_issue_min_events
// from a report's thresholds, and returns the thresholds with the latter
// listed: a report judged before it existed counts every regression, and is
// now held to the new-issue bar, as Resolve's default is.
func issueMinEvents(ths []Threshold) (newMin, regressedMin int, out []Threshold) {
	def := release.DefaultReleaseHealth()
	newMin, newSrc := def.NewIssueMinEvents, SourceDefault
	regressedMin = 0
	for _, th := range ths {
		n, err := strconv.Atoi(th.Value)
		if err != nil || n < 1 {
			continue
		}
		switch th.Name {
		case ThresholdNewIssueMinEvents:
			newMin, newSrc = n, nameOr(th.Source, SourceDefault)
		case ThresholdRegressedIssueMinEvents:
			regressedMin = n
		}
	}
	out = slices.Clone(ths)
	if regressedMin == 0 {
		regressedMin = newMin
		at := slices.IndexFunc(out, func(th Threshold) bool { return th.Name == ThresholdNewIssueMinEvents }) + 1
		if at == 0 {
			at = len(out)
		}
		out = slices.DeleteFunc(out, func(th Threshold) bool { return th.Name == ThresholdRegressedIssueMinEvents })
		out = slices.Insert(out, min(at, len(out)), Threshold{ThresholdRegressedIssueMinEvents, strconv.Itoa(regressedMin), newSrc})
	}
	return newMin, regressedMin, out
}

// withOverrides is ths with the issue thresholds the request set replaced by
// its values, tagged SourceRequest.
func withOverrides(ths []Threshold, overrides map[string]string) []Threshold {
	out := ths
	for _, name := range []string{ThresholdNewIssueMinEvents, ThresholdRegressedIssueMinEvents} {
		v, ok := overrides[name]
		if n, err := strconv.Atoi(v); !ok || err != nil || n < 1 {
			continue
		}
		th := Threshold{name, v, SourceRequest}
		if i := slices.IndexFunc(out, func(t Threshold) bool { return t.Name == name }); i >= 0 {
			if out[i] == th {
				continue
			}
			out = slices.Clone(out)
			out[i] = th
		} else {
			out = append(slices.Clone(out), th)
		}
	}
	return out
}

func keepIssues(in []Issue, minEvents int) []Issue {
	if in == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(in), func(is Issue) bool { return is.Events < int64(minEvents) })
}
