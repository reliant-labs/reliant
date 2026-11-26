package devidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSplitHandoffRedirects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"only separators", " , \n\t,", []string{}},
		{"commas", "https://a.example/cb,https://b.example/cb", []string{"https://a.example/cb", "https://b.example/cb"}},
		{"mixed separators", "https://a.example/cb, https://b.example/cb\nmyapp.example:/cb\thttp://127.0.0.1/cb", []string{"https://a.example/cb", "https://b.example/cb", "myapp.example:/cb", "http://127.0.0.1/cb"}},
		{"surrounding space", "  https://a.example/cb  ", []string{"https://a.example/cb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitHandoffRedirects(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitHandoffRedirects(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitHandoffRedirectsFeedsTheValidator(t *testing.T) {
	for _, entry := range SplitHandoffRedirects("https://app.example/cb, http://localhost/cb") {
		err := ValidateHandoffRedirect(entry)
		switch entry {
		case "https://app.example/cb":
			if err != nil {
				t.Errorf("%s refused: %v", entry, err)
			}
		case "http://localhost/cb":
			if err == nil {
				t.Errorf("%s accepted; localhost is refused for loopback redirects", entry)
			}
		}
	}
}

func TestHandoffUnavailableAnswers503WithoutLeakingTheCause(t *testing.T) {
	var logs bytes.Buffer
	h := &HandoffRoutes{cfg: HandoffConfig{Logger: slog.New(slog.NewTextHandler(&logs, nil))}}
	rec := httptest.NewRecorder()
	h.unavailable(rec, httptest.NewRequest(http.MethodPost, "/auth/handoff/start", nil), errors.New("dial tcp 10.0.0.7:8080: refused"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "10.0.0.7") || !strings.Contains(body, "Sign-in is unavailable") {
		t.Errorf("body %q must be the generic message, not the cause", body)
	}
	if !strings.Contains(logs.String(), "10.0.0.7") {
		t.Errorf("the cause was not logged: %q", logs.String())
	}
}

func TestRevokeQuietly(t *testing.T) {
	newRoutes := func(t *testing.T, status int) (*HandoffRoutes, *atomic.Int32, *bytes.Buffer, *string) {
		t.Helper()
		var calls atomic.Int32
		var token string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			token = string(raw)
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)
		var logs bytes.Buffer
		return &HandoffRoutes{cfg: HandoffConfig{
			Client: &Client{Base: srv.URL},
			Flow:   FlowConfig{ClientID: "broker"},
			Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		}}, &calls, &logs, &token
	}

	t.Run("nothing to revoke asks nobody", func(t *testing.T) {
		h, calls, _, _ := newRoutes(t, 200)
		h.revokeQuietly(context.Background(), "")
		if calls.Load() != 0 {
			t.Errorf("the issuer was called %d times for an empty token", calls.Load())
		}
	})

	t.Run("a refresh token is revoked for the broker's client", func(t *testing.T) {
		h, calls, logs, body := newRoutes(t, 200)
		h.revokeQuietly(context.Background(), "rt-1")
		if calls.Load() != 1 || !strings.Contains(*body, "token=rt-1") || !strings.Contains(*body, "client_id=broker") {
			t.Errorf("calls=%d body=%q", calls.Load(), *body)
		}
		if logs.Len() != 0 {
			t.Errorf("a clean revoke logged %q", logs.String())
		}
	})

	t.Run("an issuer failure is logged, never returned or panicked", func(t *testing.T) {
		h, _, logs, _ := newRoutes(t, 500)
		h.revokeQuietly(context.Background(), "rt-1")
		if !strings.Contains(logs.String(), "could not revoke an unused refresh token") {
			t.Errorf("failure not logged: %q", logs.String())
		}
	})

	t.Run("a cancelled request still revokes", func(t *testing.T) {
		h, calls, _, _ := newRoutes(t, 200)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h.revokeQuietly(ctx, "rt-1")
		if calls.Load() != 1 {
			t.Errorf("calls = %d; the revoke must outlive the cancelled request", calls.Load())
		}
	})
}

// brokerIssuer is a handler for the management endpoints the login-broker
// provisioning drives. Each field switches one behaviour.
type brokerIssuer struct {
	existingUserID string // user search finds this account
	createdUserID  string // machine-user creation answers this id
	token          string // PAT mint answers this token
	failSearch     bool
	failCreate     bool
	failMint       bool
	badJSON        string // path suffix whose answer is not JSON
	calls          []string
}

func (b *brokerIssuer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b.calls = append(b.calls, r.Method+" "+r.URL.Path)
		if b.badJSON != "" && strings.HasSuffix(r.URL.Path, b.badJSON) {
			_, _ = w.Write([]byte("<html>"))
			return
		}
		switch {
		case r.URL.Path == "/v2/users" && r.Method == http.MethodPost:
			if b.failSearch {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			if b.existingUserID != "" {
				fmt.Fprintf(w, `{"result":[{"userId":%q}]}`, b.existingUserID)
				return
			}
			_, _ = w.Write([]byte(`{"result":[]}`))
		case r.URL.Path == "/management/v1/users/machine":
			if b.failCreate {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `{"userId":%q}`, b.createdUserID)
		case strings.HasSuffix(r.URL.Path, "/pats"):
			if b.failMint {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `{"token":%q}`, b.token)
		case strings.HasSuffix(r.URL.Path, "/_search"): // instance members
			_, _ = w.Write([]byte(`{"result":[]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}
}

func TestEnsureLoginBroker(t *testing.T) {
	t.Run("creates the machine user, grants the role and mints a token", func(t *testing.T) {
		iss := &brokerIssuer{createdUserID: "u-new", token: "pat-1"}
		c := newTestClient(t, iss.handler(t))

		got, err := c.EnsureLoginBroker(context.Background(), "forge-login-broker")
		if err != nil {
			t.Fatalf("EnsureLoginBroker: %v", err)
		}
		if got != (BrokerCredential{UserID: "u-new", Token: "pat-1"}) {
			t.Errorf("credential = %+v", got)
		}
		joined := strings.Join(iss.calls, "\n")
		for _, want := range []string{
			"POST /v2/users",
			"POST /management/v1/users/machine",
			"POST /management/v1/users/u-new/pats",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("calls missing %q:\n%s", want, joined)
			}
		}
	})

	t.Run("adopts an existing account instead of creating a second", func(t *testing.T) {
		iss := &brokerIssuer{existingUserID: "u-old", token: "pat-2"}
		c := newTestClient(t, iss.handler(t))

		got, err := c.EnsureLoginBroker(context.Background(), "forge-login-broker")
		if err != nil {
			t.Fatal(err)
		}
		if got.UserID != "u-old" || got.Token != "pat-2" {
			t.Errorf("credential = %+v", got)
		}
		for _, call := range iss.calls {
			if strings.Contains(call, "users/machine") {
				t.Errorf("an existing account was re-created: %v", iss.calls)
			}
		}
	})

	t.Run("a blank username is refused before the issuer is asked", func(t *testing.T) {
		iss := &brokerIssuer{}
		c := newTestClient(t, iss.handler(t))
		if _, err := c.EnsureLoginBroker(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "username is required") {
			t.Errorf("err = %v, want a username-required refusal", err)
		}
		if len(iss.calls) != 0 {
			t.Errorf("the issuer was called: %v", iss.calls)
		}
	})
}

func TestEnsureLoginBrokerFailures(t *testing.T) {
	tests := []struct {
		name    string
		iss     brokerIssuer
		wantErr string
	}{
		{"user search fails", brokerIssuer{failSearch: true}, "search for the login broker account"},
		{"user search answers garbage", brokerIssuer{badJSON: "/v2/users"}, "decode user search"},
		{"machine user creation fails", brokerIssuer{failCreate: true}, "create the login broker account"},
		{"machine user creation answers garbage", brokerIssuer{badJSON: "/users/machine"}, "decode created user"},
		{"machine user creation returns no id", brokerIssuer{createdUserID: ""}, "returned no user id"},
		{"token mint fails", brokerIssuer{createdUserID: "u1", failMint: true}, `mint a token for "svc"`},
		{"token mint answers garbage", brokerIssuer{createdUserID: "u1", badJSON: "/pats"}, "decode token"},
		{"token mint returns no token", brokerIssuer{createdUserID: "u1", token: ""}, "returned no token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := tt.iss
			c := newTestClient(t, iss.handler(t))
			got, err := c.EnsureLoginBroker(context.Background(), "svc")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if got != (BrokerCredential{}) {
				t.Errorf("credential = %+v on failure, want zero", got)
			}
		})
	}
}

func TestEnsureLoginBrokerSurfacesARoleFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users":
			_, _ = w.Write([]byte(`{"result":[{"userId":"u1"}]}`))
		case strings.HasSuffix(r.URL.Path, "/_search"):
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	if _, err := c.EnsureLoginBroker(context.Background(), "svc"); err == nil {
		t.Fatal("a failed role grant did not fail provisioning")
	}
}

func TestLoginPolicyPasswordPolicy(t *testing.T) {
	t.Run("none declared", func(t *testing.T) {
		got, ok := LegacyLoginPolicy().PasswordPolicy()
		if ok || got != (PasswordPolicy{}) {
			t.Errorf("PasswordPolicy() = %+v, %v; want zero, false", got, ok)
		}
	})
	t.Run("maps every rule", func(t *testing.T) {
		p := LegacyLoginPolicy()
		p.Password = &LoginPolicyPassword{MinLength: 12, Uppercase: true, Number: true}
		got, ok := p.PasswordPolicy()
		want := PasswordPolicy{MinLength: 12, HasUppercase: true, HasNumber: true}
		if !ok || got != want {
			t.Errorf("PasswordPolicy() = %+v, %v; want %+v, true", got, ok, want)
		}
		p.Password = &LoginPolicyPassword{Lowercase: true, Symbol: true}
		got, _ = p.PasswordPolicy()
		if want := (PasswordPolicy{HasLowercase: true, HasSymbol: true}); got != want {
			t.Errorf("PasswordPolicy() = %+v, want %+v", got, want)
		}
	})
}

func TestNormalizeDomain(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{"  example.com.  ", "example.com"},
		{"Bücher.Example", "xn--bcher-kva.example"},
		{"xn--bcher-kva.example", "xn--bcher-kva.example"},
		// A label IDNA refuses (leading hyphen) is compared as typed,
		// lower-cased.
		{"-bad-.example", "-bad-.example"},
	}
	for _, tt := range tests {
		if got := normalizeDomain(tt.in); got != tt.want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLockAndUnlockUser(t *testing.T) {
	ctx := context.Background()

	t.Run("lock then unlock", func(t *testing.T) {
		z := newFakeZitadel(t)
		u := z.addUser("a@acme.com", true, "pw")
		c := z.client()

		if err := c.LockUser(ctx, u.id); err != nil {
			t.Fatalf("LockUser: %v", err)
		}
		if !z.lockedUser(u.id) {
			t.Fatal("the account is not locked")
		}
		if err := c.UnlockUser(ctx, u.id); err != nil {
			t.Fatalf("UnlockUser: %v", err)
		}
		if z.lockedUser(u.id) {
			t.Error("the account is still locked")
		}
	})

	t.Run("locking a locked account and unlocking an unlocked one are no-ops", func(t *testing.T) {
		z := newFakeZitadel(t)
		u := z.addUser("a@acme.com", true, "pw")
		c := z.client()

		if err := c.UnlockUser(ctx, u.id); err != nil {
			t.Errorf("UnlockUser of an unlocked account: %v", err)
		}
		if locked, err := c.lockUser(ctx, u.id); err != nil || !locked {
			t.Errorf("first lockUser = %v, %v; want true, nil", locked, err)
		}
		if locked, err := c.lockUser(ctx, u.id); err != nil || locked {
			t.Errorf("second lockUser = %v, %v; want false, nil (already locked)", locked, err)
		}
		if err := c.LockUser(ctx, u.id); err != nil {
			t.Errorf("LockUser of a locked account: %v", err)
		}
	})

	t.Run("other refusals are errors", func(t *testing.T) {
		z := newFakeZitadel(t)
		c := z.client()
		if err := c.LockUser(ctx, "no-such-user"); err == nil || !strings.Contains(err.Error(), "lock user") {
			t.Errorf("LockUser of an unknown account: %v", err)
		}
		u := z.addUser("a@acme.com", true, "pw")
		z.failUnlock = true
		if err := c.UnlockUser(ctx, u.id); err == nil || !strings.Contains(err.Error(), "unlock user") {
			t.Errorf("UnlockUser against a failing issuer: %v", err)
		}
	})
}

func (f *fakeZitadel) lockedUser(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[id].locked
}

func TestIsNoChangesErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"the no-changes refusal", &apiError{method: "PUT", path: "/x", status: 400, body: `{"details":[{"id":"COMMAND-1m88i"}]}`}, true},
		{"wrapped no-changes refusal", fmt.Errorf("point the login UI: %w", &apiError{status: 400, body: "COMMAND-1m88i"}), true},
		{"another refusal", &apiError{status: 400, body: `{"details":[{"id":"COMMAND-other"}]}`}, false},
		{"a plain error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		if got := isNoChangesErr(tt.err); got != tt.want {
			t.Errorf("%s: isNoChangesErr = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSetLoginUITreatsNoChangesAsSuccess(t *testing.T) {
	respond := func(status int, id string) *Client {
		return newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			zerr(w, status, id, "msg")
		})
	}
	if err := respond(400, "COMMAND-1m88i").SetLoginUI(context.Background(), "https://app.example/login"); err != nil {
		t.Errorf("a no-changes refusal must read as converged, got %v", err)
	}
	if err := respond(400, "COMMAND-other").SetLoginUI(context.Background(), "https://app.example/login"); err == nil {
		t.Error("an unrelated refusal was swallowed")
	}
}

func TestWaitReady(t *testing.T) {
	t.Run("returns as soon as the probe answers", func(t *testing.T) {
		var paths []string
		var host string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			host = r.Host
			w.WriteHeader(http.StatusOK)
		})
		c.Host = "idp.internal"
		if err := c.WaitReady(context.Background()); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
		if len(paths) != 1 || paths[0] != "/debug/ready" {
			t.Errorf("probe paths = %v, want one /debug/ready", paths)
		}
		if host != "idp.internal" {
			t.Errorf("Host = %q, want the client's Host override", host)
		}
	})

	t.Run("an unready issuer ends with the context's error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := c.WaitReady(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "not ready at "+c.Base) {
			t.Errorf("err = %v, want a not-ready error wrapping DeadlineExceeded and naming %s", err, c.Base)
		}
	})

	t.Run("an unreachable issuer ends with the context's error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		base := srv.URL
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := (&Client{Base: base}).WaitReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded", err)
		}
	})

	t.Run("a malformed base is a request error, not a wait", func(t *testing.T) {
		if err := (&Client{Base: "http://bad host"}).WaitReady(context.Background()); err == nil {
			t.Error("WaitReady accepted an unparseable base URL")
		}
	})
}

func TestEnsureOTPEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("a blank user id is refused", func(t *testing.T) {
		if err := newFakeZitadel(t).client().EnsureOTPEmail(ctx, " "); err == nil {
			t.Error("EnsureOTPEmail accepted a blank user id")
		}
	})

	t.Run("sets up the method, and a repeat is a no-op", func(t *testing.T) {
		z := newFakeZitadel(t)
		u := z.addUser("a@acme.com", true, "")
		c := z.client()
		for i := 0; i < 2; i++ {
			if err := c.EnsureOTPEmail(ctx, u.id); err != nil {
				t.Fatalf("EnsureOTPEmail #%d: %v", i+1, err)
			}
		}
		if !z.user(u.id).otpEmail {
			t.Error("the OTP-email method was not set up")
		}
	})

	t.Run("an unverified address is refused by the issuer", func(t *testing.T) {
		z := newFakeZitadel(t)
		u := z.addUser("a@acme.com", false, "")
		err := z.client().EnsureOTPEmail(ctx, u.id)
		if err == nil || !strings.Contains(err.Error(), "add the OTP-email method") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a proven-credentials client refuses an unverified account before writing", func(t *testing.T) {
		z := newFakeZitadel(t)
		u := z.addUser("a@acme.com", false, "")
		c := z.client()
		c.ProvenCredentialsOnly = true
		if err := c.EnsureOTPEmail(ctx, u.id); !errors.Is(err, ErrUnverifiedAccount) {
			t.Errorf("err = %v, want ErrUnverifiedAccount", err)
		}
		if n := z.callCount("POST /v2/users/" + u.id + "/otp_email"); n != 0 {
			t.Errorf("the method was added despite the refusal (%d writes)", n)
		}
	})

	t.Run("a bare 409 and a no-changes answer both read as already set up", func(t *testing.T) {
		for name, respond := range map[string]func(http.ResponseWriter){
			"409":        func(w http.ResponseWriter) { zerr(w, 409, "COMMAND-other", "exists") },
			"no changes": func(w http.ResponseWriter) { zerr(w, 400, "COMMAND-1m88i", "no changes") },
		} {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { respond(w) })
			if err := c.EnsureOTPEmail(ctx, "u1"); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("any other failure is reported", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { zerr(w, 500, "COMMAND-x", "boom") })
		if err := c.EnsureOTPEmail(ctx, "u1"); err == nil {
			t.Error("a 500 was swallowed")
		}
	})
}

func TestGrantProjectRole(t *testing.T) {
	ctx := context.Background()

	t.Run("each argument is required", func(t *testing.T) {
		c := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("the issuer was called") })
		for _, args := range [][3]string{{"", "p", "r"}, {"u", " ", "r"}, {"u", "p", ""}} {
			if err := c.GrantProjectRole(ctx, args[0], args[1], args[2]); err == nil {
				t.Errorf("GrantProjectRole(%q) accepted a blank argument", args)
			}
		}
	})

	t.Run("posts the role on the project", func(t *testing.T) {
		var path string
		var body map[string]any
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{}`))
		})
		if err := c.GrantProjectRole(ctx, "u 1", "proj-1", "admin"); err != nil {
			t.Fatal(err)
		}
		if path != "/management/v1/users/u 1/grants" { // r.URL.Path is the decoded form of u%201
			t.Errorf("path = %q", path)
		}
		if body["projectId"] != "proj-1" || !reflect.DeepEqual(body["roleKeys"], []any{"admin"}) {
			t.Errorf("body = %v", body)
		}
	})

	t.Run("an existing grant is left alone", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { zerr(w, 409, "COMMAND-x", "exists") })
		if err := c.GrantProjectRole(ctx, "u", "p", "r"); err != nil {
			t.Errorf("a 409 must read as already granted, got %v", err)
		}
	})

	t.Run("other refusals name the role", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { zerr(w, 404, "COMMAND-x", "no route") })
		if err := c.GrantProjectRole(ctx, "u", "p", "r"); err == nil || !strings.Contains(err.Error(), `grant "r"`) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDefaultOrg(t *testing.T) {
	ctx := context.Background()
	respond := func(body string) *Client {
		return newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	}

	got, err := respond(`{"result":[{"id":"o1","name":"Acme"},{"id":"o2","name":"Other"}]}`).DefaultOrg(ctx)
	if err != nil || got != (Org{ID: "o1", Name: "Acme"}) {
		t.Errorf("DefaultOrg = %+v, %v; want the first organization", got, err)
	}
	if _, err := respond(`{"result":[]}`).DefaultOrg(ctx); err == nil || !strings.Contains(err.Error(), "no organizations") {
		t.Errorf("empty instance: err = %v", err)
	}
	if _, err := respond(`<html>`).DefaultOrg(ctx); err == nil || !strings.Contains(err.Error(), "decode organizations") {
		t.Errorf("garbage answer: err = %v", err)
	}
	failing := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
	if _, err := failing.DefaultOrg(ctx); err == nil || !strings.Contains(err.Error(), "search organizations") {
		t.Errorf("failing issuer: err = %v", err)
	}
}
