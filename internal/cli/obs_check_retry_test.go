package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/release"
)

// healthRefuser fails the first n GetReleaseHealth reads with err (every
// read when n < 0), then answers as next does.
type healthRefuser struct {
	mu      sync.Mutex
	n       int
	err     error
	refused int
	next    *seqCaller
}

func (r *healthRefuser) Call(ctx context.Context, procedure string, req, out any) error {
	r.mu.Lock()
	if procedure == procHealth && r.n != 0 {
		r.n--
		r.refused++
		r.mu.Unlock()
		return r.err
	}
	r.mu.Unlock()
	return r.next.Call(ctx, procedure, req, out)
}

func passingCaller() *seqCaller {
	return &seqCaller{replies: map[string][]string{procHealth: {healthReply("RELEASE_VERDICT_PASS", true, "", "")}}}
}

// quotaRefusal is the control plane's refusal of a read on a spent query
// quota (control-plane internal/obsread/quota.go): ResourceExhausted, reason
// obs_query_quota, Retry-After in seconds, and the refill time as a detail.
func quotaRefusal(retryAfter time.Duration, resetsAt time.Time) *cloud.Error {
	h := http.Header{}
	if retryAfter > 0 {
		h.Set("Retry-After", fmt.Sprint(int(retryAfter.Seconds())))
	}
	e := &cloud.Error{
		HTTPStatus: http.StatusTooManyRequests, Code: cloud.CodeResourceExhausted, Reason: "obs_query_quota",
		Message: "Quota for user rg_55949942 for 3600s has been exceeded: queries = 1564/1500", Header: h,
	}
	if !resetsAt.IsZero() {
		e.Details = []cloud.ErrorDetail{{Type: "controlplane.v1.QueryQuotaExhausted",
			Debug: json.RawMessage(fmt.Sprintf(`{"lane":"agent","resetsAt":%q}`, resetsAt.Format(time.RFC3339)))}}
	}
	return e
}

// A quota refusal under --wait is waited out for its Retry-After, and the
// check then reaches its verdict: v1.21.0-rc.6 would have been proven at the
// refill instead of settled as not proven.
func TestObsCheckWaitOutlastsAQuotaRefusal(t *testing.T) {
	caller := &healthRefuser{n: 1, err: quotaRefusal(2*time.Minute, obsNow.Add(2*time.Minute)), next: passingCaller()}
	env := newObsCheckHarness()
	out, errOut, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json")
	if err != nil {
		t.Fatalf("exit = %d (%v)\n%s", exitCodeForError(err), err, errOut)
	}
	if env.sleeps != 1 || env.slept[0] != 2*time.Minute {
		t.Errorf("slept %v, want one wait of the Retry-After, 2m", env.slept)
	}
	doc := decode(t, out)
	if doc["verdict"] != "PASS" || doc["reason_code"] != nil || doc["retry_at"] != nil {
		t.Errorf("doc = %v", doc)
	}
	if !strings.Contains(errOut, "query quota is exhausted (obs_query_quota); asking again in 2m0s") {
		t.Errorf("the wait is reported on stderr: %q", errOut)
	}
}

// A Retry-After longer than the cap is waited in steps of the cap, so a quota
// raised or reset early is noticed.
func TestObsCheckQuotaWaitIsCapped(t *testing.T) {
	caller := &healthRefuser{n: 2, err: quotaRefusal(12*time.Minute, time.Time{}), next: passingCaller()}
	env := newObsCheckHarness()
	if _, errOut, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json"); err != nil {
		t.Fatalf("exit = %d (%v)\n%s", exitCodeForError(err), err, errOut)
	}
	if len(env.slept) != 2 || env.slept[0] != obsCheckMaxRefusalWait || env.slept[1] != obsCheckMaxRefusalWait {
		t.Errorf("slept %v, want two waits of %s", env.slept, obsCheckMaxRefusalWait)
	}
}

