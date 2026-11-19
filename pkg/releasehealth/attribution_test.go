package releasehealth

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// prodNight is promotion 94bf7fc8 of v1.13.0-rc.1.b2 in prod on 2026-11-10:
// promoted at 03:47:30, its first attempt failed in preflight (both gates
// failed by 03:54) and a retry under the same promotion shipped it
// (cluster-rollout passed 04:42:55, apply 04:48:49).
var (
	prodPromoted = time.Date(2026, 11, 10, 3, 47, 30, 0, time.UTC)
	prodLive     = time.Date(2026, 11, 10, 4, 48, 49, 0, time.UTC)
)

func gate(name string, status release.GateStatus, run string, finished time.Time) release.Gate {
	return release.Gate{Name: name, Status: status, RunID: run, StartedAt: tp(finished.Add(-5 * time.Minute)), FinishedAt: tp(finished)}
}

func prodGates() []release.Gate {
	return []release.Gate{
		gate(release.ClusterRolloutGateName, release.GateStatusFailed, "att_c7e8", time.Date(2026, 11, 10, 3, 51, 50, 0, time.UTC)),
		gate(ApplyGateName, release.GateStatusFailed, "att_c7e8", time.Date(2026, 11, 10, 3, 53, 58, 0, time.UTC)),
		gate(release.ClusterRolloutGateName, release.GateStatusPassed, "att_177f", time.Date(2026, 11, 10, 4, 42, 55, 0, time.UTC)),
		gate(ApplyGateName, release.GateStatusPassed, "att_177f", prodLive),
	}
}

func TestLiveAt(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }
	recorded := release.Gate{Name: ApplyGateName, Status: release.GateStatusPassed, RecordedAt: tp(at(5, 1))}
	tests := []struct {
		name  string
		gates []release.Gate
		want  time.Time
		state LiveState
	}{
		{"no gates: unknown", nil, time.Time{}, LiveUnknown},
		{"no deploy gate: unknown", []release.Gate{gate("lint", release.GateStatusPassed, "r1", at(3, 0))}, time.Time{}, LiveUnknown},
		{"a retried promotion goes live when the retry's last deploy gate passed", prodGates(), prodLive, LiveShipped},
		{"every attempt failed: not shipped", prodGates()[:2], time.Time{}, LiveNotShipped},
		{"one attempt: the latest of its gates", []release.Gate{
			gate(ApplyGateName, release.GateStatusPassed, "r1", at(4, 10)),
			gate(release.ClusterRolloutGateName, release.GateStatusPassed, "r1", at(4, 5)),
		}, at(4, 10), LiveShipped},
		{"a re-apply after it shipped does not move it", append(prodGates(),
			gate(ApplyGateName, release.GateStatusPassed, "att_late", at(9, 0))), prodLive, LiveShipped},
		{"a rollout the deploy did not wait for still shipped", []release.Gate{
			gate(release.ClusterRolloutGateName, release.GateStatusSkipped, "r1", at(4, 2)),
			gate(ApplyGateName, release.GateStatusPassed, "r1", at(4, 3)),
		}, at(4, 3), LiveShipped},
		{"an attempt with one failed gate did not ship", []release.Gate{
			gate(release.ClusterRolloutGateName, release.GateStatusFailed, "r1", at(4, 2)),
			gate(ApplyGateName, release.GateStatusPassed, "r1", at(4, 3)),
		}, time.Time{}, LiveNotShipped},
		{"an errored gate did not ship", []release.Gate{gate(ApplyGateName, release.GateStatusError, "r1", at(4, 3))}, time.Time{}, LiveNotShipped},
		{"a gate with no run id is its own attempt", []release.Gate{
			gate(ApplyGateName, release.GateStatusFailed, "", at(4, 0)),
			gate(ApplyGateName, release.GateStatusPassed, "", at(4, 30)),
		}, at(4, 30), LiveShipped},
		{"no finish time: when it was recorded", []release.Gate{recorded}, at(5, 1), LiveShipped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, state := LiveAt(tc.gates)
			if !got.Equal(tc.want) || state != tc.state {
				t.Errorf("LiveAt = %s, %s; want %s, %s", got, state, tc.want, tc.state)
			}
		})
	}
}

