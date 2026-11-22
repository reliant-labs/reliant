package repowalk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{
		"a.go", "b/c.go", "b/d/e.go", ".git/HEAD", ".forge/cache.json", "tmp/x", "z/y.go", "z/zz/w.go",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// visits records every call, as root-relative "path" (dirs end in /).
func visits(t *testing.T, walk func(string, fs.WalkDirFunc) error, root string, decide func(rel string, d fs.DirEntry) error) ([]string, error) {
	t.Helper()
	var got []string
	err := walk(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		got = append(got, rel)
		if decide != nil {
			return decide(rel, d)
		}
		return nil
	})
	return got, err
}

// TestWalkDirMatchesFilepathWalkDir: the same callbacks in the same order as
// filepath.WalkDir, for every way a callback steers a walk.
func TestWalkDirMatchesFilepathWalkDir(t *testing.T) {
	stop := errors.New("stop")
	for name, decide := range map[string]func(string, fs.DirEntry) error{
		"everything": nil,
		"skip dot and tmp dirs at the top": func(rel string, d fs.DirEntry) error {
			if d.IsDir() && rel != "./" && (strings.HasPrefix(d.Name(), ".") || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		},
		"skip a nested dir":        func(rel string, _ fs.DirEntry) error { return skipIf(rel == "b/d/", filepath.SkipDir) },
		"SkipDir on a top file":    func(rel string, _ fs.DirEntry) error { return skipIf(rel == "a.go", filepath.SkipDir) },
		"SkipDir on a nested file": func(rel string, _ fs.DirEntry) error { return skipIf(rel == "b/c.go", filepath.SkipDir) },
		"SkipAll deep inside":      func(rel string, _ fs.DirEntry) error { return skipIf(rel == "b/d/e.go", filepath.SkipAll) },
		"SkipAll at the top":       func(rel string, _ fs.DirEntry) error { return skipIf(rel == "b/", filepath.SkipAll) },
		"SkipDir on the root":      func(rel string, _ fs.DirEntry) error { return skipIf(rel == "./", filepath.SkipDir) },
		"an error stops the walk":  func(rel string, _ fs.DirEntry) error { return skipIf(rel == "b/d/", stop) },
	} {
		t.Run(name, func(t *testing.T) {
			root := tree(t)
			want, wantErr := visits(t, filepath.WalkDir, root, decide)
			got, gotErr := visits(t, WalkDir, root, decide)
			if !slices.Equal(got, want) {
				t.Errorf("visits differ from filepath.WalkDir\n got: %v\nwant: %v", got, want)
			}
			if !errors.Is(gotErr, wantErr) && gotErr != wantErr {
				t.Errorf("error = %v, filepath.WalkDir's %v", gotErr, wantErr)
			}
		})
	}
}

func skipIf(cond bool, err error) error {
	if cond {
		return err
	}
	return nil
}

func TestWalkDirReportsAMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	var saw error
	err := WalkDir(missing, func(_ string, _ fs.DirEntry, err error) error {
		saw = err
		return err
	})
	if !errors.Is(err, fs.ErrNotExist) || !errors.Is(saw, fs.ErrNotExist) {
		t.Fatalf("WalkDir(missing) = %v (callback saw %v), want the not-exist error passed through", err, saw)
	}
}
