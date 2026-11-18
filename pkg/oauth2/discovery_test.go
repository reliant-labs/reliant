package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// discoveryServer serves a discovery document at the well-known path and
// reports the issuer it was reached at, so the document's issuer field can be
// derived from the server rather than hardcoded.
func discoveryServer(t *testing.T, mutate func(issuer string, doc map[string]any)) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		doc := map[string]any{
			"issuer":                           srv.URL,
			"authorization_endpoint":           srv.URL + "/oidc/auth",
			"token_endpoint":                   srv.URL + "/oidc/token",
			"userinfo_endpoint":                srv.URL + "/oidc/me",
			"jwks_uri":                         srv.URL + "/jwks",
			"end_session_endpoint":             srv.URL + "/oidc/session/end",
			"scopes_supported":                 []string{"openid", "profile", "email", "offline_access"},
			"response_types_supported":         []string{"code"},
			"code_challenge_methods_supported": []string{"S256"},
			"custom_provider_field":            "kept-in-raw",
		}
		if mutate != nil {
			mutate(srv.URL, doc)
		}
		writeJSON(w, http.StatusOK, doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDiscoverReadsEndpointsFromTheDocument checks each field against the value
// the server actually served, re-decoded from the wire, rather than against a
// literal repeated in the test.
func TestDiscoverReadsEndpointsFromTheDocument(t *testing.T) {
	srv := discoveryServer(t, nil)

	meta, err := Discover(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Fetch the document independently and compare field by field.
	resp, err := srv.Client().Get(srv.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("fetch document directly: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	var served map[string]any
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if len(served) == 0 {
		t.Fatal("served document is empty; the comparison below would be vacuous")
	}

	for field, got := range map[string]string{
		"issuer":                 meta.Issuer,
		"authorization_endpoint": meta.AuthorizationEndpoint,
		"token_endpoint":         meta.TokenEndpoint,
		"userinfo_endpoint":      meta.UserinfoEndpoint,
		"jwks_uri":               meta.JWKSURI,
		"end_session_endpoint":   meta.EndSessionEndpoint,
	} {
		want, _ := served[field].(string)
		if want == "" {
			t.Fatalf("served document has no %q; the test fixture is wrong", field)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}

	if len(meta.ScopesSupported) != 4 {
		t.Errorf("ScopesSupported = %v, want the four the server served", meta.ScopesSupported)
	}
	if !meta.SupportsS256() {
		t.Error("SupportsS256 = false for a document advertising S256")
	}
	if meta.Raw["custom_provider_field"] != "kept-in-raw" {
		t.Errorf("Raw dropped a provider-specific field: %v", meta.Raw["custom_provider_field"])
	}
}

// TestSupportsS256DoesNotDefaultToTrue is the "guard that cannot fail" check
// for discovery: a provider that advertises nothing must report unknown
// (false), not be waved through by a permissive default. The inverse — a
// provider advertising only plain — must also report false.
func TestSupportsS256DoesNotDefaultToTrue(t *testing.T) {
	cases := map[string]struct {
		methods []string
		want    bool
	}{
		"advertises S256":          {[]string{"S256"}, true},
		"advertises both":          {[]string{"plain", "S256"}, true},
		"advertises only plain":    {[]string{"plain"}, false},
		"advertises nothing":       {nil, false},
		"advertises empty list":    {[]string{}, false},
		"advertises unknown only":  {[]string{"S512"}, false},
		"case-mismatched s256":     {[]string{"s256"}, false},
		"empty string in the list": {[]string{""}, false},
	}
	for name, tc := range cases {
		meta := ProviderMetadata{CodeChallengeMethodsSupported: tc.methods}
		if got := meta.SupportsS256(); got != tc.want {
			t.Errorf("%s: SupportsS256() = %t, want %t", name, got, tc.want)
		}
	}
}

// TestDiscoverRejectsIssuerMismatch pins OIDC Discovery §4.3. Without this
// check a document fetched from one host could describe another, which is a
// token-endpoint-substitution vector.
func TestDiscoverRejectsIssuerMismatch(t *testing.T) {
	srv := discoveryServer(t, func(issuer string, doc map[string]any) {
		doc["issuer"] = "https://attacker.example.test"
	})

	meta, err := Discover(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatalf("Discover accepted a document declaring a different issuer: %+v", meta)
	}
	if !errors.Is(err, ErrMetadataIssuerMismatch) {
		t.Errorf("error = %v, want it to wrap ErrMetadataIssuerMismatch", err)
	}
	if meta != nil {
		t.Errorf("Discover returned metadata alongside its error: %+v", meta)
	}
}

// TestDiscoverToleratesTrailingSlashIssuer covers the one inconsistency real
// providers actually exhibit.
func TestDiscoverToleratesTrailingSlashIssuer(t *testing.T) {
	srv := discoveryServer(t, func(issuer string, doc map[string]any) {
		doc["issuer"] = issuer + "/"
	})
	if _, err := Discover(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Errorf("Discover rejected a trailing-slash issuer: %v", err)
	}

	srv2 := discoveryServer(t, nil)
	if _, err := Discover(context.Background(), srv2.Client(), srv2.URL+"/"); err != nil {
		t.Errorf("Discover rejected a trailing slash on the requested issuer: %v", err)
	}
}

// TestDiscoverRequiresUsableEndpoints checks that a document missing what the
// caller needs fails at discovery, rather than yielding metadata with empty
// strings that fail later as an unexplained request to "".
func TestDiscoverRequiresUsableEndpoints(t *testing.T) {
	cases := map[string]string{
		"no authorization_endpoint": "authorization_endpoint",
		"no token_endpoint":         "token_endpoint",
	}
	for name, field := range cases {
		srv := discoveryServer(t, func(issuer string, doc map[string]any) {
			delete(doc, field)
		})
		meta, err := Discover(context.Background(), srv.Client(), srv.URL)
		if err == nil {
			t.Errorf("%s: Discover succeeded, returning %+v", name, meta)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("%s: error %q does not name the missing field", name, err)
		}
		if meta != nil {
			t.Errorf("%s: metadata returned alongside the error", name)
		}
	}
}

func TestDiscoverRejectsBadResponses(t *testing.T) {
	t.Run("404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
		defer srv.Close()
		if meta, err := Discover(context.Background(), srv.Client(), srv.URL); err == nil {
			t.Errorf("Discover succeeded on a 404: %+v", meta)
		}
	})
	t.Run("html", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>not a discovery document</html>")
		}))
		defer srv.Close()
		if meta, err := Discover(context.Background(), srv.Client(), srv.URL); err == nil {
			t.Errorf("Discover succeeded on an HTML page: %+v", meta)
		}
	})
	t.Run("empty issuer", func(t *testing.T) {
		if _, err := Discover(context.Background(), nil, ""); err == nil {
			t.Error("Discover accepted an empty issuer")
		}
	})
	t.Run("relative issuer", func(t *testing.T) {
		if _, err := Discover(context.Background(), nil, "/oidc"); err == nil {
			t.Error("Discover accepted a relative issuer")
		}
	})
}

