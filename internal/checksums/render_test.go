package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// Side-render contract (see render.go):
//
//   - WriteSideRenderNoBase parks a transient render at
//     .forge/render/<relpath> (the --explain-drift diff input) and never
//     seeds .forge/render-base/
//   - CleanSideRenders removes whatever exists under both roots
//     (including legacy fork-era render-base files)

func TestWriteSideRenderNoBase(t *testing.T) {
	root := t.TempDir()
	const rel = "pkg/app/wire_gen.go"

	v1 := []byte("package app // render v1\n")
	if err := WriteSideRenderNoBase(root, rel, v1); err != nil {
		t.Fatalf("WriteSideRenderNoBase: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, RenderDir, rel))
	if err != nil {
		t.Fatalf("side render missing: %v", err)
	}
	if string(data) != string(v1) {
		t.Errorf("render = %q, want v1", data)
	}
	// No merge base is ever seeded — that was fork-era machinery.
	if _, err := os.Stat(filepath.Join(root, RenderBaseDir, rel)); !os.IsNotExist(err) {
		t.Errorf("render-base must not be seeded by WriteSideRenderNoBase (stat err=%v)", err)
	}

	// Refreshed on every call.
	v2 := []byte("package app // render v2\n")
	if err := WriteSideRenderNoBase(root, rel, v2); err != nil {
		t.Fatalf("WriteSideRenderNoBase v2: %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(root, RenderDir, rel))
	if string(data) != string(v2) {
		t.Errorf("render = %q, want v2 (must refresh every call)", data)
	}
}

func TestCleanSideRenders(t *testing.T) {
	root := t.TempDir()
	const rel = "pkg/app/wire_gen.go"
	if err := WriteSideRenderNoBase(root, rel, []byte("render\n")); err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy fork-era merge base left by an older forge.
	basePath := filepath.Join(root, RenderBaseDir, rel)
	if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CleanSideRenders(root, rel); err != nil {
		t.Fatalf("CleanSideRenders: %v", err)
	}
	for _, p := range []string{
		filepath.Join(root, RenderDir, rel),
		filepath.Join(root, RenderBaseDir, rel),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after clean", p)
		}
	}

	// Idempotent: cleaning a never-rendered path is not an error.
	if err := CleanSideRenders(root, "never/rendered.go"); err != nil {
		t.Errorf("CleanSideRenders on missing files: %v", err)
	}
}
