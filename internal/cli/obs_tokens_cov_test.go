package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/exitcode"
)

const covAEnvList = "controlplane.v1.DeployService/ListEnvironments"

// covARunObsAdmin runs `forge obs <args>` with the token/receiver seams
// pointed at caller, in an empty directory with no forge.yaml (so the project
// comes from --project). It returns stdout and records the control plane the
// session was opened against.
func covARunObsAdmin(t *testing.T, caller cloudCaller, connectErr error, args ...string) (stdout string, connectedTo *string, err error) {
	t.Helper()
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	cp := new(string)
	prev := obsAdminDepsOverride
	obsAdminDepsOverride = &obsAdminDeps{
		Connect: func(_ context.Context, controlPlane, project, org string, bind bool) (obsSession, error) {
			*cp = controlPlane + "|" + project
			if bind || org != "" {
				t.Errorf("admin verbs must not create a project (bind=%v org=%q)", bind, org)
			}
			if connectErr != nil {
				return obsSession{}, connectErr
			}
			return obsSession{Caller: caller, OrgID: "org-1", ControlPlane: controlPlane}, nil
		},
		Stdout: &out,
	}
	t.Cleanup(func() { obsAdminDepsOverride = prev })
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"obs", "--project", "shop"}, args...))
	err = cmd.ExecuteContext(context.Background())
	return out.String(), cp, err
}

func covAEnvCaller(extra map[string]string) *fakeCloudCaller {
	r := map[string]string{
		covAEnvList: `{"environments":[{"id":"env-prod","name":"prod","project":"shop"}]}`,
	}
	for k, v := range extra {
		r[k] = v
	}
	return &fakeCloudCaller{replies: r}
}

func covACall(t *testing.T, c *fakeCloudCaller, proc string) fakeCloudCall {
	t.Helper()
	for _, call := range c.calls {
		if call.procedure == proc {
			return call
		}
	}
	t.Fatalf("no call to %s; calls: %+v", proc, c.calls)
	return fakeCloudCall{}
}