// TestDiscoverBuildsWellKnownPath checks the URL construction, including the
// trailing-slash case, by recording what the server was asked for.
func TestDiscoverBuildsWellKnownPath(t *testing.T) {
	var requested []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
		})
	}))
	defer srv.Close()

	for _, issuer := range []string{srv.URL, srv.URL + "/"} {
		if _, err := Discover(context.Background(), srv.Client(), issuer); err != nil {
			t.Fatalf("Discover(%q): %v", issuer, err)
		}
	}
	if len(requested) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(requested))
	}
	for i, path := range requested {
		if path != "/.well-known/openid-configuration" {
			t.Errorf("request %d path = %q, want /.well-known/openid-configuration", i, path)
		}
	}
}

func TestDiscoverAuthorizationServer(t *testing.T) {
	var doc map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AuthorizationServerMetadataPath || doc == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}))
	defer srv.Close()

	// A resource server naming a DIFFERENT issuer is fine.
	doc = map[string]any{
		"issuer":                 "https://admin.example.com",
		"authorization_endpoint": "https://admin.example.com/oauth/authorize",
		"token_endpoint":         "https://admin.example.com/oauth/token",
	}
	meta, err := DiscoverAuthorizationServer(context.Background(), nil, srv.URL+"/")
	if err != nil || meta.TokenEndpoint != "https://admin.example.com/oauth/token" {
		t.Fatalf("got %+v %v", meta, err)
	}

	// A token endpoint off the issuer's origin is refused.
	doc["token_endpoint"] = "https://evil.example.com/token"
	if _, err := DiscoverAuthorizationServer(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("a token endpoint off the issuer origin must be refused")
	}

	// Plain http off loopback is refused.
	doc = map[string]any{
		"issuer":                 "http://admin.example.com",
		"authorization_endpoint": "http://admin.example.com/oauth/authorize",
		"token_endpoint":         "http://admin.example.com/oauth/token",
	}
	if _, err := DiscoverAuthorizationServer(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("http endpoints off loopback must be refused")
	}

	// No document is a distinct, detectable condition.
	doc = nil
	if _, err := DiscoverAuthorizationServer(context.Background(), nil, srv.URL); !errors.Is(err, ErrNoAuthorizationServer) {
		t.Fatalf("want ErrNoAuthorizationServer; got %v", err)
	}
}
