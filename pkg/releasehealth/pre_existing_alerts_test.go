package releasehealth

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// rc9 is the judged release of the control-plane v1.23.0-rc.9 case
// (2026-11-19): promoted at t0 (12:00), live at 12:13:37.
const rc9 = "v1.23.0-rc.9"

// issueSpike is control-plane's IssueSpike alert for one issue, as the
// evaluator labels it (control-plane internal/alerting/issues.go).
func issueSpike(issue, firstBuild string, at time.Time) Alert {
	return Alert{Rule: "IssueSpike", Fingerprint: "spike-" + issue, FiringAt: at, Labels: map[string]string{
		"alertname": "IssueSpike", "severity": "warning", "issue_id": issue, "fingerprint": "ba121a0bef94939a",
		"service": "workspace-controller", "platform": "server", "first_build": firstBuild,
	}}
}

func versionEvents(version string, events int64, from, to time.Time) IssueVersionEvents {
	return IssueVersionEvents{Version: version, Events: events, FirstSeenAt: from, LastSeenAt: to}
}

// spikeInput is a passing service, rc9 promoted at t0 with its after window
// from when it went live, and the alerts, issues and history given.
func spikeInput(t *testing.T, alerts []Alert, issues []AlertIssue, history []AlertEvent) Input {
	in := input(t, true, svc("api", 1000, 1000, 0.01, 0.01, 100, 100))
	in.Subject.Release = rc9
	live := t0.Add(13*time.Minute + 37*time.Second)
	in.Windows.Before = Range{live.Add(-15 * time.Minute), live}
	in.Windows.After = Range{live, live.Add(15 * time.Minute)}
	in.Alerts, in.AlertIssues, in.AlertHistory = alerts, issues, history
	return in
}

// afterEnd is the end of spikeInput's after window: 15 minutes after rc9
// went live at 12:13:37.
var afterEnd = t0.Add(28*time.Minute + 37*time.Second)

// The issue of the rc.9 case: first seen 08:43 in v1.22.0-rc.2, 92 events on
// rc.2 before the promotion, then 8 on rc.9 in the after window — ~28 an
// hour on both. Each side is read in its own window.
func steadyIssue(id string) AlertIssue {
	return AlertIssue{ID: id, FirstSeenAt: t0.Add(-3*time.Hour - 16*time.Minute - 42*time.Second), FirstVersion: "v1.22.0-rc.2",
		Before: IssueWindow{From: t0.Add(-24 * time.Hour), To: t0, Versions: []IssueVersionEvents{
			versionEvents("v1.22.0-rc.2", 92, t0.Add(-3*time.Hour-16*time.Minute-42*time.Second), t0.Add(-time.Minute)),
		}},
		After: IssueWindow{From: t0, To: afterEnd, Versions: []IssueVersionEvents{
			// rc.2 still ran during the rollout; only the release's own events count.
			versionEvents("v1.22.0-rc.2", 4, t0.Add(-30*time.Minute), t0.Add(7*time.Minute+51*time.Second)),
			versionEvents(rc9, 8, t0.Add(10*time.Minute+7*time.Second), t0.Add(25*time.Minute+7*time.Second)),
		}},
	}
}

