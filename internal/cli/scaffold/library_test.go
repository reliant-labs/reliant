// File: internal/cli/scaffold/library_test.go
//
// Tests for `forge scaffold library <name>`. These run runLibrary in a
// temp project (with a hand-written minimal forge.yaml) and assert on
// the resulting directory, starter file, and forge.yaml mutation.
// They exercise:
//
//   - default path resolution (internal/<name>/)
//   - --path override
//   - --no-exclude
//   - --force overwriting an existing directory
//   - refusal when forge.yaml is absent
//   - refusal when the target directory already exists without --force
//   - comment preservation in forge.yaml (yaml.Node round-trip)
//   - idempotency: re-adding the same path is a no-op

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempProject creates a temp directory, writes a minimal forge.yaml
// (or whatever the caller supplies), changes the test process cwd to
// it, and registers a cleanup that restores the previous cwd.
//
// Many add commands depend on os.Getwd via projectRoot; the simplest
// way to give them a project to operate against is to actually chdir
// into one. t.TempDir + t.Chdir keeps that contained to the test.
func withTempProject(t *testing.T, forgeYAML string) string {
	t.Helper()
	dir := t.TempDir()
	if forgeYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
			t.Fatalf("write forge.yaml: %v", err)
		}
	}
	t.Chdir(dir)
	return dir
}

// minimalForgeYAML is a forge.yaml just rich enough for runLibrary
// to exercise the contracts.exclude path. It deliberately keeps a
// non-canonical key + a comment so the test can assert preservation.
const minimalForgeYAML = `# project manifest — hand-edited
name: testproj
module_path: example.com/testproj
version: 0.1.0
hot_reload: false
contracts:
  strict: true
  exclude:
    - internal/preexisting
# custom user key (should survive the round-trip)
my_custom_key: keep-me
`

func TestRunAddLibrary_DefaultPath(t *testing.T) {
	dir := withTempProject(t, minimalForgeYAML)

	if err := runLibrary("httputil", "", false, false); err != nil {
		t.Fatalf("runLibrary: %v", err)
	}

	starter := filepath.Join(dir, "internal", "httputil", "httputil.go")
	body, err := os.ReadFile(starter)
	if err != nil {
		t.Fatalf("read starter: %v", err)
	}
	if !strings.Contains(string(body), "package httputil") {
		t.Errorf("starter missing 'package httputil':\n%s", body)
	}
	if !strings.Contains(string(body), "TODO") {
		t.Errorf("starter missing TODO marker:\n%s", body)
	}

	yamlBytes, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	got := string(yamlBytes)
	if !strings.Contains(got, "internal/httputil") {
		t.Errorf("forge.yaml missing internal/httputil under contracts.exclude:\n%s", got)
	}
	// Pre-existing exclude entry must survive.
	if !strings.Contains(got, "internal/preexisting") {
		t.Errorf("forge.yaml lost the pre-existing exclude entry:\n%s", got)
	}
}

func TestRunAddLibrary_CustomPath(t *testing.T) {
	dir := withTempProject(t, minimalForgeYAML)

	if err := runLibrary("crypto", "pkg/crypto", false, false); err != nil {
		t.Fatalf("runLibrary: %v", err)
	}

	starter := filepath.Join(dir, "pkg", "crypto", "crypto.go")
	if _, err := os.Stat(starter); err != nil {
		t.Fatalf("starter not at %s: %v", starter, err)
	}

	// Default path must NOT exist (override worked).
	if _, err := os.Stat(filepath.Join(dir, "internal", "crypto")); !os.IsNotExist(err) {
		t.Errorf("default path created despite --path override: err=%v", err)
	}

	yamlBytes, _ := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if !strings.Contains(string(yamlBytes), "pkg/crypto") {
		t.Errorf("forge.yaml missing pkg/crypto under contracts.exclude:\n%s", yamlBytes)
	}
}

func TestRunAddLibrary_PreservesYAMLCommentsAndCustomKeys(t *testing.T) {
	dir := withTempProject(t, minimalForgeYAML)

	if err := runLibrary("httputil", "", false, false); err != nil {
		t.Fatalf("runLibrary: %v", err)
	}

	yamlBytes, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	got := string(yamlBytes)

	// Head comment + the in-body custom key comment must both survive
	// the round-trip. yaml.Node-based mutation should leave both intact.
	for _, want := range []string{
		"# project manifest — hand-edited",
		"# custom user key (should survive the round-trip)",
		"my_custom_key: keep-me",
		"internal/preexisting",
		"internal/httputil",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("forge.yaml missing %q after round-trip:\n%s", want, got)
		}
	}
}

