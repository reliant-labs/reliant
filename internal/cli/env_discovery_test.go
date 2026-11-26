package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListEnvs_FromFilesystem confirms the kcl-dir walker finds every
// directory containing a `main.k` file — sorted alphabetically and
// deduped against ones without main.k.
func TestListEnvs_FromFilesystem(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []string{"dev", "staging", "prod"} {
		envDir := filepath.Join(dir, "deploy", "kcl", env)
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One env dir without a main.k — should be ignored.
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "no-main"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ListEnvs(dir)
	if err != nil {
		t.Fatalf("ListEnvs: %v", err)
	}
	want := []string{"dev", "prod", "staging"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("envs[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestListEnvs_MissingKCLDir returns an empty list (no error) when
// the deploy/kcl dir doesn't exist. Brand-new projects, library-kind
// projects, and projects mid-migration all land here.
func TestListEnvs_MissingKCLDir(t *testing.T) {
	got, err := ListEnvs(t.TempDir())
	if err != nil {
		t.Fatalf("ListEnvs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}
}

// TestEnvExists confirms the boolean variant — true when main.k is
// present, false otherwise.
func TestEnvExists(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "deploy", "kcl", "dev")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, err := EnvExists(dir, "dev")
	if err != nil || !ok {
		t.Errorf("expected dev=true, got (%v, %v)", ok, err)
	}
	ok, err = EnvExists(dir, "nope")
	if err != nil || ok {
		t.Errorf("expected nope=false, got (%v, %v)", ok, err)
	}
}
