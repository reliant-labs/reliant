package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	v1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// resolveSignedOut is the resolver's own signed-out error, so the tests pin
// the line signedOutDeploy replaces against the text it really produces.
func resolveSignedOut(t *testing.T) error {
	t.Helper()
	isolatedCredentials(t)
	t.Setenv(cloudcred.HelperEnv, "")
	_, err := cloud.ResolveCredential("", cloud.Endpoint{Env: "prod", URL: "https://cp.example.com"})
	if !errors.Is(err, cloud.ErrNoCredential) || !strings.Contains(err.Error(), cloud.NoAccountHint) {
		t.Fatalf("signed-out resolve = %v; want ErrNoCredential ending in the generic no-account line", err)
	}
	return err
}

func signedOutStaticSite() *KCLEntities {
	return &KCLEntities{
		ControlPlane: &ControlPlaneEntity{},
		Frontends:    []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}},
	}
}

func signedOutService(name string) WorkloadEntity {
	w := WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeHosted}}
	w.Spec.Kind = v1alpha1.KindService
	w.Spec.Resources = v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 128 << 20}
	return w
}

// TestSignedOutDeploy_NamesTheNoAccountDeploy: a hosted deploy that fails for
// want of a credential names `--claimable` for this env, under this binary's
// name, exactly when the env's shape fits the claimable plan; the --json
// document carries the same commands in next_actions; and the missing
// credential's error_code / error_reason and errors.Is are unchanged.
func TestSignedOutDeploy_NamesTheNoAccountDeploy(t *testing.T) {
	resolved := resolveSignedOut(t)
	claimable := fmt.Sprintf("%s env deploy prod --claimable", Name())
	hint := "or deploy with no account: " + claimable + " (a live URL plus a 72-hour claim link for your user)"
	login := loginThenDeploy("prod")

	declared := signedOutStaticSite()
	declared.ControlPlane.Claimable = true
	declaredHint := `env "prod" is declared for an anonymous deploy (control_plane = forge.ControlPlane {claimable = True}): ` +
		"deploy it with " + claimable + " (a live URL plus a 72-hour claim link for your user)"
	declaredTooBig := &KCLEntities{ControlPlane: &ControlPlaneEntity{Claimable: true},
		Workloads: []WorkloadEntity{signedOutService("a"), signedOutService("b")}}
	twoServices := &KCLEntities{ControlPlane: &ControlPlaneEntity{},
		Workloads: []WorkloadEntity{signedOutService("a"), signedOutService("b")}}
	cluster := signedOutStaticSite()
	cluster.Workloads = []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster}}}

	cases := []struct {
		name     string
		e        *KCLEntities
		wantHint string // "" when no line names the no-account deploy
		wantNext []string
	}{
		{"one static site", signedOutStaticSite(), hint, []string{claimable, login}},
		{"one service", &KCLEntities{ControlPlane: &ControlPlaneEntity{},
			Workloads: []WorkloadEntity{signedOutService("api")}}, hint, []string{claimable, login}},
		{"declared claimable: signing in moves nothing", declared, declaredHint, []string{claimable}},
		// Signing in is refused for a declared env whatever its shape, so
		// --claimable stays the one action; it says what does not fit.
		{"declared claimable, over the plan", declaredTooBig, declaredHint, []string{claimable}},
		{"two services do not fit", twoServices, "", []string{login}},
		{"a part applied from this machine", cluster, "", []string{login}},
		{"no render", nil, "", []string{login}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := signedOutDeploy("prod", tc.e, resolved)
			msg := err.Error()
			if strings.Contains(msg, cloud.NoAccountHint) {
				t.Errorf("the generic no-account line survived:\n%s", msg)
			}
			if tc.wantHint == "" {
				if strings.Contains(msg, "--claimable") {
					t.Errorf("names --claimable for an env that cannot deploy that way:\n%s", msg)
				}
			} else if !strings.HasSuffix(msg, "\n"+tc.wantHint) {
				t.Errorf("does not end in %q:\n%s", tc.wantHint, msg)
			}
			if !strings.HasPrefix(msg, "no control-plane credential for https://cp.example.com\n") {
				t.Errorf("the resolver's own text was not kept:\n%s", msg)
			}
			if !errors.Is(err, cloud.ErrNoCredential) {
				t.Errorf("errors.Is(ErrNoCredential) lost: %v", err)
			}

			// The two --json documents a deploy writes.
			var env jsonEnvelope
			env.stamp(err)
			r := newDeployReport("prod", true)
			r.finish(err, 0)
			for name, doc := range map[string]any{"envelope": env, "deploy report": r.document()} {
				raw, _ := json.Marshal(doc)
				var got struct {
					ErrorCode   string   `json:"error_code"`
					ErrorReason string   `json:"error_reason"`
					NextActions []string `json:"next_actions"`
					Error       string   `json:"error"`
				}
				_ = json.Unmarshal(raw, &got)
				if got.ErrorCode != cloud.CodeUnauthenticated || got.ErrorReason != ReasonNoControlPlaneCredential {
					t.Errorf("%s: error_code/error_reason = %q/%q, want unchanged", name, got.ErrorCode, got.ErrorReason)
				}
				if !slices.Equal(got.NextActions, tc.wantNext) {
					t.Errorf("%s: next_actions = %q, want %q", name, got.NextActions, tc.wantNext)
				}
				if got.Error != msg {
					t.Errorf("%s: error = %q, want the text output's %q", name, got.Error, msg)
				}
			}
		})
	}
}

