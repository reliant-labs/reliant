package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAS is a minimal authorization server: /authorize records the request and
// redirects to the loopback with a code, /token checks the verifier against
// the RECORDED challenge. It recomputes S256 itself, so nothing is asserted
// against a value the package under test produced.
func fakeAS(t *testing.T, redirectMut func(url.Values)) (*httptest.Server, *url.Values) {
	t.Helper()
	var seen url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			seen = r.URL.Query()
			back, _ := url.Parse(seen.Get("redirect_uri"))
			q := url.Values{"code": {"code-1"}, "state": {seen.Get("state")}}
			if redirectMut != nil {
				redirectMut(q)
			}
			back.RawQuery = q.Encode()
			http.Redirect(w, r, back.String(), http.StatusFound)
		case "/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			ok := base64.RawURLEncoding.EncodeToString(sum[:]) == seen.Get("code_challenge") &&
				r.PostForm.Get("redirect_uri") == seen.Get("redirect_uri") &&
				r.PostForm.Get("code") == "code-1"
			w.Header().Set("Content-Type", "application/json")
			if !ok {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"rlat_x","token_type":"Bearer","expires_in":60,"scope":"a b"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// browser follows the authorize URL the way a real browser would.
func browser(u string) error {
	go func() {
		if resp, err := http.Get(u); err == nil {
			_ = resp.Body.Close()
		}
	}()
	return nil
}

func TestLoopbackLogin_CompletesPKCE(t *testing.T) {
	as, seen := fakeAS(t, nil)
	tok, err := LoopbackLogin{
		AuthorizeEndpoint: as.URL + "/authorize", TokenEndpoint: as.URL + "/token",
		ClientID: "forge-cli", Scopes: []string{"a", "b"},
		Extra:   url.Values{"device": {"box"}},
		OpenURL: browser, Timeout: 10 * time.Second,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tok.AccessToken != "rlat_x" || tok.Scope != "a b" {
		t.Fatalf("token: %+v", tok)
	}
	if seen.Get("code_challenge_method") != "S256" || seen.Get("response_type") != "code" || seen.Get("device") != "box" {
		t.Fatalf("authorize request: %v", *seen)
	}
	if !strings.HasPrefix(seen.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Fatalf("redirect must be loopback: %s", seen.Get("redirect_uri"))
	}
}

func TestLoopbackLogin_RejectsForgedState(t *testing.T) {
	as, _ := fakeAS(t, func(q url.Values) { q.Set("state", "forged") })
	_, err := LoopbackLogin{
		AuthorizeEndpoint: as.URL + "/authorize", TokenEndpoint: as.URL + "/token",
		ClientID: "c", OpenURL: browser, Timeout: 10 * time.Second,
	}.Run(context.Background())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("want ErrStateMismatch; got %v", err)
	}
}

func TestLoopbackLogin_SurfacesServerError(t *testing.T) {
	as, _ := fakeAS(t, func(q url.Values) {
		q.Del("code")
		q.Set("error", "access_denied")
		q.Set("error_description", "the user declined")
	})
	_, err := LoopbackLogin{
		AuthorizeEndpoint: as.URL + "/authorize", TokenEndpoint: as.URL + "/token",
		ClientID: "c", OpenURL: browser, Timeout: 10 * time.Second,
	}.Run(context.Background())
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "access_denied" {
		t.Fatalf("want access_denied; got %v", err)
	}
}

func TestLoopbackLogin_TimesOut(t *testing.T) {
	_, err := LoopbackLogin{
		AuthorizeEndpoint: "https://as.example/authorize", TokenEndpoint: "https://as.example/token",
		ClientID: "c", OpenURL: func(string) error { return nil }, Timeout: 50 * time.Millisecond,
	}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout; got %v", err)
	}
}
