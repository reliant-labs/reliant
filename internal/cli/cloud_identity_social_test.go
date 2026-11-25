package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

const testSocialCallback = "https://acme-prod-abc123.reliantidentity.com/idps/callback"

// authorizeURL is an authorize URL shaped as Zitadel v4 builds it.
func authorizeURL(endpoint, clientID, scope, redirect string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {scope}, "state": {"3209487234"}}
	return endpoint + "?" + q.Encode()
}

func TestVerifySocialAuthURL(t *testing.T) {
	const gh, google = "https://github.com/login/oauth/authorize", "https://accounts.google.com/o/oauth2/v2/auth"
	for _, tc := range []struct {
		name, typ, url string
		wantOK         bool
		wantProblem    string
	}{
		{"github", "github", authorizeURL(gh, "Iv1.abc", "read:user user:email", testSocialCallback), true, ""},
		{"google", "google", authorizeURL(google, "x.apps.googleusercontent.com", "openid profile email", testSocialCallback), true, ""},
		{"another issuer's callback", "google", authorizeURL(google, "x", "openid", "https://custom.example/idps/callback"), false,
			`the issuer sends redirect_uri "https://custom.example/idps/callback", but the callback registered at google is "` + testSocialCallback + `"`},
		{"a github URL for google", "google", authorizeURL(gh, "x", "openid", testSocialCallback), false, "want https://accounts.google.com/o/oauth2/v2/auth"},
		{"plain http", "github", authorizeURL("http://github.com/login/oauth/authorize", "x", "read:user", testSocialCallback), false, "want https://github.com/login/oauth/authorize"},
		{"no client id", "github", authorizeURL(gh, "", "read:user", testSocialCallback), false, "no client_id"},
		{"no scope", "github", authorizeURL(gh, "x", "", testSocialCallback), false, "no scope"},
		{"no state", "github", gh + "?client_id=x&scope=read%3Auser&redirect_uri=" + url.QueryEscape(testSocialCallback), false, "no state"},
		{"unknown type", "gitlab", authorizeURL(gh, "x", "read:user", testSocialCallback), false, `unknown provider type "gitlab"`},
		{"garbage", "github", "::", false, "no usable authorize URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := verifySocialAuthURL(tc.typ, tc.url, testSocialCallback)
			if c.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (problems %v)", c.OK, tc.wantOK, c.Problems)
			}
			if tc.wantProblem != "" && !strings.Contains(strings.Join(c.Problems, "\n"), tc.wantProblem) {
				t.Errorf("problems = %v, want one containing %q", c.Problems, tc.wantProblem)
			}
		})
	}
	if c := verifySocialAuthURL("github", authorizeURL(gh, "x", "read:user", testSocialCallback), ""); c.OK {
		t.Error("a check with no callback to compare with passed")
	}
}

func TestSocialCallbackFor(t *testing.T) {
	if got := socialCallbackFor(identityStatusDocument{SocialCallbackURL: "https://a/idps/callback", Issuer: "https://b"}); got != "https://a/idps/callback" {
		t.Errorf("reported callback = %q", got)
	}
	// A control plane that predates socialCallbackUrl: the issuer's own.
	if got := socialCallbackFor(identityStatusDocument{Issuer: "https://b/"}); got != "https://b/idps/callback" {
		t.Errorf("derived callback = %q", got)
	}
}

