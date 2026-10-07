// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkingDirUnavailable means a session was asked to start in a directory
// its shell cannot start in: one that does not exist, is not a directory, or
// is not an absolute path. See resolveWorkingDir.
var ErrWorkingDirUnavailable = errors.New("terminal working directory unavailable")

// IsWorkingDirUnavailable reports whether err is CreateSession refusing the
// requested working directory.
//
// It also recognises the refusal after it has crossed the daemon transport,
// which carries a command's error as a message only (daemon_router_nats.go
// flattens it into "daemon command ... failed: <message>"). Every refusal's
// message contains the sentinel's text, so that is the fallback.
func IsWorkingDirUnavailable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrWorkingDirUnavailable) ||
		strings.Contains(err.Error(), ErrWorkingDirUnavailable.Error())
}

// resolveWorkingDir returns the directory a new session's shell starts in.
//
// An empty request means the caller has no preference, and the daemon's own
// working directory (or, failing that, $HOME) is the answer.
//
// A directory the caller DID name is used as given or refused with
// ErrWorkingDirUnavailable — never swapped for another. A caller names a
// directory because it means that one: a project checkout, a worktree. This
// used to fall back to $HOME when the directory was missing, which turned "the
// checkout is not on this machine (yet)" into a healthy-looking prompt in the
// wrong place. In prod that is what a project opened while its clone was
// still running got. Only the daemon can see the filesystem, so only the
// daemon can say the directory is missing; staying silent left nobody able to.
func resolveWorkingDir(requested string) (string, error) {
	if requested == "" {
		if wd, err := os.Getwd(); err == nil {
			return wd, nil
		}
		return GetHomeDir(), nil
	}
	// A relative path would resolve against the daemon's own cwd, which no
	// caller can know.
	if !filepath.IsAbs(requested) {
		return "", fmt.Errorf("%w: %s is not an absolute path", ErrWorkingDirUnavailable, requested)
	}
	info, err := os.Stat(requested)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: %s does not exist", ErrWorkingDirUnavailable, requested)
	case err != nil:
		return "", fmt.Errorf("%w: %w", ErrWorkingDirUnavailable, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: %s is not a directory", ErrWorkingDirUnavailable, requested)
	}
	return requested, nil
}
