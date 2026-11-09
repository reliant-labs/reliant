//go:build e2e

package kcleval_test

// kcleval_e2e_test.go drives the REAL forge binary against a real project, in
// the shape a consumer's test would.
//
// It is tagged e2e because it builds forge (tens of seconds) and then
// evaluates KCL in a subprocess. That is too slow for the inner loop and it is
// exactly what must be checked: the value of this package is that it works
// through an installed forge, so a test that stubbed the subprocess would
// verify nothing about the one property it exists for.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
)

var (
	buildOnce sync.Once
	forgeBin  string
	buildErr  error
	// buildDir is the temp directory holding the built binary. Package-level
	// so TestMain can remove it; see TestMain for why t.Cleanup cannot.
	buildDir string
)

// TestMain removes the directory holding the forge binary this suite builds.
//
// The binary is ~138 MB and is built once per test binary through buildOnce,
// which is exactly why t.Cleanup cannot own it: the first test to finish would
// delete the binary every later test still executes. Nothing else could, so it
// leaked one copy per run — two were on disk (0.28 GB) when this was found.
//
// Kept on failure, with the path printed: a failing render is diagnosed by
// running that binary by hand, and deleting it would leave a message about an
// executable that no longer exists.
//
// This file is the only test file in the package, so the TestMain lives under
// the same `//go:build e2e` tag as the fixture it cleans up — there is no
// untagged test binary for it to conflict with.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		if code == 0 {
			if err := os.RemoveAll(buildDir); err != nil {
				fmt.Fprintf(os.Stderr, "kcleval: remove %s: %v\n", buildDir, err)
				code = 1
			}
		} else {
			fmt.Fprintf(os.Stderr, "kcleval: kept the forge binary at %s for diagnosis\n", buildDir)
		}
	}
	os.Exit(code)
}

// forgeBinary builds the forge CLI from this checkout once per test binary.
func forgeBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "forge-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		buildDir = dir
		out := filepath.Join(dir, "forge")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/forge")
		cmd.Dir = repoRoot(t)
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			t.Logf("build forge: %s", b)
			return
		}
		forgeBin = out
	})
	if buildErr != nil {
		t.Fatalf("build the forge binary under test: %v", buildErr)
	}
	return forgeBin
}

// repoRoot walks up to the directory holding go.mod for the forge module.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "cmd", "forge")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find the forge repo root")
		}
		dir = parent
	}
}