func share(f float64) *float64 { return &f }

// prodReport is the control plane's answer for prodNight: the after window
// anchored at the first promoted_at, a regressed issue whose events are all
// the previous release's, and the judged release on 0.05% of the traffic.
func prodReport() Report {
	worker := svc("reliant-temporal-worker", 3500, 3654, 0.01, 0.01, 50, 50)
	worker.VersionShare = share(0.0005473453749315818)
	r := Report{
		Subject: Subject{PromotionID: "94bf7fc8-ab32-4331-8774-d2598d16abf5", Release: "v1.13.0-rc.1.b2",
			PromotedAt: prodPromoted, PreviousRelease: "v1.12.0-rc.12"},
		Before:        Range{prodPromoted.Add(-15 * time.Minute), prodPromoted},
		After:         Range{prodPromoted, prodPromoted.Add(15 * time.Minute)},
		AfterComplete: true,
		Services: []ServiceHealth{worker,
			svc("reliant-api-server", 1500, 1619, 0.01, 0.01, 80, 80),
			svc("admin-server", 1500, 1557, 0.01, 0.01, 102, 234)},
		RegressedIssues: []Issue{{ID: "55ada191-6221-4547-804f-74839041161e", Service: "reliant-api-server",
			Title: "toolexec.daemonReportedFailure: rpc failed", Events: 3, FirstVersion: "v1.7.89-rc.2",
			RegressedAt: time.Date(2026, 11, 10, 3, 59, 1, 0, time.UTC)}},
		Findings: []Finding{
			{Code: release.HealthLatency, Service: "admin-server", Value: 2.29, Threshold: 1.5, Policy: PolicyWarn, Message: "admin-server: p95 102ms -> 234ms"},
			{Code: release.HealthRegressedIssue, Service: "reliant-api-server", Value: 3, Threshold: 1, Policy: PolicyFail,
				Message: "issue 55ada191-6221-4547-804f-74839041161e regressed: toolexec.daemonReportedFailure: rpc failed"},
		},
		Thresholds: Thresholds{MinRequests: 50, FailOn: []string{release.HealthRegressedIssue}}.List(),
		Verdict:    Fail,
	}
	return r
}

// fullRollout is prodReport with the judged release serving every request
// and the after window its own.
func fullRollout() Report {
	r := prodReport()
	r.Services[0].VersionShare = share(1)
	r.Subject.LiveAt = time.Time{}
	return r
}

func withWindowVersions(r Report, v map[string]int64) Report {
	r.IssueWindowVersions = map[string]map[string]int64{r.RegressedIssues[0].ID: v}
	return r
}

func withFinding(r Report, f Finding) Report {
	r.Findings = append(append([]Finding(nil), r.Findings...), f)
	return r
}