func TestObsTokensState(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	past, soon, later := now.Add(-time.Hour), now.Add(time.Hour), now.Add(48*time.Hour)
	tests := []struct {
		name string
		tok  wireIngestToken
		want string
	}{
		{"revoked in the past", wireIngestToken{ExpiresAt: later, RevokedAt: &past}, "revoked"},
		{"revoked exactly now", wireIngestToken{ExpiresAt: later, RevokedAt: &now}, "revoked"},
		{"rotating inside its grace period", wireIngestToken{ExpiresAt: later, RevokedAt: &soon}, "rotating (works until 2026-11-01T13:00:00Z)"},
		{"expired", wireIngestToken{ExpiresAt: past}, "expired"},
		{"expiring this very instant", wireIngestToken{ExpiresAt: now}, "expired"},
		{"inside the 14 day warning window", wireIngestToken{ExpiresAt: now.Add(13 * 24 * time.Hour)}, "expires soon: rotate it"},
		{"far from expiry", wireIngestToken{ExpiresAt: now.Add(60 * 24 * time.Hour)}, "live"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenState(tc.tok, now); got != tc.want {
				t.Errorf("tokenState = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestObsTokensList(t *testing.T) {
	far := time.Now().Add(60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	list := `{"tokens":[{"id":"jti-1","environmentId":"env-prod","name":"ci","expiresAt":"` + far + `"}]}`

	t.Run("table for one env resolves the env id first", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procListIngestTokens: list})
		out, cp, err := covARunObsAdmin(t, c, nil, "tokens", "list", "prod", "--all")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(*cp, "https://") || !strings.HasSuffix(*cp, "|shop") {
			t.Errorf("connected to %q: want the default control plane and the --project", *cp)
		}
		for _, want := range []string{"ID", "NAME", "STATE", "jti-1", "ci", "env-prod", "live"} {
			if !strings.Contains(out, want) {
				t.Errorf("table lacks %q:\n%s", want, out)
			}
		}
		var req map[string]any
		_ = json.Unmarshal([]byte(covACall(t, c, procListIngestTokens).body), &req)
		if req["orgId"] != "org-1" || req["environmentId"] != "env-prod" || req["includeRevoked"] != true {
			t.Errorf("request = %v", req)
		}
	})

	t.Run("no env argument lists the whole org", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procListIngestTokens: `{"tokens":[]}`})
		out, _, err := covARunObsAdmin(t, c, nil, "tokens", "list")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out) != "no ingest tokens" {
			t.Errorf("out = %q", out)
		}
		if body := covACall(t, c, procListIngestTokens).body; strings.Contains(body, "environmentId") || strings.Contains(body, `"includeRevoked":true`) {
			t.Errorf("an unscoped list must not name an env or ask for revoked: %s", body)
		}
		if len(c.calls) != 1 {
			t.Errorf("no env lookup expected, calls = %+v", c.calls)
		}
	})

	t.Run("json document has an array even when empty", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procListIngestTokens: `{}`})
		out, _, err := covARunObsAdmin(t, c, nil, "tokens", "list", "--json")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		if doc["schema"] != "forge.obs.tokens/v1" {
			t.Errorf("schema = %v", doc["schema"])
		}
		if toks, ok := doc["tokens"].([]any); !ok || len(toks) != 0 {
			t.Errorf("tokens = %#v, want []", doc["tokens"])
		}
	})

	t.Run("an env the control plane does not know says how to register it", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.replies[covAEnvList] = `{"environments":[]}`
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "list", "staging")
		if err == nil || !strings.Contains(err.Error(), "env staging of project shop is not registered") {
			t.Fatalf("err = %v", err)
		}
		if exitcode.Of(err) != 1 {
			t.Errorf("exit = %d, want 1 (the request is wrong)", exitcode.Of(err))
		}
	})

	t.Run("env lookup failure maps to could-not-look", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{covAEnvList: &cloud.Error{Code: cloud.CodeUnavailable}}
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "list", "prod")
		if exitcode.Of(err) != 2 {
			t.Fatalf("exit = %d (%v), want 2", exitcode.Of(err), err)
		}
	})

	t.Run("an old control plane is named, not a stack trace", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procListIngestTokens: &cloud.Error{Code: cloud.CodeUnimplemented}}
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "list")
		if err == nil || !strings.Contains(err.Error(), "no telemetry ingest tokens yet") || exitcode.Of(err) != 2 {
			t.Fatalf("err = %v exit %d", err, exitcode.Of(err))
		}
	})

	t.Run("sign-in failure is returned untouched", func(t *testing.T) {
		boom := errors.New("not signed in")
		_, _, err := covARunObsAdmin(t, covAEnvCaller(nil), boom, "tokens", "list")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}

const covAMinted = `{"token":{"id":"jti-9","name":"gh","expiresAt":"2027-02-09T00:00:00Z"},` +
	`"jwt":"j.w.t","otlpHeaders":"authorization=Bearer j.w.t","otlpEndpoint":"https://otlp.example.test",` +
	`"previous":{"id":"jti-8","revokedAt":"2026-11-02T00:00:00Z"}}`

func TestObsTokensCreate(t *testing.T) {
	t.Run("prints the exports once and sends name and ttl", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procCreateIngestToken: covAMinted})
		out, _, err := covARunObsAdmin(t, c, nil, "tokens", "create", "prod", "--name", "gh", "--ttl", "2h")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`ingest token "gh" (jti-9), expires 2027-02-09 — printed ONCE`,
			"export OTEL_EXPORTER_OTLP_ENDPOINT=",
			"https://otlp.example.test",
			"export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
			"the old token jti-8 keeps working until 2026-11-02T00:00:00Z",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		var req map[string]any
		_ = json.Unmarshal([]byte(covACall(t, c, procCreateIngestToken).body), &req)
		if req["environmentId"] != "env-prod" || req["name"] != "gh" || req["ttl"] != "7200s" || req["orgId"] != "org-1" {
			t.Errorf("request = %v", req)
		}
	})

	t.Run("json carries the otel env and the previous token", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procCreateIngestToken: covAMinted})
		out, _, err := covARunObsAdmin(t, c, nil, "tokens", "create", "prod", "--name", "gh", "--json")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		env, _ := doc["otel_env"].(map[string]any)
		if doc["schema"] != "forge.obs.token/v1" || env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" ||
			env["OTEL_EXPORTER_OTLP_HEADERS"] != "authorization=Bearer j.w.t" || doc["previous"] == nil {
			t.Errorf("doc = %v", doc)
		}
		if body := covACall(t, c, procCreateIngestToken).body; strings.Contains(body, "ttl") {
			t.Errorf("no --ttl must send no ttl: %s", body)
		}
	})

	t.Run("a machine credential is told to sign in as a person", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procCreateIngestToken: &cloud.Error{Code: cloud.CodePermissionDenied, Message: "machine credential refused"}}
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "create", "prod", "--name", "gh")
		if err == nil || !strings.Contains(err.Error(), "minted by a person, never by a machine credential") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a member without obs:write is told who can grant it", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procCreateIngestToken: &cloud.Error{Code: cloud.CodePermissionDenied, Message: "nope"}}
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "create", "prod", "--name", "gh")
		if err == nil || !strings.Contains(err.Error(), "obs:write") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("--name is required", func(t *testing.T) {
		_, _, err := covARunObsAdmin(t, covAEnvCaller(nil), nil, "tokens", "create", "prod")
		if err == nil || !strings.Contains(err.Error(), `"name"`) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestObsTokensRotate(t *testing.T) {
	c := covAEnvCaller(map[string]string{procRotateIngestToken: covAMinted})
	out, _, err := covARunObsAdmin(t, c, nil, "tokens", "rotate", "jti-8", "--grace", "90m", "--ttl", "48h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the old token jti-8 keeps working") {
		t.Errorf("output:\n%s", out)
	}
	var req map[string]any
	_ = json.Unmarshal([]byte(covACall(t, c, procRotateIngestToken).body), &req)
	if req["tokenId"] != "jti-8" || req["grace"] != "5400s" || req["ttl"] != "172800s" {
		t.Errorf("request = %v", req)
	}

	t.Run("default grace is 24h and no ttl is sent", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procRotateIngestToken: covAMinted})
		if _, _, err := covARunObsAdmin(t, c, nil, "tokens", "rotate", "jti-8", "--json"); err != nil {
			t.Fatal(err)
		}
		body := covACall(t, c, procRotateIngestToken).body
		if !strings.Contains(body, `"grace":"86400s"`) || strings.Contains(body, "ttl") {
			t.Errorf("body = %s", body)
		}
	})

	t.Run("a refusal is explained", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procRotateIngestToken: &cloud.Error{Code: cloud.CodeNotFound}}
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "rotate", "gone")
		if exitcode.Of(err) != 1 {
			t.Fatalf("err = %v exit %d", err, exitcode.Of(err))
		}
	})
}

