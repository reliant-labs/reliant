package devidp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tests that matter most here are the REFUSALS. buildChecks is the
// authentication boundary for the API-only sign-in flow (see login.go), and
// the failure mode it guards against is not a crash — it is a session that
// works perfectly and authenticates nobody.
//
// Verified against Zitadel v4.19.4: a session created with a user check and
// no password check yields a real access_token and id_token, with no `amr`
// claim to reveal that nothing was verified. So "does a passwordless
// session get refused" is the single most load-bearing assertion in this
// package, and it is asserted at the only place such a session could be
// constructed.

func TestBuildChecks_RefusesMissingVerifyingFactor(t *testing.T) {
	// THE BYPASS. A login name alone identifies an account; it proves
	// nothing. The issuer would accept this and mint real tokens.
	_, err := buildChecks(Credentials{LoginName: "someone@example.com"})
	if err == nil {
		t.Fatal("buildChecks accepted credentials with no verifying factor — " +
			"this mints valid tokens for an unauthenticated user")
	}
	if !strings.Contains(err.Error(), "no verifying factor") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
}

func TestBuildChecks_RefusesMissingLoginName(t *testing.T) {
	_, err := buildChecks(Credentials{Password: "hunter2"})
	if err == nil {
		t.Fatal("buildChecks accepted credentials with no login name")
	}
}

func TestBuildChecks_RefusesWhitespaceOnlyLoginName(t *testing.T) {
	// A blank-but-present field is what a form posts when the user typed
	// nothing, so it must be refused the same way an absent one is.
	if _, err := buildChecks(Credentials{LoginName: "   ", Password: "hunter2"}); err == nil {
		t.Fatal("buildChecks accepted a whitespace-only login name")
	}
}

func TestBuildChecks_PasswordProducesUserAndPasswordFactors(t *testing.T) {
	checks, err := buildChecks(Credentials{LoginName: "someone@example.com", Password: "hunter2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	user, ok := checks["user"].(map[string]any)
	if !ok || user["loginName"] != "someone@example.com" {
		t.Fatalf("user check missing or wrong: %#v", checks["user"])
	}
	password, ok := checks["password"].(map[string]any)
	if !ok || password["password"] != "hunter2" {
		t.Fatalf("password check missing or wrong: %#v", checks["password"])
	}
	if _, unexpected := checks["totp"]; unexpected {
		t.Fatal("a TOTP check appeared for credentials that carried no TOTP code")
	}
}

func TestBuildChecks_TOTPAddsToPasswordRatherThanReplacingIt(t *testing.T) {
	// Zitadel records each factor separately on the session. A second
	// factor must ADD to the password check — dropping the password when a
	// TOTP code is present would weaken the login to single-factor while
	// looking like an upgrade.
	checks, err := buildChecks(Credentials{
		LoginName: "someone@example.com",
		Password:  "hunter2",
		TOTP:      "123456",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := checks["password"]; !ok {
		t.Fatal("password check was dropped when a TOTP code was supplied")
	}
	totp, ok := checks["totp"].(map[string]any)
	if !ok || totp["code"] != "123456" {
		t.Fatalf("totp check missing or wrong: %#v", checks["totp"])
	}
}

// newTestClient returns a Client pointed at a stub issuer, plus a pointer to
// the last request body it received.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, Token: "test-token"}
}

func TestCreateSession_DoesNotCallTheIssuerWhenChecksAreRefused(t *testing.T) {
	// The refusal must happen BEFORE the network call. If a malformed
	// credential set reached the issuer, the issuer's answer — not this
	// package's rule — would decide whether it is acceptable.
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	if _, err := client.CreateSession(context.Background(), Credentials{LoginName: "someone@example.com"}); err == nil {
		t.Fatal("CreateSession accepted credentials with no verifying factor")
	}
	if called {
		t.Fatal("CreateSession contacted the issuer despite refusing the credentials")
	}
}

func TestCreateSession_SendsChecksAndReturnsSession(t *testing.T) {
	var got map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/sessions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"sessionId":"sid-1","sessionToken":"stok-1"}`))
	})

	session, err := client.CreateSession(context.Background(), Credentials{
		LoginName: "someone@example.com",
		Password:  "hunter2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.ID != "sid-1" || session.Token != "stok-1" {
		t.Fatalf("session not decoded: %#v", session)
	}

	checks, ok := got["checks"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no checks object: %#v", got)
	}
	if _, ok := checks["password"]; !ok {
		t.Fatal("the password factor did not reach the issuer")
	}
}

func TestCreateSession_RejectsIncompleteSession(t *testing.T) {
	// A session missing either half cannot finalize an auth request.
	// Failing here keeps the cause visible instead of surfacing as an
	// opaque error at the callback.
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sessionId":"sid-1"}`))
	})

	if _, err := client.CreateSession(context.Background(), Credentials{
		LoginName: "someone@example.com",
		Password:  "hunter2",
	}); err == nil {
		t.Fatal("CreateSession accepted a session with no token")
	}
}

