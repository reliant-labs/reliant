package cliauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// TestMetadataHandler_IsDiscoverable: what the API server publishes is what
// forge's discovery accepts: the CLI can find the control plane from the API
// URL alone.
func TestMetadataHandler_IsDiscoverable(t *testing.T) {
	if MetadataHandler("") != nil {
		t.Fatal("no issuer must mean no handler: a self-hosted server offers no browser login")
	}
	mux := http.NewServeMux()
	mux.Handle(oauth2.AuthorizationServerMetadataPath, MetadataHandler("https://admin.example.com/"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	meta, err := oauth2.DiscoverAuthorizationServer(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Issuer != "https://admin.example.com" || meta.AuthorizationEndpoint != "https://admin.example.com/oauth/authorize" ||
		meta.TokenEndpoint != "https://admin.example.com/oauth/token" {
		t.Fatalf("metadata: %+v", meta)
	}
	resp, _ := http.Get(srv.URL + oauth2.AuthorizationServerMetadataPath)
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	_ = resp.Body.Close()
	if m, _ := doc["code_challenge_methods_supported"].([]any); len(m) != 1 || m[0] != "S256" {
		t.Errorf("must advertise S256 only: %v", doc["code_challenge_methods_supported"])
	}
}

func TestLookup_ExpiredIsNotLoggedIn(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	if _, _, err := Lookup("https://api.example.com"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("empty file: %v", err)
	}
	past := mustPast()
	if _, err := Store("https://api.example.com", credentials.Credential{Token: "rlat_x", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Lookup("https://api.example.com"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expired entry must read as not logged in: %v", err)
	}
}

// TestAdapter_ExpiryIsJudgedOnItsClock pins the Deps seam: expiry is judged
// against Deps.Now and the file lives where Deps.Dirs says — no ambient
// environment, no wall clock.
func TestAdapter_ExpiryIsJudgedOnItsClock(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: t.TempDir()}, Now: func() time.Time { return now }})
	exp := now.Add(time.Hour)
	if _, err := svc.Store("https://api.example.com", credentials.Credential{Token: "rlat_x", ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := svc.Lookup("https://api.example.com"); err != nil || c.Token != "rlat_x" {
		t.Fatalf("live entry: %v %+v", err, c)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := svc.Lookup("https://api.example.com"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("entry past its expiry on the adapter's clock must read as not logged in: %v", err)
	}
}

// TestAdapter_LoginUsesDepsHTTPClient: discovery goes through the injected
// client, so a caller (or a test) controls every outbound call.
func TestAdapter_LoginUsesDepsHTTPClient(t *testing.T) {
	var calls int
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("offline")
	})
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: t.TempDir()}, HTTPClient: doer,
		OpenURL: func(string) error { t.Fatal("must not open a browser when discovery fails"); return nil }})
	if _, err := svc.Login(context.Background(), Login{Server: "https://api.example.com"}); err == nil {
		t.Fatal("login succeeded with an offline client")
	}
	if calls == 0 {
		t.Fatal("discovery bypassed Deps.HTTPClient")
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestNonInteractiveNeverDiscoversOrOpens(t *testing.T) {
	opened := false
	_, err := Login{Server: "https://unreachable.invalid", NonInteractive: true,
		OpenURL: func(string) error { opened = true; return nil }}.Run(context.Background())
	if !errors.Is(err, ErrInteractiveRequired) || opened {
		t.Fatalf("err=%v opened=%v", err, opened)
	}
}

func mustPast() (t time.Time) { return time.Now().Add(-time.Hour) }
