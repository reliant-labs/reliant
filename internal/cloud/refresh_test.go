package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// expiringServer plays a control plane that refuses every bearer except `good`
// with the 401 the real one sends for an expired access token.
type expiringServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []string
}

func newExpiringServer(t *testing.T, good, reason, message string) *expiringServer {
	t.Helper()
	s := &expiringServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		s.calls = append(s.calls, bearer)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if bearer == good {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.Header().Set(ReasonHeader, reason)
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "unauthenticated", "message": message})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *expiringServer) client(t *testing.T, cred Credential) *Client {
	t.Helper()
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: s.URL, TokenEnv: "ACME_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(ep, cred)
}

// An expired helper-minted token is the host's to replace. forge asks the
// helper again — dropping its own memo of the dead token — and repeats the call
// once with whatever the helper now returns.
func TestCall_ExpiredHelperTokenIsReplacedByAskingTheHelperAgain(t *testing.T) {
	useFakeHelper(t, "mint", "rlat_replacement", "3600")
	srv := newExpiringServer(t, "rlat_replacement", ReasonAccessTokenExpired, "access token expired")
	c := srv.client(t, Credential{Token: "rlat_expired", Source: SourceHelper, From: "Fake Host session"})

	var out struct{ OK bool }
	if err := c.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &out); err != nil {
		t.Fatalf("an expired helper token must be replaced, not reported: %v", err)
	}
	if !out.OK {
		t.Fatal("the retried call did not return the server's answer")
	}
	if got := strings.Join(srv.calls, ","); got != "rlat_expired,rlat_replacement" {
		t.Fatalf("bearers presented = %q, want the expired token once, then the replacement once", got)
	}
	if c.credential().Token != "rlat_replacement" {
		t.Error("the client must keep the replacement for its later calls")
	}
}

// A helper that hands back the SAME dead token has nothing new to offer, and
// retrying would only repeat the refusal. The error then says why and what to do.
func TestCall_ExpiredTokenTheHelperCannotReplaceSaysSoAndHowToFixIt(t *testing.T) {
	useFakeHelper(t, "mint", "rlat_expired", "3600")
	srv := newExpiringServer(t, "never", ReasonAccessTokenExpired, "access token expired")
	c := srv.client(t, Credential{Token: "rlat_expired", Source: SourceHelper, From: "Fake Host session"})

	err := c.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &struct{}{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"The credential has expired.", "sign in again in the application that provided this credential (Fake Host session)", "access token expired"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should contain %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "forge login") {
		t.Errorf("a helper-minted credential is never told to `forge login`:\n%s", msg)
	}
	if len(srv.calls) != 1 {
		t.Errorf("the dead token was presented %d times; a same-token refresh must not retry", len(srv.calls))
	}
}

// What the user typed, exported or logged in with is never swapped for another
// credential behind their back: the answer is a clear "sign in again".
func TestCall_ExpiredLoginIsNotRefreshedAndSaysToSignInAgain(t *testing.T) {
	useFakeHelper(t, "mint", "rlat_from-helper", "3600")
	srv := newExpiringServer(t, "rlat_from-helper", ReasonAccessTokenExpired, "access token expired")

	for _, tc := range []struct {
		name   string
		source CredentialSource
		want   string
	}{
		{"credentials file", SourceLogin, "forge login"},
		{"env", SourceEnv, "ACME_TOKEN"},
		{"flag", SourceFlag, "ACME_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.calls = nil
			c := srv.client(t, Credential{Token: "rlat_expired", Source: tc.source, From: "somewhere"})
			err := c.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &struct{}{})
			if err == nil {
				t.Fatal("a credential the user chose must not be silently replaced")
			}
			msg := err.Error()
			if !strings.Contains(msg, "The credential has expired.") || !strings.Contains(msg, tc.want) {
				t.Errorf("want the expiry named and %q suggested; got:\n%s", tc.want, msg)
			}
			if len(srv.calls) != 1 {
				t.Errorf("bearers presented = %v, want exactly one", srv.calls)
			}
		})
	}
}

// Each way a credential can be dead is worded as what it is.
func TestCall_EachRefusalReasonIsWordedTruthfully(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{
		{ReasonAccessTokenExpired, "The credential has expired."},
		{ReasonAccessTokenRevoked, "The credential was revoked, so it will not work again."},
		{ReasonAccessTokenUnknown, "does not recognize this credential"},
		{ReasonAccessTokenMalformed, "not a valid access token"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			srv := newExpiringServer(t, "never", tc.reason, "x")
			c := srv.client(t, Credential{Token: "t", Source: SourceEnv, From: "ACME_TOKEN"})
			err := c.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &struct{}{})
			var apiErr *Error
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q; got %v", tc.want, err)
			}
			if !errors.As(err, &apiErr) || !apiErr.HasReason(tc.reason) || !apiErr.Unauthenticated() {
				t.Errorf("the typed error must carry reason %q and report Unauthenticated; got %+v", tc.reason, apiErr)
			}
		})
	}
}

// A control plane that could not CHECK the token has not judged it. Telling the
// user to sign in again would send them round a loop that cannot help.
func TestCall_UnverifiableCredentialIsNotBlamedOnTheUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ReasonHeader, ReasonAccessTokenUnavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "unavailable", "message": "could not verify access token"})
	}))
	t.Cleanup(srv.Close)
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL, TokenEnv: "ACME_TOKEN"})
	c := NewClient(ep, Credential{Token: "t", Source: SourceLogin, From: "creds"})

	err := c.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &struct{}{})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "retry in a moment") || strings.Contains(msg, "forge login") || strings.Contains(msg, "expired") {
		t.Errorf("an outage must say retry and must not blame the credential; got:\n%s", msg)
	}
}

// An elevated token the control plane refuses (revoked) is traded for a new one,
// once, rather than failing the command.
func TestCall_RefusedElevatedTokenIsExchangedAgain(t *testing.T) {
	var (
		mu        sync.Mutex
		exchanges int
		seen      []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, strings.TrimPrefix(r.URL.Path, "/")+" "+bearer)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "AccessTokenService/ExchangeToken"):
			mu.Lock()
			exchanges++
			n := exchanges
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"secret": "rlat_elevated_" + string(rune('0'+n)), "token": map[string]any{"expiresAt": "2099-01-01T00:00:00Z"}})
		case bearer == "rlat_elevated_2":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case strings.HasPrefix(bearer, "rlat_elevated_"):
			w.Header().Set(ReasonHeader, ReasonAccessTokenRevoked)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "unauthenticated", "message": "access token revoked"})
		default:
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "permission_denied", "message": "this machine credential does not carry the domain:read scope"})
		}
	}))
	t.Cleanup(srv.Close)
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	c := NewClient(ep, Credential{Token: "rlat_narrow", Source: SourceLogin, From: "creds"})

	var out struct{ OK bool }
	if err := c.Call(context.Background(), "controlplane.v1.DomainService/List", map[string]any{}, &out); err != nil {
		t.Fatalf("a refused elevated token must be re-exchanged: %v\ncalls: %v", err, seen)
	}
	if !out.OK || exchanges != 2 {
		t.Fatalf("ok=%v exchanges=%d, want a second exchange to replace the refused token", out.OK, exchanges)
	}
}
