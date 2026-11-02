package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// elevatingServer plays a control plane whose stored credential is narrow:
// `narrow` may do nothing but deploy, and the exchange trades it for `wide`
// when exchangeStatus is 200.
type elevatingServer struct {
	*httptest.Server
	mu             sync.Mutex
	calls          []string // "<procedure> <bearer>"
	exchangeScopes []string
}

func newElevatingServer(t *testing.T, exchangeStatus int, exchangeMessage string) *elevatingServer {
	t.Helper()
	s := &elevatingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		s.calls = append(s.calls, strings.TrimPrefix(r.URL.Path, "/")+" "+bearer)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "AccessTokenService/ExchangeToken"):
			var req struct {
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.mu.Lock()
			s.exchangeScopes = req.Scopes
			s.mu.Unlock()
			if exchangeStatus != http.StatusOK {
				w.WriteHeader(exchangeStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "permission_denied", "message": exchangeMessage})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"secret": "rlat_elevated",
				"token":  map[string]any{"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
			})
		case bearer == "rlat_elevated":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "permission_denied", "message": "this machine credential does not carry the domain:read scope"})
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *elevatingServer) client(t *testing.T, source CredentialSource) *Client {
	t.Helper()
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(ep, Credential{Token: "rlat_narrow", Source: source, From: "test"})
}

// The bug: a stored login lacking domain:read was a dead end ("re-run forge
// login", which minted the same narrow token). Now forge exchanges for the
// scope the command just proved it needs and the command succeeds.
func TestCall_ElevatesOnAMissingScopeAndRetriesOnce(t *testing.T) {
	for _, src := range []CredentialSource{SourceLogin, SourceHelper} {
		t.Run(string(src), func(t *testing.T) {
			srv := newElevatingServer(t, http.StatusOK, "")
			c := srv.client(t, src)
			var out struct{ OK bool }
			if err := c.Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, &out); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if !out.OK {
				t.Fatal("the retried call did not succeed")
			}
			want := []string{
				"controlplane.v1.DomainService/ListDomains rlat_narrow",
				"controlplane.v1.AccessTokenService/ExchangeToken rlat_narrow",
				"controlplane.v1.DomainService/ListDomains rlat_elevated",
			}
			if strings.Join(srv.calls, "|") != strings.Join(want, "|") {
				t.Fatalf("calls:\n got %v\nwant %v", srv.calls, want)
			}
			if len(srv.exchangeScopes) != 1 || srv.exchangeScopes[0] != "domain:read" {
				t.Fatalf("asked for %v, want exactly the missing scope", srv.exchangeScopes)
			}
		})
	}
}

func TestCall_ElevationIsCachedForTheLifeOfTheClient(t *testing.T) {
	srv := newElevatingServer(t, http.StatusOK, "")
	c := srv.client(t, SourceLogin)
	for i := 0; i < 3; i++ {
		if err := c.Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	exchanges := 0
	for _, call := range srv.calls {
		if strings.Contains(call, "ExchangeToken") {
			exchanges++
		}
	}
	if exchanges != 1 {
		t.Fatalf("%d exchanges for 3 calls, want 1 (calls: %v)", exchanges, srv.calls)
	}
}

// --token and the token env var are the user saying "this exact credential".
// forge must not quietly swap them for another one.
func TestCall_ExplicitCredentialsAreNeverElevated(t *testing.T) {
	for _, src := range []CredentialSource{SourceFlag, SourceEnv} {
		t.Run(string(src), func(t *testing.T) {
			srv := newElevatingServer(t, http.StatusOK, "")
			err := srv.client(t, src).Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil)
			if err == nil {
				t.Fatal("an explicit token was elevated")
			}
			if len(srv.calls) != 1 {
				t.Fatalf("calls %v: expected the one refused call and nothing else", srv.calls)
			}
		})
	}
}

// A refused exchange keeps the ORIGINAL refusal and says why elevation did not
// help, without looping.
func TestCall_RefusedElevationExplainsItself(t *testing.T) {
	srv := newElevatingServer(t, http.StatusForbidden,
		"your organization permissions do not include domain:read; ask an organization admin to grant it")
	err := srv.client(t, SourceLogin).Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected the refusal")
	}
	for _, want := range []string{"domain:read", "elevation:", "ask an organization admin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
	if len(srv.calls) != 2 {
		t.Fatalf("calls %v: want the refused call and one exchange, no retry", srv.calls)
	}
}

// A `forge login` credential that lacks the scope and cannot be elevated,
// on a machine with a credential helper, names both ways out — log out to
// fall through to the helper, or log in again — instead of only "re-run forge
// login". forge never switches identity itself: the call is not retried.
func TestCall_LoginLackingScopeWithAHelperNamesLogout(t *testing.T) {
	t.Setenv(cloudcred.HelperEnv, `["/bin/helper"]`)
	srv := newElevatingServer(t, http.StatusForbidden, "this credential does not carry obs:read, and an exchange cannot widen a credential")
	err := srv.client(t, SourceLogin).Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected the refusal")
	}
	want := "your `forge login` credential (test, for " + srv.URL + ") lacks domain:read; run `forge logout --endpoint " + srv.URL + "` to use the Reliant credential helper"
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "or `forge login` again") {
		t.Errorf("error lacks the remedy %q:\n%v", want, err)
	}
	if len(srv.calls) != 2 {
		t.Fatalf("calls %v: want the refused call and one exchange; forge must not switch to the helper itself", srv.calls)
	}
}

