package lint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// `forge lint --fix ./internal/a/...` formats only the packages it names,
// as --help promises and as a --scope run already did: the pre-pass used to
// read only --scope, so package paths reformatted the whole tree.
func TestFixPrePassStaysInsidePackagePaths(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":            "module example.com/app\n\ngo 1.24\n",
		"internal/a/a.go":   "package a\n\nfunc F() {\n        _ = 1\n}\n",
		"internal/b/b.go":   "package b\n\nfunc F() {\n        _ = 1\n}\n",
		"internal/ab/ab.go": "package ab\n\nfunc F() {\n        _ = 1\n}\n",
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if n := structuredFixPrePass(context.Background(), dir, nil, nil, []string{"./internal/a/..."}, true); n != 1 {
		t.Errorf("pre-pass changed %d file(s), want 1 (internal/a/a.go only)", n)
	}
	for rel, wantChanged := range map[string]bool{"internal/a/a.go": true, "internal/b/b.go": false, "internal/ab/ab.go": false} {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if changed := string(got) != files[rel]; changed != wantChanged {
			t.Errorf("%s: changed = %v, want %v:\n%s", rel, changed, wantChanged, got)
		}
	}
}
