package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// A probe is never a file of its env's KCL package: a concurrent render of
// that env lists the package's .k files, and must not find (or lose) ours.
func TestWriteEnvProbe_StaysOutOfTheEnvPackage(t *testing.T) {
	envDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(envDir, "config.k"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := writeEnvProbe(envDir, "config_probe", "import ..config\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := filepath.Glob(filepath.Join(envDir, "*.k")); len(got) != 1 {
		t.Errorf("the env package's files while a probe exists = %v, want only config.k", got)
	}
	if filepath.Dir(filepath.Dir(path)) != envDir {
		t.Errorf("probe %s is not one directory below %s (its `import ..config` would miss)", path, envDir)
	}
	if src, err := os.ReadFile(path); err != nil || string(src) != "import ..config\n" {
		t.Fatalf("probe = %q, %v", src, err)
	}
	cleanup()
	if entries, _ := os.ReadDir(envDir); len(entries) != 1 {
		t.Errorf("cleanup left %v in the env dir", entries)
	}
}