func TestAlertOnAnIssueThatPredatesTheRelease(t *testing.T) {
	const id = "7f55ab8b-13e1-4052-b5bc-5b9f40612dd3"
	spikeAt := t0.Add(15*time.Minute + 11*time.Second) // first fired 12:15:11, after the promotion
	spike := issueSpike(id, "v1.22.0-rc.2", spikeAt)
	down := Alert{Rule: "ServiceDown", Fingerprint: "d1", FiringAt: spikeAt, Labels: map[string]string{"severity": "warning", "service": "api"}}
	worse := steadyIssue(id)
	// Three times as often under the release: 24 events over the same span.
	worse.After.Versions[1].Events = 24
	critical := issueSpike(id, "v1.22.0-rc.2", spikeAt)
	critical.Labels["severity"] = "critical"
	dormant := steadyIssue(id)
	dormant.Before.Versions = nil // no event of another version in the baseline window
	quiet := dormant
	quiet.After.Versions = []IssueVersionEvents{versionEvents(rc9, 2, t0.Add(20*time.Minute), t0.Add(25*time.Minute))}
	// No event of the release's version in a well-read after window: no
	// evidence of what the release did to the issue.
	silent := steadyIssue(id)
	silent.After.Versions = silent.After.Versions[:1]
	// [B2] The release's events read past the after window's end: 23 over
	// 49 minutes looks like the old rate, but the window's own events
	// cannot be told from the later ones.
	pastWindow := steadyIssue(id)
	pastWindow.After.Versions[1] = versionEvents(rc9, 23, t0.Add(10*time.Minute+7*time.Second), t0.Add(59*time.Minute+32*time.Second))
	// The baseline read across the promotion: rc.2's rollout events after it
	// are not the baseline, and nothing separates them.
	baselineLeaks := steadyIssue(id)
	baselineLeaks.Before.Versions[0].LastSeenAt = t0.Add(7*time.Minute + 51*time.Second)
	// One query over both windows, as the rule read it before: no window
	// on either side, so no evidence of either.
	unwindowed := steadyIssue(id)
	unwindowed.Before, unwindowed.After.From, unwindowed.After.To = IssueWindow{}, time.Time{}, time.Time{}
	born := steadyIssue(id)
	born.FirstSeenAt, born.FirstVersion = t0.Add(10*time.Minute), rc9
	same := map[string]string{"severity": "warning", "service": "api"}

	tests := []struct {
		name        string
		alerts      []Alert
		issues      []AlertIssue
		history     []AlertEvent
		regressed   bool
		verdict     Verdict
		preExisting bool
		why         string
	}{
		{
			// control-plane v1.23.0-rc.9 (2026-11-19): exit 1 on this alone.
			name: "an issue first seen in an older build, at an unchanged rate, only warns", alerts: []Alert{spike},
			issues: []AlertIssue{steadyIssue(id)}, verdict: Pass, preExisting: true,
			why: "pre-existing alert (first seen v1.22.0-rc.2, rate unchanged): issue " + id + " was first seen 2026-10-22T08:43:18Z in v1.22.0-rc.2",
		},
		{
			name: "an issue the release fired too few times to have risen only warns", alerts: []Alert{spike},
			issues: []AlertIssue{quiet}, verdict: Pass, preExisting: true,
			why: "too few events under the release to have risen",
		},
		{
			name: "a first-time alert naming no issue fails", alerts: []Alert{down},
			issues: []AlertIssue{steadyIssue(id)}, verdict: Fail,
		},
		{
			name: "an alert naming no issue that fired in the baseline window warns", alerts: []Alert{down},
			history: []AlertEvent{event("ServiceDown", "d0", TransitionFiring, t0.Add(-2*time.Hour), same)},
			verdict: Pass, preExisting: true, why: "flapping before this release",
		},
		{name: "a pre-existing issue firing 3x as often fails", alerts: []Alert{spike}, issues: []AlertIssue{worse}, verdict: Fail},
		{name: "an issue born with the release fails", alerts: []Alert{spike}, issues: []AlertIssue{born}, verdict: Fail},
		{name: "a dormant issue the release woke fails", alerts: []Alert{spike}, issues: []AlertIssue{dormant}, verdict: Fail},
		{name: "no release event in the after window cannot clear it", alerts: []Alert{spike}, issues: []AlertIssue{silent}, verdict: Fail},
		{name: "release events past the after window cannot clear it", alerts: []Alert{spike}, issues: []AlertIssue{pastWindow}, verdict: Fail},
		{name: "a baseline read across the promotion cannot clear it", alerts: []Alert{spike}, issues: []AlertIssue{baselineLeaks}, verdict: Fail},
		{name: "evidence read without its windows cannot clear it", alerts: []Alert{spike}, issues: []AlertIssue{unwindowed}, verdict: Fail},
		{name: "an issue that could not be read fails", alerts: []Alert{spike}, issues: []AlertIssue{{ID: "another"}}, verdict: Fail},
		{name: "a critical alert fails", alerts: []Alert{critical}, issues: []AlertIssue{steadyIssue(id)}, verdict: Fail},
		{name: "an issue the issue rule judged keeps its alert", alerts: []Alert{spike}, issues: []AlertIssue{steadyIssue(id)}, regressed: true, verdict: Fail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := spikeInput(t, tc.alerts, tc.issues, tc.history)
			if tc.regressed {
				in.RegressedIssues = []Issue{{ID: id, Events: 5, RegressedAt: t0.Add(time.Minute)}}
			}
			r := Evaluate(in)
			if r.Verdict != tc.verdict {
				t.Fatalf("verdict = %s, want %s; findings %+v", r.Verdict, tc.verdict, r.Findings)
			}
			fs := alertFindings(r)
			if len(fs) != 1 {
				t.Fatalf("alert_firing findings = %+v, want one", fs)
			}
			f := fs[0]
			if !tc.preExisting {
				if f.Policy != PolicyFail || f.PreExisting || f.Unattributed != "" || r.PreExistingAlerts != nil || len(r.FiringAlerts) != 1 {
					t.Errorf("finding = %+v, pre_existing_alerts = %+v, firing_alerts = %+v; want a counted alert", f, r.PreExistingAlerts, r.FiringAlerts)
				}
				return
			}
			if f.Policy != PolicyWarn || !f.PreExisting || !strings.Contains(f.Unattributed, tc.why) || r.FiringAlerts != nil {
				t.Errorf("finding = %+v, firing_alerts = %+v; want a pre-existing warn saying %q", f, r.FiringAlerts, tc.why)
			}
			// --fail-on re-judges every finding but a discounted one.
			if got := Rejudge(r, []string{"alert_firing"}); got.Verdict != Pass {
				t.Errorf("Rejudge(alert_firing) = %s, want PASS", got.Verdict)
			}
		})
	}
}

