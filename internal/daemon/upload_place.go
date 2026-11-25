package daemon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The placement half of a staged upload (see upload.go): checking where the
// file may go, and moving the finished part file there under a conflict
// policy. The session bookkeeping lives with the command handlers
// (internal/toolexec/daemonruntime); these are the filesystem operations, kept
// here beside WriteNewFile, whose no-replace link they share.

// ErrUploadRefusedPath is the refusal of an upload destination: outside the
// workspace root once symlinks are resolved, inside a .git directory, or a
// directory. Its text is UploadRefusedPathMarker.
var ErrUploadRefusedPath = errors.New(UploadRefusedPathMarker)

// maxRenameCandidates bounds the rename policy's search for a free name:
// "name (1).ext" through "name (1000).ext".
const maxRenameCandidates = 1000

func refusedPath(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUploadRefusedPath}, a...)...)
}

// ValidateUploadDest checks that path may receive an upload into the
// workspace at root, and returns it cleaned.
//
// Both must be absolute. path must lie strictly below root, both as written
// and once the symlinks of its nearest existing ancestor directory are
// resolved and compared with root's own resolution, so a symlinked directory
// inside the workspace cannot carry the file out of it. Components that do not
// exist yet cannot be symlinks, so the ancestor is all there is to resolve. No
// component below root may be .git (in any case, for case-insensitive
// filesystems), either way, and path must not be a directory. Every refusal
// wraps ErrUploadRefusedPath.
//
// The returned path is the cleaned path as written, not its symlink
// resolution: callers map it back to a workspace-relative path against the
// root they sent.
func ValidateUploadDest(root, path string) (string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return "", refusedPath("the workspace root and the destination must be absolute paths")
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, ok := relBelow(root, path)
	if !ok {
		return "", refusedPath("%s is outside the workspace", path)
	}
	if hasGitSegment(rel) {
		return "", refusedPath("%s is inside a .git directory", path)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", refusedPath("the workspace root %s cannot be resolved: %v", root, err)
	}
	ancestor, err := nearestExisting(filepath.Dir(path))
	if err != nil {
		return "", refusedPath("%s: %v", path, err)
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", refusedPath("%s cannot be resolved: %v", ancestor, err)
	}
	realRel, ok := relWithin(realRoot, realAncestor)
	if !ok {
		return "", refusedPath("%s resolves outside the workspace", path)
	}
	if hasGitSegment(realRel) {
		return "", refusedPath("%s resolves inside a .git directory", path)
	}
	if info, err := os.Stat(realAncestor); err != nil || !info.IsDir() {
		return "", refusedPath("%s is not a directory", ancestor)
	}
	if isDirectory(path) {
		return "", refusedPath("%s is a directory", path)
	}
	return path, nil
}

// relBelow is path relative to root when path is strictly below it.
func relBelow(root, path string) (string, bool) {
	rel, ok := relWithin(root, path)
	if !ok || rel == "." {
		return "", false
	}
	return rel, true
}

// relWithin is path relative to root when path is root or below it.
func relWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func hasGitSegment(rel string) bool {
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}

// nearestExisting is dir, or its closest ancestor that exists.
func nearestExisting(dir string) (string, error) {
	p := dir
	for {
		_, err := os.Lstat(p)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		p = parent
	}
}