// Without a helper there is nothing to log out TO, and a helper's own token
// is not a `forge login`: neither gets the remedy.
func TestCall_LogoutRemedyNeedsALoginAndAHelper(t *testing.T) {
	srv := newElevatingServer(t, http.StatusForbidden, "refused")
	t.Setenv(cloudcred.HelperEnv, "")
	if err := srv.client(t, SourceLogin).Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil); err == nil || strings.Contains(err.Error(), "forge logout") {
		t.Errorf("no helper: err = %v, want no logout remedy", err)
	}
	t.Setenv(cloudcred.HelperEnv, `["/bin/helper"]`)
	if err := srv.client(t, SourceHelper).Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil); err == nil || strings.Contains(err.Error(), "forge logout") {
		t.Errorf("helper credential: err = %v, want no logout remedy", err)
	}
}

func TestCall_ElevationAgainstAnOlderControlPlaneSaysSo(t *testing.T) {
	srv := newElevatingServer(t, http.StatusNotFound, "")
	err := srv.client(t, SourceLogin).Call(context.Background(), "controlplane.v1.DomainService/ListDomains", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "predates on-demand elevation") {
		t.Fatalf("err = %v", err)
	}
}

func TestCall_NonScopeRefusalsAreNotElevated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "permission_denied", "message": "insufficient permissions"})
	}))
	defer srv.Close()
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	c := NewClient(ep, Credential{Token: "t", Source: SourceLogin})
	if err := c.Call(context.Background(), "x.v1.S/M", map[string]any{}, nil); err == nil {
		t.Fatal("expected refusal")
	}
}

func TestMissingScopeRegexKnowsClusterManage(t *testing.T) {
	m := missingScopeRe.FindStringSubmatch("this machine credential does not carry the cluster:manage scope")
	if m == nil || m[1] != "cluster:manage" {
		t.Fatalf("match = %v", m)
	}
}

// The feedback-loop scopes elevate like the rest: a read refused for want of
// obs:read (or a triage for want of obs:triage) names the scope to exchange for.
func TestMissingScopeRegexKnowsTheFeedbackLoopScopes(t *testing.T) {
	for _, scope := range []string{"obs:read", "obs:triage", "db:query"} {
		m := missingScopeRe.FindStringSubmatch("this credential does not carry the " + scope + " scope")
		if m == nil || m[1] != scope {
			t.Errorf("%s: match = %v", scope, m)
		}
	}
}

// scopeServer plays a control plane that admits a bearer carrying want and
// refuses every other one for want of it; its exchange can only attenuate, as
// the real one does, so it always refuses to widen.
func scopeServer(t *testing.T, want string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls = append(calls, strings.TrimPrefix(r.URL.Path, "/")+" "+bearer)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "AccessTokenService/ExchangeToken"):
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "permission_denied",
				"message": "this credential does not carry " + want + ", and an exchange cannot widen a credential"})
		case strings.Contains(bearer, want):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "permission_denied",
				"message": "this credential does not carry the " + want + " scope"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// The bug: under Reliant every cloud `forge obs` verb failed "lacks obs:read".
// The helper's token is minted for LoginScopes, and the exchange only narrows
// the credential it is called with, so it could never add obs:read — though
// the session behind the helper held it. forge now asks the helper itself for
// the missing scope, and only falls back to the exchange when that fails.
func TestCall_HelperCredentialElevatesThroughTheHelper(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	useFakeHelper(t, "scoped", "deploy:read", "deploy:write", "obs:read")
	srv, calls := scopeServer(t, "obs:read")
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(ep, Credential{Token: "rlat_helper:deploy:read,deploy:write", Source: SourceHelper, From: "test"})
	var out struct{ OK bool }
	for i := 0; i < 2; i++ {
		if err := c.Call(context.Background(), "controlplane.v1.ObservabilityService/GetServiceOverview", map[string]any{}, &out); err != nil {
			t.Fatalf("Call %d: %v", i, err)
		}
	}
	if !out.OK {
		t.Fatal("the retried call did not succeed")
	}
	want := []string{
		"controlplane.v1.ObservabilityService/GetServiceOverview rlat_helper:deploy:read,deploy:write",
		"controlplane.v1.ObservabilityService/GetServiceOverview rlat_helper:deploy:read,deploy:write,obs:read",
		"controlplane.v1.ObservabilityService/GetServiceOverview rlat_helper:deploy:read,deploy:write",
		"controlplane.v1.ObservabilityService/GetServiceOverview rlat_helper:deploy:read,deploy:write,obs:read",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls:\n got %v\nwant %v (no exchange; the helper's token reused)", *calls, want)
	}
}

// A helper whose session does not hold the scope either answers without it;
// forge then tries the exchange and reports why neither helped.
func TestCall_HelperWithoutTheScopeFallsBackToTheExchange(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	useFakeHelper(t, "scoped", "deploy:read", "deploy:write")
	srv, calls := scopeServer(t, "obs:read")
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(ep, Credential{Token: "rlat_helper:deploy:read,deploy:write", Source: SourceHelper, From: "test"})
	err = c.Call(context.Background(), "controlplane.v1.ObservabilityService/GetServiceOverview", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "elevation:") {
		t.Fatalf("err = %v, want the refusal with the elevation note", err)
	}
	if len(*calls) != 2 || !strings.Contains((*calls)[1], "ExchangeToken") {
		t.Fatalf("calls %v: want the refused call and one exchange", *calls)
	}
}
