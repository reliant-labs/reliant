package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestBrowserLoginRefusesUnknownCredentialKind(t *testing.T) {
	if _, err := browserLoginAgainst(t, `{"access_token":"x","token_type":"Bearer","credential_kind":"robot"}`); err == nil {
		t.Fatal("an unknown credential kind must not be stored")
	}
}

func browserLoginAgainst(t *testing.T, tokenResponse string) (credentials.Credential, error) {
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
	return (BrowserLogin{Endpoint: Endpoint{URL: srv.URL}, OpenURL: open}).Run(context.Background())
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
