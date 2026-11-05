package generator

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The web-runtime reconcile raises a frontend's range in package.json. The
// lockfile records that range too, so unless it follows, `npm ci` fails with
// "package.json and package-lock.json are not in sync". These tests pin the
// follow-up: a refresh when the lockfile disagrees, nothing when it agrees,
// and a loud error naming the command when the refresh cannot run.

func lockWithRuntimeRange(spec string) string {
	return `{"lockfileVersion":3,"packages":{"":{"name":"web","version":"0.1.0","dependencies":{"` +
		WebRuntimePackage + `":"` + spec + `"}}}}`
}

// fakeNPMOnPath installs an `npm` that logs "<cwd> <args>" and then runs body.
func fakeNPMOnPath(t *testing.T, body string) (calls func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake npm")
	}
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$PWD $*\" >> '" + logPath + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(logPath)
		return string(b)
	}
}

// staleFrontend is a frontend whose manifest declares the web-runtime below
// forge's floor, with a lockfile recording that same stale range.
func staleFrontend(t *testing.T) (projectDir, feRel, lockPath string) {
	t.Helper()
	pinDevBuild(t, false, fakeForgeCheckout(t, filepath.Join(t.TempDir(), "forge")))
	projectDir = t.TempDir()
	manifest := writeFrontendManifest(t, projectDir, "web", "\n    \""+WebRuntimePackage+"\": \"0.1.0\"")
	lockPath = filepath.Join(filepath.Dir(manifest), "package-lock.json")
	if err := os.WriteFile(lockPath, []byte(lockWithRuntimeRange("0.1.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir, filepath.Join("frontends", "web"), lockPath
}

func TestSyncFrontendLock_RefreshesLockAfterFloorBump(t *testing.T) {
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	projectDir, feRel, lockPath := staleFrontend(t)
	// The fake package manager does what the real one would: records the
	// manifest's range in the lockfile.
	calls := fakeNPMOnPath(t, `sed 's/"0.1.0"/"`+webRuntimePublishedRange+`"/g' package-lock.json > package-lock.json.new && mv package-lock.json.new package-lock.json`)

	if !EnsureWebRuntimeDependency(projectDir, feRel, "web") {
		t.Fatal("a manifest below the floor must be reported as rewritten")
	}
	if err := SyncFrontendLock(projectDir, feRel); err != nil {
		t.Fatalf("SyncFrontendLock: %v", err)
	}

	got := calls()
	for _, want := range []string{"install", "--package-lock-only", "--ignore-scripts", "--prefix"} {
		if !strings.Contains(got, want) {
			t.Errorf("package manager invocation %q missing %q", got, want)
		}
	}
	lock, _ := os.ReadFile(lockPath)
	if !strings.Contains(string(lock), `"`+webRuntimePublishedRange+`"`) {
		t.Errorf("lockfile still records the old range:\n%s", lock)
	}

	// Converged: the lockfile agrees now, so a second run touches nothing.
	before := calls()
	if err := SyncFrontendLock(projectDir, feRel); err != nil {
		t.Fatal(err)
	}
	if calls() != before {
		t.Error("an agreeing lockfile must not invoke the package manager again")
	}
}

func TestSyncFrontendLock_LeavesAgreeingAndAbsentLocksAlone(t *testing.T) {
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	calls := fakeNPMOnPath(t, "exit 0")

	// No lockfile: nothing to refresh.
	projectDir := t.TempDir()
	writeFrontendManifest(t, projectDir, "web", "\n    \""+WebRuntimePackage+"\": \""+webRuntimePublishedRange+"\"")
	if err := SyncFrontendLock(projectDir, filepath.Join("frontends", "web")); err != nil {
		t.Fatal(err)
	}

	// A lockfile that agrees about the managed package, though it is stale
	// about an unmanaged one: forge does not own that, so it stays hands-off.
	lock := filepath.Join(projectDir, "frontends", "web", "package-lock.json")
	body := `{"lockfileVersion":3,"packages":{"":{"dependencies":{"` + WebRuntimePackage + `":"` + webRuntimePublishedRange + `","left-pad":"1.0.0"}}}}`
	if err := os.WriteFile(lock, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SyncFrontendLock(projectDir, filepath.Join("frontends", "web")); err != nil {
		t.Fatal(err)
	}
	if got := calls(); got != "" {
		t.Errorf("package manager must not run, ran: %q", got)
	}
}

func TestSyncFrontendLock_FailureNamesFrontendAndCommand(t *testing.T) {
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	projectDir, feRel, _ := staleFrontend(t)
	fakeNPMOnPath(t, "echo 'npm error code ENOTFOUND registry.npmjs.org' >&2; exit 1")
	EnsureWebRuntimeDependency(projectDir, feRel, "web")

	err := SyncFrontendLock(projectDir, feRel)
	if err == nil {
		t.Fatal("an unreachable registry must fail the step, not leave the lockfile silently stale")
	}
	for _, want := range []string{feRel, "package-lock.json", "npm install --package-lock-only", "ENOTFOUND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}
}

func TestSyncFrontendLock_SkipEnvPrintsCommandInsteadOfRunning(t *testing.T) {
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	projectDir, feRel, _ := staleFrontend(t)
	calls := fakeNPMOnPath(t, "exit 0")
	EnsureWebRuntimeDependency(projectDir, feRel, "web")

	if err := SyncFrontendLock(projectDir, feRel); err != nil {
		t.Fatal(err)
	}
	if got := calls(); got != "" {
		t.Errorf("FORGE_SKIP_NPM_INSTALL must not run a package manager, ran: %q", got)
	}
}

func TestEnsureWebRuntimeDependency_ReportsWhetherItRewrote(t *testing.T) {
	pinDevBuild(t, false, fakeForgeCheckout(t, filepath.Join(t.TempDir(), "forge")))
	projectDir := t.TempDir()
	writeFrontendManifest(t, projectDir, "web", "\n    \""+WebRuntimePackage+"\": \"0.1.0\"")
	fe := filepath.Join("frontends", "web")
	if !EnsureWebRuntimeDependency(projectDir, fe, "web") {
		t.Error("first run raises the range and must say so")
	}
	if EnsureWebRuntimeDependency(projectDir, fe, "web") {
		t.Error("second run changes nothing and must say so")
	}
}
