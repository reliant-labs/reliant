package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// fakeRelease is a GitHub-shaped release server over TLS: the releases API
// at /repos/o/r/releases/latest and each asset at /download/<name>.
type fakeRelease struct {
	srv   *httptest.Server
	tag   string
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newFakeRelease(t *testing.T, tag string, files map[string][]byte) *fakeRelease {
	t.Helper()
	f := &fakeRelease{tag: tag, files: files, hits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(f.ghRelease())
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		f.mu.Lock()
		body, ok := f.files[name]
		f.hits[name]++
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelease) ghRelease() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.files))
	for n := range f.files {
		names = append(names, n)
	}
	sort.Strings(names)
	var assets []map[string]any
	for _, n := range names {
		assets = append(assets, map[string]any{"name": n, "browser_download_url": f.srv.URL + "/download/" + n, "size": len(f.files[n])})
	}
	return map[string]any{"tag_name": f.tag, "assets": assets}
}

// release is what GitHubSource would return, built directly.
func (f *fakeRelease) release() Release {
	rel := Release{Version: strings.TrimPrefix(f.tag, "v"), Tag: f.tag}
	f.mu.Lock()
	defer f.mu.Unlock()
	for n := range f.files {
		a := Asset{Name: n, URL: f.srv.URL + "/download/" + n}
		rel.Assets = append(rel.Assets, a)
		if strings.HasSuffix(n, ".sigstore.json") {
			rel.Bundle = a
		}
	}
	return rel
}

func tarGz(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipOf(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func checksumsOf(files map[string][]byte) []byte {
	var lines []string
	for n, b := range files {
		lines = append(lines, fmt.Sprintf("%s  %s", sumHex(b), n))
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n")
}

// platformArchive is the default asset name for binary on this platform.
func platformArchive(binary string) string {
	return binary + "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
}

// script is a runnable stand-in for a release binary.
func script(body string) []byte { return []byte("#!/bin/sh\n" + body + "\n") }

// installed writes a self-owned "running binary" in its own directory.
func installed(t *testing.T, content []byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordingVerifier records what it was asked and returns err.
type recordingVerifier struct {
	err   error
	mu    sync.Mutex
	calls []map[string]string
}

func (v *recordingVerifier) Verify(_ context.Context, _ Release, files map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	cp := map[string]string{}
	for k, p := range files {
		cp[k] = p
	}
	v.calls = append(v.calls, cp)
	return v.err
}

// newTestUpdater builds an Updater whose "running binary" is exe and whose
// HTTP client trusts the fake server.
func newTestUpdater(t *testing.T, exe string, srv *fakeRelease, mutate func(*Config)) *Updater {
	t.Helper()
	cfg := Config{
		Binary:   "app",
		Current:  Build{Version: "1.0.0"},
		Source:   SourceFunc(func(context.Context, string) (Release, error) { return srv.release(), nil }),
		Verifier: &recordingVerifier{},
		StateDir: filepath.Join(t.TempDir(), "state"),
		Logger:   testkit.DiscardLogger(),
	}
	if srv != nil {
		cfg.HTTPClient = srv.srv.Client()
	}
	if mutate != nil {
		mutate(&cfg)
	}
	u, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	u.exePath = exe
	// The host running the tests may itself be a container; the container
	// probe has its own tests.
	u.detectEnv.container = nil
	return u
}

func readJSONState(t *testing.T, dir string) stateFile {
	t.Helper()
	st, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
