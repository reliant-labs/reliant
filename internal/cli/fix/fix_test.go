package fix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// Every release codemod is a `forge fix` subcommand, run over the project
// root; --dry-run reports and writes nothing.
func TestFix_CodemodSubcommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "internal", "app", "lifecycle.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const src = "package app\n\nimport \"github.com/reliant-labs/forge/pkg/lifecyclekit\"\n\nvar _ lifecyclekit.Options\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := newCmd(&factory.Factory{Out: &out})
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("forge fix %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}

	if out := run("operatorkit", "--dry-run"); !strings.Contains(out, "would rewrite: internal/app/lifecycle.go") {
		t.Errorf("dry run output:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Fatalf("--dry-run wrote the file:\n%s", got)
	}
	run("operatorkit")
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "operatorkit.Options") {
		t.Fatalf("not rewritten:\n%s", got)
	}
	if out := run("operatorkit"); !strings.Contains(out, "nothing to rewrite") {
		t.Errorf("second run output:\n%s", out)
	}
}

// --scope (a Scoped codemod) rewrites only the files under its paths; a
// codemod that is not Scoped offers no --scope at all.
func TestFix_CodemodScope(t *testing.T) {
	dir := t.TempDir()
	const src = "package app\n\nimport \"github.com/reliant-labs/forge/pkg/lifecyclekit\"\n\nvar _ lifecyclekit.Options\n"
	for _, rel := range []string{"forge.yaml", "internal/a/a.go", "internal/b/b.go"} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := src
		if rel == "forge.yaml" {
			content = "name: app\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	run := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		cmd := newCmd(&factory.Factory{Out: &out})
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	if out, err := run("operatorkit", "--scope", "internal/a"); err != nil {
		t.Fatalf("forge fix operatorkit --scope: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "internal", "a", "a.go")); !strings.Contains(string(got), "operatorkit.Options") {
		t.Errorf("internal/a is in scope but was not rewritten:\n%s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "internal", "b", "b.go")); string(got) != src {
		t.Errorf("internal/b is outside --scope but was rewritten:\n%s", got)
	}
	if out, err := run("operatorkit", "--scope", "internal/a"); err != nil || !strings.Contains(out, "nothing to rewrite — internal/a is already") {
		t.Errorf("re-run under the same scope: %v\n%s", err, out)
	}
	if out, err := run("operatorkit", "--scope", "internal/nope"); err == nil || !strings.Contains(err.Error()+out, "does not exist") {
		t.Errorf("a scope naming nothing must fail, got %v\n%s", err, out)
	}
	if _, err := run("composition-graph", "--dry-run", "--scope", "internal/a"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("composition-graph is not Scoped and must refuse --scope, got %v", err)
	}
}
