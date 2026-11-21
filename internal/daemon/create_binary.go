package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CreateBinaryFileCommand is the daemon command behind CreateBinaryFile. It is
// a command of its own, not a flag on fs.write_binary_file, because a daemon
// ignores a request field it does not know: an older daemon given an
// "exclusive" flag would overwrite the file. An older daemon given this
// command answers that it does not support it, and writes nothing.
const CreateBinaryFileCommand = "fs.create_binary_file"

// fileExistsMarker is the text of ErrFileExists. Daemon errors cross the wire
// as plain strings, so this text is the contract between the daemon's handler
// and RemoteClient, which turns it back into the sentinel.
const fileExistsMarker = "a file already exists at this path"

var (
	// ErrFileExists is CreateBinaryFile's refusal: something already exists at
	// the path, and it was left untouched.
	ErrFileExists = errors.New(fileExistsMarker)

	// ErrCreateUnsupported says the daemon predates CreateBinaryFile. Nothing
	// was written.
	ErrCreateUnsupported = errors.New("this machine's Reliant is too old to create a file without risking replacing one")
)

// Seams for tests: the failure paths of WriteNewFile cannot be reached on a
// healthy filesystem.
var (
	linkFile = os.Link
	// renameFile is removeIfSame's move aside, renameNoReplace; its tests
	// replace it to act in the instant between its moves.
	renameFile = renameNoReplace
	// restoreFile is renameNoReplace; a test creates a file at the
	// destination in the instant before it runs.
	restoreFile  = renameNoReplace
	writeContent = func(f *os.File, content []byte) error {
		_, err := f.Write(content)
		return err
	}
)

// WriteNewFile creates path with content, creating parent directories as
// needed, and fails with ErrFileExists when anything already exists there.
//
// The content is written to a temporary file in the same directory, which is
// then hard-linked to path. link(2) (CreateHardLink on Windows) refuses an
// existing destination, a symlink included, in the same call that creates it,
// so a file another process creates first is never replaced and no partial
// file is ever visible at path. A failure never has to clean up path, so it
// can never remove a file someone else put there.
//
// On a filesystem without hard links (FAT, some network mounts) it falls back
// to creating path itself with O_CREATE|O_EXCL (CREATE_NEW on Windows), which
// refuses an existing path just as atomically. If that write fails, path is
// removed only while it is still the file this call created.
func WriteNewFile(path string, content []byte) (time.Time, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return time.Time{}, fmt.Errorf("creating parent directories for %s: %w", path, err)
	}
	tmp, tmpInfo, err := writeTempFile(dir, content)
	if err != nil {
		return time.Time{}, fmt.Errorf("writing %s: %w", path, err)
	}
	linkErr := linkFile(tmp, path)
	removeIfSame(tmp, tmpInfo)
	switch {
	case linkErr == nil:
	case errors.Is(linkErr, fs.ErrExist):
		return time.Time{}, fmt.Errorf("%w: %s", ErrFileExists, path)
	default:
		// Any other refusal is most likely a filesystem without hard links.
		// The exclusive create below is just as safe; it only cannot hide a
		// partial file while it writes.
		if err := writeExclusive(path, content); err != nil {
			return time.Time{}, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, nil
	}
	return info.ModTime(), nil
}

// writeTempFile writes content to a new, uniquely named file in dir and
// returns its path and identity. A failed write removes it.
func writeTempFile(dir string, content []byte) (string, os.FileInfo, error) {
	for range 100 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", nil, err
		}
		// A short fixed name, not one derived from path's base: a base near
		// the name-length limit would otherwise fail here and not at path.
		tmp := filepath.Join(dir, ".reliant-new-"+hex.EncodeToString(suffix[:])+".tmp")
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		info, err := writeAndClose(f, content)
		if err != nil {
			if info != nil {
				removeIfSame(tmp, info)
			}
			return "", nil, err
		}
		return tmp, info, nil
	}
	return "", nil, fmt.Errorf("could not pick an unused temporary file name in %s", dir)
}

// writeExclusive creates path with O_EXCL and writes content to it. A failed
// write removes path only while it is still the file this call created.
func writeExclusive(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrFileExists, path)
		}
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if info, err := writeAndClose(f, content); err != nil {
		if info != nil {
			removeIfSame(path, info)
		}
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// writeAndClose writes content to f and closes it. The returned FileInfo is
// f's identity, taken from the open handle, so it names the file this process
// created even when the path no longer does; it is nil only if f could not be
// stat'ed at all.
func writeAndClose(f *os.File, content []byte) (os.FileInfo, error) {
	info, statErr := f.Stat()
	writeErr := writeContent(f, content)
	closeErr := f.Close()
	return info, errors.Join(statErr, writeErr, closeErr)
}

// removeIfSame removes path only if it is still the file described by info.
// A path another process removed, or replaced with a file of its own, is left
// alone.
//
// A remove by pathname cannot be made conditional on what the path names: a
// check and then a Remove leaves a window in which another process can put
// its own file at path, and the Remove deletes that one. So the file is first
// moved, atomically, to a fresh name of this call's own in the same directory;
// only that name is checked and removed. The move is itself a no-replace
// rename, retried under another name when the one picked is taken, so it
// can never replace a file another process put at that name; where no
// no-replace rename exists nothing is moved, and the file is left in place
// rather than risk anyone's. A file that turns out not to be the
// one described by info is put back only by a call that refuses a file that
// has appeared at path since: a hard link, or where there are none, a
// no-replace rename (renameNoReplace). Never a check that path is free and
// then a plain rename, which another process can beat. If neither can put it
// back it stays under the hidden name, intact, rather than being deleted.
func removeIfSame(path string, info os.FileInfo) {
	moved, ok := moveAside(path)
	if !ok {
		return
	}
	if current, err := os.Lstat(moved); err == nil && os.SameFile(current, info) {
		_ = os.Remove(moved)
		return
	}
	if err := linkFile(moved, path); err == nil {
		_ = os.Remove(moved)
		return
	}
	_ = restoreFile(moved, path)
}

// moveAside renames path to a fresh hidden name beside it and returns that
// name. The rename refuses an existing destination (renameNoReplace), so a
// name another process took, even in the instant before the rename, is never
// replaced: another name is tried instead. It reports false when path is
// gone, not ours to move, or no no-replace rename is available.
func moveAside(path string) (string, bool) {
	for range 100 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", false
		}
		moved := filepath.Join(filepath.Dir(path), ".reliant-rm-"+hex.EncodeToString(suffix[:])+".tmp")
		err := renameFile(path, moved)
		if err == nil {
			return moved, true
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", false
		}
	}
	return "", false
}

// classifyCreateBinaryFileError turns the daemon's text for a refused or
// unsupported create back into ErrFileExists or ErrCreateUnsupported, keeping
// the original error as the cause.
func classifyCreateBinaryFileError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, fileExistsMarker):
		return fmt.Errorf("%w: %w", ErrFileExists, err)
	case strings.Contains(msg, CreateBinaryFileCommand) &&
		(strings.Contains(msg, "doesn't support") || strings.Contains(msg, "unknown daemon command type")):
		// The daemon's registry miss: "this machine is running an older
		// version of Reliant (…) that doesn't support \"fs.create_binary_file\"",
		// or, from daemons older still, "unknown daemon command type: …".
		return fmt.Errorf("%w: %w", ErrCreateUnsupported, err)
	}
	return err
}
