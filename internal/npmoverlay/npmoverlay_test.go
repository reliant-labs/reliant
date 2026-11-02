package npmoverlay

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeUpstream is a registry holding @acme/lib 1.0.0 and @acme/other 3.0.0.
// It records every path it is asked for.
func fakeUpstream(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.EscapedPath())
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/@acme%2flib":
			_, _ = io.WriteString(w, `{"name":"@acme/lib","dist-tags":{"latest":"1.0.0"},`+
				`"versions":{"1.0.0":{"name":"@acme/lib","version":"1.0.0"}},"time":{"1.0.0":"2020-01-01T00:00:00Z"}}`)
		case "/@acme%2fother":
			_, _ = io.WriteString(w, `{"name":"@acme/other","dist-tags":{"latest":"3.0.0"},"versions":{"3.0.0":{}}}`)
		default:
			http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// writePackage writes a minimal scoped package checkout.
func writePackage(t *testing.T, name, version string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := `{"name":"` + name + `","version":"` + version + `","main":"index.js","files":["index.js"]}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("module.exports = "+`"`+version+`";`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// tarballFor builds, in process, the tarball npm pack would: package/ prefix.
func tarballFor(t *testing.T, dir string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []string{"package.json", "index.js"} {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + f, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = zw.Close()
	tgz := filepath.Join(t.TempDir(), "acme-lib.tgz")
	if err := os.WriteFile(tgz, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return tgz
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// TestOverlayServesTheLocalVersionBesideThePublishedOnes drives the HTTP
// surface npm uses, with the tarball loaded in process (no npm).
func TestOverlayServesTheLocalVersionBesideThePublishedOnes(t *testing.T) {
	up, seen := fakeUpstream(t)
	dir := writePackage(t, "@acme/lib", "1.2.0")
	o, err := Start(Options{Upstream: up.URL, Packages: []Package{{Dir: dir}}, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	// Stand in for npm pack.
	tgz := tarballFor(t, dir)
	lp := o.packages["@acme/lib"]
	lp.once.Do(func() { lp.err = lp.load(tgz) })
	if lp.err != nil {
		t.Fatal(lp.err)
	}

	status, body := get(t, o.URL+"@acme%2flib")
	if status != http.StatusOK {
		t.Fatalf("packument: HTTP %d: %s", status, body)
	}
	var doc struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			ID   string            `json:"_id"`
			Main string            `json:"main"`
			Dist map[string]string `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Versions["1.0.0"]; !ok {
		t.Errorf("the published 1.0.0 is gone from the packument: %s", body)
	}
	local, ok := doc.Versions["1.2.0"]
	if !ok {
		t.Fatalf("the local 1.2.0 is not in the packument: %s", body)
	}
	if doc.DistTags["latest"] != "1.2.0" {
		t.Errorf("latest = %q, want the local 1.2.0, which is ahead of the release", doc.DistTags["latest"])
	}
	if local.ID != "@acme/lib@1.2.0" || local.Main != "index.js" {
		t.Errorf("local manifest = %+v, want package.json's fields and _id", local)
	}
	data, _ := os.ReadFile(tgz)
	sum := sha512.Sum512(data)
	if want := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); local.Dist["integrity"] != want {
		t.Errorf("integrity = %q, want %q", local.Dist["integrity"], want)
	}

	status, got := get(t, local.Dist["tarball"])
	if status != http.StatusOK || !bytes.Equal(got, data) {
		t.Errorf("tarball %s: HTTP %d, %d bytes, want the %d packed bytes", local.Dist["tarball"], status, len(got), len(data))
	}
	if status, _ := get(t, o.URL+"-/tarballs/@acme%2flib/other.tgz"); status != http.StatusNotFound {
		t.Errorf("an unknown tarball answered HTTP %d, want 404", status)
	}

	// Another package in the routed scope passes through untouched.
	status, body = get(t, o.URL+"@acme%2fother")
	if status != http.StatusOK || !strings.Contains(string(body), `"3.0.0"`) {
		t.Errorf("proxied @acme/other: HTTP %d: %s", status, body)
	}
	if status, _ := get(t, o.URL+"@acme%2fmissing"); status != http.StatusNotFound {
		t.Errorf("a package upstream lacks answered HTTP %d, want upstream's 404", status)
	}
	want := []string{"/@acme%2flib", "/@acme%2fother", "/@acme%2fmissing"}
	if got := seen(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("upstream saw %v, want %v (the overlaid packument fetched once)", got, want)
	}
}

