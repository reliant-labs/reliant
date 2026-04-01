package filepreview

import (
	"path/filepath"
	"testing"
)

func TestValidatePathAllowsDescendant(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "workspace")
	full, err := ValidatePath(base, "src/main.go")
	if err != nil {
		t.Fatalf("ValidatePath returned error: %v", err)
	}
	want := filepath.Join(base, "src", "main.go")
	if full != want {
		t.Fatalf("ValidatePath = %q, want %q", full, want)
	}
}

func TestValidatePathRejectsTraversal(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "workspace")
	_, err := ValidatePath(base, "../secret.txt")
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if err != ErrPathOutsideBase {
		t.Fatalf("error = %v, want %v", err, ErrPathOutsideBase)
	}
}
