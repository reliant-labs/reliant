package installers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildFixtureAssets runs scripts/build-release-assets.sh against a tiny
// stand-in module (same cmd/forge ldflag targets as the real one) so the test
// exercises the real script without compiling forge itself.
func buildFixtureAssets(t *testing.T, version string, targets string) string {
	t.Helper()
	out, b, err := runFixtureAssets(t, version, targets)
	if err != nil {
		t.Fatalf("build-release-assets.sh: %v\n%s", err, b)
	}
	return out
}

// runFixtureAssets is buildFixtureAssets returning the script's result; extra
// is prepended to the script's environment.
func runFixtureAssets(t *testing.T, version string, targets string, extra ...string) (string, []byte, error) {
	t.Helper()
	for _, bin := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	script := repoFile(t, "scripts/build-release-assets.sh")
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/forgefixture\n\ngo 1.21\n")
	write("cmd/forge/main.go", `package main

import "fmt"

var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

func main() { fmt.Printf("forge version %s (commit %s, built %s)\n", Version, GitCommit, BuildDate) }
`)
	write("LICENSE", "license\n")
	write("NOTICE", "notice\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@example.com", "-c", "user.name=t", "add", "."}, {"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "x"}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	out := filepath.Join(t.TempDir(), "dist")
	cmd := exec.CommandContext(context.Background(), "bash", script, version, out)
	cmd.Dir = root
	cmd.Env = append(append(os.Environ(), "FORGE_RELEASE_TARGETS="+targets, "FORGE_RELEASE_DATE=2026-01-02"), extra...)
	b, err := cmd.CombinedOutput()
	return out, b, err
}

// A build step that writes its output and then fails must fail the release:
// build_one runs as the left operand of `&&`, where bash suspends `set -e`, so
// only explicit propagation stops a failed go build whose tar still succeeds
// from being certified and checksummed.
func TestReleaseAssets_AFailedBuildStepFailsTheRelease(t *testing.T) {
	shim := t.TempDir()
	// go: write the -o output, then exit non-zero (a build tool that leaves an
	// executable behind and still fails).
	goShim := "#!/usr/bin/env bash\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && { printf x >\"$2\"; shift; }; shift; done\necho 'go build: link failed' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(shim, "go"), []byte(goShim), 0o755); err != nil {
		t.Fatal(err)
	}
	out, b, err := runFixtureAssets(t, "v1.2.3", "linux/amd64", "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil {
		t.Fatalf("a failed go build was accepted:\n%s", b)
	}
	if !strings.Contains(string(b), "link failed") {
		t.Errorf("the failure does not show the build's log:\n%s", b)
	}
	if _, serr := os.Stat(filepath.Join(out, "checksums.txt")); serr == nil {
		t.Errorf("checksums.txt was written for a failed build")
	}
}

func tarMembers(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = b
	}
}

