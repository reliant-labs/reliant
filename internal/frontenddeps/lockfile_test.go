package frontenddeps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const runtimePkg = "@reliantlabs/forge-web-runtime"

func manifest(spec string) string {
	return `{"name":"web","dependencies":{"` + runtimePkg + `":"` + spec + `","react":"19.0.0"}}`
}

func npmLock(member, spec string) string {
	return `{"lockfileVersion":3,"packages":{"` + member + `":{"name":"web","dependencies":{"` + runtimePkg + `":"` + spec + `","react":"19.0.0"}}}}`
}

func TestFindLockPrefersNearestAndStopsAtBoundary(t *testing.T) {
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	writeFileAt(t, root, "frontends/web/package.json", manifest("^0.9.0"))
	if _, ok := FindLock(fe, root); ok {
		t.Fatal("no lockfile anywhere, FindLock reported one")
	}

	writeFileAt(t, root, "package-lock.json", "{}")
	got, ok := FindLock(fe, root)
	if !ok || got.Dir != root || got.Runner != "npm" {
		t.Fatalf("workspace-root lockfile not found: %+v ok=%v", got, ok)
	}

	writeFileAt(t, root, "frontends/web/pnpm-lock.yaml", "")
	got, _ = FindLock(fe, root)
	if got.Dir != fe || got.Runner != "pnpm" {
		t.Fatalf("the frontend's own lockfile must win over the root's: %+v", got)
	}

	// A lockfile above the boundary is not this project's.
	outer := t.TempDir()
	writeFileAt(t, outer, "package-lock.json", "{}")
	inner := filepath.Join(outer, "proj")
	writeFileAt(t, inner, "frontends/web/package.json", manifest("^0.9.0"))
	if _, ok := FindLock(filepath.Join(inner, "frontends", "web"), inner); ok {
		t.Fatal("FindLock walked above stopAt")
	}
}

func TestLockDriftNPM(t *testing.T) {
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	writeFileAt(t, root, "frontends/web/package.json", manifest("^0.9.0"))
	lock := Lock{Dir: fe, File: "package-lock.json", Runner: "npm"}

	writeFileAt(t, fe, "package-lock.json", npmLock("", "^0.9.0"))
	if d, err := LockDrift(lock, fe); err != nil || d != nil {
		t.Fatalf("agreeing lock reported drift: %+v %v", d, err)
	}

	writeFileAt(t, fe, "package-lock.json", npmLock("", "0.8.0"))
	d, err := LockDrift(lock, fe)
	if err != nil || d == nil {
		t.Fatalf("stale lock not detected: %+v %v", d, err)
	}
	if len(d.Details) != 1 || !strings.Contains(d.Details[0], runtimePkg) ||
		!strings.Contains(d.Details[0], `"^0.9.0"`) || !strings.Contains(d.Details[0], `"0.8.0"`) {
		t.Fatalf("drift detail must name the package and both ranges: %v", d.Details)
	}

	// Narrowing to other packages ignores the runtime's drift.
	if d, _ := LockDrift(lock, fe, "react"); d != nil {
		t.Fatalf("drift outside the named packages was reported: %+v", d)
	}
	if d, _ := LockDrift(lock, fe, runtimePkg); d == nil {
		t.Fatal("drift in the named package was missed")
	}
}

func TestLockDriftNPMWorkspaceMember(t *testing.T) {
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	writeFileAt(t, root, "frontends/web/package.json", manifest("^0.9.0"))
	writeFileAt(t, root, "package-lock.json", npmLock("frontends/web", "0.8.0"))
	lock, ok := FindLock(fe, root)
	if !ok {
		t.Fatal("no lock")
	}
	if d, err := LockDrift(lock, fe); err != nil || d == nil {
		t.Fatalf("member drift under a root lockfile not detected: %+v %v", d, err)
	}
	writeFileAt(t, root, "package-lock.json", npmLock("frontends/web", "^0.9.0"))
	if d, err := LockDrift(lock, fe); err != nil || d != nil {
		t.Fatalf("agreeing member lock reported drift: %+v %v", d, err)
	}
}

func TestLockDriftNothingToCompare(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, root, "package.json", manifest("^0.9.0"))
	lock := Lock{Dir: root, File: "package-lock.json", Runner: "npm"}

	// Package not filed in the lockfile (a different member): not comparable.
	writeFileAt(t, root, "package-lock.json", npmLock("elsewhere", "0.8.0"))
	if d, err := LockDrift(lock, root); err != nil || d != nil {
		t.Fatalf("unfiled package must not be reported: %+v %v", d, err)
	}
	// Yarn's lockfile is not a per-package record.
	if d, err := LockDrift(Lock{Dir: root, File: "yarn.lock", Runner: "yarn"}, root); err != nil || d != nil {
		t.Fatalf("yarn must be skipped: %+v %v", d, err)
	}
	// No manifest.
	if d, err := LockDrift(lock, filepath.Join(root, "nope")); err != nil || d != nil {
		t.Fatalf("missing manifest must be skipped: %+v %v", d, err)
	}
}

