package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// recordingCaller is a cloudCaller that records the one call it gets and
// answers with a scripted response body.
type recordingCaller struct {
	procedure string
	req       map[string]any
	resp      string
	err       error
}

func (c *recordingCaller) Call(_ context.Context, procedure string, req, out any) error {
	c.procedure = procedure
	raw, _ := json.Marshal(req)
	_ = json.Unmarshal(raw, &c.req)
	if c.err != nil {
		return c.err
	}
	return json.Unmarshal([]byte(c.resp), out)
}

// create sends the org-token request and, with --json, puts the one-time
// secret in the document — the thing the scaffolded docs pipe into
// `gh secret set`.
func TestCloudTokenCreate_SendsTheRequestAndEmitsTheSecret(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{resp: `{"token":{"id":"tok_1","name":"github-actions","scopes":["deploy:read","deploy:write"]},"secret":"rlat_s3cret"}`}
	var out bytes.Buffer
	if err := runCloudTokenCreate(context.Background(), caller, "github-actions", []string{"deploy:read", "deploy:write"}, 0, true, &out); err != nil {
		t.Fatalf("create: %v", err)
	}
	if caller.procedure != procCreateToken {
		t.Errorf("procedure = %q, want %q", caller.procedure, procCreateToken)
	}
	if caller.req["name"] != "github-actions" {
		t.Errorf("request name = %v", caller.req["name"])
	}
	if _, has := caller.req["expiresAt"]; has {
		t.Error("no --expires-in must send no expiresAt (a never-expiring token)")
	}
	var doc struct {
		OK     bool   `json:"ok"`
		Secret string `json:"secret"`
		Token  struct {
			ID string `json:"id"`
		} `json:"token"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("--json must be one document: %v\n%s", err, out.String())
	}
	if !doc.OK || doc.Secret != "rlat_s3cret" || doc.Token.ID != "tok_1" {
		t.Errorf("document = %+v", doc)
	}
}

// --expires-in reaches the wire as an absolute expiry.
func TestCloudTokenCreate_ExpiryIsSent(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{resp: `{"token":{"id":"tok_1"},"secret":"x"}`}
	if err := runCloudTokenCreate(context.Background(), caller, "n", []string{"deploy:read"}, 3600e9, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, has := caller.req["expiresAt"]; !has {
		t.Error("--expires-in must send expiresAt")
	}
}

// A server refusal (e.g. a machine credential, which the control plane will
// not let mint a token) surfaces as the command's error, never as success.
func TestCloudTokenCreate_ServerRefusalIsAnError(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{err: errors.New("permission_denied: a human session is required")}
	err := runCloudTokenCreate(context.Background(), caller, "n", []string{"deploy:read"}, 0, true, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "human session") {
		t.Fatalf("err = %v", err)
	}
}

// A control plane that refuses token administration to a CLI login (reason
// person_session_required, control-plane#2636) gets a message that sends the
// person to the web app's API tokens page, and exit 9 — for every verb. The
// control plane's code and reason survive into the --json error facts.
func TestCloudToken_PersonSessionRequiredPointsAtTheWebApp(t *testing.T) {
	t.Setenv(cloud.ConsoleURLEnv, "")
	refusal := func(endpoint string) *cloud.Error {
		return &cloud.Error{
			HTTPStatus: 403, Code: cloud.CodePermissionDenied, Reason: cloud.ReasonPersonSessionRequired,
			Message:  "Managing access tokens or member permissions (Settings → API tokens and Members in the web app) needs a person signed in to the Reliant web app",
			Endpoint: endpoint,
		}
	}
	run := map[string]func(cloudCaller) error{
		"mint": func(c cloudCaller) error {
			return runCloudTokenCreate(context.Background(), c, "ci", []string{"deploy:write"}, 0, true, &bytes.Buffer{})
		},
		"list": func(c cloudCaller) error {
			return runCloudTokenList(context.Background(), c, false, true, &bytes.Buffer{})
		},
		"revoke": func(c cloudCaller) error {
			return runCloudTokenRevoke(context.Background(), c, "tok_1", true, &bytes.Buffer{})
		},
	}
	for action, fn := range run {
		t.Run(action, func(t *testing.T) {
			err := fn(&recordingCaller{err: refusal(cloud.DefaultEndpoint)})
			if got := exitCodeForError(err); got != exitNeedsHuman {
				t.Errorf("exit = %d, want %d (%v)", got, exitNeedsHuman, err)
			}
			msg := err.Error()
			for _, want := range []string{
				"does not let a CLI login (`forge login`) " + action + " access tokens",
				action + " tokens in the web app: https://forge.reliantlabs.io/settings/tokens (Settings → API tokens)",
				"signing in again with `forge login` does not help",
				"server said: Managing access tokens",
				"reason: person_session_required",
			} {
				if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
					t.Errorf("message missing %q:\n%s", want, msg)
				}
			}
			if facts := errorFactsOf(err); facts.ErrorCode != cloud.CodePermissionDenied || facts.ErrorReason != cloud.ReasonPersonSessionRequired {
				t.Errorf("error facts = %+v", facts)
			}
		})
	}

	// A control plane whose web app forge cannot know is named by its endpoint.
	err := runCloudTokenCreate(context.Background(), &recordingCaller{err: refusal("https://cp.example.com")}, "ci", []string{"deploy:write"}, 0, false, &bytes.Buffer{})
	if msg := err.Error(); !strings.Contains(msg, "Settings → API tokens (the web app of the control plane at https://cp.example.com)") ||
		!strings.Contains(msg, "FORGE_CONTROL_PLANE_TOKEN") {
		t.Errorf("unknown web app:\n%s", msg)
	}
}

// Only that reason is mapped: another PermissionDenied (a machine credential,
// which the control plane refuses on its own terms) keeps its own error and
// exit code, so a control plane that admits a CLI login is never second-guessed.
func TestCloudToken_OtherRefusalsAreUnchanged(t *testing.T) {
	t.Parallel()
	machine := &cloud.Error{HTTPStatus: 403, Code: cloud.CodePermissionDenied, Message: "machine credentials cannot manage machine credentials"}
	err := runCloudTokenCreate(context.Background(), &recordingCaller{err: machine}, "ci", []string{"deploy:write"}, 0, true, &bytes.Buffer{})
	var refusal *personSessionRefusal
	if errors.As(err, &refusal) {
		t.Fatalf("a refusal without person_session_required must not be mapped: %v", err)
	}
	if !errors.Is(err, machine) || exitCodeForError(err) != exitWrong {
		t.Errorf("err = %v (exit %d), want the server's error, exit 1", err, exitCodeForError(err))
	}
}

// The scaffolded release workflow and the skills send a person to the web app
// to mint the CI token, not to `forge cloud token create` under `forge login`,
// which a control plane may refuse (control-plane#2636).
func TestCITokenMintingTextPointsAtTheWebApp(t *testing.T) {
	t.Parallel()
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	release := read("../templates/ci/github/release.yml.tmpl")
	if !strings.Contains(release, "Settings → API tokens") || !strings.Contains(release, "https://forge.reliantlabs.io/settings/tokens") {
		t.Errorf("release.yml.tmpl must send a person to the web app to mint FORGE_CONTROL_PLANE_TOKEN")
	}
	if strings.Contains(release, "--json | jq -r .secret") {
		t.Errorf("release.yml.tmpl still mints the CI token with `forge cloud token create`")
	}
	for _, skill := range []string{
		"adopt/deploy", "deploy/record", "deploy/domains", "deploy/lifecycle", "observability/query",
	} {
		body := read("../templates/project/skills/forge/" + skill + "/SKILL.md")
		if !strings.Contains(body, "Settings → API tokens") {
			t.Errorf("skill %s: no pointer to the web app's Settings → API tokens", skill)
		}
		for _, stale := range []string{"forge cloud token create --name ci", "forge cloud token\n  create --env staging", "an org admin can mint\n`forge cloud token create"} {
			if strings.Contains(body, stale) {
				t.Errorf("skill %s still tells users to mint the CI token with %q", skill, stale)
			}
		}
	}
}

