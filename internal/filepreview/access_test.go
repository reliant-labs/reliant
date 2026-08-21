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

// An absolute path that already points inside the base must resolve to itself.
// filepath.Join would concatenate it onto the base a second time, producing a
// path that does not exist and, for a write, creating a file in the wrong place.
func TestValidatePathAllowsAbsoluteInsideBase(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "workspace")
	want := filepath.Join(base, "src", "main.go")

	got, err := ValidatePath(base, want)
	if err != nil {
		t.Fatalf("ValidatePath returned error: %v", err)
	}
	if got != want {
		t.Fatalf("ValidatePath = %q, want %q", got, want)
	}
}

// "" and "/" address the workspace root, not the filesystem root. The file
// tree depends on this: it defaults an unset path to "/" to list the top of the
// workspace, so treating "/" as an absolute path outside the base breaks it.
func TestValidatePathTreatsRootAsBase(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "workspace")

	for _, requested := range []string{"", "/"} {
		got, err := ValidatePath(base, requested)
		if err != nil {
			t.Fatalf("ValidatePath(%q) returned error: %v", requested, err)
		}
		if got != base {
			t.Fatalf("ValidatePath(%q) = %q, want %q", requested, got, base)
		}
	}
}

// An absolute path outside the base must be refused outright. Silently rebasing
// it onto the base is the worst of the three options: it neither honours the
// request nor refuses it, and for a write it lands somewhere the caller never
// named.
func TestValidatePathRejectsAbsoluteOutsideBase(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "workspace")
	outside := filepath.Join(string(filepath.Separator), "etc", "passwd")

	got, err := ValidatePath(base, outside)
	if err == nil {
		t.Fatalf("expected absolute path outside base to be rejected, got %q", got)
	}
	if err != ErrAbsolutePathOutsideBase {
		t.Fatalf("error = %v, want %v", err, ErrAbsolutePathOutsideBase)
	}
}