func TestAttribution(t *testing.T) {
	failing := func(r Report) []string {
		var out []string
		for _, f := range r.Findings {
			if f.Policy == PolicyFail {
				out = append(out, f.Code)
			}
		}
		return out
	}
	criticalAlert := Alert{Rule: "ApiDown", Labels: map[string]string{"severity": "critical"}, FiringAt: prodPromoted.Add(5 * time.Minute)}
	errorRate := Finding{Code: release.HealthErrorRate, Service: "reliant-temporal-worker", Value: 0.2, Threshold: 0.01, Policy: PolicyFail, Message: "worker: error rate up"}
	tests := []struct {
		name string
		// apply is the fold under test.
		apply   func() Report
		verdict Verdict
		// failing are the codes still failing the release.
		failing []string
		// notLive and rollout are substrings of WindowNotLive and
		// RolloutIncomplete ("" = must be empty).
		notLive, rollout string
		// unattributed is a substring of the regressed issue finding's reason.
		unattributed string
	}{
		{
			name:    "the retried promotion: a window that ended before the release went live judges nothing",
			apply:   func() Report { return WithLiveAt(prodReport(), prodLive, LiveShipped) },
			verdict: Inconclusive, notLive: "ended before the release went live at 2026-11-10T04:48:49Z",
			rollout: "served 0.1% of the after window", unattributed: "not the live release's",
		},
		{
			name:    "a low share alone takes no finding off the release: the issue with no version evidence stands",
			apply:   func() Report { return Attribute(prodReport()) },
			verdict: Fail, failing: []string{release.HealthRegressedIssue}, rollout: "served 0.1%",
		},
		{
			name: "an issue whose after-window events are all another version's never fails the release",
			apply: func() Report {
				return Attribute(withWindowVersions(prodReport(), map[string]int64{"v1.12.0-rc.12": 3}))
			},
			verdict: Inconclusive, rollout: "served 0.1%", unattributed: "all tagged v1.12.0-rc.12, not v1.13.0-rc.1.b2",
		},
		{
			name: "fully rolled out, another version's issue is that version's: PASS",
			apply: func() Report {
				return Attribute(withWindowVersions(fullRollout(), map[string]int64{"v1.12.0-rc.12": 3}))
			},
			verdict: Pass, unattributed: "all tagged v1.12.0-rc.12",
		},
		{
			name: "an issue with an event of the release is the release's",
			apply: func() Report {
				return Attribute(withWindowVersions(fullRollout(), map[string]int64{"v1.12.0-rc.12": 3, "v1.13.0-rc.1.b2": 1}))
			},
			verdict: Fail, failing: []string{release.HealthRegressedIssue},
		},
		{
			name:    "fully rolled out with nothing read: the finding stands",
			apply:   func() Report { return Attribute(fullRollout()) },
			verdict: Fail, failing: []string{release.HealthRegressedIssue},
		},
		{
			name:    "a live time inside the judged window leaves it the release's",
			apply:   func() Report { return WithLiveAt(fullRollout(), prodPromoted.Add(4*time.Minute), LiveShipped) },
			verdict: Fail, failing: []string{release.HealthRegressedIssue},
		},
		{
			name:    "no attempt has shipped: the window is the previous release's",
			apply:   func() Report { return WithLiveAt(fullRollout(), time.Time{}, LiveNotShipped) },
			verdict: Inconclusive, notLive: "no attempt of this promotion has shipped", unattributed: "not the live release's",
		},
		{
			name:    "unknown live time changes nothing",
			apply:   func() Report { return WithLiveAt(fullRollout(), time.Time{}, LiveUnknown) },
			verdict: Fail, failing: []string{release.HealthRegressedIssue},
		},
		{
			name: "a service the release barely served cannot fail it, and leaves it unproven",
			apply: func() Report {
				r := fullRollout()
				r.Services = append(r.Services, svc("lagging", 1000, 1000, 0, 0.2, 10, 10))
				r.Services[3].VersionShare = share(0.1)
				r.Services[0].After.Requests = 100000 // the release's share stays high
				r.Findings = []Finding{{Code: release.HealthErrorRate, Service: "lagging", Value: 0.2, Threshold: 0.01, Policy: PolicyFail, Message: "lagging: error rate up"}}
				return Attribute(r)
			},
			verdict: Inconclusive, rollout: "served only 10.0% of lagging's",
		},
		{
			name: "a service the release fully served fails it",
			apply: func() Report {
				r := fullRollout()
				r.Findings = []Finding{errorRate}
				return Attribute(r)
			},
			verdict: Fail, failing: []string{release.HealthErrorRate},
		},
		{
			name: "a critical alert opened in a window the release barely served still fails it",
			apply: func() Report {
				r := withWindowVersions(prodReport(), map[string]int64{"v1.12.0-rc.12": 3})
				return Attribute(WithStandingAlerts(Attribute(r), []Alert{criticalAlert}))
			},
			verdict: Fail, failing: []string{release.HealthAlertFiring}, rollout: "served 0.1%", unattributed: "all tagged v1.12.0-rc.12",
		},
		{
			name: "a non-critical alert in a window the release barely served is never a PASS",
			apply: func() Report {
				r := withWindowVersions(prodReport(), map[string]int64{"v1.12.0-rc.12": 3})
				r.FiringAlerts = []Alert{{Rule: "SlowQueries", Labels: map[string]string{"severity": "warning"}, FiringAt: prodPromoted.Add(5 * time.Minute)}}
				r = withFinding(r, Finding{Code: release.HealthAlertFiring, Ref: "SlowQueries", Value: 1, Threshold: 1, Policy: PolicyWarn,
					Message: "alert SlowQueries opened after the promotion and is still firing"})
				return Attribute(WithStandingAlerts(Attribute(r), nil))
			},
			verdict: Inconclusive, rollout: "served 0.1%", unattributed: "all tagged v1.12.0-rc.12",
		},
		{
			name: "a restart in a window the release barely served stays the release's",
			apply: func() Report {
				r := withWindowVersions(prodReport(), map[string]int64{"v1.12.0-rc.12": 3})
				r.Workloads = []Workload{{Workload: "api", Restarts: 3, LastTerminatedReason: "OOMKilled"}}
				r = withFinding(r, Finding{Code: release.HealthWorkloadRestarts, Ref: "api", Value: 3, Threshold: 1, Policy: PolicyFail, Message: "api restarted 3 time(s)"})
				return Attribute(r)
			},
			verdict: Fail, failing: []string{release.HealthWorkloadRestarts}, rollout: "served 0.1%", unattributed: "all tagged v1.12.0-rc.12",
		},
		{
			name: "a critical alert in a window from before the release went live is INCONCLUSIVE, never a PASS",
			apply: func() Report {
				r := WithLiveAt(prodReport(), prodLive, LiveShipped)
				return Attribute(WithStandingAlerts(r, []Alert{criticalAlert}))
			},
			verdict: Inconclusive, notLive: "ended before the release went live", rollout: "served 0.1%", unattributed: "not the live release's",
		},
		{
			name: "a critical alert in a window the release served fails it",
			apply: func() Report {
				r := withWindowVersions(fullRollout(), map[string]int64{"v1.12.0-rc.12": 3})
				return Attribute(WithStandingAlerts(Attribute(r), []Alert{criticalAlert}))
			},
			verdict: Fail, failing: []string{release.HealthAlertFiring}, unattributed: "all tagged v1.12.0-rc.12",
		},
		{
			name: "--fail-on cannot re-fail an unattributed finding",
			apply: func() Report {
				r := withWindowVersions(prodReport(), map[string]int64{"v1.12.0-rc.12": 3})
				return Rejudge(Attribute(r), []string{release.HealthRegressedIssue})
			},
			verdict: Inconclusive, rollout: "served 0.1%", unattributed: "all tagged v1.12.0-rc.12",
		},
		{
			name: "a local report (no version share, no release) is as judged",
			apply: func() Report {
				r := withFinding(prodReport(), errorRate)
				r.Subject.Release = ""
				for i := range r.Services {
					r.Services[i].VersionShare = nil
				}
				return Attribute(r)
			},
			verdict: Fail, failing: []string{release.HealthRegressedIssue, release.HealthErrorRate},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.apply()
			if r.Verdict != tc.verdict {
				t.Errorf("verdict = %s, want %s (findings %+v)", r.Verdict, tc.verdict, r.Findings)
			}
			if got := strings.Join(failing(r), ","); got != strings.Join(tc.failing, ",") {
				t.Errorf("failing = %q, want %q", got, strings.Join(tc.failing, ","))
			}
			check := func(field, got, want string) {
				if (want == "") != (got == "") || !strings.Contains(got, want) {
					t.Errorf("%s = %q, want %q", field, got, want)
				}
			}
			check("WindowNotLive", r.WindowNotLive, tc.notLive)
			check("RolloutIncomplete", r.RolloutIncomplete, tc.rollout)
			var why string
			for _, f := range r.Findings {
				if f.Code == release.HealthRegressedIssue {
					why = f.Unattributed
				}
			}
			check("regressed issue Unattributed", why, tc.unattributed)
			if r.Verdict == Inconclusive && (r.WindowNotLive != "" || r.RolloutIncomplete != "") {
				if w := InconclusiveWhy(r); w != r.WindowNotLive && w != r.RolloutIncomplete {
					t.Errorf("InconclusiveWhy = %q, want the attribution reason", w)
				}
				code := map[bool]string{true: ReasonWindowNotLive, false: ReasonRolloutIncomplete}[r.WindowNotLive != ""]
				if got := InconclusiveCode(r); got != code {
					t.Errorf("InconclusiveCode = %q, want %q (the code of the reason InconclusiveWhy gives)", got, code)
				}
			}
			if again := Attribute(r); again.Verdict != r.Verdict || len(failing(again)) != len(failing(r)) || again.RolloutIncomplete != r.RolloutIncomplete {
				t.Errorf("Attribute is not idempotent: %s -> %s", r.Verdict, again.Verdict)
			}
		})
	}
}

