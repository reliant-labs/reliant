package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