// A typo is refused BEFORE the round trip, naming the real vocabulary — not
// a minted token that cannot do what CI needs.
func TestParseTokenScopes(t *testing.T) {
	t.Parallel()
	got, err := parseTokenScopes(" deploy:read , deploy:write ,")
	if err != nil || strings.Join(got, ",") != "deploy:read,deploy:write" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseTokenScopes("deploy:writes"); err == nil || !strings.Contains(err.Error(), "deploy:write") {
		t.Errorf("a typo must be refused with the known scopes; err = %v", err)
	}
	if _, err := parseTokenScopes(" , "); err == nil {
		t.Error("an empty scope list must be refused")
	}
	// sourcemaps:write is a whole credential: alone it is accepted, beside
	// anything else it is refused before the round trip.
	if got, err := parseTokenScopes("sourcemaps:write"); err != nil || strings.Join(got, ",") != "sourcemaps:write" {
		t.Errorf("sourcemaps:write alone = %v, %v", got, err)
	}
	for _, mixed := range []string{"sourcemaps:write,deploy:write", "deploy:read,sourcemaps:write"} {
		if _, err := parseTokenScopes(mixed); err == nil || !strings.Contains(err.Error(), "registry-only") {
			t.Errorf("%s: err = %v, want a registry-only refusal", mixed, err)
		}
	}
}

