package cli

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestGoarchToELFMachine locks the GOARCH→e_machine mapping the guard pins the
// COPYed binary's CPU arch against. amd64/arm64 are the two forge cloud envs
// actually target; a wrong mapping would let a mismatched binary through.
func TestGoarchToELFMachine(t *testing.T) {
	cases := []struct {
		arch  string
		want  elf.Machine
		known bool
	}{
		{"amd64", elf.EM_X86_64, true},
		{"arm64", elf.EM_AARCH64, true},
		{"riscv64", elf.EM_RISCV, true},
		{"sparc-made-up", 0, false},
	}
	for _, c := range cases {
		got, ok := goarchToELFMachine(c.arch)
		if ok != c.known || (c.known && got != c.want) {
			t.Errorf("goarchToELFMachine(%q) = (%v, %v), want (%v, %v)", c.arch, got, ok, c.want, c.known)
		}
	}
}

// TestAssertLinuxELFBinary_RejectsDarwinHostBuild is the regression backstop:
// a native (host GOOS, e.g. darwin) build that skipped GOOS=linux must FAIL the
// guard, with an actionable message — never sail through to a docker COPY and
// CrashLoopBackOff on the node. We build a tiny binary for the HOST (no GOOS
// override); on a non-linux host this is a Mach-O/PE that elf.Open rejects.
func TestAssertLinuxELFBinary_RejectsDarwinHostBuild(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("host is linux: a native host build IS a Linux ELF, so there's no Mach-O to reject here")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "control-plane")
	buildTinyBinary(t, bin, "", "") // native host build → darwin Mach-O on macOS

	err := assertLinuxELFBinary(bin, "amd64", "staging")
	if err == nil {
		t.Fatal("assertLinuxELFBinary accepted a native host (non-ELF) binary; the guard must reject it")
	}
	if !containsAll(err.Error(), "GOOS=linux", "staging", "linux/amd64") {
		t.Errorf("guard error missing actionable remedy: %v", err)
	}
}

// TestAssertLinuxELFBinary_AcceptsCorrectLinuxArch proves a correctly
// cross-compiled GOOS=linux GOARCH=<arch> binary passes for the matching arch
// and FAILS for a mismatched arch (arm64 binary, amd64-targeted image — the
// other half of the `exec format error` class).
func TestAssertLinuxELFBinary_AcceptsCorrectLinuxArch(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	dir := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		bin := filepath.Join(dir, "control-plane-"+arch)
		buildTinyBinary(t, bin, "linux", arch)

		if err := assertLinuxELFBinary(bin, arch, "staging"); err != nil {
			t.Errorf("assertLinuxELFBinary rejected a correct linux/%s binary: %v", arch, err)
		}
		// The same binary must FAIL when the image targets the OTHER arch.
		other := "arm64"
		if arch == "arm64" {
			other = "amd64"
		}
		if err := assertLinuxELFBinary(bin, other, "staging"); err == nil {
			t.Errorf("assertLinuxELFBinary accepted a linux/%s binary for a linux/%s image (cross-arch mismatch must fail)", arch, other)
		}
	}
}

// TestReleasePathBuildsLinuxBinary is the regression test for the reported bug:
// `forge env build --release` (which forces opts.buildDocker=true) must build the
// project-image host binary as GOOS=linux GOARCH=<platform>, NOT a native
// darwin/arm64 host build. It exercises the REAL buildSequential path with
// buildDocker=true + a cluster platform, then asserts the produced binary is a
// Linux ELF of the expected arch — the exact `file bin/control-plane` check the
// release verify step runs.
func TestReleasePathBuildsLinuxBinary(t *testing.T) {
	dir := t.TempDir()
	writeTinyGoModule(t, dir)
	t.Chdir(dir)

	cfg := &config.ProjectConfig{Name: "control-plane"}
	// One go target for the shared project binary, mirroring the KCL-driven
	// project image target (./cmd/control-plane → bin/control-plane).
	goTargets := []goBuildTarget{{cmd: "./cmd/control-plane", outputName: "control-plane"}}

	// --release forces buildDocker=true (see newBuildCmd RunE). staging's
	// rendered cluster platform is amd64; pass it as cfgArchForDocker exactly
	// as runBuild does from kclFirstClusterPlatform.
	opts := buildOptions{
		outputDir:   "bin",
		buildDocker: true,
		release:     "vTEST",
		env:         "staging",
	}
	const cfgArchForDocker = "amd64"

	// No Dockerfile in the temp dir → dockerBuildProject skips the docker
	// build (and thus the guard) gracefully; we assert the BINARY arch, which
	// is what the COPY would ship.
	results := buildSequential(context.Background(), buildPlan{
		cfg:               cfg,
		goTargets:         goTargets,
		skipProjectDocker: false,
		cfgArchForDocker:  cfgArchForDocker,
		resolvedTag:       "vTEST",
		resolvedVersion:   versionInfo{version: "vTEST", commit: "none", date: "now"},
		opts:              opts,
	})
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("build %s failed: %v", r.name, r.err)
		}
	}

	bin := filepath.Join(dir, "bin", "control-plane")
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatalf("--release built a non-ELF binary (the reported bug — likely native darwin/%s): %v", runtime.GOARCH, err)
	}
	defer f.Close()
	if f.Machine != elf.EM_X86_64 {
		t.Errorf("--release built a linux/%s binary, want linux/amd64 (staging platform)", elfMachineGOARCH(f.Machine))
	}
}

// buildTinyBinary compiles a trivial main package to outPath. Empty goos/goarch
// means a native host build (used to produce a non-ELF darwin binary on macOS).
func buildTinyBinary(t *testing.T, outPath, goos, goarch string) {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module tiny\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "CGO_ENABLED=0")
	if goos != "" {
		env = append(env, "GOOS="+goos)
	}
	if goarch != "" {
		env = append(env, "GOARCH="+goarch)
	}
	runGo(t, src, env, "build", "-o", outPath, ".")
}

// writeTinyGoModule scaffolds a minimal module with ./cmd/control-plane so a
// real `go build ./cmd/control-plane` succeeds inside buildSequential.
func writeTinyGoModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module control-plane\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmdDir := filepath.Join(dir, "cmd", "control-plane")
	if err := os.MkdirAll(cmdDir, 0755); err != nil {
		t.Fatal(err)
	}
	main := "package main\n\nvar version, commit, date string\n\nfunc main() { _ = version; _ = commit; _ = date }\n"
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(main), 0644); err != nil {
		t.Fatal(err)
	}
}

func runGo(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "go", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %v failed: %v\n%s", args, err, out)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
