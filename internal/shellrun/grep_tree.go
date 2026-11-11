package shellrun

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// treeFilter is grep's file selection for a recursive search: --include and
// --exclude match a file's base name, --exclude-dir a directory's, each as a
// filepath.Match glob, as GNU grep does.
type treeFilter struct {
	include, exclude, excludeDir []string
}

func matchesAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, name); ok {
			return true
		}
	}
	return false
}

func (f treeFilter) keepFile(name string) bool {
	if len(f.include) > 0 && !matchesAny(f.include, name) {
		return false
	}
	return !matchesAny(f.exclude, name)
}

func grepTree(root string, filter treeFilter, scan func(io.Reader, string)) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && matchesAny(filter.excludeDir, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !filter.keepFile(d.Name()) {
			return nil
		}
		fh, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		defer func() { _ = fh.Close() }()
		scan(fh, p)
		return nil
	})
}