// A quota that stays spent until --timeout is exit 8 with reason_code
// quota_exhausted and no verdict, never exit 2.
func TestObsCheckQuotaUntilTheTimeoutIsExit8(t *testing.T) {
	caller := &healthRefuser{n: -1, err: quotaRefusal(2*time.Minute, time.Time{}), next: passingCaller()}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--timeout", "10m", "--json")
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Fatalf("exit = %d (%v), want 8", got, err)
	}
	if env.sleeps != 4 || caller.refused != 5 { // 0, 2, 4, 6, 8m; at 8m another 2m reaches the deadline
		t.Errorf("sleeps = %d refused reads = %d, want 4 and 5", env.sleeps, caller.refused)
	}
	doc := decode(t, out)
	if doc["exit_code"] != float64(8) || doc["reason_code"] != checkReasonQuotaExhausted || doc["waited"] != "8m0s" {
		t.Errorf("doc = %v", doc)
	}
	if _, has := doc["verdict"]; has {
		t.Errorf("a refused check judged nothing, so states no verdict: %v", doc["verdict"])
	}
	if doc["retry_at"] != obsNow.Add(10*time.Minute).Format(time.RFC3339) {
		t.Errorf("retry_at = %v, want the last refusal's Retry-After from 12:08", doc["retry_at"])
	}
	if s := fmt.Sprint(doc["suggested"]); !strings.Contains(s, "forge obs check prod --wait --json") {
		t.Errorf("suggested = %v", s)
	}
}

// A refill after --timeout cannot be outlasted: exit 8 at once, rather than
// spending the budget on reads that will be refused.
func TestObsCheckQuotaRefillAfterTheTimeoutIsExit8AtOnce(t *testing.T) {
	resets := obsNow.Add(39 * time.Minute)
	caller := &healthRefuser{n: -1, err: quotaRefusal(39*time.Minute, resets), next: passingCaller()}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json")
	if got := exitCodeForError(err); got != exitTimedOut || env.sleeps != 0 || caller.refused != 1 {
		t.Fatalf("exit = %d sleeps = %d refused = %d (%v), want 8 at once", got, env.sleeps, caller.refused, err)
	}
	doc := decode(t, out)
	if doc["reason_code"] != checkReasonQuotaExhausted || doc["retry_at"] != resets.Format(time.RFC3339) {
		t.Errorf("doc = %v", doc)
	}
	if !strings.Contains(fmt.Sprint(doc["error"]), "obs_query_quota") {
		t.Errorf("the error names the refusal: %v", doc["error"])
	}
}

// Without --wait a refusal is not a verdict either: exit 8, check again later.
func TestObsCheckQuotaWithoutWaitIsExit8(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"quota with Retry-After", quotaRefusal(2*time.Minute, time.Time{}), checkReasonQuotaExhausted},
		{"quota reason alone", &cloud.Error{Code: cloud.CodeResourceExhausted, Reason: "obs_query_quota"}, checkReasonQuotaExhausted},
		{"quota refill in a detail", quotaRefusal(0, obsNow.Add(time.Hour)), checkReasonQuotaExhausted},
		{"in-flight limit", &cloud.Error{Code: cloud.CodeResourceExhausted, Reason: "in_flight_limit", Header: http.Header{"Retry-After": {"5"}}}, checkReasonRateLimited},
		{"a proxy's bare 429", &cloud.Error{HTTPStatus: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}}, checkReasonRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &healthRefuser{n: -1, err: tc.err, next: passingCaller()}
			env := newObsCheckHarness()
			out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
			if got := exitCodeForError(err); got != exitTimedOut || env.sleeps != 0 {
				t.Fatalf("exit = %d sleeps = %d (%v), want 8 without waiting", got, env.sleeps, err)
			}
			doc := decode(t, out)
			if doc["exit_code"] != float64(8) || doc["reason_code"] != tc.code || doc["scope"].(map[string]any)["env"] != "prod" {
				t.Errorf("doc = %v", doc)
			}
			if _, has := doc["waited"]; has {
				t.Errorf("no --wait, no waited: %v", doc["waited"])
			}
		})
	}
}

// A throttle on this caller is waited out like a quota.
func TestObsCheckWaitOutlastsAThrottle(t *testing.T) {
	refusal := &cloud.Error{Code: cloud.CodeResourceExhausted, Reason: "in_flight_limit", Header: http.Header{"Retry-After": {"5"}}}
	caller := &healthRefuser{n: 2, err: refusal, next: passingCaller()}
	env := newObsCheckHarness()
	if _, errOut, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json"); err != nil {
		t.Fatalf("exit = %d (%v)\n%s", exitCodeForError(err), err, errOut)
	}
	if len(env.slept) != 2 || env.slept[0] != 5*time.Second {
		t.Errorf("slept %v, want two waits of 5s", env.slept)
	}
}

