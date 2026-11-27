package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestGitHubSourceLatest(t *testing.T) {
	t.Parallel()
	var auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/repos/reliant-labs/forge/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.4.0",
			"assets": []map[string]any{
				{"name": "forge_linux_amd64.tar.gz", "browser_download_url": "https://x/a", "size": 10},
				{"name": "checksums.txt", "browser_download_url": "https://x/c", "size": 1},
				{"name": "forge-release.sigstore.json", "browser_download_url": "https://x/s", "size": 2},
			},
		})
	}))
	defer srv.Close()

	src := GitHubSource{Repo: "reliant-labs/forge", Bundle: "forge-release.sigstore.json", APIBaseURL: srv.URL, Token: "tok", Client: srv.Client()}
	rel, err := src.Latest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.4.0" || rel.Tag != "v1.4.0" || len(rel.Assets) != 3 {
		t.Fatalf("release = %+v", rel)
	}
	if rel.Bundle.URL != "https://x/s" {
		t.Fatalf("Bundle = %+v, want the named signature asset", rel.Bundle)
	}
	if a, ok := rel.Asset("checksums.txt"); !ok || a.URL != "https://x/c" {
		t.Fatalf("Asset(checksums.txt) = %+v, %v", a, ok)
	}
	if auth != "Bearer tok" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestGitHubSourcePrereleaseChannel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v1.4.0"},
			{"tag_name": "v1.5.0-rc.1", "prerelease": true},
			{"tag_name": "v1.6.0", "draft": true},
			{"tag_name": "v1.3.9"},
		})
	}))
	defer srv.Close()
	rel, err := GitHubSource{Repo: "o/r", APIBaseURL: srv.URL, Client: srv.Client()}.Latest(context.Background(), "prerelease")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v1.5.0-rc.1" {
		t.Fatalf("prerelease channel picked %s, want the newest non-draft v1.5.0-rc.1", rel.Tag)
	}
}

func TestGitHubSourceRefusals(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	ctx := context.Background()
	cases := map[string]struct {
		src     GitHubSource
		channel string
		want    string
	}{
		"repo without owner":     {GitHubSource{Repo: "forge"}, "", "owner/name"},
		"repo with extra path":   {GitHubSource{Repo: "o/r/releases"}, "", "owner/name"},
		"repo dot-dot name":      {GitHubSource{Repo: "o/.."}, "", "owner/name"},
		"repo dot-dot owner":     {GitHubSource{Repo: "../r"}, "", "owner/name"},
		"repo with query":        {GitHubSource{Repo: "o/r?x=1"}, "", "owner/name"},
		"repo with space":        {GitHubSource{Repo: "o w/r"}, "", "owner/name"},
		"repo with @":            {GitHubSource{Repo: "o@evil.com/r"}, "", "owner/name"},
		"unknown channel":        {GitHubSource{Repo: "o/r", APIBaseURL: srv.URL, Client: srv.Client()}, "nightly", "no channel"},
		"http API":               {GitHubSource{Repo: "o/r", APIBaseURL: "http://example.com"}, "", "must be https"},
		"userinfo in API":        {GitHubSource{Repo: "o/r", APIBaseURL: "https://api.github.com@evil.example"}, "", "userinfo"},
		"user:pass in API":       {GitHubSource{Repo: "o/r", APIBaseURL: "https://u:p@api.github.com"}, "", "userinfo"},
		"query in API":           {GitHubSource{Repo: "o/r", APIBaseURL: "https://api.github.com?next=https://evil"}, "", "query"},
		"fragment in API":        {GitHubSource{Repo: "o/r", APIBaseURL: "https://api.github.com#x"}, "", "fragment"},
		"no host in API":         {GitHubSource{Repo: "o/r", APIBaseURL: "https:///path"}, "", "no host"},
		"scheme-relative in API": {GitHubSource{Repo: "o/r", APIBaseURL: "//api.github.com"}, "", "must be https"},
		"API error":              {GitHubSource{Repo: "o/r", APIBaseURL: srv.URL, Client: srv.Client()}, "latest", "403"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.src.Latest(ctx, tc.channel)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// authRecorder is a TLS server that records the Authorization header of
// every request it sees.
type authRecorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen map[string]string
}

func newAuthRecorder(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *authRecorder {
	t.Helper()
	a := &authRecorder{seen: map[string]string{}}
	a.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.seen[r.URL.Path] = r.Header.Get("Authorization")
		a.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *authRecorder) auth(path string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.seen[path]
	return v, ok
}

func TestGitHubSourceTokenStaysOnTheAPIOrigin(t *testing.T) {
	t.Parallel()
	// Another origin (httptest servers share one certificate, so one client
	// trusts both). It answers like the API so the redirect completes.
	other := newAuthRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.0.0"})
	})
	api := newAuthRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/away/releases/latest":
			http.Redirect(w, r, other.srv.URL+"/elsewhere", http.StatusFound)
		case "/repos/o/home/releases/latest":
			http.Redirect(w, r, "/repos/o/home/releases/v1", http.StatusFound)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.0.0"})
		}
	})
	src := func(repo string) GitHubSource {
		return GitHubSource{Repo: repo, APIBaseURL: api.srv.URL, Token: "secret", Client: api.srv.Client()}
	}
	ctx := context.Background()

	if _, err := src("o/home").Latest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := api.auth("/repos/o/home/releases/v1"); got != "Bearer secret" {
		t.Errorf("a same-origin redirect lost the token: %q", got)
	}

	if _, err := src("o/away").Latest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := api.auth("/repos/o/away/releases/latest"); got != "Bearer secret" {
		t.Errorf("the API itself did not get the token: %q", got)
	}
	got, ok := other.auth("/elsewhere")
	if !ok {
		t.Fatal("the cross-origin redirect was not followed; the test proves nothing")
	}
	if got != "" {
		t.Fatalf("the token followed a redirect to another origin: %q", got)
	}
}

func TestAPIClientStripsTheTokenOffOrigin(t *testing.T) {
	t.Parallel()
	base, _ := url.Parse("https://api.github.com/api/v3")
	c := apiClient(&http.Client{}, base)
	for target, keep := range map[string]bool{
		"https://api.github.com/repos/o/r":     true,
		"https://api.github.com:443/repos/o/r": true,
		"https://API.GITHUB.COM/x":             true,
		"https://evil.example/x":               false,
		"https://api.github.com.evil.example/": false,
		"https://api.github.com:8443/x":        false,
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer secret")
		if err := c.CheckRedirect(req, []*http.Request{req}); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if got := req.Header.Get("Authorization") != ""; got != keep {
			t.Errorf("redirect to %s: token kept = %v, want %v", target, got, keep)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/x", nil)
	if err := c.CheckRedirect(req, []*http.Request{req}); err == nil {
		t.Error("a redirect to http must be refused")
	}
}