func TestMergePackument(t *testing.T) {
	lp := &localPackage{name: "@acme/lib", version: "1.0.0", file: "acme-lib-1.0.0.tgz",
		manifest: map[string]any{"name": "@acme/lib", "version": "1.0.0", "description": "local"}}
	t.Run("replaces a published version of the same number", func(t *testing.T) {
		up := []byte(`{"dist-tags":{"latest":"2.0.0"},"versions":{"1.0.0":{"description":"published"},"2.0.0":{}}}`)
		out, err := mergePackument(up, lp, "http://x/")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			DistTags map[string]string         `json:"dist-tags"`
			Versions map[string]map[string]any `json:"versions"`
		}
		_ = json.Unmarshal(out, &doc)
		if doc.Versions["1.0.0"]["description"] != "local" {
			t.Errorf("1.0.0 = %v, want the local manifest", doc.Versions["1.0.0"])
		}
		if doc.DistTags["latest"] != "2.0.0" {
			t.Errorf("latest = %q, want the newer published 2.0.0 kept", doc.DistTags["latest"])
		}
	})
	t.Run("a never-published package", func(t *testing.T) {
		out, err := mergePackument(nil, lp, "http://x/")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"latest":"1.0.0"`) || !strings.Contains(string(out), `"tarball":"http://x/-/tarballs/@acme%2flib/acme-lib-1.0.0.tgz"`) {
			t.Errorf("packument = %s", out)
		}
	})
}

// TestOverlayKeepsTheGlobalConfigItReplaces: pointing npm at the overlay's
// global file must take nothing away from the one npm read before.
func TestOverlayKeepsTheGlobalConfigItReplaces(t *testing.T) {
	prev := filepath.Join(t.TempDir(), "npmrc")
	if err := os.WriteFile(prev, []byte("fund=false"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("npm_config_globalconfig", prev)
	o, err := Start(Options{Upstream: "http://127.0.0.1:1", Packages: []Package{{Dir: writePackage(t, "@acme/lib", "1.0.0")}}, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	got, err := os.ReadFile(envValue(t, o, "npm_config_globalconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "fund=false\n@acme:registry=" + o.URL + "\n"; string(got) != want {
		t.Errorf("overlay npmrc = %q, want %q", got, want)
	}
}

// TestOverlayKeepsTheUserConfigPnpmReads: pnpm takes its routes from the
// user .npmrc, so the overlay's pnpm file must carry the user's own (auth
// included) and add only the scope routes; and the overlaid packages must be
// exempt from pnpm's release-age gate, which would otherwise pass over a
// version the overlay dates now.
func TestOverlayKeepsTheUserConfigPnpmReads(t *testing.T) {
	prev := filepath.Join(t.TempDir(), "npmrc")
	if err := os.WriteFile(prev, []byte("//registry.example/:_authToken=${TOKEN}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("pnpm_config_userconfig", prev)
	o, err := Start(Options{Upstream: "http://127.0.0.1:1", Packages: []Package{{Dir: writePackage(t, "@acme/lib", "1.0.0")}}, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	got, err := os.ReadFile(envValue(t, o, "pnpm_config_userconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "//registry.example/:_authToken=${TOKEN}\n@acme:registry=" + o.URL + "\n"; string(got) != want {
		t.Errorf("overlay pnpm user config = %q, want %q", got, want)
	}
	if got := envValue(t, o, "pnpm_config_minimum_release_age_exclude"); got != `["@acme/lib"]` {
		t.Errorf("pnpm_config_minimum_release_age_exclude = %s, want [\"@acme/lib\"]", got)
	}
}

// envValue returns the value Env gives key, failing when it gives none.
func envValue(t *testing.T, o *Overlay, key string) string {
	t.Helper()
	for _, kv := range o.Env() {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v
		}
	}
	t.Fatalf("Env() = %v, has no %s", o.Env(), key)
	return ""
}

func TestStartRefusesAnUnscopedPackage(t *testing.T) {
	_, err := Start(Options{Packages: []Package{{Dir: writePackage(t, "lib", "1.0.0")}}, Log: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "not a scoped package") {
		t.Fatalf("err = %v, want a refusal naming the unscoped package", err)
	}
}

// TestNpmInstallsTheLocalVersionThroughTheOverlay is the claim the package
// makes, end to end: a real npm, a range only the local checkout satisfies,
// and the user's own config untouched.
func TestNpmInstallsTheLocalVersionThroughTheOverlay(t *testing.T) {
	if testing.Short() {
		t.Skip("runs npm pack and npm install; runs in task test")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	up, _ := fakeUpstream(t)
	dir := writePackage(t, "@acme/lib", "1.2.0")
	o, err := Start(Options{Upstream: up.URL, Packages: []Package{{Dir: dir}}, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	app := t.TempDir()
	if err := os.WriteFile(filepath.Join(app, "package.json"),
		[]byte(`{"name":"app","version":"0.0.0","private":true,"dependencies":{"@acme/lib":"^1.2.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("npm", "install", "--no-audit", "--no-fund")
	cmd.Dir = app
	cmd.Env = append(append(os.Environ(), o.Env()...), "npm_config_cache="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm install: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(app, "node_modules", "@acme", "lib", "index.js"))
	if err != nil || !strings.Contains(string(got), "1.2.0") {
		t.Fatalf("installed @acme/lib index.js = %q (%v), want the local 1.2.0", got, err)
	}
	lock, _ := os.ReadFile(filepath.Join(app, "package-lock.json"))
	if !strings.Contains(string(lock), o.URL+"-/tarballs/") || !strings.Contains(string(lock), `"integrity": "sha512-`) {
		t.Errorf("lockfile does not record the overlay tarball with its integrity:\n%s", lock)
	}
}

// TestPnpmInstallsTheLocalVersionThroughTheOverlay is the same claim for
// pnpm, which reads none of npm's routing. The range also admits the
// published 1.0.0, dated years ago, so it proves the release-age exemption
// too: without it pnpm prefers the mature 1.0.0 over the local 1.2.0 the
// overlay dates now (and this fake 1.0.0 has no tarball, so that fails).
func TestPnpmInstallsTheLocalVersionThroughTheOverlay(t *testing.T) {
	if testing.Short() {
		t.Skip("runs npm pack and pnpm install; runs in task test")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not on PATH")
	}
	up, _ := fakeUpstream(t)
	dir := writePackage(t, "@acme/lib", "1.2.0")
	o, err := Start(Options{Upstream: up.URL, Packages: []Package{{Dir: dir}}, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	app := t.TempDir()
	if err := os.WriteFile(filepath.Join(app, "package.json"),
		[]byte(`{"name":"app","version":"0.0.0","private":true,"dependencies":{"@acme/lib":"^1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("pnpm", "install")
	cmd.Dir = app
	cmd.Env = append(append(os.Environ(), o.Env()...),
		"pnpm_config_store_dir="+t.TempDir(), "pnpm_config_cache_dir="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pnpm install: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(app, "node_modules", "@acme", "lib", "index.js"))
	if err != nil || !strings.Contains(string(got), "1.2.0") {
		t.Fatalf("installed @acme/lib index.js = %q (%v), want the local 1.2.0", got, err)
	}
	lock, _ := os.ReadFile(filepath.Join(app, "pnpm-lock.yaml"))
	if !strings.Contains(string(lock), "1.2.0") || !strings.Contains(string(lock), "integrity: sha512-") {
		t.Errorf("lockfile does not record the local version with its integrity:\n%s", lock)
	}
}
