package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// FI7: the identity line, and the deploy readiness wait's "waiting on
// identity". The control plane's answers are stated in its own wire form
// (proto3 JSON, enum value names) through the same fake control plane the
// rollout tests use, so a misspelt field fails here rather than in prod.

func identityDoc(js string) identityStatusDocument {
	var d identityStatusDocument
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		panic(err)
	}
	return d
}

var (
	identityNotDeclared  = identityDoc(`{"notEnabledReason":"IDENTITY_NOT_ENABLED_REASON_NOT_DECLARED"}`)
	identityPending      = identityDoc(`{"notEnabledReason":"IDENTITY_NOT_ENABLED_REASON_PENDING"}`)
	identityCreating     = identityDoc(`{"hasInstance":true,"state":"IDENTITY_STATE_CREATING"}`)
	identityIsReady      = identityDoc(`{"hasInstance":true,"state":"IDENTITY_STATE_READY","issuer":"https://shop-prod-1a2b3c.reliantidentity.com"}`)
	identityIsFailing    = identityDoc(`{"hasInstance":true,"state":"IDENTITY_STATE_FAILING","lastError":"zitadel: 503"}`)
	identityIsRefused    = identityDoc(`{"refused":"share_with names an environment of another project"}`)
	identityIsDegraded   = identityDoc(`{"hasInstance":true,"state":"IDENTITY_STATE_DEGRADED","deliveries":[{"cluster":"prod","namespace":"shop","name":"forge-identity-identity","state":"IDENTITY_DELIVERY_STATE_FAILING","lastError":"forbidden"}]}`)
	identityIsWithdrawn  = identityDoc(`{"hasInstance":true,"state":"IDENTITY_STATE_WITHDRAWN"}`)
	identityPredatesFlag = identityDoc(`{}`) // a control plane without not_enabled_reason, env declares none
)

func TestClassifyIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  identityStatusDocument
		want string // the line; "" means no line at all
	}{
		{"not declared", identityNotDeclared, ""},
		{"an older control plane, nothing declared", identityPredatesFlag, ""},
		{"declared, not created yet", identityPending, "identity: provisioning (the identity reconciler has not created the instance yet)"},
		{"creating", identityCreating, "identity: provisioning (creating)"},
		{"ready", identityIsReady, "identity: ready"},
		{"failing", identityIsFailing, "identity: failed: zitadel: 503"},
		{"refused", identityIsRefused, "identity: failed: the control plane refused the declaration: share_with names an environment of another project"},
		{"degraded", identityIsDegraded, "identity: degraded: delivery prod/shop/forge-identity-identity: forbidden"},
		{"withdrawn", identityIsWithdrawn, "identity: withdrawn (withdrawn)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyIdentity(tc.doc)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("want no line, got %q", got.line())
			case tc.want != "" && (got == nil || got.line() != tc.want):
				t.Fatalf("line = %v, want %q", got, tc.want)
			}
		})
	}
	if r := classifyIdentity(identityIsRefused); !r.terminal {
		t.Error("a refused declaration is terminal: only a new deploy changes it")
	}
}

func identityRollout(identity ...identityStatusDocument) *fakeRolloutService {
	fake := newFakeRollout(wireRolloutPhaseSucceeded)
	fake.identity = identity
	return fake
}

func TestEnvWait_WaitsOnIdentityUntilItIsReady(t *testing.T) {
	fake := identityRollout(identityPending, identityCreating, identityIsReady)
	opts := waitOpts(fake)
	opts.Timeout = 5 * opts.Timeout
	var (
		report envWaitReport
		err    error
	)
	out := captureStdout(t, func() { report, err = waitForRollout(context.Background(), "prod", opts) })
	if err != nil {
		t.Fatalf("a rollout that succeeded and an identity that became ready: %v", err)
	}
	if report.Identity == nil || report.Identity.State != identityReady || report.Phase != "succeeded" {
		t.Fatalf("report: phase %q identity %+v", report.Phase, report.Identity)
	}
	var phases []string
	for _, tr := range report.Transitions {
		phases = append(phases, tr.Phase)
	}
	if got := strings.Join(phases, ","); got != "waiting_on_identity,succeeded" {
		t.Fatalf("transitions = %s", got)
	}
	for _, want := range []string{"prod: rollout succeeded; waiting on identity: provisioning (the identity reconciler", "prod: rollout succeeded; waiting on identity: provisioning (creating)", "prod: succeeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress must say %q:\n%s", want, out)
		}
	}
	if fake.identityCalls != 3 {
		t.Errorf("identity read %d times, want once per poll until ready (3)", fake.identityCalls)
	}
	// The final report a deploy prints carries the line.
	if text := captureStdout(t, func() { renderEnvWaitText(os.Stdout, report) }); !strings.Contains(text, "  identity: ready") {
		t.Errorf("the report must carry the identity line:\n%s", text)
	}
}

