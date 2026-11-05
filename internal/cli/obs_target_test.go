package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// obsTargetProject writes a project whose prod env declares a control plane at
// endpoint, and returns its directory. The KCL is never evaluated by these
// tests: renderKCLRawHook stands in for it.
func obsTargetProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: obstarget\nmodule_path: github.com/example/obstarget\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"obstarget-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write("deploy/kcl/prod/main.k", "# declaration under test; never evaluated\n")
	ensureLedgerReady(t, dir)
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	t.Setenv(cloud.DefaultTokenEnv, "tok")
	stubObsRender(t, endpoint, nil)
	return dir
}

// stubObsRender replaces the KCL evaluation with a fixed hosted declaration and
// counts how often it runs.
func stubObsRender(t *testing.T, endpoint string, onRender func()) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	prev := renderKCLRawHook
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		n.Add(1)
		if onRender != nil {
			onRender()
		}
		return []byte(`{"output":{"control_plane":{"type":"ControlPlane","endpoint":"` + endpoint + `"},` +
			`"observability":{"enabled":true,"signals":{}},"workloads":[]}}`), nil
	}
	t.Cleanup(func() { renderKCLRawHook = prev })
	return &n
}

func obsCloudForbidden(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"permission_denied","message":"token lacks obs:read"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func runObsIssuesCloud(t *testing.T) (time.Duration, error) {
	t.Helper()
	old := obsDepsOverride
	obsDepsOverride = nil
	t.Cleanup(func() { obsDepsOverride = old })
	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"obs", "issues", "prod", "--backend", "cloud"})
	start := time.Now()
	err := cmd.ExecuteContext(context.Background())
	return time.Since(start), err
}

