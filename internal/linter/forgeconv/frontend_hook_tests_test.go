package forgeconv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintFrontendHookTests_NoFrontendsTreeIsNoop asserts the rule
// silently passes on library/cli-shape projects with no frontends/
// directory. The lint shouldn't fail loudly on legitimate non-frontend
// projects.
func TestLintFrontendHookTests_NoFrontendsTreeIsNoop(t *testing.T) {
	dir := t.TempDir()
	res, err := LintFrontendHookTests(dir)
	if err != nil {
		t.Fatalf("LintFrontendHookTests: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("expected no findings for project without frontends/, got: %+v", res.Findings)
	}
}

// TestLintFrontendHookTests_WarnsWhenNoSibling asserts the canonical
// case: hooks.ts present, neither .test.tsx nor .test.tsx.starter
// present. Expect one warning per hook file.
func TestLintFrontendHookTests_WarnsWhenNoSibling(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "frontends", "admin", "src", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hooksDir, "user-service-hooks.ts"), "export const x = 1;")

	res, err := LintFrontendHookTests(dir)
	if err != nil {
		t.Fatalf("LintFrontendHookTests: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Rule != "forgeconv-frontend-hook-tests" {
		t.Errorf("rule = %q", f.Rule)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", f.Severity)
	}
	if !strings.Contains(f.File, "user-service-hooks.ts") {
		t.Errorf("file = %q, want it to reference user-service-hooks.ts", f.File)
	}
}

// TestLintFrontendHookTests_FiresOnAnInertStarter asserts the rule DOES
// fire when the only thing beside the hooks file is a
// `<svc>-hooks.test.tsx.starter`.
//
// It used to be silent there, on the theory that codegen had nudged the user
// and activation was a one-rename step. Nobody ever took the step: a whole
// dogfood run shipped a generated suite per service that had never executed,
// with this lint quiet throughout. An inert file is not a test, and `forge
// generate` now writes the test live — so a starter means an OLD project
// that needs a regen, and the lint says so.
func TestLintFrontendHookTests_FiresOnAnInertStarter(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "frontends", "admin", "src", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hooksDir, "user-service-hooks.ts"), "export const x = 1;")
	mustWrite(t, filepath.Join(hooksDir, "user-service-hooks.test.tsx.starter"), "// never ran")

	res, err := LintFrontendHookTests(dir)
	if err != nil {
		t.Fatalf("LintFrontendHookTests: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding for an inert starter, got: %+v", res.Findings)
	}
}

// TestLintFrontendHookTests_SilentWhenActiveTestPresent asserts the
// rule does NOT fire when a live test sits beside the hooks file.
func TestLintFrontendHookTests_SilentWhenActiveTestPresent(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "frontends", "admin", "src", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hooksDir, "user-service-hooks.ts"), "export const x = 1;")
	mustWrite(t, filepath.Join(hooksDir, "user-service-hooks.test.tsx"), "// user-written test")

	res, err := LintFrontendHookTests(dir)
	if err != nil {
		t.Fatalf("LintFrontendHookTests: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("expected no findings when active test is present, got: %+v", res.Findings)
	}
}

// TestLintFrontendHookTests_SkipsNodeModules asserts that *-hooks.ts
// files inside node_modules don't trip the rule. Transitive npm deps
// sometimes ship -hooks.ts files; treating those as user code would
// generate hundreds of false-positive warnings.
func TestLintFrontendHookTests_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	noisy := filepath.Join(dir, "frontends", "admin", "node_modules", "some-pkg", "use-thing-hooks.ts")
	if err := os.MkdirAll(filepath.Dir(noisy), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, noisy, "// stray transitive dep")

	res, err := LintFrontendHookTests(dir)
	if err != nil {
		t.Fatalf("LintFrontendHookTests: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("expected node_modules to be skipped, got: %+v", res.Findings)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
