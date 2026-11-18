package daemon

import (
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

// WriteNewFile creates path with content, creating parent directories as
// needed, and fails with ErrFileExists when anything already exists there.
// The existence check and the create are one open with O_CREATE|O_EXCL
// (CREATE_NEW on Windows), so a file another process creates first is never
// replaced, and a symlink at the path is refused rather than followed. A
// failed write removes the file it created, so no partial file is left.
func WriteNewFile(path string, content []byte) (time.Time, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return time.Time{}, fmt.Errorf("creating parent directories for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return time.Time{}, fmt.Errorf("%w: %s", ErrFileExists, path)
		}
		return time.Time{}, fmt.Errorf("creating %s: %w", path, err)
	}
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return time.Time{}, fmt.Errorf("writing %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, nil
	}
	return info.ModTime(), nil
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