// TestRunHostedDeploy_SignedOutNamesTheNoAccountDeploy: the hosted publish
// stage's own credential site (runHostedDeploy) words a missing credential
// through signedOutDeploy for every env, a declared-claimable one included —
// it once returned its own sentence there, with no next_actions.
func TestRunHostedDeploy_SignedOutNamesTheNoAccountDeploy(t *testing.T) {
	isolatedCredentials(t)
	t.Setenv(cloudcred.HelperEnv, "")
	stubShapeManifests(t, "")
	claimable := fmt.Sprintf("%s env deploy prod --claimable", Name())

	for _, tc := range []struct {
		name     string
		declared bool
		wantNext []string
		wantLine string
	}{
		{"declared claimable", true, []string{claimable},
			"is declared for an anonymous deploy (control_plane = forge.ControlPlane {claimable = True}): deploy it with " + claimable},
		{"undeclared env that fits the plan", false, []string{claimable, loginThenDeploy("prod")},
			"or deploy with no account: " + claimable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := signedOutStaticSite()
			site.ControlPlane = &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://cp.invalid", Claimable: tc.declared}
			var err error
			captureStdout(t, func() { err = runHostedDeploy(context.Background(), "prod", site, hostedGroupOf(), deployOptions{}) })
			if !errors.Is(err, cloud.ErrNoCredential) {
				t.Fatalf("runHostedDeploy = %v; want it to stop at the missing credential", err)
			}
			if !strings.Contains(err.Error(), tc.wantLine) {
				t.Errorf("error does not carry %q:\n%s", tc.wantLine, err)
			}
			facts := errorFactsOf(err)
			if facts.ErrorReason != ReasonNoControlPlaneCredential || !slices.Equal(facts.NextActions, tc.wantNext) {
				t.Errorf("error_reason %q, next_actions %q; want %q, %q",
					facts.ErrorReason, facts.NextActions, ReasonNoControlPlaneCredential, tc.wantNext)
			}
		})
	}
}

// TestSignedOutDeploy_LeavesOtherFailuresAlone: only a missing credential is
// reworded. A helper's or the control plane's refusal names its own fix (a
// grant, not an anonymous deploy), and a second wrap adds nothing.
func TestSignedOutDeploy_LeavesOtherFailuresAlone(t *testing.T) {
	t.Parallel()
	site := signedOutStaticSite()
	helperDenied := fmt.Errorf("%w for https://cp: %w", cloud.ErrNoCredential,
		&cloudcred.HelperError{Code: cloudcred.CodeDenied, Message: "no session can authorize", Reason: "scope_missing"})
	for _, err := range []error{
		nil,
		errors.New("boom"),
		helperDenied,
		&cloud.Error{Code: "permission_denied", Reason: "scope_missing", Message: "m"},
	} {
		if got := signedOutDeploy("prod", site, err); got != err { //nolint:errorlint // identity is the assertion
			t.Errorf("signedOutDeploy(%v) = %v, want it unchanged", err, got)
		}
	}
	once := signedOutDeploy("prod", site, fmt.Errorf("%w for https://cp", cloud.ErrNoCredential))
	if twice := signedOutDeploy("prod", site, once); twice != once { //nolint:errorlint // identity is the assertion
		t.Errorf("a second wrap changed the error: %v", twice)
	}
	if strings.Count(once.Error(), "--claimable") != 1 {
		t.Errorf("the no-account line is not said once:\n%s", once)
	}
}
