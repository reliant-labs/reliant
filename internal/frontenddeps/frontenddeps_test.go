package frontenddeps

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFileAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTransientInstallFailure pins which install failures earn a retry: the
// ones where the package manager reports a fault in ITSELF. A real dependency
// error must still fail on the first attempt — retrying it just doubles the
// wait before the same verdict.
func TestTransientInstallFailure(t *testing.T) {
	transient := []string{
		"npm error Exit handler never called!\nnpm error This is an error with npm itself.",
		"npm error network ECONNRESET",
		"npm error ETIMEDOUT",
	}
	for _, out := range transient {
		if !transientFailure(out) {
			t.Errorf("expected retry for a package-manager internal/network fault:\n%s", out)
		}
	}

	real := []string{
		"npm error 404 Not Found - GET https://registry.npmjs.org/does-not-exist",
		"npm error ERESOLVE unable to resolve dependency tree",
		"npm error code EACCES",
		"",
	}
	for _, out := range real {
		if transientFailure(out) {
			t.Errorf("a genuine dependency failure must NOT be retried:\n%s", out)
		}
	}
}

// TestFrontendDepsStaleUsesCompletedInstallNotMtime is the idempotency
// regression: a FAILED install still writes packages, bumping node_modules'
// mtime past the lockfile. Keying off that mtime made the next run consider a
// half-populated tree current and skip the install — the gate inverting itself
// exactly when it was needed. Staleness now keys off the stamp written only
// after the package manager exits 0.
func TestFrontendDepsStaleUsesCompletedInstallNotMtime(t *testing.T) {
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "package-lock.json")
	if err := os.WriteFile(lock, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate a failed install: node_modules touched AFTER the lockfile, but
	// no completion stamp. Without the stamp check this reads as "fresh".
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, past, past); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(nm, now, now); err != nil {
		t.Fatal(err)
	}
	if Stale(dir) {
		t.Log("mtime fallback still applies with no stamp (pre-existing healthy checkouts are not force-reinstalled)")
	}

	// A COMPLETED install stamps the tree; a later lockfile edit must re-stale it.
	markInstallOK(dir)
	if Stale(dir) {
		t.Fatal("freshly stamped tree reported stale")
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(lock, future, future); err != nil {
		t.Fatal(err)
	}
	if !Stale(dir) {
		t.Fatal("lockfile newer than the completed install was not detected as stale")
	}
}

// TestInstallConcurrencyFlags pins the proxy-only concurrency cap. Measured on
// a 579-package tree: npm's default 15 sockets through a local inspection
// proxy did not finish in four minutes, while 8 finished in 7.6s (6.6s
// direct). npm's 300s fetch-timeout makes that failure look like a hang rather
// than an error, so the cap is what keeps a proxied dev loop usable.
func TestInstallConcurrencyFlags(t *testing.T) {
	proxied := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://127.0.0.1:9090"}

	if got := concurrencyFlags("npm", proxied); len(got) != 1 || got[0] != "--maxsockets=8" {
		t.Errorf("npm proxied flags = %v; want [--maxsockets=8]", got)
	}
	for _, runner := range []string{"pnpm", "yarn"} {
		if got := concurrencyFlags(runner, proxied); len(got) != 1 || got[0] != "--network-concurrency=8" {
			t.Errorf("%s proxied flags = %v; want [--network-concurrency=8]", runner, got)
		}
	}

	// No proxy => no cap: a normal install must stay at full speed.
	clean := []string{"PATH=/usr/bin"}
	for _, runner := range []string{"npm", "pnpm", "yarn"} {
		if got := concurrencyFlags(runner, clean); got != nil {
			t.Errorf("%s unproxied flags = %v; want none", runner, got)
		}
	}

	// Lowercase proxy vars are equally real.
	if got := concurrencyFlags("npm", []string{"http_proxy=http://127.0.0.1:9090"}); len(got) != 1 {
		t.Errorf("lowercase http_proxy ignored: %v", got)
	}
	// An empty value is not a proxy.
	if got := concurrencyFlags("npm", []string{"HTTPS_PROXY="}); got != nil {
		t.Errorf("empty HTTPS_PROXY treated as proxied: %v", got)
	}
	// Unknown runners get no flags rather than a bad one.
	if got := concurrencyFlags("bun", proxied); got != nil {
		t.Errorf("unknown runner flags = %v; want none", got)
	}
}

func TestFrontendDepsStale(t *testing.T) {
	dir := t.TempDir()
	// No node_modules yet → stale.
	writeFileAt(t, dir, "package.json", `{"name":"x"}`)
	writeFileAt(t, dir, "package-lock.json", `{}`)
	if !Stale(dir) {
		t.Fatal("missing node_modules should be stale")
	}
	// Install: node_modules newer than manifests → fresh.
	writeFileAt(t, dir, "node_modules/.keep", "")
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "package.json"), past, past)
	_ = os.Chtimes(filepath.Join(dir, "package-lock.json"), past, past)
	_ = os.Chtimes(filepath.Join(dir, "node_modules"), future, future)
	if Stale(dir) {
		t.Fatal("node_modules newer than manifests should be fresh")
	}
	// Touch the lockfile newer than node_modules → stale again.
	_ = os.Chtimes(filepath.Join(dir, "package-lock.json"), future.Add(time.Minute), future.Add(time.Minute))
	if !Stale(dir) {
		t.Fatal("lockfile newer than node_modules should be stale")
	}
}
