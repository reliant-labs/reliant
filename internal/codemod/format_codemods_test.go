package codemod

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pin bot's first live run on control-plane: `forge project upgrade` ran the
// connect-errors codemod (209 rewrites) and then reported 39 gofmt and 39
// goimports findings as needs_human — formatting forge produced itself.
//
// These run the real codemods over their fixtures, in a project whose
// golangci-lint config is control-plane's (goimports, the module as its
// local-prefix), and grade every Go file they touched with independent gofmt and
// goimports engines (formatFindings), not with forge's own formatter.

// writeControlPlaneGolangci declares what control-plane's .golangci.yml does:
// goimports, with the module as its local prefix.
func writeControlPlaneGolangci(t *testing.T, dir, module string) {
	t.Helper()
	cfg := "version: \"2\"\nformatters:\n  enable:\n    - goimports\n  settings:\n    goimports:\n      local-prefixes:\n        - " + module + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// retargetModule renames the scratch module from old to new in go.mod and every
// Go file's imports.
func retargetModule(t *testing.T, dir, old, new string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".go") || filepath.Base(p) == "go.mod") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(p, []byte(strings.ReplaceAll(string(b), old, new)), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// changedGo lists the Go files whose content differs from before, among the
// project's own (not generated, not testdata).
func changedGo(t *testing.T, dir string, before map[string]string) []string {
	t.Helper()
	var out []string
	for rel, now := range snapshotGo(t, dir) {
		if was, ok := before[rel]; !ok || was != now {
			out = append(out, rel)
		}
	}
	return out
}

func snapshotGo(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConnectErrors_LeavesNoFormattingFindings(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks a scratch module against forge; runs in task test")
	}
	// control-plane's shape: the module shares the github.com/reliant-labs/
	// prefix of the forge import the codemod adds, which is what makes
	// astutil place svcerr in the module's own import group.
	const module = "github.com/reliant-labs/cescratch"
	_, dir := connectErrorsScratch(t)
	retargetModule(t, dir, "example.com/cescratch", module)
	writeControlPlaneGolangci(t, dir, module)
	before := snapshotGo(t, dir)

	rep, err := Lookup0(t, "connect-errors").Run(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("connect-errors: %v", err)
	}
	if !rep.Changed() {
		t.Fatal("connect-errors rewrote nothing: the test would pass vacuously")
	}
	touched := changedGo(t, dir, before)
	if len(touched) < 5 {
		t.Fatalf("expected the codemod to touch many files, touched %v", touched)
	}
	if got := formatFindings(t, dir, module, touched); len(got) != 0 {
		t.Fatalf("connect-errors left %d formatting finding(s) of its own:\n%s", len(got), strings.Join(got, "\n"))
	}
}

func TestCompositionGraph_LeavesNoFormattingFindings(t *testing.T) {
	// control-plane's shape: the module shares the github.com/reliant-labs/
	// prefix with the forge imports the generated compose.go carries.
	const module = "github.com/reliant-labs/app"
	dir := copyLegacy(t)
	retargetModule(t, dir, "example.com/app", module)
	writeControlPlaneGolangci(t, dir, module)
	before := snapshotGo(t, dir)

	rep, err := Lookup0(t, "composition-graph").Run(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("composition-graph: %v", err)
	}
	if !rep.Changed() {
		t.Fatalf("composition-graph rewrote nothing (blocked: %v): the test would pass vacuously", rep.Blocked)
	}
	touched := changedGo(t, dir, before)
	// Deleted files are gone from the snapshot; what is left to grade is what
	// the codemod wrote (compose.go, the rewritten edges).
	var written []string
	for _, rel := range touched {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
			written = append(written, rel)
		}
	}
	if len(written) == 0 {
		t.Fatal("composition-graph wrote no Go file: the test would pass vacuously")
	}
	if got := formatFindings(t, dir, module, written); len(got) != 0 {
		t.Fatalf("composition-graph left formatting finding(s) of its own:\n%s", strings.Join(got, "\n"))
	}
}

// Lookup0 is Lookup that fails the test when the codemod is missing.
func Lookup0(t *testing.T, name string) Codemod {
	t.Helper()
	c, ok := Lookup(name)
	if !ok {
		t.Fatalf("codemod %q is not registered", name)
	}
	return c
}
