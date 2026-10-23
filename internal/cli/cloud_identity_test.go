package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// Run real Cobra parsing, credential resolution and Connect HTTP calls. Only
// KCL rendering is replaced: no native runtime or subprocess in the short tier.
func identityTestTarget(t *testing.T, endpoint string) {
	t.Helper()
	previous := resolveIdentityCloudTarget
	resolveIdentityCloudTarget = func(_ context.Context, env, token string) (cloud.Endpoint, cloud.Credential, error) {
		if env != "prod" {
			return cloud.Endpoint{}, cloud.Credential{}, fmt.Errorf("unexpected env %q", env)
		}
		ep, err := cloud.ResolveEndpoint(env, &cloud.Declaration{Endpoint: endpoint, TokenEnv: "IDENTITY_TEST_TOKEN"})
		if err != nil {
			return ep, cloud.Credential{}, err
		}
		cred, err := cloud.ResolveCredential(token, ep)
		return ep, cred, err
	}
	t.Cleanup(func() { resolveIdentityCloudTarget = previous })
	withProjectName(t, "shop")
}

func executeIdentity(args ...string) (string, error) {
	cmd := newCloudCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs(append([]string{"identity"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestCloudIdentityWire(t *testing.T) {
	for _, tc := range []struct {
		name, proc, response string
		args                 []string
		fields               map[string]any
		want                 []string
	}{
		{"status", procIdentityStatus,
			`{"hasInstance":true,"state":"IDENTITY_STATE_READY","issuer":"https://issuer.example","apps":[{"name":"prod:api","clientId":"public-client","brokerToken":"do-not-print"}],"deliveries":[{"kind":"identity","state":"IDENTITY_DELIVERY_STATE_DELIVERED"}],"brokerToken":"do-not-print"}`,
			[]string{"status", "prod", "--json"}, nil, []string{`"ok": true`, `"state": "IDENTITY_STATE_READY"`, "public-client", "IDENTITY_DELIVERY_STATE_DELIVERED"}},
		{"users", procIdentityUsers,
			`{"users":[{"userId":"u1","email":"recipient@example.com","state":"IDENTITY_USER_STATE_INVITED","roles":["member"],"password":"do-not-print"}],"roles":[{"key":"owner","declared":true}],"nextPageToken":"next"}`,
			[]string{"users", "prod", "--query", "recipient", "--page-size", "12", "--page-token", "previous", "--json"},
			map[string]any{"query": "recipient", "pageSize": float64(12), "pageToken": "previous"}, []string{`"ok": true`, `"nextPageToken": "next"`, `"declared": true`, "recipient@example.com"}},
		{"invite", procIdentityInvite, `{"userId":"new-user","created":true,"password":"do-not-print"}`,
			[]string{"invite", "prod", "--email", "recipient@example.com", "--role", "member", "--role", "operator", "--json"},
			map[string]any{"email": "recipient@example.com", "roles": []any{"member", "operator"}}, []string{`"userId": "new-user"`, `"created": true`, `"ok": true`}},
		{"existing account", procIdentityInvite, `{"userId":"existing","created":false}`,
			[]string{"invite", "prod", "--email", "recipient@example.com", "--role", "member"},
			map[string]any{"email": "recipient@example.com", "roles": []any{"member"}}, []string{"Invited recipient@example.com (user existing, created=false)"}},
		{"invite --return-code", procIdentityInvite, `{"userId":"new-user","created":true,"code":{"code":"c0de","resetUrl":"https://app.example/auth/reset?code=c0de"}}`,
			[]string{"invite", "prod", "--email", "recipient@example.com", "--role", "member", "--return-code"},
			map[string]any{"email": "recipient@example.com", "roles": []any{"member"}, "returnCode": true}, []string{"created=true", "a credential", "https://app.example/auth/reset?code=c0de"}},
		{"invite --return-code, account has a credential", procIdentityInvite, `{"userId":"existing","created":false}`,
			[]string{"invite", "prod", "--email", "recipient@example.com", "--role", "member", "--return-code"},
			map[string]any{"email": "recipient@example.com", "roles": []any{"member"}, "returnCode": true}, []string{"already has a credential", "reset --return-code"}},
		{"status mail", procIdentityStatus, `{"hasInstance":true,"state":"IDENTITY_STATE_READY","mailDelivery":"IDENTITY_MAIL_DELIVERY_NONE"}`,
			[]string{"status", "prod"}, nil, []string{"Mail: none (invite and reset need --return-code"}},
		{"status mail json", procIdentityStatus, `{"hasInstance":true,"mailDelivery":"IDENTITY_MAIL_DELIVERY_SMTP"}`,
			[]string{"status", "prod", "--json"}, nil, []string{`"mailDelivery": "IDENTITY_MAIL_DELIVERY_SMTP"`}},
		{"empty users", procIdentityUsers, `{}`,
			[]string{"users", "prod", "--json"}, map[string]any{"pageSize": float64(20), "pageToken": ""}, []string{`"users": []`, `"roles": []`}},
		{"status absent", procIdentityStatus, `{"refused":"identity is not declared"}`,
			[]string{"status", "prod"}, nil, []string{"No managed identity instance", "Refused: identity is not declared"}},
		{"status not enabled", procIdentityStatus, `{"notEnabledReason":"IDENTITY_NOT_ENABLED_REASON_NOT_DECLARED","notEnabledMessage":"This environment declares no managed identity."}`,
			[]string{"status", "prod"}, nil, []string{"No managed identity instance", "This environment declares no managed identity."}},
		{"status names environments", procIdentityStatus,
			`{"hasInstance":true,"state":"IDENTITY_STATE_READY","ownerEnvironmentId":"owner-env","ownerEnvironment":{"id":"owner-env","name":"prod","project":"shop"},"sharerEnvironmentIds":["s1","gone"],"sharerEnvironments":[{"id":"s1","name":"staging","project":"shop"},{"id":"gone"}],"deliveries":[{"kind":"identity","cluster":"c","namespace":"n","name":"x","state":"IDENTITY_DELIVERY_STATE_DELIVERED","lastWrittenAt":"2026-10-22T09:00:00Z"}]}`,
			[]string{"status", "prod"}, nil, []string{"Owner: shop/prod", "Shared with: shop/staging", "Shared with: gone", "written 2026-10-22T09:00:00Z"}},
		{"users with last sign-in and a total", procIdentityUsers,
			`{"users":[{"userId":"u1","email":"active@example.com","state":"IDENTITY_USER_STATE_ACTIVE","lastSignInAt":"2026-10-20T08:30:00Z"},{"userId":"u2","email":"new@example.com","state":"IDENTITY_USER_STATE_INVITED"}],"totalSize":7}`,
			[]string{"users", "prod"}, map[string]any{"pageSize": float64(20), "pageToken": ""}, []string{"LAST SIGN-IN", "2026-10-20T08:30:00Z", "2 of 7 user(s)"}},
		{"export", procIdentityExport, `{"exportUrl":"https://storage.example/org/env/on-demand.json?X-Goog-Signature=abc","expiresAt":"2026-10-22T10:10:00Z"}`,
			[]string{"export", "prod"}, nil, []string{"a credential", "password hash", "https://storage.example/org/env/on-demand.json?X-Goog-Signature=abc", "at 2026-10-22T10:10:00Z"}},
		{"export json", procIdentityExport, `{"exportUrl":"https://storage.example/x","expiresAt":"2026-10-22T10:10:00Z"}`,
			[]string{"export", "prod", "--json"}, nil, []string{`"ok": true`, `"exportUrl": "https://storage.example/x"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer opaque-human-session" {
					t.Errorf("invalid Connect request headers/method")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/" + cloud.ProcGetOrganization:
					if len(body) != 0 {
						t.Errorf("org lookup must not choose an org: %v", body)
					}
					_, _ = fmt.Fprintf(w, `{"%s":{"orgId":"authenticated-org"}}`, orgResponseKey)
				case "/controlplane.v1.DeployService/ListEnvironments":
					if !reflect.DeepEqual(body, map[string]any{"project": "shop", "search": "prod"}) {
						t.Errorf("environment lookup = %v", body)
					}
					_, _ = w.Write([]byte(`{"environments":[{"id":"wrong","name":"prod-eu","project":"shop"},{"id":"other","name":"prod","project":"another"},{"id":"exact-env","name":"prod","project":"shop"}]}`))
				case "/" + tc.proc:
					want := map[string]any{"orgId": "authenticated-org", "environmentId": "exact-env"}
					for key, value := range tc.fields {
						want[key] = value
					}
					if !reflect.DeepEqual(body, want) {
						t.Errorf("identity request = %#v, want %#v", body, want)
					}
					_, _ = w.Write([]byte(tc.response))
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			identityTestTarget(t, srv.URL)
			t.Setenv("IDENTITY_TEST_TOKEN", "opaque-human-session")
			out, err := executeIdentity(tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 3 || calls[2] != "/"+tc.proc {
				t.Fatalf("calls = %v", calls)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q: %s", want, out)
				}
			}
			if strings.Contains(out, "opaque-human-session") || strings.Contains(out, "do-not-print") {
				t.Error("output leaked credential or unrecognized secret fields")
			}
		})
	}
}

func TestCloudIdentityRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, stage, reply, want string
		status                   int
	}{
		{"scope", "org", `{"code":"permission_denied","message":"credential lacks deploy:read"}`, "deploy:read", 403},
		{"no org", "org", `{}`, "no organization id", 200},
		{"unknown env", "env", `{"environments":[{"id":"e","name":"prod-eu"}]}`, "named \"prod\"", 200},
		{"ambiguous env", "env", `{"environments":[{"id":"a","name":"prod"},{"id":"b","name":"prod"}]}`, "refusing to guess", 200},
		{"machine", "identity", `{"code":"permission_denied","message":"a human session is required"}`, "human session", 403},
		{"cross org", "identity", `{"code":"permission_denied","message":"environment belongs to a different organization"}`, "different organization", 403},
		{"not ready", "identity", `{"code":"failed_precondition","message":"identity is not ready"}`, "not ready", 400},
		{"declared role", "identity", `{"code":"failed_precondition","message":"role is managed in deploy/kcl/identity.k"}`, "identity.k", 400},
		{"no admin", "identity", `{"code":"permission_denied","message":"org admin required"}`, "org admin", 403},
		{"incomplete success", "identity", `{}`, "no user id", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stages []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				stage := "identity"
				reply := `{"userId":"u1","created":true}`
				if r.URL.Path == "/"+cloud.ProcGetOrganization {
					stage = "org"
					reply = fmt.Sprintf(`{"%s":{"orgId":"org"}}`, orgResponseKey)
				} else if strings.HasSuffix(r.URL.Path, "/ListEnvironments") {
					stage = "env"
					reply = `{"environments":[{"id":"e","name":"prod"}]}`
				}
				stages = append(stages, stage)
				w.Header().Set("Content-Type", "application/json")
				if stage == tc.stage {
					w.WriteHeader(tc.status)
					reply = tc.reply
				}
				_, _ = w.Write([]byte(reply))
			}))
			defer srv.Close()
			identityTestTarget(t, srv.URL)
			t.Setenv("IDENTITY_TEST_TOKEN", "human-token")
			out, err := executeIdentity("invite", "prod", "--email", "explicit@example.com", "--role", "member", "--json")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if stages[len(stages)-1] != tc.stage || out != "" {
				t.Errorf("must stop on refusal without success output: %v %q", stages, out)
			}
		})
	}
}

func TestCloudIdentityParsing(t *testing.T) {
	for _, args := range [][]string{
		{"typo"}, {"status"}, {"users", "prod", "extra"},
		{"invite", "prod", "--role", "member"},
		{"invite", "prod", "--email", "explicit@example.com"},
		{"invite", "prod", "--email", "", "--role", "member"},
		{"invite", "prod", "--email", "Explicit <explicit@example.com>", "--role", "member"},
		{"invite", "prod", "--email", "explicit@example.com", "--role", ""},
		{"invite", "prod", "--email", "explicit@example.com", "--role", "member", "--password", "forbidden"},
		{"status", "prod", "--org", "other"},
		{"users", "prod", "--page-size", "101"},
		{"users", "prod", "--page-size", "-1"},
		{"users", "prod", "--query", strings.Repeat("x", 201)},
		{"users", "prod", "--page-token", strings.Repeat("x", 65)},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			previous := resolveIdentityCloudTarget
			resolveIdentityCloudTarget = func(context.Context, string, string) (cloud.Endpoint, cloud.Credential, error) {
				t.Fatal("invalid command reached credential resolution")
				return cloud.Endpoint{}, cloud.Credential{}, nil
			}
			t.Cleanup(func() { resolveIdentityCloudTarget = previous })
			if _, err := executeIdentity(args...); err == nil {
				t.Fatal("invalid command succeeded")
			}
		})
	}
	for _, args := range [][]string{{"--help"}, {"status", "--help"}, {"users", "--help"}, {"invite", "--help"}} {
		if out, err := executeIdentity(args...); err != nil || !strings.Contains(out, "Usage:") {
			t.Errorf("help %v: %v %s", args, err, out)
		}
	}
}

// A refused export is the control plane's sentence, and an answer without a
// URL is an error, never an empty success.
func TestCloudIdentityExportRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		status            int
	}{
		{"disabled", `{"code":"failed_precondition","message":"export is not enabled for this deployment"}`, "export is not enabled", 400},
		{"no grant", `{"code":"permission_denied","message":"downloading an identity export needs the identity:manage permission, which you do not hold"}`, "identity:manage", 403},
		{"no url", `{}`, "no download URL", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/"+cloud.ProcGetOrganization:
					_, _ = fmt.Fprintf(w, `{"%s":{"orgId":"org"}}`, orgResponseKey)
				case strings.HasSuffix(r.URL.Path, "/ListEnvironments"):
					_, _ = w.Write([]byte(`{"environments":[{"id":"e","name":"prod","project":"shop"}]}`))
				default:
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.reply))
				}
			}))
			defer srv.Close()
			identityTestTarget(t, srv.URL)
			t.Setenv("IDENTITY_TEST_TOKEN", "human-token")
			out, err := executeIdentity("export", "prod")
			if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
				t.Fatalf("out=%q err=%v, want an error naming %q", out, err, tc.want)
			}
		})
	}
}

func TestCloudIdentityNoCredential(t *testing.T) {
	isolatedCredentials(t)
	identityTestTarget(t, "https://identity.invalid")
	out, err := executeIdentity("status", "prod")
	if err == nil || !strings.Contains(err.Error(), "credential") || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
