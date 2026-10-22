// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultRootFilesCap is the most the files loose at a workspace root may add
// up to for removal to save them instead of holding the workspace. Agents leave
// plans, notes and scratch output there (it is outside every checkout, so git
// never sees it); bigger than this it is probably real data, and a person
// decides.
const DefaultRootFilesCap int64 = 50 << 20

func (r *Reclaimer) rootFilesCap() int64 {
	if r.RootFilesCap > 0 {
		return r.RootFilesCap
	}
	return DefaultRootFilesCap
}

// reclaimedRoot is where saved root files go: <home>/.reliant/reclaimed, beside
// the worktree root, one directory per worktree id.
func (r *Reclaimer) reclaimedRoot() string {
	return filepath.Join(filepath.Dir(r.Root), "reclaimed")
}

// rootFilesUnsavable says why the files outside every checkout cannot be saved
// ("" when they can): only regular files and symlinks can, and only up to cap
// bytes together.
func rootFilesUnsavable(files []string, rootCap int64) string {
	var total int64
	for _, f := range files {
		fi, err := os.Lstat(f)
		switch {
		case err != nil:
			return "one could not be read"
		case fi.Mode()&os.ModeSymlink != 0:
		case fi.Mode().IsRegular():
			total += fi.Size()
		default:
			return "one is not a regular file"
		}
		if total > rootCap {
			return fmt.Sprintf("together over the %d MB that is saved on removal", rootCap>>20)
		}
	}
	return ""
}

// savedRootFiles is the root files copied out, and where each came from.
type savedRootFiles struct {
	dir   string
	pairs [][2]string // original, copy
}

// saveRootFiles copies each file loose at the workspace root into
// <reclaimed>/<worktree id>/<path relative to the root>, and verifies each copy
// byte for byte. A copy that already exists with other content is not
// overwritten: the new one gets a timestamp suffix. Nothing is deleted here.
func (r *Reclaimer) saveRootFiles(w Worktree, files []string) (savedRootFiles, error) {
	var out savedRootFiles
	if len(files) == 0 {
		return out, nil
	}
	out.dir = filepath.Join(r.reclaimedRoot(), sanitizeRefComponent(w.ID))
	for _, src := range files {
		// Lexical, not relTo: the scan walked w.Path without following
		// symlinks, and relTo would resolve a symlink to its target's name.
		rel, err := filepath.Rel(w.Path, src)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return out, fmt.Errorf("%s is not under the workspace", src)
		}
		dst := filepath.Join(out.dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return out, err
		}
		dst, err = copyRootFile(src, dst)
		if err != nil {
			return out, fmt.Errorf("%s: %w", rel, err)
		}
		out.pairs = append(out.pairs, [2]string{src, dst})
	}
	return out, nil
}

// copyRootFile writes a verified copy of src at dst (or at dst plus a suffix
// when dst holds something else) and returns where it landed.
func copyRootFile(src, dst string) (string, error) {
	fi, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(dst); err == nil {
			if cur, err := os.Readlink(dst); err == nil && cur == target {
				return dst, nil
			}
			dst = freeName(dst)
		}
		return dst, os.Symlink(target, dst)
	}
	if di, err := os.Lstat(dst); err == nil {
		if di.Mode().IsRegular() && sameBytes(src, dst) {
			return dst, nil
		}
		dst = freeName(dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".reclaim-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	if !sameBytes(src, dst) {
		return "", fmt.Errorf("the saved copy differs from the original")
	}
	return dst, nil
}

// freeName returns dst with a timestamp inserted, for a name already taken.
func freeName(dst string) string {
	for i := 0; ; i++ {
		cand := fmt.Sprintf("%s.%d", dst, time.Now().UnixNano()+int64(i))
		if _, err := os.Lstat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// removeOriginals deletes each original whose saved copy still matches it. A
// file written to since it was saved stays, and then keeps the workspace.
func (s savedRootFiles) removeOriginals() {
	for _, p := range s.pairs {
		src, dst := p[0], p[1]
		fi, err := os.Lstat(src)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			a, errA := os.Readlink(src)
			b, errB := os.Readlink(dst)
			if errA == nil && errB == nil && a == b {
				_ = os.Remove(src)
			}
			continue
		}
		if fi.Mode().IsRegular() && sameBytes(src, dst) {
			_ = os.Remove(src)
		}
	}
}
