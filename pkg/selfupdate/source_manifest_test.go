package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A manifest in reliant's daemon-channels.json shape (schema 1).
const testManifest = `{
  "schema": 1,
  "repository": "reliant-labs/reliant",
  "written_by": {"tag": "v1.9.0", "commit": "abc", "bundle": {"url": "https://dl.example/cli/v1.9.0/reliant-release.sigstore.json"}},
  "channels": {
    "latest": {
      "version": "1.9.0", "tag": "v1.9.0", "commit": "abc", "commit_time_unix": 1763500000,
      "checksums": {"url": "https://dl.example/cli/v1.9.0/checksums.txt", "sha256": "00"},
      "bundle": {"url": "https://dl.example/cli/v1.9.0/reliant-release.sigstore.json"},
      "platforms": {
        "linux/amd64": {"url": "https://dl.example/cli/v1.9.0/reliant_linux_amd64.tar.gz", "sha256": "11"},
        "darwin/arm64": {"url": "https://dl.example/cli/v1.9.0/reliant_darwin_arm64.tar.gz", "sha256": "22"}
      }
    }
  }
}`

func TestManifestSourceLatest(t *testing.T) {
	t.Parallel()
	var cacheControl string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cacheControl = r.Header.Get("Cache-Control")
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()

	rel, err := ManifestSource{Repository: "reliant-labs/reliant", URL: srv.URL + "/daemon-channels.json", AllowedOrigins: []string{"https://dl.example"}, Client: srv.Client()}.Latest(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.9.0" || rel.Tag != "v1.9.0" || rel.Channel != "latest" {
		t.Fatalf("release = %+v", rel)
	}
	if !rel.CommitTime.Equal(time.Unix(1763500000, 0)) {
		t.Fatalf("CommitTime = %v", rel.CommitTime)
	}
	if rel.Bundle.Name != "reliant-release.sigstore.json" {
		t.Fatalf("Bundle = %+v", rel.Bundle)
	}
	for _, name := range []string{"checksums.txt", "reliant_linux_amd64.tar.gz", "reliant_darwin_arm64.tar.gz"} {
		if _, ok := rel.Asset(name); !ok {
			t.Errorf("no asset %s in %+v", name, rel.Assets)
		}
	}
	if cacheControl != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cacheControl)
	}
}

func TestManifestSourceRequiresMatchingRepository(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, repository, body, want string
	}{
		{"different", "reliant-labs/reliant", strings.Replace(testManifest, `"repository": "reliant-labs/reliant"`, `"repository": "other/repo"`, 1), "does not match expected repository"},
		{"missing", "reliant-labs/reliant", strings.Replace(testManifest, `"repository": "reliant-labs/reliant",`, "", 1), "does not match expected repository"},
		{"empty", "reliant-labs/reliant", strings.Replace(testManifest, `"repository": "reliant-labs/reliant"`, `"repository": ""`, 1), "does not match expected repository"},
		{"unconfigured", "", testManifest, "ManifestSource.Repository is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			rel, err := (ManifestSource{
				Repository: tc.repository, URL: srv.URL + "/daemon-channels.json",
				AllowedOrigins: []string{"https://dl.example"}, Client: srv.Client(),
			}).Latest(context.Background(), "latest")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if rel.Tag != "" || len(rel.Assets) != 0 {
				t.Fatalf("returned release on repository failure: %+v", rel)
			}
		})
	}
}

func TestManifestRefuses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body, channel, want string
	}{
		"another schema":     {strings.Replace(testManifest, `"schema": 1`, `"schema": 2`, 1), "latest", "schema 2"},
		"an unknown channel": {testManifest, "alpha", `no channel "alpha"`},
		"a non-https file": {
			strings.Replace(testManifest, "https://dl.example/cli/v1.9.0/checksums.txt", "http://dl.example/checksums.txt", 1),
			"latest", "not https",
		},
		"two files with one name": {
			strings.Replace(testManifest, "reliant_darwin_arm64.tar.gz", "reliant_linux_amd64.tar.gz", 1),
			"latest", "two files",
		},
		"a file on another host": {
			strings.Replace(testManifest, "https://dl.example/cli/v1.9.0/reliant_linux_amd64.tar.gz", "https://evil.example/reliant_linux_amd64.tar.gz", 1),
			"latest", "not on an allowed origin",
		},
		"no tag": {strings.Replace(testManifest, `"tag": "v1.9.0", "commit": "abc", "commit_time_unix"`, `"tag": "", "commit": "abc", "commit_time_unix"`, 1), "latest", "names no tag"},
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := parseManifest([]byte(tc.body))
			if err == nil {
				_, err = doc.release(tc.channel, map[string]bool{"https://dl.example": true})
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestManifestSourceRefusesPlainHTTP(t *testing.T) {
	t.Parallel()
	_, err := ManifestSource{Repository: "reliant-labs/reliant", URL: "http://dl.example/daemon-channels.json"}.Latest(context.Background(), "latest")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestSourceDefaultsToItsOwnOrigin(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()
	// The test manifest names dl.example, not the server it is served from.
	_, err := ManifestSource{Repository: "reliant-labs/reliant", URL: srv.URL + "/daemon-channels.json", Client: srv.Client()}.Latest(context.Background(), "latest")
	if err == nil || !strings.Contains(err.Error(), "not on an allowed origin") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestSourceRefusesARedirectOffItsOrigin(t *testing.T) {
	t.Parallel()
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testManifest))
	}))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/daemon-channels.json", http.StatusFound)
	}))
	defer srv.Close()
	client := srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs.AddCert(other.Certificate())
	_, err := ManifestSource{Repository: "reliant-labs/reliant", URL: srv.URL + "/daemon-channels.json", AllowedOrigins: []string{"https://dl.example"}, Client: client}.Latest(context.Background(), "latest")
	if err == nil || !strings.Contains(err.Error(), "redirect of the channel manifest") {
		t.Fatalf("err = %v", err)
	}
}
