package toolpin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

func stubGoMod(t *testing.T, versions map[string]string) {
	t.Helper()
	prev := goModVersion
	t.Cleanup(func() {
		goModVersion = prev
		mu.Lock()
		cache = map[string]Buf{}
		mu.Unlock()
	})
	goModVersion = func(dir string) string { return versions[dir] }
}

// A project whose go.mod requires buf runs that buf; one that does not runs
// forge's pin. There is no third place a buf version can come from.
func TestBufFor(t *testing.T) {
	pinned, plain := t.TempDir(), t.TempDir()
	stubGoMod(t, map[string]string{pinned: "v1.57.0"})

	b := BufFor(pinned)
	if b.Version != "v1.57.0" || b.Bare() != "1.57.0" || !b.Declared() {
		t.Errorf("go.mod-pinned buf = %+v", b)
	}
	if b := BufFor(plain); b.Version != "v"+templates.BufVersion || b.Declared() {
		t.Errorf("undeclared buf = %+v, want forge's v%s", b, templates.BufVersion)
	}
	if b := BufFor(""); b != ForgeBuf() {
		t.Errorf("no project = %+v, want forge's pin", b)
	}
}

// The real lookup: a directory with no go.mod declares nothing, and does not
// fail.
func TestGoModVersion_NoModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if v := goModVersion(dir); v != "" {
		t.Errorf("goModVersion(no go.mod) = %q, want \"\"", v)
	}
}