// The whole chain on real artifacts: build the archives, serve them as a
// release, run install.sh against them, and execute what it installed.
func TestReleaseAssets_BuiltArchivesInstallAndRun(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles a fixture module in go build subprocesses the test cache cannot see; runs in task test")
	}
	requireUnix(t)
	host := runtime.GOOS + "/" + runtime.GOARCH
	dist := buildFixtureAssets(t, "v1.2.3", host+" windows/arm64 linux/arm64")

	for _, want := range []string{"forge_windows_arm64.tar.gz", "forge_linux_arm64.tar.gz", hostAsset(), "checksums.txt"} {
		if _, err := os.Stat(filepath.Join(dist, want)); err != nil {
			t.Errorf("missing release asset %s: %v", want, err)
		}
	}
	win := tarMembers(t, filepath.Join(dist, "forge_windows_arm64.tar.gz"))
	for _, name := range []string{"forge.exe", "LICENSE", "NOTICE"} {
		if _, ok := win[name]; !ok {
			t.Errorf("windows archive lacks %s (has %v)", name, keys(win))
		}
	}
	if _, ok := win["forge"]; ok {
		t.Errorf("windows archive must hold forge.exe, not forge")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /download/v1.2.3/<file> and /latest/download/<file> both map to dist/<file>.
		http.ServeFile(w, r, filepath.Join(dist, filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)

	for _, pin := range []string{"", "v1.2.3"} {
		rel := &release{srv: srv}
		var env []string
		if pin != "" {
			env = append(env, "FORGE_VERSION="+pin)
		}
		res := runInstall(t, "sh", rel, "", env...)
		if res.err != nil {
			t.Fatalf("install (pin=%q) failed: %v\n%s", pin, res.err, res.out)
		}
		for _, want := range []string{"forge version v1.2.3", "commit ", "built 2026-01-02"} {
			if !strings.Contains(res.out, want) {
				t.Errorf("installed binary output lacks %q:\n%s", want, res.out)
			}
		}
	}
}

func keys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestRenderHomebrewFormula(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := repoFile(t, "scripts/render-homebrew-formula.sh")
	sum := func(c string) string { return strings.Repeat(c, 64) }
	sums := filepath.Join(t.TempDir(), "checksums.txt")
	body := sum("1") + "  forge_darwin_arm64.tar.gz\n" +
		sum("2") + "  forge_darwin_amd64.tar.gz\n" +
		sum("3") + "  forge_linux_arm64.tar.gz\n" +
		sum("4") + "  forge_linux_amd64.tar.gz\n" +
		sum("5") + "  forge_windows_amd64.tar.gz\n"
	if err := os.WriteFile(sums, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(context.Background(), "bash", script, "v1.2.3", sums).CombinedOutput()
	if err != nil {
		t.Fatalf("render failed: %v\n%s", err, out)
	}
	f := string(out)
	for _, want := range []string{
		`class Forge < Formula`,
		`releases/download/v1.2.3/forge_darwin_arm64.tar.gz"` + "\n      sha256 \"" + sum("1") + `"`,
		`releases/download/v1.2.3/forge_darwin_amd64.tar.gz"` + "\n      sha256 \"" + sum("2") + `"`,
		`releases/download/v1.2.3/forge_linux_arm64.tar.gz"` + "\n      sha256 \"" + sum("3") + `"`,
		`releases/download/v1.2.3/forge_linux_amd64.tar.gz"` + "\n      sha256 \"" + sum("4") + `"`,
		`bin.install "forge"`,
	} {
		if !strings.Contains(f, want) {
			t.Errorf("formula lacks %q:\n%s", want, f)
		}
	}
	if strings.Contains(f, sum("5")) {
		t.Errorf("formula leaked the windows checksum")
	}
	// Homebrew scans the version from the URL; `brew audit --strict` fails a
	// formula whose `version` stanza repeats it.
	if strings.Contains(f, "\n  version ") {
		t.Errorf("formula declares a redundant version stanza:\n%s", f)
	}

	// A prerelease, or a checksums file missing a platform, must fail loudly
	// rather than render a formula that cannot install.
	if out, err := exec.CommandContext(context.Background(), "bash", script, "v1.2.3-rc.1", sums).CombinedOutput(); err == nil {
		t.Errorf("a prerelease rendered a formula:\n%s", out)
	}
	partial := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(partial, []byte(sum("1")+"  forge_darwin_arm64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(context.Background(), "bash", script, "1.2.3", partial).CombinedOutput(); err == nil || !strings.Contains(string(out), "forge_darwin_amd64.tar.gz") {
		t.Errorf("a partial checksums.txt must fail naming the missing asset, got err=%v\n%s", err, out)
	}
}

// The shim's unit tests are plain node:test, so npm/ needs no dev dependencies.
func TestNpmShim(t *testing.T) {
	if testing.Short() {
		t.Skip("execs node over files the test cache cannot see; runs in task test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	// Read the sources here so a cached PASS is invalidated by an edit.
	for _, f := range []string{"npm/bin/forge.js", "npm/lib/install.js", "npm/package.json", "npm/test/install.test.js"} {
		repoFile(t, f)
	}
	cmd := exec.CommandContext(context.Background(), node, "--test", repoFile(t, "npm/test/install.test.js"))
	cmd.Dir = filepath.Dir(repoFile(t, "npm/package.json"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm shim tests failed: %v\n%s", err, out)
	}
}
