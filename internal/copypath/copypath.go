// Copyright (c) 2025 Reliant Labs

// Leaf utility package: the exported surface is concrete helpers over the
// stdlib and the OS, with no collaborator to fake and no second
// implementation. An interface here would have exactly one implementor and
// one caller shape, which is indirection without a seam.
//
//forge:exclude-contract: copies named paths between two directory trees; local filesystem only
package copypath

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A workspace's copy_files are EXACT paths, never patterns.
//
// Each entry names one file or directory relative to the source root, and is
// copied to the same relative location under the destination. Nothing is
// searched. The point of the list is to carry over what a fresh checkout does
// not bring — gitignored pieces like .env or node_modules — and a name that
// is searched for has to walk the whole tree to find them, node_modules
// included: that was ~2.6s per repo per name on reliant, on every create, to
// find nothing. A path costs one stat.

// Clean validates one copy_files entry and returns it in canonical slash
// form. It rejects what cannot name a path inside the root: an empty entry,
// an absolute path, and anything that climbs out with "..". A trailing slash
// is accepted ("node_modules/") and dropped.
func Clean(entry string) (string, error) {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return "", errors.New("copy path is empty")
	}
	slashed := filepath.ToSlash(trimmed)
	if strings.HasPrefix(slashed, "/") || filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" {
		return "", fmt.Errorf("copy path %q must be relative to the workspace root, not absolute", entry)
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(slashed)))
	if cleaned == "." {
		return "", fmt.Errorf("copy path %q names the workspace root itself; name a file or directory inside it", entry)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("copy path %q leaves the workspace root", entry)
	}
	return cleaned, nil
}

// CleanAll validates every entry, dropping duplicates while keeping order.
// It stops at the first invalid entry so a typo is reported, not skipped.
func CleanAll(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		cleaned, err := Clean(entry)
		if err != nil {
			return nil, err
		}
		if !seen[cleaned] {
			seen[cleaned] = true
			out = append(out, cleaned)
		}
	}
	return out, nil
}

// Result reports what Copy did with each entry.
type Result struct {
	Copied  []string // entries copied, in request order
	Missing []string // entries absent from the source — skipped, not an error
	Failed  []Failure
}

// Failure is an entry that exists in the source but could not be copied.
type Failure struct {
	Path string
	Err  error
}

// Copy copies each entry from srcRoot to the same relative path under
// dstRoot. Entries must already be validated with Clean. A missing entry is
// recorded and skipped; one entry failing does not stop the rest.
//
// Directories are copied whole, ignored contents and all — copying
// node_modules is a legitimate request. Symlinks are copied AS symlinks,
// never followed: a link that points outside the source must not drag its
// target in, and node_modules/.bin is made of links that stop working if
// they are flattened into copies.
//
// An entry that already exists in the destination — a file the checkout
// itself created — is replaced. The caller asked for the source's version.
func Copy(srcRoot, dstRoot string, entries []string) Result {
	var res Result
	for _, entry := range entries {
		src := filepath.Join(srcRoot, filepath.FromSlash(entry))
		dst := filepath.Join(dstRoot, filepath.FromSlash(entry))

		info, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			res.Missing = append(res.Missing, entry)
			continue
		}
		if err == nil {
			err = copyEntry(src, dst, info)
		}
		if err != nil {
			res.Failed = append(res.Failed, Failure{Path: entry, Err: err})
			continue
		}
		res.Copied = append(res.Copied, entry)
	}
	return res
}

func copyEntry(src, dst string, info fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear destination: %w", err)
	}
	// One clone call for the whole entry — file or directory tree — where the
	// filesystem supports it. Measured on APFS, reliant's web/node_modules
	// (605MB, 42k files): 1.2s cloned whole, 6.6s cloned file by file, 22s
	// byte-copied. Clones also share blocks until written, so a copied
	// node_modules costs almost no disk.
	if cloneTree(src, dst) {
		return nil
	}
	return copyTree(src, dst, info)
}

// copyTree is the portable fallback: a recursive copy that recreates
// symlinks rather than following them and keeps permission bits.
func copyTree(src, dst string, info fs.FileInfo) error {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.Mkdir(dst, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), childInfo); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		return copyFile(src, dst, info.Mode().Perm())
	default:
		// Sockets, FIFOs and devices have no meaningful copy; a dev server's
		// socket left in a copied directory is skipped, not an error.
		return nil
	}
}

func copyFile(src, dst string, perm fs.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