func TestFinalizeAuthRequest_ReturnsCallbackURL(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The v2 id prefix must survive: it is part of the id, not
		// decoration.
		if !strings.Contains(r.URL.Path, "V2_123") {
			t.Errorf("auth request id missing or mangled in path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"callbackUrl":"http://app.example/auth/callback?code=abc&state=xyz"}`))
	})

	callback, err := client.FinalizeAuthRequest(context.Background(), "V2_123", Session{ID: "sid", Token: "stok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(callback, "code=abc") {
		t.Fatalf("callback URL not returned intact: %q", callback)
	}
}

func TestFinalizeAuthRequest_RefusesEmptyID(t *testing.T) {
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	if _, err := client.FinalizeAuthRequest(context.Background(), "  ", Session{ID: "sid", Token: "stok"}); err == nil {
		t.Fatal("FinalizeAuthRequest accepted an empty auth request id")
	}
	if called {
		t.Fatal("FinalizeAuthRequest contacted the issuer with an empty id")
	}
}

func TestFinalizeAuthRequest_ErrorsWhenNoCallbackReturned(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	if _, err := client.FinalizeAuthRequest(context.Background(), "V2_123", Session{ID: "s", Token: "t"}); err == nil {
		t.Fatal("FinalizeAuthRequest accepted a response with no callback URL")
	}
}

// A user with no instance membership must be POSTed, not PUT. Getting this
// wrong fails with "Errors.NotFound (INSTANCE-D8JxR)" — an error naming the
// INSTANCE rather than the missing membership, which sends you looking at
// the wrong thing entirely. A freshly created service account is always in
// this state, so it is the normal path.
func TestEnsureLoginClientRole_CreatesMembershipForNonMember(t *testing.T) {
	var method, path string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_search") {
			_, _ = w.Write([]byte(`{"result":[]}`)) // no memberships at all
			return
		}
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.EnsureLoginClientRole(context.Background(), "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("a non-member must be added with POST, got %s %s", method, path)
	}
}

// An EXISTING member is updated in place, and keeps the roles it already
// holds — dropping an unrelated role while granting this one would be a
// silent privilege removal.
func TestEnsureLoginClientRole_UpdatesExistingMemberPreservingRoles(t *testing.T) {
	var method string
	var sent map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_search") {
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-1","roles":["IAM_OWNER"]}]}`))
			return
		}
		method = r.Method
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.EnsureLoginClientRole(context.Background(), "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != http.MethodPut {
		t.Fatalf("an existing member must be updated with PUT, got %s", method)
	}
	roles, _ := sent["roles"].([]any)
	if len(roles) != 2 {
		t.Fatalf("the existing role was not preserved: %#v", sent["roles"])
	}
}

// Already granted: no write at all, so re-running the provisioner is a
// no-op rather than a needless membership update.
func TestEnsureLoginClientRole_IdempotentWhenAlreadyGranted(t *testing.T) {
	wrote := false
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_search") {
			_, _ = w.Write([]byte(`{"result":[{"userId":"user-1","roles":["IAM_LOGIN_CLIENT"]}]}`))
			return
		}
		wrote = true
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.EnsureLoginClientRole(context.Background(), "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wrote {
		t.Fatal("re-granting an existing role issued a write")
	}
}

func TestSetLoginUI_EnablesAndDisables(t *testing.T) {
	var got map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/features/instance" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.SetLoginUI(context.Background(), "http://app.example/auth/login"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	feature, _ := got["loginV2"].(map[string]any)
	if feature["required"] != true || feature["baseUri"] != "http://app.example/auth/login" {
		t.Fatalf("login UI not pointed at the app: %#v", feature)
	}

	// Empty restores the issuer-hosted UI, and must not send a baseUri.
	if err := client.SetLoginUI(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	feature, _ = got["loginV2"].(map[string]any)
	if feature["required"] != false {
		t.Fatalf("empty login URI should disable the feature: %#v", feature)
	}
	if _, present := feature["baseUri"]; present {
		t.Fatalf("a baseUri was sent when disabling: %#v", feature)
	}
}