// A critical alert opened in a window from before the release went live is
// not the release's: folded in after attribution it stays a warn, and the
// check is INCONCLUSIVE, not FAIL and never PASS. Opened in a window the
// release barely served, it fails the release: share alone moves no alert.
func TestWithStandingAlertsAndAttribution(t *testing.T) {
	alert := Alert{Rule: "Down", Labels: map[string]string{"severity": "critical"}, FiringAt: prodPromoted.Add(5 * time.Minute)}
	base := withWindowVersions(withFinding(prodReport(), Finding{Code: release.HealthAlertFiring, Ref: "Down", Value: 1, Threshold: 1, Policy: PolicyFail,
		Message: "alert Down opened after the promotion and is still firing"}), map[string]int64{"v1.12.0-rc.12": 3})
	base.FiringAlerts = []Alert{alert}
	alertFinding := func(r Report) Finding {
		for _, f := range r.Findings {
			if f.Code == release.HealthAlertFiring {
				return f
			}
		}
		t.Fatalf("no alert finding: %+v", r.Findings)
		return Finding{}
	}

	notLive := WithStandingAlerts(WithLiveAt(base, prodLive, LiveShipped), []Alert{alert})
	if f := alertFinding(notLive); f.Policy != PolicyWarn || f.Unattributed == "" {
		t.Errorf("window not live: alert finding = %+v, want a warn with a reason", f)
	}
	if notLive.Verdict != Inconclusive {
		t.Errorf("window not live: verdict = %s, want INCONCLUSIVE", notLive.Verdict)
	}

	lowShare := WithStandingAlerts(Attribute(base), []Alert{alert})
	if f := alertFinding(lowShare); f.Policy != PolicyFail || f.Unattributed != "" {
		t.Errorf("low share: alert finding = %+v, want a fail", f)
	}
	if lowShare.Verdict != Fail {
		t.Errorf("low share: verdict = %s, want FAIL", lowShare.Verdict)
	}
}