// The rule reaches a report judged without it (a control plane's answer):
// WithPreExistingAlerts discounts the alert there, AlertIssueIDs names the
// issue to read, and an alert of the live state the answer did not list is
// judged too.
func TestWithPreExistingAlertsOverAJudgedReport(t *testing.T) {
	const id = "7f55ab8b"
	spike := issueSpike(id, "v1.22.0-rc.2", t0.Add(15*time.Minute+11*time.Second))
	judged := Evaluate(spikeInput(t, []Alert{spike}, nil, nil))
	if judged.Verdict != Fail {
		t.Fatalf("judged without the issue: verdict = %s, want FAIL", judged.Verdict)
	}
	if ids := AlertIssueIDs(judged, nil); len(ids) != 1 || ids[0] != id {
		t.Fatalf("AlertIssueIDs = %v, want [%s]", ids, id)
	}

	r := WithPreExistingAlerts(judged, []AlertIssue{steadyIssue(id)}, nil, nil)
	if r.Verdict != Pass || len(r.PreExistingAlerts) != 1 || r.FiringAlerts != nil {
		t.Fatalf("verdict = %s, pre_existing_alerts = %+v, firing_alerts = %+v", r.Verdict, r.PreExistingAlerts, r.FiringAlerts)
	}
	pe := r.PreExistingAlerts[0].Issue
	if pe == nil || pe.ID != id || pe.FirstVersion != "v1.22.0-rc.2" || pe.ReleaseEvents != 8 || pe.BaselineEvents != 92 ||
		pe.ReleasePerHour < 25 || pe.ReleasePerHour > 29 || pe.BaselinePerHour < 25 || pe.BaselinePerHour > 29 {
		t.Errorf("issue = %+v, want both rates ~28/h", pe)
	}
	if !strings.Contains(alertFindings(r)[0].Message, "its issue "+id+" predates the release") {
		t.Errorf("message = %q", alertFindings(r)[0].Message)
	}
	// Idempotent, and nothing to read once discounted.
	if again := WithPreExistingAlerts(r, []AlertIssue{steadyIssue(id)}, nil, nil); len(again.PreExistingAlerts) != 1 || again.Verdict != Pass {
		t.Errorf("applied twice: %+v", again.PreExistingAlerts)
	}
	if ids := AlertIssueIDs(r, nil); ids != nil {
		t.Errorf("AlertIssueIDs after = %v, want none", ids)
	}

	// An answer that lists no alerts: the live state names it.
	bare := judged
	bare.FiringAlerts = nil
	if r := WithPreExistingAlerts(bare, []AlertIssue{steadyIssue(id)}, []Alert{spike}, nil); r.Verdict != Pass {
		t.Errorf("from the live state: verdict = %s; findings %+v", r.Verdict, r.Findings)
	}
	// [B1] The same answer from a control plane that predates Finding.Alert:
	// its finding names no alert, so only the report's own alert list can
	// match it, and the live state alone cannot.
	legacy := bare
	legacy.Findings = withoutAlertNames(bare.Findings)
	if r := WithPreExistingAlerts(legacy, []AlertIssue{steadyIssue(id)}, []Alert{spike}, nil); r.Verdict != Fail || r.PreExistingAlerts != nil {
		t.Errorf("unnamed finding, alert only in the live state: verdict = %s, pre_existing_alerts = %+v; want it counted", r.Verdict, r.PreExistingAlerts)
	}
	legacy.FiringAlerts = judged.FiringAlerts
	if r := WithPreExistingAlerts(legacy, []AlertIssue{steadyIssue(id)}, nil, nil); r.Verdict != Pass {
		t.Errorf("unnamed finding, the one alert of its rule listed: verdict = %s; findings %+v", r.Verdict, r.Findings)
	}
	// new_issue_min_events from the request: a bar above the release's 24
	// events reads them as too few to have risen.
	worse := steadyIssue(id)
	worse.After.Versions[1].Events = 24
	if r := WithPreExistingAlerts(judged, []AlertIssue{worse}, nil, nil); r.Verdict != Fail {
		t.Errorf("3x the rate: verdict = %s, want FAIL", r.Verdict)
	}
	if r := WithPreExistingAlerts(judged, []AlertIssue{worse}, nil, map[string]string{ThresholdNewIssueMinEvents: "100"}); r.Verdict != Pass {
		t.Errorf("3x the rate under new_issue_min_events=100: verdict = %s, want PASS", r.Verdict)
	}
}