// Unavailable, DeadlineExceeded and a dropped connection are asked again
// under --wait with a backoff: the control plane rolling out is when a
// release check runs.
func TestObsCheckWaitRetriesATransientFault(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unavailable", &cloud.Error{Code: cloud.CodeUnavailable}},
		{"deadline exceeded", &cloud.Error{Code: cloud.CodeDeadlineExceeded}},
		{"a proxy's 503", &cloud.Error{HTTPStatus: http.StatusServiceUnavailable}},
		{"a dropped connection", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &healthRefuser{n: 3, err: tc.err, next: passingCaller()}
			env := newObsCheckHarness()
			out, errOut, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--json")
			if err != nil {
				t.Fatalf("exit = %d (%v)\n%s", exitCodeForError(err), err, errOut)
			}
			want := []time.Duration{obsCheckPollEvery, 2 * obsCheckPollEvery, 4 * obsCheckPollEvery}
			if fmt.Sprint(env.slept) != fmt.Sprint(want) {
				t.Errorf("slept %v, want the backoff %v", env.slept, want)
			}
			if doc := decode(t, out); doc["verdict"] != "PASS" {
				t.Errorf("doc = %v", doc)
			}
		})
	}
}

// A control plane down for all of --timeout keeps the exit it always had: 2,
// could not look. Without --wait a fault is not retried.
func TestObsCheckTransientFaultThatOutlastsTheWaitIsExit2(t *testing.T) {
	caller := &healthRefuser{n: -1, err: &cloud.Error{Code: cloud.CodeUnavailable}, next: passingCaller()}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--timeout", "5m", "--json")
	if got := exitCodeForError(err); got != exitUndetermined {
		t.Fatalf("exit = %d (%v), want 2", got, err)
	}
	// 15s, 30s, 60s, 120s, then the 65s left before the final read at 4m50s.
	if env.sleeps != 5 || env.now.Sub(obsNow) != 5*time.Minute-obsCheckFinalReadMargin {
		t.Errorf("sleeps = %d over %s: the fault is retried until the final read %s before --timeout", env.sleeps, env.now.Sub(obsNow), obsCheckFinalReadMargin)
	}
	for _, d := range env.slept {
		if d > obsCheckMaxFaultWait {
			t.Errorf("a backoff of %s exceeds the cap %s", d, obsCheckMaxFaultWait)
		}
	}
	if doc := decode(t, out); doc["reason_code"] != nil {
		t.Errorf("a fault is not a refusal: %v", doc)
	}

	caller = &healthRefuser{n: -1, err: &cloud.Error{Code: cloud.CodeUnavailable}, next: passingCaller()}
	env = newObsCheckHarness()
	if _, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--json"); exitCodeForError(err) != exitUndetermined || env.sleeps != 0 {
		t.Errorf("without --wait: exit = %d sleeps = %d, want 2 and none", exitCodeForError(err), env.sleeps)
	}
}

// A transient fault whose next backoff would pass the deadline uses what is
// left of --timeout: the sleep is cut to the final read, obsCheckFinalReadMargin
// before the deadline, and only a fault on that read is exit 2.
func TestObsCheckTransientFaultUsesTheTimeLeft(t *testing.T) {
	// --timeout 1m: faults at 0s and 15s back off 15s and 30s; at 45s the 60s
	// backoff would pass the deadline, so it sleeps the 5s left to 50s.
	want := []time.Duration{15 * time.Second, 30 * time.Second, 5 * time.Second}
	for _, tc := range []struct {
		name     string
		failures int
		exit     int
		verdict  any
	}{
		{"the final read succeeds", 3, 0, "PASS"},
		{"the final read fails", -1, exitUndetermined, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &healthRefuser{n: tc.failures, err: &cloud.Error{Code: cloud.CodeUnavailable}, next: passingCaller()}
			env := newObsCheckHarness()
			out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--timeout", "1m", "--json")
			if got := exitCodeForError(err); got != tc.exit {
				t.Fatalf("exit = %d (%v), want %d", got, err, tc.exit)
			}
			if fmt.Sprint(env.slept) != fmt.Sprint(want) {
				t.Errorf("slept %v, want %v: the last sleep ends at the final read", env.slept, want)
			}
			if got := env.now.Sub(obsNow); got != time.Minute-obsCheckFinalReadMargin {
				t.Errorf("final read at +%s, want +%s", got, time.Minute-obsCheckFinalReadMargin)
			}
			if tc.failures < 0 && caller.refused != 4 {
				t.Errorf("reads = %d, want 4: the final read is made, then no more", caller.refused)
			}
			if doc := decode(t, out); doc["verdict"] != tc.verdict || doc["reason_code"] != nil {
				t.Errorf("doc = %v", doc)
			}
		})
	}
}