// socialCheckCP is a control plane serving status and, unless
// checkReply is empty, procIdentityCheckSocial.
func socialCheckCP(t *testing.T, status string, checkStatus int, checkReply string) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/" + cloud.ProcGetOrganization:
			_, _ = fmt.Fprintf(w, `{"%s":{"orgId":"org"}}`, orgResponseKey)
		case "/controlplane.v1.DeployService/ListEnvironments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"env","name":"prod","project":"shop"}]}`))
		case "/" + procIdentityStatus:
			_, _ = w.Write([]byte(status))
		case "/" + procIdentityCheckSocial:
			if body["orgId"] != "org" || body["environmentId"] != "env" {
				t.Errorf("check request = %v", body)
			}
			w.WriteHeader(checkStatus)
			_, _ = w.Write([]byte(checkReply))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestCloudIdentityStatusCheckSocial(t *testing.T) {
	const gh, google = "https://github.com/login/oauth/authorize", "https://accounts.google.com/o/oauth2/v2/auth"
	applied := `{"hasInstance":true,"state":"IDENTITY_STATE_READY","issuer":"https://acme-prod-abc123.reliantidentity.com","socialCallbackUrl":"` + testSocialCallback +
		`","social":[{"type":"github","state":"applied"},{"type":"google","state":"applied"}]}`
	reply := func(ghRedirect, googleRedirect string) string {
		b, _ := json.Marshal(map[string]any{"providers": []map[string]string{
			{"type": "github", "authUrl": authorizeURL(gh, "Iv1.abc", "read:user user:email", ghRedirect)},
			{"type": "google", "authUrl": authorizeURL(google, "x.apps.googleusercontent.com", "openid profile email", googleRedirect)},
		}})
		return string(b)
	}
	for _, tc := range []struct {
		name, status, checkReply string
		checkStatus              int
		args                     []string
		wantErr                  string
		want                     []string
	}{
		{"both pass", applied, reply(testSocialCallback, testSocialCallback), 200, nil, "",
			[]string{"Social check: github ok — redirect_uri is the callback " + testSocialCallback, "Social check: google ok",
				"authorize: https://github.com/login/oauth/authorize (client_id Iv1.abc, scope read:user user:email)"}},
		{"google sends another callback", applied, reply(testSocialCallback, "https://custom.example/idps/callback"), 200, nil,
			"social sign-in check failed for google", []string{"Social check: github ok", "Social check: google FAILED", `the issuer sends redirect_uri "https://custom.example/idps/callback"`}},
		{"the control plane could not start one", applied, `{"providers":[{"type":"github","authUrl":"` + authorizeURL(gh, "x", "read:user", testSocialCallback) + `"},{"type":"google","error":"identity provider forge:google is not in the instance"}]}`, 200, nil,
			"failed for google", []string{"Social check: google FAILED", "forge:google is not in the instance"}},
		{"a provider not applied is skipped and fails", `{"hasInstance":true,"socialCallbackUrl":"` + testSocialCallback + `","social":[{"type":"github","state":"secret_missing"},{"type":"google","state":"applied"}]}`,
			`{"providers":[{"type":"google","authUrl":"` + authorizeURL(google, "x", "openid", testSocialCallback) + `"}]}`, 200, nil,
			"failed for github", []string{"Social check: github SKIPPED — the provider is secret_missing, not applied", "Social check: google ok"}},
		{"nothing declared", `{"hasInstance":true}`, "", 200, nil, "", []string{"Social check: no social provider is declared."}},
		{"an older control plane", applied, `{"code":"unimplemented","message":"controlplane.v1.IdentityService/CheckSocialSignIn is not implemented"}`, 501, nil,
			"pass --app", []string{"Social callback: " + testSocialCallback}},
		{"an older control plane without connect", applied, `404 page not found`, 404, nil, "is not implemented", nil},
		{"no instance", `{"hasInstance":false}`, "", 200, nil, "no managed identity instance", nil},
		{"json", applied, reply(testSocialCallback, "https://custom.example/idps/callback"), 200, []string{"--json"}, "failed for google",
			[]string{`"ok": false`, `"socialChecks": [`, `"redirectUri": "https://custom.example/idps/callback"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := socialCheckCP(t, tc.status, tc.checkStatus, tc.checkReply)
			identityTestTarget(t, srv.URL)
			t.Setenv("IDENTITY_TEST_TOKEN", "human-token")
			out, err := executeIdentity(append([]string{"status", "prod", "--check-social"}, tc.args...)...)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.wantErr, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
			if strings.Contains(out, "human-token") {
				t.Error("output leaked the credential")
			}
		})
	}
}

func TestCloudIdentityStatusCheckSocialThroughTheAppBroker(t *testing.T) {
	const gh = "https://github.com/login/oauth/authorize"
	var starts []string
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/social/start" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("broker request %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Provider string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		starts = append(starts, body.Provider)
		w.Header().Set("Content-Type", "application/json")
		switch body.Provider {
		case "github":
			_ = json.NewEncoder(w).Encode(map[string]string{"redirect": authorizeURL(gh, "Iv1.abc", "read:user", testSocialCallback)})
		case "google":
			// The deployed broker's LOGIN_POLICY has no google yet.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"unknown_provider","error":"That sign-in method is not available."}`))
		}
	}))
	defer broker.Close()
	srv, calls := socialCheckCP(t, `{"hasInstance":true,"socialCallbackUrl":"`+testSocialCallback+`","social":[{"type":"github","state":"applied"},{"type":"google","state":"applied"}]}`, 500, `{"code":"internal"}`)
	identityTestTarget(t, srv.URL)
	t.Setenv("IDENTITY_TEST_TOKEN", "human-token")
	out, err := executeIdentity("status", "prod", "--check-social", "--app", broker.URL+"/api/")
	if err == nil || !strings.Contains(err.Error(), "failed for google") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, w := range []string{"Social check: github ok", "Social check: google FAILED", "the app's broker does not offer google sign-in (unknown_provider)"} {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
	if strings.Join(starts, ",") != "github,google" {
		t.Errorf("broker starts = %v", starts)
	}
	for _, c := range *calls {
		if c == "/"+procIdentityCheckSocial {
			t.Error("--app also asked the control plane to start the sign-ins")
		}
	}
}

func TestCloudIdentityStatusCheckSocialFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "prod", "--app", "https://app.example"}, "--app goes with --check-social"},
		{[]string{"status", "prod", "--check-social", "--app", "app.example"}, "must be an http(s) URL"},
	} {
		if _, err := executeIdentity(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}
