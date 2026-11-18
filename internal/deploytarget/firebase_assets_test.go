package deploytarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostingEmulator serves a deployed tree the way Firebase Hosting does, for
// the parts of firebase.json forge renders:
//
//   - an uploaded file at the exact path wins (a directory serves its
//     index.html);
//   - otherwise the first matching rewrite serves its destination, 200;
//   - otherwise 404 with Firebase's HTML "Page Not Found";
//   - every header rule whose source matches the REQUEST path applies, and
//     for a header set twice the last match wins — on every response,
//     404s included.
//
// release() replaces the whole tree, as a Firebase release does: a file the
// new tree lacks is gone.
type hostingEmulator struct {
	mu       sync.Mutex
	public   string
	rewrites []map[string]any
	headers  []headerRule
	// tamper, when set, replaces the body served for a path.
	tamper map[string]string
}

func (h *hostingEmulator) release(t *testing.T, workdir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(workdir, "firebase.json"))
	if err != nil {
		t.Fatalf("read firebase.json: %v", err)
	}
	var doc struct {
		Hosting struct {
			Public   string           `json:"public"`
			Rewrites []map[string]any `json:"rewrites"`
			Headers  []headerRule     `json:"headers"`
		} `json:"hosting"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("firebase.json: %v", err)
	}
	// Snapshot the tree: the next deploy wipes the staging dir, and the
	// live release must not change under the test.
	snap := t.TempDir()
	if err := copyDir(filepath.Join(workdir, doc.Hosting.Public), snap); err != nil {
		t.Fatalf("snapshot release: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.public, h.rewrites, h.headers = snap, doc.Hosting.Rewrites, doc.Hosting.Headers
}

// firebaseGlob converts the glob subset forge renders ("**", "/x/**",
// "/*.html") to an anchored regexp.
func firebaseGlob(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch {
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case glob[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func ruleMatches(rule map[string]any, p string) bool {
	if re, ok := rule["regex"].(string); ok {
		return regexp.MustCompile(re).MatchString(p)
	}
	src, _ := rule["source"].(string)
	return src != "" && firebaseGlob(src).MatchString(p)
}

func (h *hostingEmulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	public, rewrites, headers, tamper := h.public, h.rewrites, h.headers, h.tamper
	h.mu.Unlock()

	p := r.URL.Path
	for _, rule := range headers {
		if firebaseGlob(rule.Source).MatchString(p) {
			for _, hd := range rule.Headers {
				w.Header().Set(hd.Key, hd.Value)
			}
		}
	}
	serve := func(rel string) bool {
		file := filepath.Join(public, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
		if info, err := os.Stat(file); err == nil && info.IsDir() {
			file = filepath.Join(file, "index.html")
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		if t, ok := tamper[rel]; ok {
			body = []byte(t)
		}
		ct := mime.TypeByExtension(path.Ext(file))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return true
	}
	if public != "" && serve(p) {
		return
	}
	for _, rule := range rewrites {
		if dest, _ := rule["destination"].(string); dest != "" && ruleMatches(rule, p) && serve(dest) {
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "<html><body>Page Not Found</body></html>")
}

type hostingResponse struct {
	status       int
	body         string
	contentType  string
	cacheControl string
}

func hostingGet(t *testing.T, srv *httptest.Server, p string) hostingResponse {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + p)
	if err != nil {
		t.Fatalf("GET %s: %v", p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return hostingResponse{resp.StatusCode, string(b), resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control")}
}

// firebaseSite is one Vite SPA deployed release after release through the
// real FirebaseProvider (build and `firebase deploy` faked; assembly,
// retention and firebase.json real) onto a hostingEmulator.
type firebaseSite struct {
	t          *testing.T
	emu        *hostingEmulator
	srv        *httptest.Server
	prov       FirebaseProvider
	fe         FirebaseFrontend
	projectDir string
	out        string // stdout of the last deploy
}

func newFirebaseSite(t *testing.T, keep int) *firebaseSite {
	t.Helper()
	emu := &hostingEmulator{}
	srv := httptest.NewServer(emu)
	t.Cleanup(srv.Close)
	projectDir := t.TempDir()
	return &firebaseSite{
		t: t, emu: emu, srv: srv, projectDir: projectDir,
		prov: FirebaseProvider{
			ProjectDir:  projectDir,
			Runner:      &fakeRunner{},
			StagingRoot: filepath.Join(t.TempDir(), "public"),
			HTTPClient:  srv.Client(),
			SiteURL:     func(string) string { return srv.URL },
			Sleep:       func(context.Context, time.Duration) error { return nil },
		},
		fe: FirebaseFrontend{
			Name: "reliant-web", Path: "web",
			Spec: FirebaseHostingSpec{
				Project: "reliant-labs-475814", Site: "reliant-prod", PublicDir: "dist",
				SPAFallback: "/index.html", AssetDir: "assets", KeepAssetReleases: keep,
			},
		},
	}
}

// deploy builds `files` as the release's dist/, deploys it, and makes it
// the live release.
func (s *firebaseSite) deploy(files map[string]string) {
	s.t.Helper()
	dist := filepath.Join(s.projectDir, "web", "dist")
	if err := os.RemoveAll(dist); err != nil {
		s.t.Fatal(err)
	}
	for rel, body := range files {
		writeFile(s.t, filepath.Join(dist, filepath.FromSlash(rel)), body)
	}
	var err error
	s.out = captureStdout(s.t, func() {
		err = s.prov.Deploy(context.Background(), ServiceGroup{ProviderID: s.prov.Name(), Frontends: []FirebaseFrontend{s.fe}})
	})
	if err != nil {
		s.t.Fatalf("deploy: %v\n%s", err, s.out)
	}
	s.emu.release(s.t, filepath.Dir(s.prov.StagingRoot))
}

func viteBuild(id string, shared map[string]string) map[string]string {
	files := map[string]string{
		"index.html":                     `<!doctype html><script type="module" src="/assets/index-` + id + `.js"></script>`,
		"assets/index-" + id + ".js":     "import('./InboxPage-" + id + ".js') // " + id,
		"assets/InboxPage-" + id + ".js": "export default 'inbox " + id + "'",
		"assets/index-" + id + ".css":    "body{} /* " + id + " */",
		"version.json":                   `{"version":"` + id + `"}`,
	}
	for k, v := range shared {
		files[k] = v
	}
	return files
}

// THE INCIDENT, end to end. A tab that loaded release 1 navigates after
// release 2 went live: its chunk must still be served (not deleted, and
// not answered with index.html), while a chunk no release ever had is a
// real 404, routes get the entry document, and every answer carries the
// right Cache-Control.
func TestFirebaseDeploy_ATabFromThePreviousReleaseKeepsLoadingItsChunks(t *testing.T) {
	site := newFirebaseSite(t, 2)
	vendor := map[string]string{"assets/vendor-react-V1.js": "react"}

	site.deploy(viteBuild("R1", vendor))
	if !strings.Contains(site.out, "no asset manifest") {
		t.Errorf("the first deploy should report it had nothing to carry:\n%s", site.out)
	}
	site.deploy(viteBuild("R2", vendor))

	// The release-1 tab lazy-loads its Inbox chunk.
	old := hostingGet(t, site.srv, "/assets/InboxPage-R1.js")
	if old.status != http.StatusOK || old.body != "export default 'inbox R1'" {
		t.Fatalf("release 1's chunk after release 2: %d %q, want 200 with its own bytes", old.status, old.body)
	}
	if !strings.Contains(old.contentType, "javascript") {
		t.Errorf("release 1's chunk served as %q", old.contentType)
	}
	if old.cacheControl != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset Cache-Control = %q, want immutable for a year", old.cacheControl)
	}
	if cur := hostingGet(t, site.srv, "/assets/InboxPage-R2.js"); cur.status != http.StatusOK {
		t.Errorf("release 2's own chunk: %d", cur.status)
	}

	// A chunk no live release has is a 404, never the app shell.
	missing := hostingGet(t, site.srv, "/assets/does-not-exist-abc123.js")
	if missing.status != http.StatusNotFound {
		t.Errorf("missing /assets/*.js: %d %s %q — want 404, not the SPA's index.html", missing.status, missing.contentType, missing.body)
	}

	// Routes get release 2's entry document, revalidated on every load.
	for _, route := range []string{"/", "/inbox", "/project/d70701a6/settings"} {
		got := hostingGet(t, site.srv, route)
		if got.status != http.StatusOK || !strings.Contains(got.body, "/assets/index-R2.js") {
			t.Errorf("route %s: %d %q, want release 2's index.html", route, got.status, got.body)
		}
		if got.cacheControl != "no-cache" {
			t.Errorf("route %s Cache-Control = %q, want no-cache", route, got.cacheControl)
		}
	}
	if v := hostingGet(t, site.srv, "/version.json"); v.cacheControl != "no-cache" || !strings.Contains(v.body, "R2") {
		t.Errorf("version.json: %q %q, want release 2's, no-cache", v.cacheControl, v.body)
	}
	if !strings.Contains(site.out, "release 2 serves 4 built + 3 carried asset(s) from 1 earlier release(s)") {
		t.Errorf("deploy summary missing or wrong:\n%s", site.out)
	}
}

// Retention is a window, not a hoard: with keep_asset_releases = 2 an asset
// stays one release past the last build that contained it. An asset every
// build contains (a stable vendor chunk) is never at risk.
func TestFirebaseDeploy_RetentionWindowExpiresOldAssets(t *testing.T) {
	site := newFirebaseSite(t, 2)
	vendor := map[string]string{"assets/vendor-react-V1.js": "react"}
	site.deploy(viteBuild("R1", vendor))
	site.deploy(viteBuild("R2", vendor))
	site.deploy(viteBuild("R3", vendor))

	if got := hostingGet(t, site.srv, "/assets/InboxPage-R1.js"); got.status != http.StatusNotFound {
		t.Errorf("release 1's chunk after release 3 (keep 2): %d, want 404", got.status)
	}
	for _, p := range []string{"/assets/InboxPage-R2.js", "/assets/InboxPage-R3.js", "/assets/vendor-react-V1.js"} {
		if got := hostingGet(t, site.srv, p); got.status != http.StatusOK {
			t.Errorf("%s after release 3: %d, want 200", p, got.status)
		}
	}

	var m assetManifest
	if err := json.Unmarshal([]byte(hostingGet(t, site.srv, "/"+AssetManifestName).body), &m); err != nil {
		t.Fatalf("live manifest: %v", err)
	}
	if m.Release != 3 || m.Schema != assetManifestSchema {
		t.Errorf("manifest release/schema = %d/%q", m.Release, m.Schema)
	}
	want := map[string]int{
		"assets/InboxPage-R2.js":    2,
		"assets/InboxPage-R3.js":    3,
		"assets/vendor-react-V1.js": 3, // rebuilt every release
	}
	for rel, release := range want {
		if got := m.Assets[rel]; got.Release != release {
			t.Errorf("manifest %s release = %d, want %d", rel, got.Release, release)
		}
	}
	if _, ok := m.Assets["assets/InboxPage-R1.js"]; ok {
		t.Error("the manifest still lists an expired asset")
	}
}

// A carried asset is republished only with the bytes its manifest recorded.
// Anything else — a 200 that is really an HTML fallback page, a changed
// file — is dropped and reported, and the deploy goes on.
func TestFirebaseDeploy_CarriesOnlyVerifiedBytes(t *testing.T) {
	site := newFirebaseSite(t, 3)
	site.deploy(viteBuild("R1", nil))
	site.emu.mu.Lock()
	site.emu.tamper = map[string]string{"/assets/InboxPage-R1.js": "<!doctype html><title>app shell</title>"}
	site.emu.mu.Unlock()
	site.deploy(viteBuild("R2", nil))

	if got := hostingGet(t, site.srv, "/assets/InboxPage-R1.js"); got.status != http.StatusNotFound {
		t.Errorf("a carried asset whose bytes did not verify was republished: %d %q", got.status, got.body)
	}
	if got := hostingGet(t, site.srv, "/assets/index-R1.js"); got.status != http.StatusOK {
		t.Errorf("an asset that verified was not carried: %d", got.status)
	}
	if !strings.Contains(site.out, "could not carry /assets/InboxPage-R1.js: content does not match the manifest") {
		t.Errorf("the dropped asset was not reported:\n%s", site.out)
	}
}

// The first deploy onto a site still running a release from before
// retention: its catch-all rewrite answers the manifest path with
// index.html and a 200. That is "no manifest", not a parse failure, and not
// a reason to block the deploy.
func TestFirebaseDeploy_LegacyLiveReleaseIsReadAsNoManifest(t *testing.T) {
	site := newFirebaseSite(t, 2)
	legacy := t.TempDir()
	writeFile(t, filepath.Join(legacy, "public", "index.html"), "<!doctype html>legacy")
	writeFile(t, filepath.Join(legacy, "firebase.json"),
		`{"hosting":{"public":"public","rewrites":[{"source":"**","destination":"/index.html"}]}}`)
	site.emu.release(t, legacy)

	site.deploy(viteBuild("R1", nil))
	if !strings.Contains(site.out, "answered with an HTML page") {
		t.Errorf("legacy live release not recognised:\n%s", site.out)
	}
	if got := hostingGet(t, site.srv, "/assets/InboxPage-R1.js"); got.status != http.StatusOK {
		t.Errorf("the deploy did not go live: %d", got.status)
	}
}

// The live site unreachable: retention degrades to "carry nothing", says
// so, and the deploy still ships.
func TestFirebaseDeploy_UnreachableSiteDoesNotBlockTheDeploy(t *testing.T) {
	site := newFirebaseSite(t, 2)
	site.srv.Close()
	site.deploy(viteBuild("R1", nil))
	if !strings.Contains(site.out, "after 3 attempts") {
		t.Errorf("an unreachable site was not reported:\n%s", site.out)
	}
	manifest := filepath.Join(site.prov.StagingRoot, AssetManifestName)
	assertFileContains(t, manifest, `"release": 1`)
}

// The emulator reproduces the production behaviour the incident turned on:
// under the conventional catch-all SPA rewrite a missing chunk is answered
// with index.html and a 200 (verified against app.reliantlabs.io with curl
// on 2026-10-10). Without this, the assertions above could pass against an
// emulator that 404s everything.
func TestHostingEmulator_ReproducesTheCatchAllIncident(t *testing.T) {
	emu := &hostingEmulator{}
	srv := httptest.NewServer(emu)
	defer srv.Close()
	legacy := t.TempDir()
	writeFile(t, filepath.Join(legacy, "public", "index.html"), "<!doctype html>app")
	writeFile(t, filepath.Join(legacy, "firebase.json"),
		`{"hosting":{"public":"public","rewrites":[{"source":"**","destination":"/index.html"}]}}`)
	emu.release(t, legacy)
	got := hostingGet(t, srv, "/assets/does-not-exist-abc123.js")
	if got.status != http.StatusOK || !strings.HasPrefix(got.contentType, "text/html") {
		t.Fatalf("catch-all rewrite: %d %q — the emulator no longer models Firebase", got.status, got.contentType)
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The manifest comes off the network, so a record that is not a clean path
// under the asset directory, a malformed hash, or an impossible release
// number is never carried — and nothing outside the window is.
func TestPlanAssetCarry(t *testing.T) {
	good := sha("x")
	prev := &assetManifest{Schema: assetManifestSchema, Release: 5, Assets: map[string]assetRecord{
		"assets/kept-4.js":       {SHA256: good, Release: 4},
		"assets/kept-3.js":       {SHA256: good, Release: 3},
		"assets/expired-2.js":    {SHA256: good, Release: 2},
		"assets/rebuilt.js":      {SHA256: good, Release: 4},
		"assets/future.js":       {SHA256: good, Release: 6},
		"assets/zero.js":         {SHA256: good, Release: 0},
		"assets/bad-sha.js":      {SHA256: "nothex", Release: 5},
		"assets/../../etc/x.js":  {SHA256: good, Release: 5},
		"/assets/abs.js":         {SHA256: good, Release: 5},
		"other/outside.js":       {SHA256: good, Release: 5},
		"assets":                 {SHA256: good, Release: 5},
		"assets/./dot.js":        {SHA256: good, Release: 5},
		`assets\windows.js`:      {SHA256: good, Release: 5},
		"assets/nested/ok-5.css": {SHA256: good, Release: 5},
	}}
	release, carry := planAssetCarry(prev, map[string]string{"assets/rebuilt.js": good}, "assets", 3)
	if release != 6 {
		t.Errorf("release = %d, want 6", release)
	}
	want := []string{"assets/kept-4.js", "assets/nested/ok-5.css"}
	if len(carry) != len(want) {
		t.Errorf("carry = %v, want exactly %v", carry, want)
	}
	for _, rel := range want {
		if _, ok := carry[rel]; !ok {
			t.Errorf("%s not carried", rel)
		}
	}

	if release, carry := planAssetCarry(nil, nil, "assets", 3); release != 1 || len(carry) != 0 {
		t.Errorf("no previous manifest: release %d, carry %v", release, carry)
	}
}

func TestEffectiveKeepAssetReleases(t *testing.T) {
	for in, want := range map[int]int{0: defaultKeepAssetReleases, 1: 2, 2: 2, 7: 7} {
		if got := effectiveKeepAssetReleases(in); got != want {
			t.Errorf("effectiveKeepAssetReleases(%d) = %d, want %d", in, got, want)
		}
	}
}

// --dry-run says where retention reads from and touches nothing.
func TestFirebaseDryRunPlan_ReportsAssetRetention(t *testing.T) {
	prov := FirebaseProvider{ProjectDir: t.TempDir(), Runner: &fakeRunner{}, StagingRoot: filepath.Join(t.TempDir(), "public")}
	fe := FirebaseFrontend{Name: "reliant-web", Path: "web", Spec: FirebaseHostingSpec{
		Project: "reliant-labs-475814", Site: "reliant-prod", PublicDir: "dist",
		SPAFallback: "/index.html", AssetDir: "assets",
	}}
	out := captureStdout(t, func() {
		if err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe}, DryRun: true}); err != nil {
			t.Fatalf("dry-run: %v", err)
		}
	})
	if !strings.Contains(out, "asset retention: keep /assets/** of the last 10 release(s), read from https://reliant-prod.web.app/forge-assets.json") {
		t.Errorf("dry-run plan does not describe retention:\n%s", out)
	}
	if !strings.Contains(out, `"regex": "^/(?:[^/]*/)*`) {
		t.Errorf("dry-run firebase.json has no SPA fallback regex:\n%s", out)
	}
}
