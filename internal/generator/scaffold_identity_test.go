package generator

import (
	"os"
	"path/filepath"
	"testing"
)

// identity.k is the project's from birth: a second scaffold keeps its edits.
func TestScaffoldIdentityKCL_IsWriteIfAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := ScaffoldIdentityKCL(dir, "shop"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "deploy", "kcl", "identity.k")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("not written: %v", err)
	}
	if err := os.WriteFile(path, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ScaffoldIdentityKCL(dir, "shop"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "# mine\n" {
		t.Errorf("an edited identity.k was overwritten:\n%s", b)
	}
}
