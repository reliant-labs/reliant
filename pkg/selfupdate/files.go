package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// writeFileAtomic writes data to path through a sibling temp file, so a
// crash leaves the old content or the new, never a torn file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := finishFile(f, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// finishFile sets perm, fsyncs and closes f.
func finishFile(f *os.File, perm os.FileMode) error {
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// copyToTemp copies src into a new temp file in dir named after pattern,
// fsynced and executable, and returns its path. The caller renames or
// removes it.
func copyToTemp(src, dir, pattern string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return "", err
	}
	if err := finishFile(out, 0o755); err != nil {
		_ = os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// backupBinary copies src into dir under a NEW name
// (<base>.<label>.<unixnano>) and returns it. It never removes or overwrites a
// file, so an earlier rollback image survives whatever happens here;
// pruneBackups retires old ones only after a swap is committed. It copies
// rather than hard-links: a link shares the install's inode, so anything that
// later rewrote the install in place (cp over it) would rewrite the backup.
func backupBinary(src, dir, label string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if !safeName(label) {
		label = "unknown"
	}
	base := filepath.Base(src)
	tmp, err := copyToTemp(src, dir, "."+base+".backup-*")
	if err != nil {
		return "", fmt.Errorf("copy %s into %s: %w", src, dir, err)
	}
	defer func() { _ = os.Remove(tmp) }()
	for i := int64(0); ; i++ {
		dst := filepath.Join(dir, fmt.Sprintf("%s.%s.%d", base, label, now.UnixNano()+i))
		// Link, not rename: link fails rather than replace an existing name.
		err := os.Link(tmp, dst)
		if err == nil {
			return dst, syncDir(dir)
		}
		if _, statErr := os.Lstat(dst); errors.Is(err, fs.ErrExist) || statErr == nil {
			continue
		}
		// No hard links on this filesystem. The caller holds the state lock,
		// so nothing else names files here and dst is still free.
		if err := os.Rename(tmp, dst); err != nil {
			return "", fmt.Errorf("save the backup at %s: %w", dst, err)
		}
		return dst, syncDir(dir)
	}
}

// pruneBackups removes every regular file in dir whose path is not in keep.
func pruneBackups(dir string, keep map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !keep[p] && e.Type().IsRegular() {
			_ = os.Remove(p)
		}
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