func TestEnvWait_IdentityExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity []identityStatusDocument
		failFast bool
		once     bool
		want     int
		says     string
	}{
		{"not declared: the rollout alone decides", []identityStatusDocument{identityNotDeclared}, false, false, exitOK, ""},
		{"ready", []identityStatusDocument{identityIsReady}, false, false, exitOK, ""},
		{"withdrawn does not hold a deploy", []identityStatusDocument{identityIsWithdrawn}, false, false, exitOK, ""},
		{"still provisioning at the deadline", []identityStatusDocument{identityCreating}, false, false, exitTimedOut, "still waiting on its managed identity"},
		{"a single read while provisioning", []identityStatusDocument{identityPending}, false, true, exitTimedOut, "identity: provisioning"},
		{"failing at the deadline", []identityStatusDocument{identityIsFailing}, false, false, exitWrong, "identity: failed: zitadel: 503"},
		{"degraded at the deadline", []identityStatusDocument{identityIsDegraded}, false, false, exitWrong, "identity: degraded"},
		{"refused is terminal", []identityStatusDocument{identityIsRefused}, false, false, exitWrong, "refused the declaration"},
		{"failing with --fail-fast", []identityStatusDocument{identityIsFailing}, true, false, exitWrong, "not usable"},
		{"failing, then recovered", []identityStatusDocument{identityIsFailing, identityIsReady}, false, false, exitOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := identityRollout(tc.identity...)
			opts := waitOpts(fake)
			opts.FailFast = tc.failFast
			if tc.once {
				opts.Once, opts.Timeout = true, 0
			}
			var err error
			captureStdout(t, func() { _, err = waitForRollout(context.Background(), "prod", opts) })
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.want, err)
			}
			if tc.says != "" && !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("the message must say %q:\n%v", tc.says, err)
			}
			if (tc.name == "refused is terminal" || tc.failFast) && fake.identityCalls != 1 {
				t.Errorf("a terminal identity failure must stop at the first read, read %d times", fake.identityCalls)
			}
		})
	}
}

// A control plane that cannot be asked about identity never holds a release
// whose rollout succeeded: forge cannot tell there is anything to wait for.
// The report says so, and a permanent refusal is not asked again.
func TestEnvWait_AnUnreadableIdentityIsReportedNotWaitedOn(t *testing.T) {
	fake := identityRollout()
	fake.identityErr = &cloud.Error{Code: cloud.CodeUnimplemented, Procedure: procIdentityStatus, Message: "unknown procedure"}
	fake.phases = []string{wireRolloutPhaseProgressing, wireRolloutPhaseProgressing, wireRolloutPhaseSucceeded}
	var (
		report envWaitReport
		err    error
	)
	captureStdout(t, func() { report, err = waitForRollout(context.Background(), "prod", waitOpts(fake)) })
	if err != nil {
		t.Fatalf("an unreadable identity failed the wait: %v", err)
	}
	if report.Identity == nil || report.Identity.State != identityUnknown || !strings.Contains(report.Identity.Reason, "unknown procedure") {
		t.Fatalf("the report must say identity could not be read: %+v", report.Identity)
	}
	if fake.identityCalls != 1 {
		t.Errorf("an unimplemented identity API was asked %d times, want once", fake.identityCalls)
	}
}