// isDirectory reports whether path is a directory or a symlink to one.
func isDirectory(path string) bool {
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// PlaceUpload moves the finished part file to dest under policy and returns
// the path the file took and whether it is a new file. dest's directory must
// exist. part is left where it was: on success it may share dest's inode (a
// hard link), so the caller must remove it, and must stop writing to it,
// before anything else.
//
//   - UploadConflictFail ("" too): dest must not exist. part is hard-linked to
//     dest, which link(2) refuses atomically when anything is there, as in
//     WriteNewFile. When part is on another filesystem, or the filesystem has
//     no hard links, it is first copied to a temporary file beside dest and
//     that is linked; failing that, dest is created with O_EXCL and filled.
//     An existing dest is ErrFileExists, and nothing changes.
//   - UploadConflictRename: the same, at dest and then "name (1).ext",
//     "name (2).ext", … until a name is free.
//   - UploadConflictOverwrite: part is linked (or copied) to a temporary file
//     beside dest and renamed over it, so dest is never seen half-written.
//     A directory at dest is refused.
func PlaceUpload(part, dest, policy string) (string, bool, error) {
	switch policy {
	case "", UploadConflictFail:
		if err := placeNoReplace(part, dest); err != nil {
			return "", false, err
		}
		return dest, true, nil
	case UploadConflictRename:
		for i := 0; i <= maxRenameCandidates; i++ {
			candidate := renameCandidate(dest, i)
			err := placeNoReplace(part, candidate)
			if err == nil {
				return candidate, true, nil
			}
			if !errors.Is(err, ErrFileExists) {
				return "", false, err
			}
		}
		return "", false, fmt.Errorf("no free name for %s: it and the next %d numbered names are taken", dest, maxRenameCandidates)
	case UploadConflictOverwrite:
		return placeReplace(part, dest)
	default:
		return "", false, fmt.Errorf("unknown conflict policy %q", policy)
	}
}

// renameCandidate is dest for n == 0, else dest with " (n)" before its
// extension. A dotfile with no other extension (".env") takes the number at
// its end.
func renameCandidate(dest string, n int) string {
	if n == 0 {
		return dest
	}
	dir, base := filepath.Split(dest)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	return filepath.Join(dir, stem+" ("+strconv.Itoa(n)+")"+ext)
}

func existsAt(path string) error {
	return fmt.Errorf("%w: %s", ErrFileExists, path)
}

// placeNoReplace puts part's content at dest only if nothing is there.
func placeNoReplace(part, dest string) error {
	err := linkFile(part, dest)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return existsAt(dest)
	}
	// Most likely part is on another filesystem (EXDEV): copy it beside dest,
	// then link that, which keeps the no-replace check in one call.
	tmp, tmpInfo, err := copyToTemp(filepath.Dir(dest), part)
	if err != nil {
		return fmt.Errorf("copying the upload beside %s: %w", dest, err)
	}
	linkErr := linkFile(tmp, dest)
	removeIfSame(tmp, tmpInfo)
	switch {
	case linkErr == nil:
		return nil
	case errors.Is(linkErr, fs.ErrExist):
		return existsAt(dest)
	}
	// A filesystem without hard links: an exclusive create refuses an
	// existing dest just as atomically.
	return copyExclusive(part, dest)
}

// placeReplace puts part's content at dest, replacing whatever file is there.
func placeReplace(part, dest string) (string, bool, error) {
	if isDirectory(dest) {
		return "", false, refusedPath("%s is a directory", dest)
	}
	_, statErr := os.Lstat(dest)
	created := errors.Is(statErr, fs.ErrNotExist)
	dir := filepath.Dir(dest)
	tmp, tmpInfo, err := linkToTemp(dir, part)
	if err != nil {
		if tmp, tmpInfo, err = copyToTemp(dir, part); err != nil {
			return "", false, fmt.Errorf("copying the upload beside %s: %w", dest, err)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		removeIfSame(tmp, tmpInfo)
		return "", false, fmt.Errorf("replacing %s: %w", dest, err)
	}
	return dest, created, nil
}

// linkToTemp hard-links src to a fresh temporary name in dir.
func linkToTemp(dir, src string) (string, os.FileInfo, error) {
	for range 100 {
		tmp, err := tempName(dir, ".reliant-new-")
		if err != nil {
			return "", nil, err
		}
		err = linkFile(src, tmp)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		info, err := os.Lstat(tmp)
		if err != nil {
			return "", nil, err
		}
		return tmp, info, nil
	}
	return "", nil, fmt.Errorf("could not pick an unused temporary file name in %s", dir)
}

// copyToTemp copies src to a new temporary file in dir. A failed copy removes
// it.
func copyToTemp(dir, src string) (string, os.FileInfo, error) {
	f, tmp, err := createTempFile(dir)
	if err != nil {
		return "", nil, err
	}
	info, err := copyAndClose(f, src)
	if err != nil {
		if info != nil {
			removeIfSame(tmp, info)
		}
		return "", nil, err
	}
	return tmp, info, nil
}

// copyExclusive creates dest with O_EXCL and copies src into it. A failed copy
// removes dest only while it is still the file this call created.
func copyExclusive(src, dest string) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return existsAt(dest)
		}
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	if info, err := copyAndClose(f, src); err != nil {
		if info != nil {
			removeIfSame(dest, info)
		}
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}

// copyAndClose copies the file at src into f and closes f. The FileInfo is f's
// identity, as in writeAndClose.
func copyAndClose(f *os.File, src string) (os.FileInfo, error) {
	info, statErr := f.Stat()
	copyErr := func() error {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(f, in)
		return err
	}()
	closeErr := f.Close()
	return info, errors.Join(statErr, copyErr, closeErr)
}
