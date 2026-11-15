package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// The control plane's answer for promotion 94bf7fc8 (v1.13.0-rc.1.b2) in prod
// on 2026-11-10 (forge#b7e1113e): promoted 03:47:30, first attempt failed in
// preflight, the retry shipped it at 04:48:49. The answer judged
// [03:47:30, 04:02:30), when the previous release still served, and failed it
// on an issue whose events are all v1.12.0-rc.12's.
func retriedPromotionReply(workerShare float64) string {
	return fmt.Sprintf(`{
 "promotion":{"id":"94bf7fc8","releaseVersion":"v1.13.0-rc.1.b2","gitCommit":"44a4cd377","promotedAt":"2026-11-10T03:47:30.163753Z","previousReleaseVersion":"v1.12.0-rc.12"},
 "before":{"start":"2026-11-10T03:32:30.163753Z","end":"2026-11-10T03:47:30.163753Z"},
 "after":{"start":"2026-11-10T03:47:30.163753Z","end":"2026-11-10T04:02:30.163753Z"},
 "afterComplete":true,
 "services":[
  {"service":"reliant-temporal-worker","before":{"requests":"3500","errorRate":0.01,"p95Ms":50},"after":{"requests":"3654","errorRate":0.01,"p95Ms":50},"versionShare":%v},
  {"service":"reliant-api-server","before":{"requests":"1500","errorRate":0.01,"p95Ms":80},"after":{"requests":"1619","errorRate":0.01,"p95Ms":80}}
 ],
 "regressedIssues":[{"id":"55ada191","service":"reliant-api-server","title":"toolexec.daemonReportedFailure: rpc failed","events":"3","firstSeenAt":"2026-11-03T16:56:21Z","firstVersion":"v1.7.89-rc.2","regressedAt":"2026-11-10T03:59:01Z"}],
 "verdict":"RELEASE_VERDICT_FAIL",
 "findings":[{"code":"RELEASE_FINDING_CODE_REGRESSED_ISSUE","service":"reliant-api-server","value":3,"threshold":1,"policy":"fail","message":"issue 55ada191 regressed: toolexec.daemonReportedFailure: rpc failed"}],
 "thresholds":[{"name":"min_requests","value":"50","source":"default"},{"name":"window","value":"15m","source":"default"},{"name":"fail_on","value":"error_rate,new_issue,regressed_issue","source":"release_shape"}]
}`, workerShare)
}

const (
	// retriedGates: the first attempt failed both deploy gates, the retry
	// passed them.
	retriedGates = `{"gates":[
 {"name":"cluster-rollout","status":"failed","startedAt":"2026-11-10T03:47:31Z","finishedAt":"2026-11-10T03:51:50Z","runId":"att_c7e8"},
 {"name":"apply","status":"failed","startedAt":"2026-11-10T03:47:31Z","finishedAt":"2026-11-10T03:53:58Z","runId":"att_c7e8"},
 {"name":"cluster-rollout","status":"passed","startedAt":"2026-11-10T04:29:46Z","finishedAt":"2026-11-10T04:42:55Z","runId":"att_177f"},
 {"name":"apply","status":"passed","startedAt":"2026-11-10T04:29:46Z","finishedAt":"2026-11-10T04:48:49.154721Z","runId":"att_177f"}]}`
	// unshippedGates: only the failed first attempt.
	unshippedGates = `{"gates":[
 {"name":"cluster-rollout","status":"failed","finishedAt":"2026-11-10T03:51:50Z","runId":"att_c7e8"},
 {"name":"apply","status":"failed","finishedAt":"2026-11-10T03:53:58Z","runId":"att_c7e8"}]}`
	// liveInWindowGates: one attempt, shipped four minutes in.
	liveInWindowGates = `{"gates":[{"name":"apply","status":"passed","finishedAt":"2026-11-10T03:51:30Z","runId":"att_1"}]}`
	// oldVersionIssue: the issue's after-window events, all the previous
	// release's.
	oldVersionIssue = `{"issue":{"id":"55ada191","service":"reliant-api-server"},"byVersion":[{"version":"v1.12.0-rc.12","events":"3"}]}`
	newVersionIssue = `{"issue":{"id":"55ada191","service":"reliant-api-server"},"byVersion":[{"version":"v1.12.0-rc.12","events":"3"},{"version":"v1.13.0-rc.1.b2","events":"1"}]}`
)

func criticalAlertAt(at string) string {
	return `{"alerts":[{"rule":"Down","state":"firing","fingerprint":"f2","firingAt":"` + at + `","labels":{"severity":"critical"}}]}`
}

