package pathshadow

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

const realOutput = "forge version v1.7.0 (commit 0123456789ab, built 2026-10-01)\n" +
	"  forge build:    v1.7.0\n" +
	"  commit:         0123456789ab\n" +
	"  invoked as:     forge (standalone binary)\n"

func TestParseVersionOutput(t *testing.T) {
	for _, tc := range []struct {
		name, out, want string
		ok              bool
	}{
		{"reliant forge", realOutput, "v1.7.0", true},
		{"pseudo-version", "forge version v1.11.0-0.20261120-abcdef+dirty\n  forge build:    x\n", "v1.11.0-0.20261120-abcdef+dirty", true},
		{"laravel forge", "Laravel Forge CLI 1.3.0\n\nUsage:\n", "", false},
		{"foundry forge", "forge Version: 1.0.0-stable\nCommit SHA: abc\n", "", false},
		{"no identity block", "forge version v1.7.0\n", "", false},
		{"unversioned dev build", "forge version dev (unknown build)\n  forge build:    dev\n", "", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseVersionOutput([]byte(tc.out))
			if got != tc.want || ok != tc.ok {
				t.Errorf("ParseVersionOutput(%q) = %q, %v; want %q, %v", tc.out, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// writeExe writes an executable file and returns its path.
func writeExe(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePackage(t *testing.T, root, name, version string) {
	t.Helper()
	writeExe(t, filepath.Join(root, "bin", "forge.js"), "#!/usr/bin/env node\n")
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"`+name+`","version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fakeProbe struct {
	mu    sync.Mutex
	out   map[string]string
	calls []string
}

func (f *fakeProbe) probe(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
	out, ok := f.out[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(out), nil
}

// TestScanFindsEveryReliantForgeOnPath is the evaluator's machine: a stale
// `go install` forge first on PATH, the npm shim of the current release
// after it, and the running binary. The npm shim is never run (its first run
// downloads a binary), a program named forge that is not Reliant Forge is
// ignored, and a directory listed twice is reported once.
func TestScanFindsEveryReliantForgeOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix PATH layout: npm links bin/forge to the package's forge.js")
	}
	tmp := t.TempDir()
	goBin := filepath.Join(tmp, "go", "bin")
	stale := writeExe(t, filepath.Join(goBin, "forge"), "elf")

	npmBin := filepath.Join(tmp, "npm", "bin")
	pkg := filepath.Join(tmp, "npm", "lib", "node_modules", "@reliantlabs", "forge")
	writePackage(t, pkg, "@reliantlabs/forge", "1.10.0")
	if err := os.MkdirAll(npmBin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(npmBin, "forge")
	if err := os.Symlink(filepath.Join(pkg, "bin", "forge.js"), shim); err != nil {
		t.Fatal(err)
	}

	laravel := writeExe(t, filepath.Join(tmp, "laravel", "forge"), "php")
	self := writeExe(t, filepath.Join(tmp, "self", "forge"), "elf")
	writeExe(t, filepath.Join(tmp, "notexec", "forge"), "x")
	if err := os.Chmod(filepath.Join(tmp, "notexec", "forge"), 0o644); err != nil {
		t.Fatal(err)
	}

	probe := &fakeProbe{out: map[string]string{
		stale:   realOutput,
		laravel: "Laravel Forge CLI 1.3.0\n",
	}}
	s := Scanner{
		PathEnv: strings.Join([]string{goBin, npmBin, filepath.Join(tmp, "laravel"), goBin, filepath.Join(tmp, "notexec"), filepath.Join(tmp, "self"), filepath.Join(tmp, "empty")}, string(os.PathListSeparator)),
		GOOS:    runtime.GOOS,
		Self:    self,
		Probe:   probe.probe,
	}
	rep := s.Scan(context.Background(), "v1.10.0")

	want := []Binary{
		{Path: stale, Version: "v1.7.0"},
		{Path: shim, Version: "v1.10.0", NPM: true},
		{Path: self, Version: "v1.10.0", Self: true},
	}
	if len(rep.Binaries) != len(want) {
		t.Fatalf("Scan found %+v, want %+v", rep.Binaries, want)
	}
	for i := range want {
		if rep.Binaries[i] != want[i] {
			t.Errorf("binary %d = %+v, want %+v", i, rep.Binaries[i], want[i])
		}
	}
	for _, c := range probe.calls {
		if c == shim || c == self {
			t.Errorf("Scan ran %s; the npm shim and the running forge are never run", c)
		}
	}
	if !rep.Mismatched() {
		t.Error("Mismatched() = false with a v1.7.0 forge first on PATH")
	}
}

func TestScanAlreadyCanceledDoesNotProbe(t *testing.T) {
	path := writeExe(t, filepath.Join(t.TempDir(), "forge.exe"), "elf")
	probe := &fakeProbe{out: map[string]string{path: realOutput}}
	s := Scanner{PathEnv: filepath.Dir(path), GOOS: "windows", Probe: probe.probe}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := s.Scan(ctx, "v1.10.0")
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.calls) != 0 || len(rep.Binaries) != 0 || rep.Running != "v1.10.0" {
		t.Fatalf("canceled scan = %+v, probe calls = %v", rep, probe.calls)
	}
}

func TestScanCancellationReturnsBeforeBlockedProbe(t *testing.T) {
	// Windows does not preserve Unix executable bits. Use its candidate names
	// and discovery rules on every host; the injected probe never runs the file.
	path := writeExe(t, filepath.Join(t.TempDir(), "forge.exe"), "elf")
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		defer close(release)
		s := Scanner{PathEnv: filepath.Dir(path), GOOS: "windows", Probe: func(context.Context, string) ([]byte, error) {
			close(started)
			<-release // Deliberately ignore cancellation.
			defer close(finished)
			return []byte(realOutput), nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan Report, 1)
		go func() { done <- s.Scan(ctx, "v1.10.0") }()
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("probe did not start")
		}
		cancel()
		synctest.Wait()
		select {
		case rep := <-done:
			if len(rep.Binaries) != 0 || rep.Running != "v1.10.0" {
				t.Fatalf("canceled scan = %+v", rep)
			}
		default:
			t.Fatal("Scan waited for a probe that ignores cancellation")
		}
		select {
		case <-finished:
			t.Fatal("probe unexpectedly finished before release")
		default:
		}
	})
}

func TestWarningNamesBothForgesAndTheFix(t *testing.T) {
	rep := Report{Running: "v1.10.0", Binaries: []Binary{
		{Path: "/home/u/go/bin/forge", Version: "v1.7.0"},
		{Path: "/usr/local/bin/forge", Version: "v1.10.0", NPM: true},
	}}
	msg := rep.Warning("forge", "/home/u/.cache/forge/forge")
	for _, want := range []string{
		"forge v1.10.0, /home/u/.cache/forge/forge",
		"/home/u/go/bin/forge  v1.7.0   (first on PATH: a bare `forge` runs this one)",
		"/usr/local/bin/forge  v1.10.0  (npm @reliantlabs/forge)",
		"put the directory of the one you do first on PATH",
		"Or update /home/u/go/bin/forge (v1.7.0) in place: `go install github.com/reliant-labs/forge/cmd/forge@latest`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning is missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Or update /usr/local/bin/forge") {
		t.Errorf("warning offers to update the newest forge:\n%s", msg)
	}
}

// TestWarningWhenTheRunningForgeIsTheStaleOne: a newer forge later on PATH
// is reported too, and the update hint names the running forge's npm path.
func TestWarningWhenTheRunningForgeIsTheStaleOne(t *testing.T) {
	rep := Report{Running: "v1.7.0", Binaries: []Binary{
		{Path: "/usr/local/bin/forge", Version: "v1.7.0", NPM: true},
		{Path: "/opt/forge/forge", Version: "v1.10.0"},
	}}
	msg := rep.Warning("forge", "")
	if !strings.Contains(msg, "/opt/forge/forge      v1.10.0\n") {
		t.Errorf("warning does not name the newer forge:\n%s", msg)
	}
	if !strings.Contains(msg, "Or update /usr/local/bin/forge (v1.7.0) in place: `npm i -g @reliantlabs/forge@latest`") {
		t.Errorf("warning does not say how to update the npm forge:\n%s", msg)
	}
}

// TestNoUpdateHintPastADevelopmentBuild: @latest is older than a dev build,
// so updating to it would move a forge backwards.
func TestNoUpdateHintPastADevelopmentBuild(t *testing.T) {
	rep := Report{Running: "v1.10.1-0.20261125030301-e18ed71e13ce+dirty", Binaries: []Binary{
		{Path: "/home/u/go/bin/forge", Version: "v1.7.0"},
	}}
	msg := rep.Warning("forge", "")
	if !strings.Contains(msg, "/home/u/go/bin/forge") || strings.Contains(msg, "Or update") {
		t.Errorf("want the forge named and no update hint:\n%s", msg)
	}
}

func TestNoWarningWhenEveryForgeAgrees(t *testing.T) {
	for _, rep := range []Report{
		{Running: "v1.10.0"},
		{Running: "v1.10.0", Binaries: []Binary{{Path: "/a/forge", Version: "v1.10.0", Self: true}}},
		// The npm shim of the running release: "1.10.0" in package.json.
		{Running: "1.10.0", Binaries: []Binary{{Path: "/b/forge", Version: "v1.10.0", NPM: true}}},
	} {
		if msg := rep.Warning("forge", "/a/forge"); msg != "" {
			t.Errorf("Warning(%+v) = %q, want none", rep, msg)
		}
	}
}

func TestNPMShimScripts(t *testing.T) {
	tmp := t.TempDir()

	// pnpm's shell shim names the package relative to $basedir.
	pnpmBin := filepath.Join(tmp, "pnpm")
	writePackage(t, filepath.Join(pnpmBin, ".pnpm", "@reliantlabs+forge@1.9.0", "node_modules", "@reliantlabs", "forge"), "@reliantlabs/forge", "1.9.0")
	pnpm := writeExe(t, filepath.Join(pnpmBin, "forge"), "#!/bin/sh\n"+
		`basedir=$(dirname "$(echo "$0" | sed -e 's,\\,/,g')")`+"\n"+
		`exec node "$basedir/.pnpm/@reliantlabs+forge@1.9.0/node_modules/@reliantlabs/forge/bin/forge.js" "$@"`+"\n")
	if v, ok, isShim := npmShimVersion(pnpm); v != "v1.9.0" || !ok || !isShim {
		t.Errorf("pnpm shim: npmShimVersion = %q, %v, %v; want v1.9.0, true, true", v, ok, isShim)
	}

	// npm's windows .cmd shim, beside the global node_modules.
	npmDir := filepath.Join(tmp, "npm")
	writePackage(t, filepath.Join(npmDir, "node_modules", "@reliantlabs", "forge"), "@reliantlabs/forge", "1.10.0")
	cmd := writeExe(t, filepath.Join(npmDir, "forge.cmd"), "@ECHO off\r\n"+
		`"%_prog%"  "%dp0%\node_modules\@reliantlabs\forge\bin\forge.js" %*`+"\r\n")
	if v, ok, isShim := npmShimVersion(cmd); v != "v1.10.0" || !ok || !isShim {
		t.Errorf(".cmd shim: npmShimVersion = %q, %v, %v; want v1.10.0, true, true", v, ok, isShim)
	}

	// A shim whose package is gone is still a shim: skipped, never run.
	orphan := writeExe(t, filepath.Join(tmp, "orphan", "forge"), "#!/bin/sh\nexec node /nowhere/node_modules/@reliantlabs/forge/bin/forge.js\n")
	if _, ok, isShim := npmShimVersion(orphan); ok || !isShim {
		t.Errorf("orphan shim: ok=%v isShim=%v; want false, true", ok, isShim)
	}

	// Another package's package.json does not count.
	other := filepath.Join(tmp, "other")
	writePackage(t, filepath.Join(other, "node_modules", "@reliantlabs", "forge"), "something-else", "9.9.9")
	if _, ok := packageVersion(filepath.Join(other, "node_modules", "@reliantlabs", "forge")); ok {
		t.Error("packageVersion accepted a package.json for another package")
	}

	// A binary is not a shim, even if it mentions the package.
	bin := writeExe(t, filepath.Join(tmp, "bin", "forge"), "\x7fELF... @reliantlabs/forge")
	if _, _, isShim := npmShimVersion(bin); isShim {
		t.Error("a binary was taken for an npm shim")
	}
}

// TestProbeSetsDisableEnv runs the real probe against a script: the forge it
// asks must see FORGE_NO_PATH_CHECK, or a newer forge would scan PATH in turn.
func TestProbeSetsDisableEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake forge is a shell script")
	}
	t.Setenv(DisableEnv, "")
	fake := writeExe(t, filepath.Join(t.TempDir(), "forge"), "#!/bin/sh\n"+
		`echo "forge version v1.7.0"`+"\n"+
		`echo "  forge build:    $`+DisableEnv+`"`+"\n")
	out, err := probeVersion(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "forge build:    1") {
		t.Errorf("probed forge did not see %s=1:\n%s", DisableEnv, out)
	}
}
