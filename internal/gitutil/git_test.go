package gitutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureGitignoreContains_CreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureGitignoreContains(dir, ".reliant.local/"); err != nil {
		t.Fatalf("EnsureGitignoreContains failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	got := string(data)
	want := ".reliant.local/\n"
	if got != want {
		t.Fatalf("unexpected .gitignore content\nwant: %q\ngot:  %q", want, got)
	}
}

func TestEnsureGitignoreContains_IsIdempotent_WithOrWithoutSlash(t *testing.T) {
	dir := t.TempDir()
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("node_modules/\n.reliant.local\n"), 0o644); err != nil {
		t.Fatalf("failed to seed .gitignore: %v", err)
	}

	if err := EnsureGitignoreContains(dir, ".reliant.local/"); err != nil {
		t.Fatalf("EnsureGitignoreContains failed: %v", err)
	}

	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	got := string(data)
	want := "node_modules/\n.reliant.local\n"
	if got != want {
		t.Fatalf("unexpected .gitignore content\nwant: %q\ngot:  %q", want, got)
	}
}
