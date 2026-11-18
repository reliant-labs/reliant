// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"archive/zip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

const sdkModule = "github.com/reliant-labs/reliant/sdk/go"

// TestSDKReleasePinDropsTheReplace runs scripts/sdk-release-pin.sh, which the
// RC train runs on every reliant release commit (reliant#2824), against a
// fixture checkout and an offline module proxy. main's go.mod replaces the SDK
// with ./sdk/go; the release commit must not, because `go install
// <module>@<version>` refuses a module with a replace, and a consumer resolves
// the SDK by the require line alone.
func TestSDKReleasePinDropsTheReplace(t *testing.T) {
	testkit.Slow(t, "runs scripts/sdk-release-pin.sh: git, and go get / go mod tidy against a file proxy",
		testkit.Inputs("scripts/sdk-release-pin.sh"))
	script := filepath.Join(repoRoot(t), "scripts", "sdk-release-pin.sh")

	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const oldVersion = "v0.0.0-20200101000000-aaaaaaaaaaaa"
	sdkMod := "module " + sdkModule + "\n\ngo 1.22\n"
	sdkSrc := "package sdk\n\nconst Name = \"sdk\"\n"
	write("go.mod", "module github.com/reliant-labs/reliant\n\ngo 1.22\n\nrequire "+sdkModule+" "+oldVersion+"\n\n"+
		"// The app builds against this tree's SDK (reliant#2824).\n"+
		"replace "+sdkModule+" => ./sdk/go\n")
	write("main.go", "package main\n\nimport sdk \""+sdkModule+"\"\n\nfunc main() { println(sdk.Name) }\n")
	write("sdk/go/go.mod", sdkMod)
	write("sdk/go/sdk.go", sdkSrc)

	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_COMMITTER_DATE=2026-11-18T10:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	newVersion := "v0.0.0-20261118100000-" + base[:12]

	// The proxy serves the SDK at the base commit (by its sha, as `go get @sha`
	// asks) and the old version the require line names.
	proxy := t.TempDir()
	at := filepath.Join(proxy, sdkModule, "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	info := func(name, version string) {
		b, _ := json.Marshal(map[string]any{"Version": version, "Time": time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)})
		if err := os.WriteFile(filepath.Join(at, name+".info"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{oldVersion, newVersion} {
		info(v, v)
		if err := os.WriteFile(filepath.Join(at, v+".mod"), []byte(sdkMod), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(at, v+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for name, body := range map[string]string{"go.mod": sdkMod, "sdk.go": sdkSrc} {
			w, err := zw.Create(sdkModule + "@" + v + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	info(base, newVersion)

	run := func() (string, error) {
		cmd := exec.Command("bash", script, base)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GOPROXY=file://"+filepath.ToSlash(proxy), "GOSUMDB=off", "GONOSUMDB=", "GOPRIVATE=", "GONOPROXY=",
			"GOFLAGS=-modcacherw", "GOMODCACHE="+t.TempDir(), "GOTOOLCHAIN=local", "GOWORK=off")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run()
	if err != nil {
		t.Fatalf("sdk-release-pin.sh: %v\n%s", err, out)
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "=>") {
		t.Errorf("go.mod still has a replace:\n%s", mod)
	}
	if strings.Contains(string(mod), "reliant#2824") {
		t.Errorf("go.mod kept the replace's comment:\n%s", mod)
	}
	if !strings.Contains(string(mod), sdkModule+" "+newVersion) {
		t.Errorf("go.mod does not require %s %s:\n%s", sdkModule, newVersion, mod)
	}
	// The release gate runs TestSDKModuleIsSelfContained on this commit.
	for _, p := range rootGoModSDKProblems(mod) {
		t.Errorf("the release commit's go.mod fails the SDK check: %s\n%s", p, mod)
	}
	if sum, err := os.ReadFile(filepath.Join(dir, "go.sum")); err != nil || !strings.Contains(string(sum), sdkModule+" "+newVersion+" h1:") {
		t.Errorf("go.sum has no hash for %s %s (%v):\n%s", sdkModule, newVersion, err, sum)
	}

	// A second run finds no replace and changes nothing.
	if out, err := run(); err != nil || !strings.Contains(out, "nothing to do") {
		t.Errorf("second run: err=%v\n%s", err, out)
	}

	// An SDK the tree changed after the base commit is refused: the release
	// would require a different SDK than the one it was built against.
	git("checkout", "-q", "--", ".")
	if err := os.Remove(filepath.Join(dir, "go.sum")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	write("sdk/go/sdk.go", sdkSrc+"\nconst Extra = 1\n")
	if out, err := run(); err == nil || !strings.Contains(out, "differs from") {
		t.Errorf("an SDK edited after %s was pinned anyway: err=%v\n%s", base[:12], err, out)
	}
}
