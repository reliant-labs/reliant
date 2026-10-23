package forge_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCrossPlatformBuild guards forge's PORTABILITY.
//
// Why this exists: forge used Unix-only syscalls in three packages without
// build tags — syscall.Flock in pkg/pgtest, and syscall.Kill / SysProcAttr's
// Setsid in internal/hostinfra and internal/debug. Nothing caught it, because
// every developer machine and every CI runner that builds forge is unix.
//
// It surfaced somewhere far away and expensive: a DOWNSTREAM project
// (reliant) that depends on forge could not cross-compile its own Windows
// release binaries, and the failure read as a problem with that project's
// release pipeline rather than with forge. The cost of the bug was paid three
// times — once per release-candidate round trip through CI.
//
// A compile is the cheapest possible check for this class of defect: a
// `go build` for another GOOS needs no toolchain beyond the one already
// present, and it fails with the exact undefined symbol. It runs in `task test`
// and CI, but NOT under -short, for two reasons:
//
//   - Its inputs are invisible to Go's test cache. The build reads every
//     package in the module from a `go build` SUBPROCESS, and the cache only
//     tracks what the test process itself opens — this package imports
//     nothing from forge, so its test binary never changes either. Under the
//     cached inner loop (`task test:short`, no -count=1) an edit that breaks
//     the windows build would replay the previous PASS. A guard that can be
//     stale is worse than one that runs at the end, where -count=1 forces it.
//   - It is two whole-module builds: 30-50s cold, measured on a loaded host.
//
// Windows is the target that matters here because it is the one whose syscall
// surface genuinely differs. darwin/linux are covered by everyone's normal
// build.
//
// SCOPE. `./...` reaches pkg/* since the single-module collapse — it used to
// stop at the root module's boundary, so forge's runtime libraries were never
// actually covered by the guard they exist to protect downstream projects
// with. Widening it surfaced one package that cannot pass and cannot be
// fixed here; see crossPlatformExempt.
func TestCrossPlatformBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles the whole module for windows in a go build subprocess the test cache cannot see; runs in task test")
	}
	t.Parallel()

	for _, target := range []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"windows", "arm64"},
	} {
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			t.Parallel()

			// No build tags. forge talks to dlv through its own JSON-RPC
			// client (internal/debug/rpcclient.go) and links none of delve's
			// debugger backend, so windows/arm64 builds as plainly as every
			// other target. Passing delve's `exp.winarm64` tag here would
			// MASK a regression that re-links service/debugger: the build
			// would stay green on a configuration no plain `go install` gets.
			args := append([]string{"build"}, crossPlatformTargets(t)...)
			cmd := exec.Command("go", args...)
			cmd.Env = append(cmd.Environ(),
				"GOOS="+target.goos,
				"GOARCH="+target.goarch,
				// The build cache is per-target, so this neither pollutes nor
				// is polluted by the host-arch build.
				"CGO_ENABLED=0",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("forge must cross-compile for %s/%s — a downstream project cannot ship a %s binary otherwise.\n"+
					"Guard the platform-specific call behind a build tag (see pkg/pgtest/pool_lock_%s.go for the pattern).\n\n%s",
					target.goos, target.goarch, target.goos, target.goos, strings.TrimSpace(string(out)))
			}
		})
	}
}

// crossPlatformExempt lists packages excluded from the windows build, each
// with the reason. An exemption is a claim that NOTHING shippable imports the
// package, so keep it to test harnesses and keep the list short.
var crossPlatformExempt = map[string]string{
	// Empty. pkg/controller/controllertest used to be exempt (controller-
	// runtime's envtest does not compile for windows through v0.25.2); it now
	// ships envtest_windows.go, which keeps its exported API and skips.
}

// crossPlatformTargets expands ./... minus the exemptions, so the guard keeps
// covering every OTHER package instead of being switched off wholesale.
func crossPlatformTargets(t *testing.T) []string {
	t.Helper()
	// GoFiles is the filter that `./...` applied implicitly: naming a
	// test-only package on the command line makes `go build` fail with "no
	// non-test Go files", which has nothing to do with portability.
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{len .GoFiles}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	var pkgs []string
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, goFiles, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || path == "" {
			continue
		}
		listed[path] = true
		if goFiles == "0" {
			continue
		}
		if _, skip := crossPlatformExempt[path]; skip {
			continue
		}
		pkgs = append(pkgs, path)
	}
	if len(pkgs) == 0 {
		t.Fatal("no packages to cross-compile — the exemption list or `go list` is wrong, " +
			"and an empty build would pass while checking nothing")
	}
	// Every exemption must name a package that still exists, or it is silently
	// widening the guard's blind spot.
	for p := range crossPlatformExempt {
		if !listed[p] {
			t.Errorf("crossPlatformExempt names %q, which is not a package in this module — "+
				"delete the stale entry rather than leaving an exemption that matches nothing", p)
		}
	}
	return pkgs
}