func TestWithLiveAtRecordsTheLiveTime(t *testing.T) {
	r := WithLiveAt(prodReport(), prodLive, LiveShipped)
	if !r.Subject.LiveAt.Equal(prodLive) {
		t.Errorf("Subject.LiveAt = %s, want %s", r.Subject.LiveAt, prodLive)
	}
	if r.VersionShare == nil || *r.VersionShare >= 0.01 {
		t.Errorf("VersionShare = %v, want the worker's 0.05%%", r.VersionShare)
	}
}

func TestEvaluateAttributesByVersionShare(t *testing.T) {
	in := input(t, true, svc("api", 1000, 1000, 0.001, 0.05, 100, 100))
	in.Services[0].VersionShare = share(0.2)
	if r := Evaluate(in); r.Verdict != Inconclusive || r.Findings[0].Policy != PolicyWarn || r.RolloutIncomplete == "" {
		t.Errorf("a window the release served 20%% of: verdict %s, findings %+v", r.Verdict, r.Findings)
	}
	in.Services[0].VersionShare = share(0.9)
	if r := Evaluate(in); r.Verdict != Fail {
		t.Errorf("a window the release served 90%% of: verdict %s", r.Verdict)
	}
}

func TestAfterEndsFollowsTheWindowsStart(t *testing.T) {
	r := Report{Subject: Subject{PromotedAt: prodPromoted}, After: Range{prodLive, prodLive.Add(time.Minute)},
		Thresholds: Thresholds{Window: 15 * time.Minute}.List()}
	if got, want := r.AfterEnds(), prodLive.Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("AfterEnds = %s, want %s: a window anchored at the live time ends a window after it", got, want)
	}
}

// Issue is part of forge/pkg's semver-1 API, and it was comparable: per-issue
// version counts live on the Report (IssueWindowVersions), never on Issue.
// This does not compile if Issue gains a field that makes it not comparable.
var _ = Issue{} == Issue{}
