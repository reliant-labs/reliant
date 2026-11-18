package oauth2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// RFC 6749 §3.3 says scope is space-delimited, and that stays the default. But
// some providers that a forge app must integrate with read it comma-delimited
// (Slack's OAuth v2 user_scope/scope, among others). Before ScopeSeparator
// existed, such a consumer could not express its scope at all: Scopes was
// always space-joined, and "scope" in Extra was refused as reserved.

func TestAuthRequestScopeSeparatorComma(t *testing.T) {
	_, challenge := newTestPair(t)
	raw, err := AuthRequest{
		Endpoint:       "https://slack.example.test/oauth/v2/authorize",
		ClientID:       "client",
		RedirectURI:    "https://app.example.test/cb",
		State:          "state",
		Challenge:      challenge,
		Scopes:         []string{"chat:write", "channels:history", "reactions:read"},
		ScopeSeparator: ",",
	}.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL: %v", err)
	}
	q, _ := url.ParseQuery(mustQuery(t, raw))
	if got, want := q.Get("scope"), "chat:write,channels:history,reactions:read"; got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
}

func TestRefreshScopeSeparatorComma(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 60,
		})
	}))
	t.Cleanup(srv.Close)

	rt, err := NewRefreshToken("secret")
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	ex := &Exchanger{Client: srv.Client()}
	if _, err := ex.Refresh(context.Background(), RefreshRequest{
		Endpoint:       srv.URL + "/token",
		ClientID:       "c",
		RefreshToken:   rt,
		Scopes:         []string{"a", "b"},
		ScopeSeparator: ",",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got, want := gotForm.Get("scope"), "a,b"; got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
}

// The separator is joined into one parameter value, so only the two delimiters
// providers actually use are accepted. Anything else ("&scope=", a newline,
// a multi-character string) is refused rather than encoded, because a caller
// that reached for it was configured wrongly, and a provider would read the
// result as a different scope set than the one the caller listed.
func TestScopeSeparatorRejectsAnythingButSpaceOrComma(t *testing.T) {
	_, challenge := newTestPair(t)
	for _, sep := range []string{"&", ";", "\n", ", ", "&scope=admin "} {
		_, err := AuthRequest{
			Endpoint:       "https://idp.example.test/authorize",
			ClientID:       "client",
			RedirectURI:    "https://app.example.test/cb",
			State:          "state",
			Challenge:      challenge,
			Scopes:         []string{"a", "b"},
			ScopeSeparator: sep,
		}.URL()
		if err == nil || !strings.Contains(err.Error(), "ScopeSeparator") {
			t.Errorf("AuthRequest.URL with separator %q: err = %v, want a ScopeSeparator error", sep, err)
		}

		rt, _ := NewRefreshToken("secret")
		ex := &Exchanger{Client: http.DefaultClient}
		_, err = ex.Refresh(context.Background(), RefreshRequest{
			Endpoint:       "https://idp.example.test/token",
			ClientID:       "c",
			RefreshToken:   rt,
			Scopes:         []string{"a", "b"},
			ScopeSeparator: sep,
		})
		if err == nil || !strings.Contains(err.Error(), "ScopeSeparator") {
			t.Errorf("Refresh with separator %q: err = %v, want a ScopeSeparator error", sep, err)
		}
	}
}

// A scope that itself contains the separator would be split into two scopes
// by the provider. Refuse it instead of silently widening or corrupting the
// grant. A space inside a scope was already a corruption under the default.
func TestScopeContainingTheSeparatorIsRejected(t *testing.T) {
	_, challenge := newTestPair(t)
	_, err := AuthRequest{
		Endpoint:       "https://idp.example.test/authorize",
		ClientID:       "client",
		RedirectURI:    "https://app.example.test/cb",
		State:          "state",
		Challenge:      challenge,
		Scopes:         []string{"chat:write,admin"},
		ScopeSeparator: ",",
	}.URL()
	if err == nil {
		t.Fatal("a scope containing the separator was accepted")
	}
	_, err = AuthRequest{
		Endpoint:    "https://idp.example.test/authorize",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		State:       "state",
		Challenge:   challenge,
		Scopes:      []string{"openid admin"},
	}.URL()
	if err == nil {
		t.Fatal("a scope containing a space was accepted under the default separator")
	}
}
