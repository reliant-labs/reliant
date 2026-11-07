package cloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// An org-bound token (a control plane that predates human identities) is a
// delegated capability: the login succeeds, but it is never reclassified as an
// identity, so every later command takes the delegated GetTenant path.
func TestBrowserLoginKeepsOrgBoundTokenDelegated(t *testing.T) {
	cred, err := browserLoginAgainst(t, `{"access_token":"rlat_legacy","token_type":"Bearer","org_id":"A"}`)
	if err != nil {
		t.Fatalf("login against an org-bound control plane: %v", err)
	}
	if cred.Kind == KindHuman || cred.Kind != KindOrgLogin {
		t.Fatalf("org-bound automation-style login reclassified: kind %q", cred.Kind)
	}
}

// A control plane that ignores credential_kind (one that predates human
// identities, or was rolled back past them) answers with an org-bound token.
// It is kept, on the delegated path, but the login says so: getting a
// different credential than the one asked for is never silent.
func TestBrowserLoginSaysWhenTheServerIgnoredCredentialKind(t *testing.T) {
	var out strings.Builder
	cred, err := browserLoginAgainstOut(t, `{"access_token":"rlat_legacy","token_type":"Bearer"}`, &out)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if cred.Kind != KindOrgLogin {
		t.Fatalf("kind %q, want %q", cred.Kind, KindOrgLogin)
	}
	if !strings.Contains(out.String(), "organization-bound login, not a human identity") {
		t.Fatalf("an ignored credential_kind must be reported; output:\n%s", out.String())
	}
	out.Reset()
	if _, err := browserLoginAgainstOut(t, `{"access_token":"rlci_x","token_type":"Bearer","credential_kind":"human"}`, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "organization-bound") {
		t.Fatalf("a human identity login must not carry the org-bound note:\n%s", out.String())
	}
}

func TestBrowserLoginRefusesUnknownCredentialKind(t *testing.T) {
	if _, err := browserLoginAgainst(t, `{"access_token":"x","token_type":"Bearer","credential_kind":"robot"}`); err == nil {
		t.Fatal("an unknown credential kind must not be stored")
	}
}

func browserLoginAgainst(t *testing.T, tokenResponse string) (credentials.Credential, error) {
	t.Helper()
	return browserLoginAgainstOut(t, tokenResponse, nil)
}

func browserLoginAgainstOut(t *testing.T, tokenResponse string, out io.Writer) (credentials.Credential, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tokenResponse))
	}))
	defer srv.Close()
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		back, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			return err
		}
		v := back.Query()
		v.Set("code", "code")
		v.Set("state", q.Get("state"))
		back.RawQuery = v.Encode()
		resp, err := http.Get(back.String())
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	return (BrowserLogin{Endpoint: Endpoint{URL: srv.URL}, OpenURL: open, Out: out}).Run(context.Background())
}

func TestCredentialKindComesFromOAuthResponseNotTokenPrefix(t *testing.T) {
	token := &oauth2.Token{AccessToken: "opaque-human", Raw: map[string]any{"credential_kind": "human"}}
	if tokenCredentialKind(token) != "human" {
		t.Fatal("human identity metadata lost")
	}
}

// The token reply names who the login signed in as; the stored credential
// keeps it, so a client can say whose login it holds without a network call.
// An issuer that does not say leaves both empty.
func TestCredentialFromTokenRecordsTheAccount(t *testing.T) {
	for _, tc := range []struct {
		name            string
		raw             map[string]any
		wantID, wantEml string
	}{
		{"named", map[string]any{"account_id": "ext-1", "account_email": "a@example.test"}, "ext-1", "a@example.test"},
		{"older issuer", map[string]any{"org_id": "A"}, "", ""},
		{"wrong type", map[string]any{"account_id": 7}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := CredentialFromToken(&oauth2.Token{AccessToken: "rlat_0123456789abcdef", Raw: tc.raw}, time.Now())
			if c.AccountID != tc.wantID || c.AccountEmail != tc.wantEml {
				t.Fatalf("account = %q %q, want %q %q", c.AccountID, c.AccountEmail, tc.wantID, tc.wantEml)
			}
		})
	}
}