// fixtureProject writes a minimal forge project whose KCL declares the shapes
// a real project's library files have: nested constants, a list, and a record
// keyed by env.
func fixtureProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"forge.yaml":         "name: kcleval-fixture\nmodule: example.com/kcleval\n",
		"deploy/kcl/kcl.mod": "[package]\nname = \"kcleval_fixture\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/lib/pool.k": `import forge

sbd = {
    image_family = "workspace-sbd"
    disk_size_gb = 200
}
pool_defaults = {
    node_label_key = "reliant.dev/pool"
}
daemon_placement = {
    prod = {client_id = "prod-daemon", context = "gke_prod-daemon-v2"}
    staging = {client_id = "staging-daemon", context = "vke-staging"}
}
tiers = ["small", "large"]
_forge_present = forge.Bundle is not None
`,
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSelectThroughTheRealBinary: the Go API reads a string, an int, a record
// and a list out of a project's KCL, with no `kcl` on PATH.
func TestSelectThroughTheRealBinary(t *testing.T) {
	bin := forgeBinary(t)
	dir := fixtureProject(t)
	opts := kcleval.Options{ProjectDir: dir, Binary: bin}
	ctx := context.Background()

	var family string
	if err := kcleval.Select(ctx, "deploy/kcl/lib/pool.k", "sbd.image_family", &family, opts); err != nil {
		t.Fatal(err)
	}
	if family != "workspace-sbd" {
		t.Errorf("sbd.image_family = %q, want workspace-sbd", family)
	}

	// An int must decode as an int. KCL's 200 arrives as JSON 200, and a
	// caller passing it to a size flag needs 200 rather than 2e+02.
	var size int
	if err := kcleval.Select(ctx, "deploy/kcl/lib/pool.k", "sbd.disk_size_gb", &size, opts); err != nil {
		t.Fatal(err)
	}
	if size != 200 {
		t.Errorf("sbd.disk_size_gb = %d, want 200", size)
	}

	// The daemon_placement shape: a record keyed by env, decoded into a typed
	// map. This is the selection control-plane's placement test needs.
	var placement map[string]struct {
		ClientID string `json:"client_id"`
		Context  string `json:"context"`
	}
	if err := kcleval.Select(ctx, "deploy/kcl/lib/pool.k", "daemon_placement", &placement, opts); err != nil {
		t.Fatal(err)
	}
	if got := placement["prod"].Context; got != "gke_prod-daemon-v2" {
		t.Errorf("daemon_placement.prod.context = %q", got)
	}
	if got := placement["staging"].ClientID; got != "staging-daemon" {
		t.Errorf("daemon_placement.staging.client_id = %q", got)
	}

	// A list decodes as ONE list — the case kcl's own -S renders as one YAML
	// document per element.
	var tiers []string
	if err := kcleval.Select(ctx, "deploy/kcl/lib/pool.k", "tiers", &tiers, opts); err != nil {
		t.Fatal(err)
	}
	if len(tiers) != 2 || tiers[0] != "small" {
		t.Errorf("tiers = %#v, want [small large]", tiers)
	}
}

// TestSelectWorksFromASubdirectoryCwd: a test runs in its own package
// directory, never at the project root, so this is the normal case rather than
// an edge one.
func TestSelectWorksFromASubdirectoryCwd(t *testing.T) {
	bin := forgeBinary(t)
	dir := fixtureProject(t)
	sub := filepath.Join(dir, "internal", "operators", "shared")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	// No ProjectDir: forge walks up from the cwd to the forge.yaml, which is
	// what a test in a package directory relies on.
	var key string
	err := kcleval.Select(context.Background(), "deploy/kcl/lib/pool.k",
		"pool_defaults.node_label_key", &key, kcleval.Options{Binary: bin})
	if err != nil {
		t.Fatalf("Select from a subdirectory cwd: %v", err)
	}
	if key != "reliant.dev/pool" {
		t.Errorf("pool_defaults.node_label_key = %q", key)
	}
}

// TestSelectSurfacesAMissingFieldAsAnError: the property that makes such a
// test trustworthy. A selector typo must fail, not decode as a zero value that
// an assertion would read as a legitimately empty declaration.
func TestSelectSurfacesAMissingFieldAsAnError(t *testing.T) {
	bin := forgeBinary(t)
	dir := fixtureProject(t)

	var s string
	err := kcleval.Select(context.Background(), "deploy/kcl/lib/pool.k",
		"pool_defaults.node_label_ky", &s, kcleval.Options{ProjectDir: dir, Binary: bin})
	if err == nil {
		t.Fatalf("a typo'd selector returned %q with no error", s)
	}
	// forge's message names the fields that WERE available; the wrapper must
	// pass it through rather than replacing it with its own summary.
	if !strings.Contains(err.Error(), "node_label_key") {
		t.Errorf("error does not name the available field:\n%v", err)
	}
}

// TestMissingBinaryIsTyped: a test can skip on a missing forge while CI, which
// installs it, treats the same condition as a failure.
func TestMissingBinaryIsTyped(t *testing.T) {
	var s string
	err := kcleval.Select(context.Background(), "x.k", "y", &s,
		kcleval.Options{Binary: "forge-that-does-not-exist"})
	if err == nil {
		t.Fatal("a missing binary succeeded")
	}
	if !errors.Is(err, kcleval.ErrForgeNotFound) {
		t.Errorf("error = %v; want ErrForgeNotFound so a caller can skip on it", err)
	}
}