// A refusal whose refill lands after the deadline is still exit 8 at once,
// however much of --timeout is left: a read before the refill is refused again.
func TestObsCheckRefusalPastTheDeadlineIsExit8AtOnce(t *testing.T) {
	caller := &healthRefuser{n: -1, err: quotaRefusal(time.Minute, time.Time{}), next: passingCaller()}
	env := newObsCheckHarness()
	out, _, err := env.run(t, cloudFacts(), caller, nil, "check", "prod", "--wait", "--timeout", "1m", "--json")
	if got := exitCodeForError(err); got != exitTimedOut || env.sleeps != 0 || caller.refused != 1 {
		t.Fatalf("exit = %d sleeps = %d reads = %d (%v), want 8 at once after one read", got, env.sleeps, caller.refused, err)
	}
	if doc := decode(t, out); doc["reason_code"] != checkReasonQuotaExhausted || doc["waited"] != "0s" {
		t.Errorf("doc = %v", doc)
	}
}

// Every other failure keeps its mapping, with or without --wait, and is not
// retried.
func TestObsCheckOtherFailuresKeepTheirExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		exit int
		code string // the document's reason_code; "" for none
	}{
		{"a ResourceExhausted that says neither reason nor retry", &cloud.Error{Code: cloud.CodeResourceExhausted}, exitUndetermined, ""},
		{"too much data", &cloud.Error{Code: cloud.CodeResourceExhausted, Message: "the query would read too much data", Header: http.Header{"Retry-After": {"60"}}}, exitUndetermined, ""},
		{"permission denied", &cloud.Error{Code: cloud.CodePermissionDenied}, exitUndetermined, checkReasonPermissionDenied},
		{"unauthenticated", &cloud.Error{Code: cloud.CodeUnauthenticated}, exitUndetermined, checkReasonUnauthenticated},
		{"unimplemented", &cloud.Error{Code: cloud.CodeUnimplemented}, exitUndetermined, ""},
		{"failed precondition", &cloud.Error{Code: cloud.CodeFailedPrecondition}, exitUndetermined, ""},
		{"invalid argument", &cloud.Error{Code: cloud.CodeInvalidArgument}, exitWrong, ""},
		{"not found", &cloud.Error{Code: cloud.CodeNotFound}, exitWrong, ""},
		{"a failure forge made itself", obsbackend.Undetermined("encode GetReleaseHealth request"), exitUndetermined, ""},
	} {
		for _, wait := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wait=%t", tc.name, wait), func(t *testing.T) {
				caller := &healthRefuser{n: -1, err: tc.err, next: passingCaller()}
				env := newObsCheckHarness()
				args := []string{"check", "prod", "--json"}
				if wait {
					args = append(args, "--wait")
				}
				out, _, err := env.run(t, cloudFacts(), caller, nil, args...)
				if got := exitCodeForError(err); got != tc.exit || env.sleeps != 0 || caller.refused != 1 {
					t.Fatalf("exit = %d sleeps = %d reads = %d (%v), want %d with one read", got, env.sleeps, caller.refused, err, tc.exit)
				}
				doc := decode(t, out)
				if code, _ := doc["reason_code"].(string); code != tc.code || doc["retry_at"] != nil {
					t.Errorf("doc = %v", doc)
				}
			})
		}
	}
}

// A refused check is an error gate for `forge gate record`, never a pass.
func TestObsCheckRefusedGateIsAnError(t *testing.T) {
	path := t.TempDir() + "/check.json"
	caller := &healthRefuser{n: -1, err: quotaRefusal(2*time.Minute, time.Time{}), next: passingCaller()}
	_, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--gate-json", path, "--json")
	if exitCodeForError(err) != exitTimedOut {
		t.Fatalf("exit = %d (%v)", exitCodeForError(err), err)
	}
	gate, gerr := gateFromDocumentFile(path, "")
	if gerr != nil || gate.Status != release.GateStatusError || !strings.Contains(gate.Summary, "query quota") {
		t.Fatalf("gate = %+v (%v), want an error gate naming the quota", gate, gerr)
	}
}

// FailureOf reads a google.rpc.RetryInfo detail when no Retry-After header
// came back, so a refusal behind a proxy that drops the header still says
// when to ask again.
func TestFailureOfReadsRetryInfo(t *testing.T) {
	e := &cloud.Error{Code: cloud.CodeResourceExhausted, Reason: "obs_query_quota",
		Details: []cloud.ErrorDetail{{Type: "google.rpc.RetryInfo", Debug: json.RawMessage(`{"retryDelay":"90s"}`)}}}
	if got := obsbackend.FailureOf(e).RetryAfter; got != 90*time.Second {
		t.Errorf("RetryAfter = %s, want 90s", got)
	}
	e.Header = http.Header{"Retry-After": {"30"}}
	if got := obsbackend.FailureOf(e).RetryAfter; got != 30*time.Second {
		t.Errorf("the header wins: RetryAfter = %s, want 30s", got)
	}
}
