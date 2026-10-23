package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
)

// fakePackageManager puts a stub `name` on PATH that appends its argv to a log
// and, for install verbs, creates node_modules — so the test observes what the
// build ran without touching the network.
func fakePackageManager(t *testing.T, name string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	bin := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> " + logPath + "\n" +
		"case \"$1\" in install|ci) mkdir -p node_modules ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	return logPath
}

func readCalls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func newFrontendDir(t *testing.T, lockfile string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"package.json", lockfile} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBuildFrontendInstallsMissingNodeModules(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	cases := []struct {
		lockfile, manager string
		want              []string
	}{
		{"package-lock.json", "npm", []string{"ci", "run build"}},
		{"pnpm-lock.yaml", "pnpm", []string{"install --frozen-lockfile", "run build"}},
	}
	for _, tc := range cases {
		t.Run(tc.manager, func(t *testing.T) {
			logPath := fakePackageManager(t, tc.manager)
			dir := newFrontendDir(t, tc.lockfile)
			fe := config.FrontendConfig{Name: "web", DevRunner: tc.manager}.WithDir(dir)

			if r := buildFrontend(context.Background(), fe, buildMemoryCaps{}); r.err != nil {
				t.Fatalf("build failed: %v", r.err)
			}
			got := readCalls(t, logPath)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("package manager calls = %q, want %q", got, tc.want)
			}

			// Second build: deps are current, so install must not run again.
			if r := buildFrontend(context.Background(), fe, buildMemoryCaps{}); r.err != nil {
				t.Fatalf("second build failed: %v", r.err)
			}
			got = readCalls(t, logPath)
			if len(got) != len(tc.want)+1 || got[len(got)-1] != "run build" {
				t.Fatalf("warm build reinstalled: %q", got)
			}
		})
	}
}

func TestBuildFrontendUsesLockfileManagerOverDevRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	logPath := fakePackageManager(t, "pnpm")
	dir := newFrontendDir(t, "pnpm-lock.yaml")
	fe := config.FrontendConfig{Name: "web"}.WithDir(dir) // dev_runner unset → npm

	if err := frontenddeps.Ensure(context.Background(), "[build]", fe.Name, dir, "npm", true); err != nil {
		t.Fatal(err)
	}
	if got := readCalls(t, logPath); got[0] != "install --frozen-lockfile" {
		t.Fatalf("calls = %q, want pnpm frozen install", got)
	}
}
