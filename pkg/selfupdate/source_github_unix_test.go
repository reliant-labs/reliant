//go:build unix

package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestGitHubSourceTokenNeverReachesAssetDownloads(t *testing.T) {
	t.Parallel()
	files := releaseFiles(t, script("echo 2.0.0"))
	assets := newAuthRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(files[strings.TrimPrefix(r.URL.Path, "/download/")])
	})
	// The API lives on its own origin and lists assets on the asset server.
	api := newAuthRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		var list []map[string]any
		for n := range files {
			list = append(list, map[string]any{"name": n, "browser_download_url": assets.srv.URL + "/download/" + n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2.0.0", "assets": list})
	})
	src := GitHubSource{Repo: "o/r", Bundle: "app-release.sigstore.json", APIBaseURL: api.srv.URL, Token: "secret", Client: api.srv.Client()}
	u, err := New(Config{
		Binary: "app", Current: Build{Version: "1.0.0"}, Source: src, Verifier: &recordingVerifier{},
		StateDir: t.TempDir(), HTTPClient: assets.srv.Client(), Logger: testkit.DiscardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	u.exePath = installed(t, script("echo 1.0.0"))
	u.detectEnv.container = nil
	rel, _, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Prepare(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if got, _ := api.auth("/repos/o/r/releases/latest"); got != "Bearer secret" {
		t.Errorf("API request token = %q", got)
	}
	for n := range files {
		got, ok := assets.auth("/download/" + n)
		if !ok {
			t.Errorf("%s was never downloaded", n)
		}
		if got != "" {
			t.Errorf("the token reached the asset download of %s: %q", n, got)
		}
	}
}