func withoutAlertNames(fs []Finding) []Finding {
	out := slices.Clone(fs)
	for i := range out {
		out[i].Alert = ""
	}
	return out
}

// [B1] Two alerts of one rule, one on an issue that predates the release and
// one on an issue the release made worse: only the first one's own finding
// warns. A report whose findings name no alert cannot say which finding is
// whose, so both stay counted.
func TestPreExistingAlertMatchesItsOwnFinding(t *testing.T) {
	const old, hot = "issue-old", "issue-hot"
	at := t0.Add(15 * time.Minute)
	a, b := issueSpike(old, "v1.22.0-rc.2", at), issueSpike(hot, "v1.22.0-rc.2", at)
	b.Labels["service"] = "api"
	worse := steadyIssue(hot)
	worse.After.Versions[1].Events = 24
	in := spikeInput(t, []Alert{b, a}, []AlertIssue{steadyIssue(old), worse}, nil)

	r := Evaluate(in)
	if r.Verdict != Fail {
		t.Fatalf("verdict = %s, want FAIL: the hot issue's alert counts", r.Verdict)
	}
	var warned, failed []string
	for _, f := range alertFindings(r) {
		if f.PreExisting {
			warned = append(warned, f.Alert)
		} else if f.Policy == PolicyFail {
			failed = append(failed, f.Alert)
		}
	}
	if !slices.Equal(warned, []string{alertKey(a)}) || !slices.Equal(failed, []string{alertKey(b)}) {
		t.Errorf("pre-existing findings = %v, failing = %v; want [%s] and [%s]", warned, failed, alertKey(a), alertKey(b))
	}
	if len(r.PreExistingAlerts) != 1 || r.PreExistingAlerts[0].Issue.ID != old || len(r.FiringAlerts) != 1 || r.FiringAlerts[0].Issue != nil {
		t.Errorf("pre_existing_alerts = %+v, firing_alerts = %+v", r.PreExistingAlerts, r.FiringAlerts)
	}

	// The same two alerts judged without the rule, by a control plane whose
	// findings name no alert: nothing ties either finding to the old issue.
	in.AlertIssues = nil
	judged := Evaluate(in)
	judged.Findings = withoutAlertNames(judged.Findings)
	got := WithPreExistingAlerts(judged, []AlertIssue{steadyIssue(old), worse}, nil, nil)
	if got.PreExistingAlerts != nil || len(got.FiringAlerts) != 2 {
		t.Errorf("unnamed findings: pre_existing_alerts = %+v, firing_alerts = %+v; want both counted", got.PreExistingAlerts, got.FiringAlerts)
	}
	for _, f := range alertFindings(got) {
		if f.PreExisting || f.Policy != PolicyFail {
			t.Errorf("unnamed finding %+v was discounted", f)
		}
	}
}
