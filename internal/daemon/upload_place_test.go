package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s holds %q, want %q", path, got, want)
	}
}

func TestValidateUploadDest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(root, "dir", "file.txt"), []byte("x"))
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinks := runtime.GOOS != "windows"
	if symlinks {
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "gitlink")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "inside")); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name     string
		path     string
		ok       bool
		symlinks bool
	}{
		{name: "new file at the root", path: filepath.Join(root, "a.txt"), ok: true},
		{name: "existing file", path: filepath.Join(root, "dir", "file.txt"), ok: true},
		{name: "missing parents", path: filepath.Join(root, "new", "deeper", "a.txt"), ok: true},
		{name: "the root itself", path: root},
		{name: "a directory", path: filepath.Join(root, "dir")},
		{name: "dot-dot out", path: root + string(filepath.Separator) + ".." + string(filepath.Separator) + "x.txt"},
		{name: "another directory", path: filepath.Join(outside, "a.txt")},
		{name: "relative", path: "a.txt"},
		{name: ".git", path: filepath.Join(root, ".git", "config")},
		{name: ".GIT any case", path: filepath.Join(root, "sub", ".GIT", "hooks", "pre-commit")},
		{name: "symlinked dir out of root", path: filepath.Join(root, "escape", "a.txt"), symlinks: true},
		{name: "symlinked dir out of root, missing below", path: filepath.Join(root, "escape", "new", "a.txt"), symlinks: true},
		{name: "symlink into .git", path: filepath.Join(root, "gitlink", "config"), symlinks: true},
		{name: "symlinked dir inside root", path: filepath.Join(root, "inside", "a.txt"), ok: true, symlinks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.symlinks && !symlinks {
				t.Skip("symlinks need privileges on Windows")
			}
			got, err := ValidateUploadDest(root, tc.path)
			if tc.ok {
				if err != nil {
					t.Fatalf("ValidateUploadDest(%s): %v", tc.path, err)
				}
				if got != filepath.Clean(tc.path) {
					t.Fatalf("got %s, want %s", got, filepath.Clean(tc.path))
				}
				return
			}
			if !errors.Is(err, ErrUploadRefusedPath) || !strings.Contains(err.Error(), UploadRefusedPathMarker) {
				t.Fatalf("ValidateUploadDest(%s) = %q, %v; want the refusal", tc.path, got, err)
			}
		})
	}
}

func TestRenameCandidate(t *testing.T) {
	t.Parallel()
	dir := filepath.FromSlash("/w/d")
	for _, tc := range []struct {
		base string
		n    int
		want string
	}{
		{"a.txt", 0, "a.txt"},
		{"a.txt", 1, "a (1).txt"},
		{"a.tar.gz", 2, "a.tar (2).gz"},
		{"Makefile", 3, "Makefile (3)"},
		{".env", 1, ".env (1)"},
	} {
		if got := renameCandidate(filepath.Join(dir, tc.base), tc.n); got != filepath.Join(dir, tc.want) {
			t.Errorf("renameCandidate(%s, %d) = %s, want %s", tc.base, tc.n, got, tc.want)
		}
	}
}

// stagePart writes an upload's finished part file in a staging directory of
// its own, apart from the destination's.
func stagePart(t *testing.T, data []byte) string {
	t.Helper()
	part := filepath.Join(t.TempDir(), "0123.part")
	mustWrite(t, part, data)
	return part
}