func TestLockDriftPNPM(t *testing.T) {
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	writeFileAt(t, root, "frontends/web/package.json", manifest("^0.9.0"))
	lock := Lock{Dir: root, File: "pnpm-lock.yaml", Runner: "pnpm"}
	pnpm := func(spec string) string {
		return "lockfileVersion: '9.0'\nimporters:\n  frontends/web:\n    dependencies:\n      '" + runtimePkg +
			"':\n        specifier: " + spec + "\n        version: 0.9.1\n      react:\n        specifier: 19.0.0\n        version: 19.0.0\n"
	}
	writeFileAt(t, root, "pnpm-lock.yaml", pnpm("0.8.0"))
	if d, err := LockDrift(lock, fe); err != nil || d == nil {
		t.Fatalf("stale pnpm lock not detected: %+v %v", d, err)
	}
	writeFileAt(t, root, "pnpm-lock.yaml", pnpm("^0.9.0"))
	if d, err := LockDrift(lock, fe); err != nil || d != nil {
		t.Fatalf("agreeing pnpm lock reported drift: %+v %v", d, err)
	}
}

func TestLockRefreshCommand(t *testing.T) {
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	cases := []struct {
		name   string
		lock   Lock
		want   []string
		absent []string
	}{
		{"npm beside the manifest is pinned there", Lock{Dir: fe, File: "package-lock.json", Runner: "npm"},
			[]string{"npm install --package-lock-only", "--ignore-scripts", "--prefix " + fe}, nil},
		{"npm at a workspace root runs at the root", Lock{Dir: root, File: "package-lock.json", Runner: "npm"},
			[]string{"(cd " + root, "--package-lock-only"}, []string{"--prefix"}},
		{"pnpm", Lock{Dir: root, File: "pnpm-lock.yaml", Runner: "pnpm"},
			[]string{"pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile"}, nil},
	}
	for _, c := range cases {
		got := LockRefreshCommand(c.lock, fe)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
		for _, a := range c.absent {
			if strings.Contains(got, a) {
				t.Errorf("%s: %q must not contain %q", c.name, got, a)
			}
		}
	}
}

// fakeNPM puts an `npm` on PATH that records its args and cwd, then runs body.
func fakeNPM(t *testing.T, body string) (calls func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake npm")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$PWD $*\" >> '" + log + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

func TestRefreshLockRunsPackageManagerAndReportsFailure(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, root, "package.json", manifest("^0.9.0"))
	lock := Lock{Dir: root, File: "package-lock.json", Runner: "npm"}

	calls := fakeNPM(t, "exit 0")
	if err := RefreshLock(context.Background(), lock, root); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !strings.Contains(got, "install --package-lock-only --ignore-scripts") {
		t.Fatalf("unexpected invocation: %q", got)
	}

	fakeNPM(t, "echo 'npm error code E404' >&2; exit 1")
	err := RefreshLock(context.Background(), lock, root)
	if err == nil || !strings.Contains(err.Error(), "E404") {
		t.Fatalf("a failed refresh must surface the package manager's output, got %v", err)
	}
}

// npm's --package-lock-only rewrites node_modules/.package-lock.json to the
// new lockfile's tree without installing it. RefreshLock puts the file back,
// content and mtime, so node_modules never claims a version it does not hold.
func TestRefreshLockKeepsNPMHiddenLockfileTruthful(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, root, "package.json", manifest("^0.12.0"))
	lock := Lock{Dir: root, File: "package-lock.json", Runner: "npm"}
	hidden := filepath.Join(root, "node_modules", ".package-lock.json")
	// What npm 10 does: the hidden lockfile now names the refreshed tree.
	fakeNPM(t, `mkdir -p node_modules && echo '{"claims":"0.12.0"}' > node_modules/.package-lock.json`)

	t.Run("an installed tree's record is restored", func(t *testing.T) {
		writeFileAt(t, root, "node_modules/.package-lock.json", `{"claims":"0.10.1"}`)
		old := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(hidden, old, old); err != nil {
			t.Fatal(err)
		}
		if err := RefreshLock(context.Background(), lock, root); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(hidden)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"claims":"0.10.1"}` {
			t.Errorf("hidden lockfile = %s, want the installed tree's record back", got)
		}
		if info, _ := os.Stat(hidden); !info.ModTime().Equal(old) {
			t.Errorf("hidden lockfile mtime = %v, want %v: a newer stamp vouches for a tree nobody installed", info.ModTime(), old)
		}
	})

	t.Run("no installed tree, no record", func(t *testing.T) {
		if err := os.Remove(hidden); err != nil {
			t.Fatal(err)
		}
		if err := RefreshLock(context.Background(), lock, root); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(hidden); !os.IsNotExist(err) {
			t.Errorf("the refresh left a hidden lockfile behind where none existed: %v", err)
		}
	})
}