func TestObsTokensRevoke(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
	}{
		{"revoked now", `{"revoked":true}`, "revoked jti-3\n"},
		{"already revoked", `{"revoked":false}`, "jti-3 was already revoked\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := covAEnvCaller(map[string]string{procRevokeIngestToken: tc.reply})
			out, _, err := covARunObsAdmin(t, c, nil, "tokens", "revoke", "jti-3", "--reason", "leaked")
			if err != nil {
				t.Fatal(err)
			}
			if out != tc.want {
				t.Errorf("out = %q, want %q", out, tc.want)
			}
			var req map[string]any
			_ = json.Unmarshal([]byte(covACall(t, c, procRevokeIngestToken).body), &req)
			if req["tokenId"] != "jti-3" || req["reason"] != "leaked" {
				t.Errorf("request = %v", req)
			}
		})
	}
	t.Run("a failure is mapped", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procRevokeIngestToken: &cloud.Error{Code: cloud.CodeUnavailable}}
		_, _, err := covARunObsAdmin(t, c, nil, "tokens", "revoke", "jti-3")
		if exitcode.Of(err) != 2 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestObsAdminSessionNeedsAProject(t *testing.T) {
	t.Chdir(t.TempDir())
	prev := obsAdminDepsOverride
	obsAdminDepsOverride = &obsAdminDeps{Connect: func(context.Context, string, string, string, bool) (obsSession, error) {
		t.Error("must not connect without a project")
		return obsSession{}, nil
	}, Stdout: &bytes.Buffer{}}
	t.Cleanup(func() { obsAdminDepsOverride = prev })
	cmd := newTestRootCmd(t)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"obs", "tokens", "list"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "name the project") {
		t.Fatalf("err = %v", err)
	}
}

func TestObsAdminSessionHonoursControlPlaneFlag(t *testing.T) {
	c := covAEnvCaller(map[string]string{procListIngestTokens: `{"tokens":[]}`})
	_, cp, err := covARunObsAdmin(t, c, nil, "--control-plane", "https://cp.example.test/", "tokens", "list")
	if err != nil {
		t.Fatal(err)
	}
	if *cp != "https://cp.example.test/|shop" {
		t.Errorf("connected to %q", *cp)
	}
}

func TestWriteMintedWithoutPrevious(t *testing.T) {
	var b bytes.Buffer
	tok := mintedIngestToken{OTLPEndpoint: "https://o", OTLPHeaders: "authorization=Bearer x y"}
	if err := writeMinted(&b, tok, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "keeps working") {
		t.Errorf("no previous token, yet: %s", b.String())
	}
	if !strings.Contains(b.String(), "OTEL_EXPORTER_OTLP_HEADERS=") || !strings.Contains(b.String(), "authorization=Bearer x y") {
		t.Errorf("headers missing:\n%s", b.String())
	}
}