// TestObsCloudRefusalDoesNotRenderKCL: with the env's declaration unchanged
// since the last read, a control plane's 403 is reported without evaluating
// the env's KCL — a render of a large env is tens of seconds, spent before the
// first request and then again by the backend.
func TestObsCloudRefusalDoesNotRenderKCL(t *testing.T) {
	srv, calls := obsCloudForbidden(t)
	dir := obsTargetProject(t, srv.URL)

	// An earlier command's read of the declaration.
	warm, err := resolveObsTarget(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if warm.Facts.ControlPlane != srv.URL {
		t.Fatalf("warm-up target = %+v", warm)
	}
	if _, err := os.Stat(obsTargetPath(dir, "prod")); err != nil {
		t.Fatalf("the target was not stamped: %v", err)
	}
	obsTargetMu.Lock()
	clear(obsTargetLast) // a fresh process
	obsTargetMu.Unlock()

	renders := stubObsRender(t, srv.URL, func() { time.Sleep(5 * time.Second) })
	elapsed, err := runObsIssuesCloud(t)
	if err == nil {
		t.Fatal("a 403 must fail the command")
	}
	var ce *cloud.Error
	if !errors.As(err, &ce) || !ce.HasCode(cloud.CodePermissionDenied) {
		t.Fatalf("error = %v, want the control plane's permission_denied", err)
	}
	if n := renders.Load(); n != 0 {
		t.Errorf("KCL was evaluated %d times before the first request; a stamped target needs none", n)
	}
	if calls.Load() == 0 {
		t.Error("the control plane was never called")
	}
	// The render stub sleeps 5s: finishing well inside that proves the failure
	// did not wait on it, whatever the machine's speed.
	if elapsed > 2*time.Second {
		t.Errorf("a refused read took %s; it must not wait on a render", elapsed)
	}
}

// TestObsTargetRendersOnceThenChangesInvalidate: the first read renders and
// stamps; the next reads the stamp; an edit to deploy/kcl renders again.
func TestObsTargetRendersOnceThenChangesInvalidate(t *testing.T) {
	srv, _ := obsCloudForbidden(t)
	dir := obsTargetProject(t, srv.URL)
	renders := stubObsRender(t, srv.URL, nil)
	fresh := func() {
		obsTargetMu.Lock()
		clear(obsTargetLast)
		obsTargetMu.Unlock()
	}
	read := func() {
		t.Helper()
		fresh()
		if _, err := resolveObsTarget(context.Background(), "prod"); err != nil {
			t.Fatal(err)
		}
	}

	read()
	read()
	if n := renders.Load(); n != 1 {
		t.Fatalf("renders after two reads = %d, want 1", n)
	}

	later := time.Now().Add(time.Minute)
	main := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	if err := os.WriteFile(main, []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(main, later, later); err != nil {
		t.Fatal(err)
	}
	read()
	if n := renders.Load(); n != 2 {
		t.Fatalf("renders after an edit = %d, want 2", n)
	}
	read()
	if n := renders.Load(); n != 2 {
		t.Fatalf("renders after the edited declaration was stamped = %d, want 2", n)
	}
}

// TestObsTargetWithinOneProcessRendersOnce: backend resolution and opening the
// backend both ask for the target; one render serves both even with no stamp.
func TestObsTargetWithinOneProcessRendersOnce(t *testing.T) {
	srv, _ := obsCloudForbidden(t)
	obsTargetProject(t, srv.URL)
	obsTargetMu.Lock()
	clear(obsTargetLast)
	obsTargetMu.Unlock()
	renders := stubObsRender(t, srv.URL, nil)

	if _, err := runObsIssuesCloud(t); err == nil {
		t.Fatal("a 403 must fail the command")
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("a cold obs command rendered %d times, want 1", n)
	}
}

// TestObsTargetUnstampableProjectHasNoFingerprint: an input that cannot be
// stamped disables the cache rather than guessing.
func TestObsTargetUnstampableProjectHasNoFingerprint(t *testing.T) {
	srv, _ := obsCloudForbidden(t)
	dir := obsTargetProject(t, srv.URL)
	if err := os.Remove(filepath.Join(dir, "forge.yaml")); err != nil {
		t.Fatal(err)
	}
	if fp := obsTargetFingerprint(dir, "prod"); fp != "" {
		t.Fatalf("a project without forge.yaml fingerprinted as %q", fp)
	}
}

// TestObsTargetIsNotTrustedPastItsAgeOrWithAPathDependency: a stamp is only as
// good as the inputs it can see.
func TestObsTargetCachePreservesClusterLocation(t *testing.T) {
	dir := obsTargetProject(t, "https://cp.example.com")
	fp := obsTargetFingerprint(dir, "prod")
	facts := obsbackend.Facts{Env: "prod", Declared: true, Enabled: true, HasCluster: true, ControlPlane: "https://cp.example.com"}
	writeObsTarget(dir, "prod", fp, obsTarget{Facts: facts})
	got, ok := readObsTarget(dir, "prod", fp)
	if !ok || got.Facts != facts {
		t.Fatalf("cached facts = %+v, hit %v; want %+v", got.Facts, ok, facts)
	}
	dec, err := obsbackend.Resolve(got.Facts, "")
	if err != nil || dec.Backend != obsbackend.BackendCloud {
		t.Fatalf("cached remote env needs no backend flag: %+v, %v", dec, err)
	}
}

func TestObsTargetIsNotTrustedPastItsAgeOrWithAPathDependency(t *testing.T) {
	srv, _ := obsCloudForbidden(t)
	dir := obsTargetProject(t, srv.URL)
	fp := obsTargetFingerprint(dir, "prod")
	if fp == "" {
		t.Fatal("a plain project must fingerprint")
	}
	target := obsTarget{TokenEnv: "T"}
	target.Facts.ControlPlane = srv.URL
	writeObsTarget(dir, "prod", fp, target)
	if _, ok := readObsTarget(dir, "prod", fp); !ok {
		t.Fatal("a fresh stamp must be read")
	}

	raw, err := os.ReadFile(obsTargetPath(dir, "prod"))
	if err != nil {
		t.Fatal(err)
	}
	old := bytes.Replace(raw, []byte(`"at":"`), []byte(`"at":"2001-01-01T00:00:00Z","was":"`), 1)
	if err := os.WriteFile(obsTargetPath(dir, "prod"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readObsTarget(dir, "prod", fp); ok {
		t.Error("a stamp older than its max age was trusted")
	}

	mod := filepath.Join(dir, "deploy", "kcl", "kcl.mod")
	if err := os.WriteFile(mod, []byte("[dependencies]\nforge = { path = \"../../../forge/kcl\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := obsTargetFingerprint(dir, "prod"); got != "" {
		t.Errorf("a path dependency is KCL the stamp cannot see; fingerprint = %q", got)
	}
	if !kclModHasPathDependency("[dependencies]\nforge = { git = \"x\", path = \"../f\" }\n") ||
		kclModHasPathDependency("[dependencies]\nforge = { git = \"https://x/y\", tag = \"v1\" }\n") {
		t.Error("kclModHasPathDependency misreads kcl.mod")
	}
}