// Once the env is known to declare an identity, a failed read does not release
// the wait: the last state still holds it.
func TestEnvWait_AFailedReadOfADeclaredIdentityKeepsWaiting(t *testing.T) {
	fake := identityRollout(identityCreating)
	opts := waitOpts(fake)
	opts.Identity = func() func(context.Context, waitTarget) (identityStatusDocument, error) {
		n := 0
		return func(context.Context, waitTarget) (identityStatusDocument, error) {
			n++
			if n == 1 {
				return identityCreating, nil
			}
			return identityStatusDocument{}, &cloud.Error{Code: cloud.CodeUnavailable, Message: "control plane restarting"}
		}
	}()
	var err error
	captureStdout(t, func() { _, err = waitForRollout(context.Background(), "prod", opts) })
	if got := exitCodeForError(err); got != exitTimedOut || !strings.Contains(err.Error(), "latest read failed") {
		t.Fatalf("exit %d, %v", got, err)
	}
}

// The wait reads the identity of the env it resolved, for the credential's own
// org.
func TestEnvWait_ReadsTheResolvedEnvsIdentity(t *testing.T) {
	var asked map[string]any
	fake := identityRollout(identityIsReady)
	client := callerFunc(func(ctx context.Context, proc string, req, out any) error {
		if proc == procIdentityStatus {
			asked, _ = req.(map[string]any)
		}
		return fake.Call(ctx, proc, req, out)
	})
	opts := waitOpts(fake)
	opts.Target = func(context.Context, string) (waitTarget, error) {
		return waitTarget{Client: client, EnvironmentID: "env-prod-uuid"}, nil
	}
	captureStdout(t, func() { _, _ = waitForRollout(context.Background(), "prod", opts) })
	if asked["orgId"] != "org-1" || asked["environmentId"] != "env-prod-uuid" {
		t.Fatalf("identity request = %v", asked)
	}
}

type callerFunc func(ctx context.Context, proc string, req, out any) error

func (f callerFunc) Call(ctx context.Context, proc string, req, out any) error {
	return f(ctx, proc, req, out)
}

// `forge env status <env>` for a hosted env carries the identity line in text
// and --json, and it never changes the verdict.
func TestEnvStatus_HostedEnvReportsItsIdentity(t *testing.T) {
	pin := sha("1")
	_, store := hostedPromoteFixture(t, "v1")
	read := func(context.Context, string, string) (wireRollout, error) {
		return wireRollout{Workloads: []wireWorkloadRollout{{
			Name: "api", Artifact: "api", PinnedDigest: pin, ObservedDigest: pin,
			ObservedState: "DEPLOY_OBSERVED_STATE_READY", Phase: wireRolloutPhaseSucceeded,
		}}}, nil
	}
	for _, tc := range []struct {
		name string
		doc  identityStatusDocument
		line string
	}{
		{"failed", identityIsFailing, "identity: failed: zitadel: 503"},
		{"ready", identityIsReady, "identity: ready"},
		{"none declared", identityNotDeclared, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			opts := envStatusOptions{
				Bindings: store, HostedRollout: read, Resolver: stubResolver{},
				HostedIdentity: func(context.Context, string) (identityStatusDocument, error) { return tc.doc, nil },
			}
			var err error
			out := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
			if err != nil {
				t.Fatalf("identity must not change the verdict: %v", err)
			}
			if tc.line != "" && !strings.Contains(out, tc.line) {
				t.Fatalf("text must carry %q:\n%s", tc.line, out)
			}
			if tc.line == "" && strings.Contains(out, "identity:") {
				t.Fatalf("an env that declares no identity gets no line:\n%s", out)
			}

			opts.JSON = true
			raw := captureStdout(t, func() { err = runEnvStatusRelease(context.Background(), "prod", opts) })
			var doc struct {
				Identity *envIdentityReport `json:"identity"`
			}
			if jerr := json.Unmarshal([]byte(raw), &doc); jerr != nil || err != nil {
				t.Fatalf("json: %v %v\n%s", jerr, err, raw)
			}
			if (doc.Identity != nil) != (tc.line != "") || (doc.Identity != nil && doc.Identity.line() != tc.line) {
				t.Fatalf("json identity = %+v, want %q", doc.Identity, tc.line)
			}
		})
	}
}