func TestRunAddLibrary_NoExcludeSkipsYAML(t *testing.T) {
	dir := withTempProject(t, minimalForgeYAML)

	before, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}

	if err := runLibrary("httputil", "", false, true); err != nil {
		t.Fatalf("runLibrary: %v", err)
	}

	// Starter still produced.
	if _, err := os.Stat(filepath.Join(dir, "internal", "httputil", "httputil.go")); err != nil {
		t.Fatalf("starter not written: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("forge.yaml mutated despite --no-exclude:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRunAddLibrary_ForceOverwritesExistingDir(t *testing.T) {
	dir := withTempProject(t, minimalForgeYAML)

	target := filepath.Join(dir, "internal", "httputil")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	stale := filepath.Join(target, "stale.go")
	if err := os.WriteFile(stale, []byte("// stale\n"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	// Without --force: refuses.
	err := runLibrary("httputil", "", false, false)
	if err == nil {
		t.Fatal("expected refusal when dir exists without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error message should mention 'already exists', got: %v", err)
	}
	// Stale file untouched.
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale file removed despite refusal: %v", err)
	}

	// With --force: overwrites.
	if err := runLibrary("httputil", "", true, false); err != nil {
		t.Fatalf("runLibrary --force: %v", err)
	}
	// Starter is now present.
	if _, err := os.Stat(filepath.Join(target, "httputil.go")); err != nil {
		t.Fatalf("starter not written under --force: %v", err)
	}
	// Stale file is gone (RemoveAll inside --force).
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file survived --force: err=%v", err)
	}
}

func TestRunAddLibrary_NoForgeYAMLRefuses(t *testing.T) {
	withTempProject(t, "") // no forge.yaml

	err := runLibrary("httputil", "", false, false)
	if err == nil {
		t.Fatal("expected refusal when forge.yaml absent")
	}
	if !strings.Contains(err.Error(), "forge.yaml") {
		t.Errorf("error message should mention forge.yaml, got: %v", err)
	}
}

func TestRunAddLibrary_InvalidName(t *testing.T) {
	withTempProject(t, minimalForgeYAML)

	cases := map[string]string{
		"empty":      "",
		"keyword":    "func",
		"leads-dot":  ".x",
		"with-space": "foo bar",
	}
	for label, in := range cases {
		t.Run(label, func(t *testing.T) {
			err := runLibrary(in, "", false, false)
			if err == nil {
				t.Errorf("runLibrary(%q) accepted invalid name", in)
			}
		})
	}
}

func TestRunAddLibrary_IdempotentExclude(t *testing.T) {
	dir := withTempProject(t, minimalForgeYAML)

	if err := runLibrary("httputil", "", false, false); err != nil {
		t.Fatalf("first runLibrary: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml after first: %v", err)
	}

	// Second invocation with --force (so the dir conflict doesn't block).
	if err := runLibrary("httputil", "", true, false); err != nil {
		t.Fatalf("second runLibrary: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml after second: %v", err)
	}

	// The exclude list should not have been duplicated.
	gotCount := strings.Count(string(second), "- internal/httputil")
	if gotCount != 1 {
		t.Errorf("internal/httputil appears %d times in contracts.exclude, want 1:\n%s",
			gotCount, second)
	}
	// And the yaml shouldn't have meaningfully diverged from the first
	// write (idempotent re-run of the contracts.exclude append).
	if string(first) != string(second) {
		t.Errorf("forge.yaml drifted on idempotent re-run:\nfirst:\n%s\nsecond:\n%s",
			first, second)
	}
}

func TestAppendToContractsExclude_CreatesMissingKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "forge.yaml")
	// No contracts: key at all — the helper must create both contracts:
	// and contracts.exclude: from scratch.
	if err := os.WriteFile(configPath, []byte("name: tiny\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	added, err := appendToContractsExclude(configPath, "internal/foo")
	if err != nil {
		t.Fatalf("appendToContractsExclude: %v", err)
	}
	if !added {
		t.Errorf("expected added=true on first append")
	}

	got, _ := os.ReadFile(configPath)
	if !strings.Contains(string(got), "contracts:") || !strings.Contains(string(got), "internal/foo") {
		t.Errorf("contracts.exclude not created cleanly:\n%s", got)
	}

	// Second append of same entry: idempotent.
	added, err = appendToContractsExclude(configPath, "internal/foo")
	if err != nil {
		t.Fatalf("appendToContractsExclude (second): %v", err)
	}
	if added {
		t.Errorf("expected added=false on idempotent re-add")
	}
}

func TestRunAddLibrary_PathRejectsTraversal(t *testing.T) {
	withTempProject(t, minimalForgeYAML)

	err := runLibrary("escape", "../outside", false, false)
	if err == nil {
		t.Fatal("expected --path with .. traversal to be rejected")
	}
	if !strings.Contains(err.Error(), "project-relative") {
		t.Errorf("error message should mention 'project-relative', got: %v", err)
	}
}
