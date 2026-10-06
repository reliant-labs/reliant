// Copyright (c) 2025 Reliant Labs
package provetest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// resolvedFile is one named file after validation.
type resolvedFile struct {
	// abs is absolute with every symlinked ancestor resolved, so it is the
	// location a write actually lands on.
	abs string
	// rel is relative to the repository root, slash-separated.
	rel string
	// baseline is the file at the baseline revision, content included.
	baseline fileState
}

// resolveFiles turns the caller's paths into real locations inside the
// workspace, refusing anything a write could escape through.
func resolveFiles(workspace, dir string, paths []string) ([]*resolvedFile, error) {
	if workspace == "" || !filepath.IsAbs(workspace) {
		return nil, fmt.Errorf("provetest: workspace must be an absolute path, got %q", workspace)
	}
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace %s: %w", workspace, err)
	}
	if dir == "" {
		dir = workspace
	}

	seen := make(map[string]bool, len(paths))
	files := make([]*resolvedFile, 0, len(paths))
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			return nil, errors.New("files contains an empty path")
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, abs)
		}
		real, err := realPath(filepath.Clean(abs))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		rel, err := filepath.Rel(realWorkspace, real)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("refusing %s: it resolves to %s, outside the workspace %s. prove_test only touches files inside the workspace", p, real, realWorkspace)
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if part == ".git" {
				return nil, fmt.Errorf("refusing %s: it is inside a .git directory", p)
			}
		}
		if info, statErr := os.Lstat(real); statErr == nil {
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				return nil, fmt.Errorf("refusing %s: it is a symlink. Name the file it points to instead; prove_test does not write through links", p)
			case info.IsDir():
				return nil, fmt.Errorf("refusing %s: it is a directory. Name the individual files your fix changed", p)
			case !info.Mode().IsRegular():
				return nil, fmt.Errorf("refusing %s: it is not a regular file", p)
			}
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", p, statErr)
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		files = append(files, &resolvedFile{abs: real})
	}
	return files, nil
}

// realPath resolves every symlink in path's EXISTING ancestors and re-attaches
// the components that do not exist yet. The final component is not followed:
// a symlinked file is refused by the caller rather than silently written
// through.
func realPath(path string) (string, error) {
	existing := filepath.Dir(path)
	var missing []string
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("no existing ancestor directory for %s", path)
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	parts := append([]string{resolved}, missing...)
	parts = append(parts, filepath.Base(path))
	return filepath.Join(parts...), nil
}

// canonicalCase rewrites rel to the spelling the filesystem stores, for every
// component that exists. On a case-insensitive filesystem (the macOS and
// Windows defaults) "Calc.go" opens calc.go, but git is case-sensitive: it
// would find no Calc.go at baseline, and the proof would DELETE the file for
// the before-run and recreate it under the wrong name. Two proofs spelling one
// file differently would also take two different locks.
//
// A differently-cased entry is adopted only when the filesystem itself
// resolves the given spelling to it, so on a case-sensitive filesystem, where
// calc.go and Calc.go can be two files, nothing is rewritten.
func canonicalCase(top, rel string) string {
	parts := strings.Split(rel, "/")
	dir := top
	for i, part := range parts {
		entries, err := os.ReadDir(dir)
		if err != nil {
			break
		}
		exact, folded := false, ""
		for _, e := range entries {
			if e.Name() == part {
				exact = true
				break
			}
			if strings.EqualFold(e.Name(), part) {
				folded = e.Name()
			}
		}
		if !exact {
			if folded == "" || !sameFile(filepath.Join(dir, part), filepath.Join(dir, folded)) {
				break
			}
			parts[i] = folded
		}
		dir = filepath.Join(dir, parts[i])
	}
	return strings.Join(parts, "/")
}