// list never carries a secret, and --json is an array even when empty.
func TestCloudTokenList(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{resp: `{"tokens":[{"id":"tok_1","name":"github-actions","displayPrefix":"rlat_ab","scopes":["deploy:read"]},{"id":"tok_2","name":"old","revokedAt":"2026-09-01T00:00:00Z"}]}`}
	var out bytes.Buffer
	if err := runCloudTokenList(context.Background(), caller, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if caller.procedure != procListTokens || caller.req["includeRevoked"] != true {
		t.Errorf("call = %s %v", caller.procedure, caller.req)
	}
	for _, want := range []string{"github-actions", "rlat_ab", "active", "revoked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output missing %q:\n%s", want, out.String())
		}
	}

	empty := &recordingCaller{resp: `{}`}
	out.Reset()
	if err := runCloudTokenList(context.Background(), empty, false, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"tokens": []`) {
		t.Errorf("an empty list must be [] in JSON, got:\n%s", out.String())
	}
}

func TestCloudTokenRevoke(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{resp: `{}`}
	var out bytes.Buffer
	if err := runCloudTokenRevoke(context.Background(), caller, "tok_1", true, &out); err != nil {
		t.Fatal(err)
	}
	if caller.procedure != procRevokeToken || caller.req["id"] != "tok_1" {
		t.Errorf("call = %s %v", caller.procedure, caller.req)
	}
	if !strings.Contains(out.String(), `"revoked": "tok_1"`) || !strings.Contains(out.String(), `"ok": true`) {
		t.Errorf("revoke --json = %s", out.String())
	}
}

// A repo with no deploy/kcl (one that only records its own CI's deploys) mints
// its CI token on Reliant cloud, with or without --env; an env forge's KCL
// declares still resolves through its declaration.
func TestCloudTokenTarget_DefaultsToReliantCloudWithoutKCL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, env := range []string{"", "prod"} {
		f := cloudTokenFlags{env: env, token: "rlat_test"}
		ep, cred, err := f.target(context.Background(), dir)
		if err != nil {
			t.Fatalf("env %q: %v", env, err)
		}
		if ep.URL != cloud.DefaultEndpoint || cred.Token != "rlat_test" {
			t.Errorf("env %q: endpoint %q cred %q; want Reliant cloud with the flag's token", env, ep.URL, cred.Token)
		}
	}
}
