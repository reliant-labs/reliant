package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLintFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lockFixture(t *testing.T, lockedRange string) string {
	t.Helper()
	root := t.TempDir()
	writeLintFile(t, root, "frontends/web/package.json",
		`{"name":"web","dependencies":{"@reliantlabs/forge-web-runtime":"^0.9.0"}}`)
	writeLintFile(t, root, "frontends/web/package-lock.json",
		`{"lockfileVersion":3,"packages":{"":{"name":"web","dependencies":{"@reliantlabs/forge-web-runtime":"`+lockedRange+`"}}}}`)
	t.Chdir(root)
	return root
}

func TestFrontendLockLane_FlagsDriftWithRemedy(t *testing.T) {
	root := lockFixture(t, "0.8.0")
	fs, gated, err := collectFrontendLockJSON(&lintRunCtx{cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if !gated || len(fs) != 1 {
		t.Fatalf("want one gating finding, got gated=%v %+v", gated, fs)
	}
	f := fs[0]
	if f.Rule != ruleFrontendLockDrift || f.Severity != lintSevError || f.File != "frontends/web/package.json" {
		t.Errorf("unexpected finding: %+v", f)
	}
	for _, want := range []string{"@reliantlabs/forge-web-runtime", `"^0.9.0"`, `"0.8.0"`, "npm ci"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q missing %q", f.Message, want)
		}
	}
	// The hint is a command to paste into this host's shell, so it names the
	// directory in the host's own separators.
	for _, want := range []string{"npm install --package-lock-only", "--ignore-scripts", filepath.FromSlash("frontends/web")} {
		if !strings.Contains(f.FixHint, want) {
			t.Errorf("fix hint %q missing %q", f.FixHint, want)
		}
	}
}

func TestFrontendLockLane_CleanWhenLockAgreesOrAbsent(t *testing.T) {
	root := lockFixture(t, "^0.9.0")
	if fs, gated, err := collectFrontendLockJSON(&lintRunCtx{cwd: root}); err != nil || gated || len(fs) != 0 {
		t.Fatalf("agreeing lock: gated=%v %+v %v", gated, fs, err)
	}
	if err := os.Remove(filepath.Join(root, "frontends/web/package-lock.json")); err != nil {
		t.Fatal(err)
	}
	if fs, gated, err := collectFrontendLockJSON(&lintRunCtx{cwd: root}); err != nil || gated || len(fs) != 0 {
		t.Fatalf("no lock: gated=%v %+v %v", gated, fs, err)
	}
}

func TestFrontendLockLane_IsRegisteredAndGates(t *testing.T) {
	for _, s := range lintPipeline() {
		if s.name == "frontend lockfile" {
			if !s.gates {
				t.Error("the lockfile lane must gate: a drifted lockfile fails every frozen install")
			}
			return
		}
	}
	t.Fatal("frontend lockfile lane is not registered in lintPipeline()")
}