func sameFile(a, b string) bool {
	ia, errA := os.Lstat(a)
	ib, errB := os.Lstat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// fileState is a file's existence, bytes and identity at one moment.
type fileState struct {
	Exists bool        `json:"exists"`
	Mode   fs.FileMode `json:"mode,omitempty"`
	SHA256 string      `json:"sha256,omitempty"`
	Size   int64       `json:"size,omitempty"`
	// ModTimeNs and Ident (an inode, where the platform has one) identify the
	// write that produced the bytes. Two writes of identical bytes differ
	// here, which is what lets restore tell "still what we wrote" from
	// "someone rewrote it with the same content". Zero means unknown.
	ModTimeNs int64  `json:"mtime_ns,omitempty"`
	Ident     uint64 `json:"ident,omitempty"`
	Content   []byte `json:"-"`
}

// modeBits is the part of a file mode that is snapshot and restored.
const modeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readState reads a file's bytes and identity. A missing file is a valid
// state (Exists false); a symlink or directory where a file was named is an
// error, because nothing safe can be done with it.
func readState(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("%s is no longer a regular file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fileState{}, err
	}
	st := statOf(info)
	st.Content = content
	st.SHA256 = hashBytes(content)
	return st, nil
}

// statState is readState without reading the bytes.
func statState(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	return statOf(info), nil
}

func statOf(info fs.FileInfo) fileState {
	return fileState{
		Exists:    true,
		Mode:      info.Mode() & modeBits,
		Size:      info.Size(),
		ModTimeNs: info.ModTime().UnixNano(),
		Ident:     fileIdent(info),
	}
}

// sameContent compares existence and bytes, ignoring identity.
func sameContent(a, b fileState) bool {
	return a.Exists == b.Exists && (!a.Exists || a.SHA256 == b.SHA256)
}

// statChanged reports whether b was produced by a different write than a.
func statChanged(a, b fileState) bool {
	if a.Exists != b.Exists {
		return true
	}
	if !a.Exists {
		return false
	}
	return a.Size != b.Size || a.ModTimeNs != b.ModTimeNs ||
		(a.Ident != 0 && b.Ident != 0 && a.Ident != b.Ident)
}

// stillWhatWeWrote reports whether cur is the exact write recorded in wrote.
// An entry journalled before its write landed carries no identity, and is
// matched on content alone.
func stillWhatWeWrote(cur, wrote fileState) bool {
	if !sameContent(cur, wrote) {
		return false
	}
	return wrote.ModTimeNs == 0 || !statChanged(cur, wrote)
}

// restoredExactly reports whether cur is byte- and mode-identical to want.
// Windows has no POSIX mode to compare beyond the read-only bit.
func restoredExactly(cur, want fileState) bool {
	if !sameContent(cur, want) {
		return false
	}
	return !cur.Exists || runtime.GOOS == "windows" || cur.Mode == want.Mode
}

// writeAtomic replaces path with content in one rename, so a concurrent reader
// sees the old bytes or the new ones and never a half-written file. The data
// is synced before the rename: a restore must be durable before the journal
// entry that could redo it is deleted.
//
// The modification time is deliberately NOT carried over. Build tools that
// key on mtime (make, some bundlers) just saw the baseline written with a
// newer one; putting the original time back would make them believe nothing
// changed since and keep the baseline build.
func writeAtomic(path string, content []byte, mode fs.FileMode) (err error) {
	dir, base := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, "."+base+".prove-test-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes a rename or create in dir durable. Best effort: not every
// platform can open a directory for syncing.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// writeFileSync writes and syncs a journal file.
func writeFileSync(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// testFilePatterns recognise a test file by the naming conventions of the
// common runners. They feed a WARNING only — the tool never refuses a path on
// its name, it just points out the most likely mistake.
var testFileSuffixes = []string{
	"_test.go", "_test.py", "_spec.rb", "Test.java", "Tests.java", "Test.kt",
	".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".test.mjs", ".test.cjs",
	".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx", ".spec.mjs", ".spec.cjs",
}

func looksLikeTestFile(rel string) bool {
	base := filepath.Base(filepath.FromSlash(rel))
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		return true
	}
	if strings.Contains("/"+rel, "/__tests__/") {
		return true
	}
	for _, suffix := range testFileSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func testFileWarnings(files []*resolvedFile) []string {
	var warnings []string
	for _, f := range files {
		if looksLikeTestFile(f.rel) {
			warnings = append(warnings, fmt.Sprintf(
				"%s looks like a test file. List only the IMPLEMENTATION files of the fix: the test must stay in place for both runs, or the before-run is not running it", f.rel))
		}
	}
	return warnings
}
