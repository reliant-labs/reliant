package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWriteFileCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	if err := WriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "two" {
		t.Fatalf("got %q", got)
	}
	if left := tempFiles(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestWriteFileMissingParentFails(t *testing.T) {
	if err := WriteFile(filepath.Join(t.TempDir(), "no", "such", "f"), nil, 0o644); err == nil {
		t.Fatal("want error for a missing parent directory, as os.WriteFile gives")
	}
}

func TestWriteFileKeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	p := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want the existing 0755 preserved", fi.Mode().Perm())
	}
}

func TestWriteFileNewFileHonoursPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	p := filepath.Join(t.TempDir(), "secret")
	if err := WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteFileWritesThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(real, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a regular file")
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Fatalf("target = %q, want the write to land on the link's target", got)
	}
}

// A crash between "start writing" and "finish" must leave the old bytes. The
// rename is the commit point, so failing it models every earlier failure too.
func TestFailedRenameLeavesOldContentAndNoTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "middleware_gen.go")
	if err := os.WriteFile(p, []byte("complete old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected: process died before rename")
	orig := renameFile
	renameFile = func(string, string) error { return boom }
	t.Cleanup(func() { renameFile = orig })

	if err := WriteFile(p, []byte("new content"), 0o644); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "complete old content" {
		t.Fatalf("target = %q, want the old content untouched", got)
	}
	if left := tempFiles(t, dir); len(left) != 0 {
		t.Fatalf("temp file left behind after a failed write: %v", left)
	}
}

func TestFailedFsyncNeverReplacesTarget(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected: fsync failed")
	orig, origRename := syncFile, renameFile
	syncFile = func(*os.File) error { return boom }
	renameFile = func(string, string) error { t.Fatal("renamed after a failed fsync"); return nil }
	t.Cleanup(func() { syncFile, renameFile = orig, origRename })

	if err := WriteFile(p, []byte("new"), 0o644); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "old" {
		t.Fatalf("target = %q", got)
	}
	if left := tempFiles(t, dir); len(left) != 0 {
		t.Fatalf("temp files: %v", left)
	}
}

// The new bytes are on disk, complete, in the temp file at the moment of the
// rename — never a prefix.
func TestTempFileIsCompleteAtRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	orig := renameFile
	var seen string
	renameFile = func(oldPath, newPath string) error {
		b, _ := os.ReadFile(oldPath)
		seen = string(b)
		return orig(oldPath, newPath)
	}
	t.Cleanup(func() { renameFile = orig })
	payload := strings.Repeat("generated line\n", 5000)
	if err := WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if seen != payload {
		t.Fatalf("temp held %d bytes at rename, want %d", len(seen), len(payload))
	}
}