// withWarningAlert adds to a reply a non-critical alert that opened in the
// after window, and its (warn) finding.
func withWarningAlert(reply string) string {
	return strings.Replace(reply, `"findings":[`,
		`"firingAlerts":[{"rule":"SlowQueries","fingerprint":"f9","firingAt":"2026-11-10T03:50:00Z","labels":{"severity":"warning"}}],
 "findings":[{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","value":1,"threshold":1,"policy":"warn","message":"alert SlowQueries opened after the promotion and is still firing"},`, 1)
}

func TestObsCheckAttribution(t *testing.T) {
	tests := []struct {
		name    string
		replies map[string][]string
		errs    map[string]error
		args    []string
		exit    int
		verdict string
		// want are substrings of the JSON document; stderr of stderr.
		want   []string
		stderr string
	}{
		{
			name: "the retried promotion is judged by when it went live, not its first promoted_at",
			replies: map[string][]string{procHealth: {retriedPromotionReply(0.0005473453749315818)},
				procListGatesCheck: {retriedGates}, procGetIssueCheck: {oldVersionIssue}},
			exit: 2, verdict: "INCONCLUSIVE",
			want: []string{`"live_at": "2026-11-10T04:48:49.154721Z"`, `"reason_code": "window_not_live"`, `"window_versions": {
        "v1.12.0-rc.12": 3
      }`,
				`"window_not_live": "the judged after window [2026-11-10T03:47:30Z, 2026-11-10T04:02:30Z) ended before the release went live at 2026-11-10T04:48:49Z`,
				`"unattributed": "the after window is not the live release's"`, `"policy": "warn"`, `"version_share": 0.0005473453749315818`,
				// The remedy: a window from promoted_at that the release was
				// live for most of (61m19s late: twice that, rounded up).
				`"forge obs check prod --promotion 94bf7fc8 --window 123m"`},
		},
		{
			name: "without the gates, the window's version share keeps it from failing",
			replies: map[string][]string{procHealth: {retriedPromotionReply(0.0005473453749315818)},
				procGetIssueCheck: {oldVersionIssue}},
			errs: map[string]error{procListGatesCheck: &cloud.Error{Code: cloud.CodeUnavailable}},
			exit: 2, verdict: "INCONCLUSIVE",
			want:   []string{`"reason_code": "rollout_incomplete"`, `"rollout_incomplete": "the judged release served 0.1% of the after window's measured traffic`, `"unattributed": "its 3 after-window event(s) are all tagged v1.12.0-rc.12, not v1.13.0-rc.1.b2"`},
			stderr: "could not read the deploy gates of promotion 94bf7fc8",
		},
		{
			name: "rolled out, an issue all of whose events are another version's does not fail the release",
			replies: map[string][]string{procHealth: {retriedPromotionReply(1)},
				procListGatesCheck: {liveInWindowGates}, procGetIssueCheck: {oldVersionIssue}},
			exit: 0, verdict: "PASS",
			want: []string{`"live_at": "2026-11-10T03:51:30Z"`, `"unattributed": "its 3 after-window event(s) are all tagged v1.12.0-rc.12`},
		},
		{
			name: "rolled out, an issue with an event of the release fails it",
			replies: map[string][]string{procHealth: {retriedPromotionReply(1)},
				procListGatesCheck: {liveInWindowGates}, procGetIssueCheck: {newVersionIssue}},
			exit: 1, verdict: "FAIL",
		},
		{
			name:    "reads that fail leave a rolled-out release's finding standing",
			replies: map[string][]string{procHealth: {retriedPromotionReply(1)}},
			errs:    map[string]error{procListGatesCheck: &cloud.Error{Code: cloud.CodeUnavailable}, procGetIssueCheck: &cloud.Error{Code: cloud.CodeUnavailable}},
			exit:    1, verdict: "FAIL",
			stderr: "could not read issue 55ada191's after-window events",
		},
		{
			name:    "a low share with no version evidence leaves the issue the release's",
			replies: map[string][]string{procHealth: {retriedPromotionReply(0.0005473453749315818)}},
			errs:    map[string]error{procListGatesCheck: &cloud.Error{Code: cloud.CodeUnavailable}, procGetIssueCheck: &cloud.Error{Code: cloud.CodeUnavailable}},
			exit:    1, verdict: "FAIL",
			want: []string{`"rollout_incomplete": "the judged release served 0.1%`},
		},
		{
			name: "--wait waits for an attempt to ship, then judges",
			replies: map[string][]string{procHealth: {retriedPromotionReply(0.0005473453749315818)},
				procListGatesCheck: {unshippedGates, unshippedGates, retriedGates}, procGetIssueCheck: {oldVersionIssue}},
			args: []string{"--wait"}, exit: 2, verdict: "INCONCLUSIVE",
			want:   []string{`"live_at": "2026-11-10T04:48:49.154721Z"`},
			stderr: "waiting for an attempt of release v1.13.0-rc.1.b2 to ship its release",
		},
		{
			name: "--wait on a promotion that never ships times out with exit 8",
			replies: map[string][]string{procHealth: {retriedPromotionReply(0.0005473453749315818)},
				procListGatesCheck: {unshippedGates}, procGetIssueCheck: {oldVersionIssue}},
			args: []string{"--wait", "--timeout", "1m"}, exit: 8, verdict: "INCONCLUSIVE",
			want: []string{`"window_not_live": "no attempt of this promotion has shipped its release yet`, `had shipped its release after 1m0s`},
		},
		{
			name: "a critical alert opened in a window the release barely served fails it: share alone moves no alert",
			replies: map[string][]string{procHealth: {retriedPromotionReply(0.0005473453749315818)},
				procGetIssueCheck: {oldVersionIssue}, procListAlertsCheck: {criticalAlertAt("2026-11-10T03:50:00Z")}},
			errs: map[string]error{procListGatesCheck: &cloud.Error{Code: cloud.CodeUnavailable}},
			exit: 1, verdict: "FAIL",
			want: []string{`"rollout_incomplete": "the judged release served 0.1%`, `"code": "alert_firing"`},
		},
		{
			name: "a non-critical alert in a window the release barely served is never a PASS",
			replies: map[string][]string{procHealth: {withWarningAlert(retriedPromotionReply(0.0005473453749315818))},
				procGetIssueCheck: {oldVersionIssue}},
			errs: map[string]error{procListGatesCheck: &cloud.Error{Code: cloud.CodeUnavailable}},
			exit: 2, verdict: "INCONCLUSIVE",
			want: []string{`"rollout_incomplete": "the judged release served 0.1%`, `"message": "alert SlowQueries opened after the promotion and is still firing"`},
		},
		{
			// The alert comes from the alert read, so this pins obsCheck's
			// second Attribute AFTER that read: without it, or before it,
			// the alert fails the release (exit 1).
			name: "a critical alert in a window from before the release went live is INCONCLUSIVE, not a PASS",
			replies: map[string][]string{procHealth: {retriedPromotionReply(1)},
				procListGatesCheck: {retriedGates}, procGetIssueCheck: {newVersionIssue}, procListAlertsCheck: {criticalAlertAt("2026-11-10T03:50:00Z")}},
			exit: 2, verdict: "INCONCLUSIVE",
			want: []string{`"window_not_live": "the judged after window`, `"code": "alert_firing"`},
		},
		{
			name: "a critical alert in the live release's window fails it",
			replies: map[string][]string{procHealth: {retriedPromotionReply(1)},
				procListGatesCheck: {liveInWindowGates}, procGetIssueCheck: {oldVersionIssue}, procListAlertsCheck: {criticalAlertAt("2026-11-10T03:55:00Z")}},
			exit: 1, verdict: "FAIL",
		},
		{
			name: "without --wait, an unshipped promotion is INCONCLUSIVE",
			replies: map[string][]string{procHealth: {retriedPromotionReply(1)},
				procListGatesCheck: {unshippedGates}, procGetIssueCheck: {newVersionIssue}},
			exit: 2, verdict: "INCONCLUSIVE",
			want: []string{`"window_not_live": "no attempt of this promotion has shipped`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.errs
			if errs == nil {
				errs = map[string]error{}
			}
			caller := &seqCaller{replies: tc.replies, errs: errs}
			h := newObsCheckHarness()
			out, stderr, err := h.run(t, cloudFacts(), caller, nil, append([]string{"check", "prod", "--json"}, tc.args...)...)
			if got := exitCodeForError(err); got != tc.exit {
				t.Fatalf("exit = %d, want %d (%v)\n%s", got, tc.exit, err, out)
			}
			doc := decode(t, out)
			if doc["verdict"] != tc.verdict {
				t.Errorf("verdict = %v, want %s", doc["verdict"], tc.verdict)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("document lacks %s:\n%s", w, out)
				}
			}
			if tc.stderr != "" && !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr lacks %q:\n%s", tc.stderr, stderr)
			}
			if strings.Contains(strings.Join(tc.args, " "), "--wait") && tc.exit != 8 && h.sleeps == 0 {
				t.Errorf("--wait on an unshipped promotion did not wait")
			}
		})
	}
}