func TestPlaceUpload(t *testing.T) {
	t.Parallel()
	upload := []byte("uploaded \x00\xff bytes")
	theirs := []byte("already here")

	t.Run("fail places a new file", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "a.txt")
		got, created, err := PlaceUpload(stagePart(t, upload), dest, UploadConflictFail)
		if err != nil || got != dest || !created {
			t.Fatalf("PlaceUpload = %s, %t, %v", got, created, err)
		}
		assertContent(t, dest, upload)
	})
	t.Run("fail refuses an existing file and leaves it", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "a.txt")
		mustWrite(t, dest, theirs)
		part := stagePart(t, upload)
		_, _, err := PlaceUpload(part, dest, "")
		if !errors.Is(err, ErrFileExists) || !strings.Contains(err.Error(), UploadExistsMarker) {
			t.Fatalf("err = %v, want the exists refusal", err)
		}
		assertContent(t, dest, theirs)
		assertContent(t, part, upload)
		assertOnlyEntries(t, filepath.Dir(dest), "a.txt")
	})
	t.Run("rename takes the first free name", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "a.txt"), theirs)
		mustWrite(t, filepath.Join(dir, "a (1).txt"), theirs)
		got, created, err := PlaceUpload(stagePart(t, upload), filepath.Join(dir, "a.txt"), UploadConflictRename)
		if err != nil || !created || got != filepath.Join(dir, "a (2).txt") {
			t.Fatalf("PlaceUpload = %s, %t, %v", got, created, err)
		}
		assertContent(t, got, upload)
		assertContent(t, filepath.Join(dir, "a.txt"), theirs)
	})
	t.Run("overwrite replaces an existing file", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "a.txt")
		mustWrite(t, dest, theirs)
		got, created, err := PlaceUpload(stagePart(t, upload), dest, UploadConflictOverwrite)
		if err != nil || got != dest || created {
			t.Fatalf("PlaceUpload = %s, %t, %v", got, created, err)
		}
		assertContent(t, dest, upload)
		assertOnlyEntries(t, filepath.Dir(dest), "a.txt")
	})
	t.Run("overwrite creates a missing file", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "a.txt")
		_, created, err := PlaceUpload(stagePart(t, upload), dest, UploadConflictOverwrite)
		if err != nil || !created {
			t.Fatalf("PlaceUpload = %t, %v", created, err)
		}
		assertContent(t, dest, upload)
	})
	t.Run("overwrite refuses a directory", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "d")
		if err := os.Mkdir(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := PlaceUpload(stagePart(t, upload), dest, UploadConflictOverwrite); !errors.Is(err, ErrUploadRefusedPath) {
			t.Fatalf("err = %v, want the refusal", err)
		}
	})
	t.Run("unknown policy", func(t *testing.T) {
		t.Parallel()
		if _, _, err := PlaceUpload(stagePart(t, upload), filepath.Join(t.TempDir(), "a"), "merge"); err == nil {
			t.Fatal("an unknown policy was accepted")
		}
	})
}

// stubLinkAcrossDevices fails a link from part the way link(2) does when part
// is on another filesystem; links between other files still work.
func stubLinkAcrossDevices(t *testing.T, part string) {
	t.Helper()
	orig := linkFile
	linkFile = func(oldname, newname string) error {
		if oldname == part {
			return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
		}
		return orig(oldname, newname)
	}
	t.Cleanup(func() { linkFile = orig })
}

// Not parallel: these replace the linkFile seam.
func TestPlaceUpload_PartOnAnotherFilesystemIsCopied(t *testing.T) {
	upload := []byte("from another disk")
	for _, policy := range []string{UploadConflictFail, UploadConflictRename, UploadConflictOverwrite} {
		t.Run(policy, func(t *testing.T) {
			part := stagePart(t, upload)
			stubLinkAcrossDevices(t, part)
			dest := filepath.Join(t.TempDir(), "a.txt")
			if _, _, err := PlaceUpload(part, dest, policy); err != nil {
				t.Fatalf("PlaceUpload: %v", err)
			}
			assertContent(t, dest, upload)
			assertOnlyEntries(t, filepath.Dir(dest), "a.txt")
		})
	}
	t.Run("fail still refuses an existing file", func(t *testing.T) {
		part := stagePart(t, upload)
		stubLinkAcrossDevices(t, part)
		dest := filepath.Join(t.TempDir(), "a.txt")
		mustWrite(t, dest, []byte("theirs"))
		if _, _, err := PlaceUpload(part, dest, UploadConflictFail); !errors.Is(err, ErrFileExists) {
			t.Fatalf("err = %v, want the exists refusal", err)
		}
		assertContent(t, dest, []byte("theirs"))
		assertOnlyEntries(t, filepath.Dir(dest), "a.txt")
	})
}

func TestPlaceUpload_WithoutHardLinks(t *testing.T) {
	upload := []byte("no hard links here")
	for _, policy := range []string{UploadConflictFail, UploadConflictOverwrite} {
		t.Run(policy, func(t *testing.T) {
			stubLinkUnsupported(t)
			dest := filepath.Join(t.TempDir(), "a.txt")
			if _, _, err := PlaceUpload(stagePart(t, upload), dest, policy); err != nil {
				t.Fatalf("PlaceUpload: %v", err)
			}
			assertContent(t, dest, upload)
			assertOnlyEntries(t, filepath.Dir(dest), "a.txt")
		})
	}
	t.Run("fail still refuses an existing file", func(t *testing.T) {
		stubLinkUnsupported(t)
		dest := filepath.Join(t.TempDir(), "a.txt")
		mustWrite(t, dest, []byte("theirs"))
		if _, _, err := PlaceUpload(stagePart(t, upload), dest, UploadConflictFail); !errors.Is(err, ErrFileExists) {
			t.Fatalf("err = %v, want the exists refusal", err)
		}
		assertContent(t, dest, []byte("theirs"))
	})
}

func TestAvailableBytes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("free space is not read on " + runtime.GOOS)
	}
	// A path that does not exist yet reads its nearest existing ancestor.
	free, ok := AvailableBytes(filepath.Join(t.TempDir(), "not", "yet"))
	if !ok || free == 0 {
		t.Fatalf("AvailableBytes = %d, %t", free, ok)
	}
}
