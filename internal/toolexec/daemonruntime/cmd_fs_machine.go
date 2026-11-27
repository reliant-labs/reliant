// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// fs.list_machine_dir lists one directory anywhere on this machine for the
// Files tab's machine view (FileSystemService.ListMachineDirectory): the home
// directory, the chat's scratch directory, /tmp, "/". It is a viewer read, so
// it carries the request's workspace root as viewer_root and refuses to list a
// credential store (~/.ssh, ~/.aws, browser profiles, …) exactly as the file
// viewer refuses to open one (daemonpolicy.DenyViewerRead), through symlinks
// too.
//
// A new command rather than a flag on fs.list_dir, as create_binary.go's
// header sets out: a daemon from before it ignores an unknown field and would
// list ~/.ssh with no guard, whereas it answers an unknown command with the
// registry's "update it" refusal, which the server reports as an outdated
// machine. fs.list_dir stays the project picker's, unchanged.
func init() {
	RegisterCommand(fsListMachineDirCommand, handleFSListMachineDir)
}

const fsListMachineDirCommand = "fs.list_machine_dir"

// machineDirEntryCap bounds one listing. A directory with more entries than
// this (a node_modules, /usr/lib) comes back as its first entries in name
// order, marked truncated; the view says so rather than hanging on it.
var machineDirEntryCap = 5000

// errNotADirectory is the refusal for a listing of a file. Its text is the
// contract the server keys on (file_system.ListMachineDirectory), which tells
// the client to open the path as a file instead.
var errNotADirectory = errors.New("not a directory")

type fsListMachineDirRequest struct {
	// Path is absolute, "~" or "~/…" (this machine's home), or empty for the
	// home directory. A relative path is refused: only the server knows the
	// workspace it would be relative to, and it sends it joined.
	Path string `json:"path"`
	// ViewerRoot is the request's workspace root, for the credential guard
	// (fsReadFileRequest.ViewerRoot). Required: this command serves only the
	// viewer.
	ViewerRoot string `json:"viewer_root"`
}

type fsMachineDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// IsDir is true for a directory. Symlinks are name-only entries: their
	// targets are not inspected, since pathname checks race with link swaps.
	IsDir     bool  `json:"is_dir"`
	IsSymlink bool  `json:"is_symlink,omitempty"`
	Size      int64 `json:"size"`
	// Modified is RFC 3339, empty when the entry could not be stat'ed (it
	// vanished mid-listing, or a symlink is dangling and has no target).
	Modified string `json:"modified,omitempty"`
}

type fsListMachineDirResponse struct {
	// Path is the directory listed: absolute and cleaned, symlinks NOT
	// resolved, so the view's breadcrumb shows the path the user went to.
	Path      string              `json:"path"`
	HomeDir   string              `json:"home_dir"`
	Entries   []fsMachineDirEntry `json:"entries"`
	Truncated bool                `json:"truncated,omitempty"`
	viewerGuard
}

func handleFSListMachineDir(ctx context.Context, payload []byte) ([]byte, error) {
	var req fsListMachineDirRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if strings.TrimSpace(req.ViewerRoot) == "" {
		return nil, errors.New("viewer_root is required")
	}
	home, _ := os.UserHomeDir()
	path, err := machinePath(req.Path, home)
	if err != nil {
		return nil, err
	}
	// A confined caller stays inside its grant (a no-op for the unconfined
	// daemon the Files tab reaches), and the shell sandbox's own credential
	// files are refused before anything is opened.
	if _, err := daemonpolicy.ResolveDir(ctx, path); err != nil {
		return nil, err
	}

	// openForRead runs the viewer's credential guard on the pathname, opens
	// it, and checks the open directory is the one the pathname names now,
	// so a symlink swapped to ~/.ssh after the check is refused rather than
	// listed. The listing reads the open directory, never the path again.
	dir, err := openForRead(path, req.ViewerRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", errNotADirectory, path)
	}

	entries, truncated, err := readMachineDir(dir, path, req.ViewerRoot)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fsListMachineDirResponse{
		Path:        path,
		HomeDir:     home,
		Entries:     entries,
		Truncated:   truncated,
		viewerGuard: viewerGuard{true},
	})
}

// machinePath turns a requested path into the absolute, cleaned path to list:
// "" and "~" are the home directory, "~/x" is x in it.
func machinePath(p, home string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home == "" {
			return "", errors.New("this machine has no home directory to resolve ~ against")
		}
		if len(p) <= 2 {
			return filepath.Clean(home), nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute: %s", p)
	}
	return filepath.Clean(p), nil
}

// readMachineDir reads the open directory dir (at path) in name order, up to
// machineDirEntryCap entries. An entry that vanishes or cannot be stat'ed
// mid-listing is still listed, by name; only a failure to read the directory
// itself fails the listing (an unreadable directory: permission denied).
func readMachineDir(dir *os.File, path, viewerRoot string) ([]fsMachineDirEntry, bool, error) {
	des, err := dir.ReadDir(-1)
	if err != nil && len(des) == 0 && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("read dir %s: %w", path, err)
	}
	sortDirEntries(des)
	truncated := false
	if len(des) > machineDirEntryCap {
		des, truncated = des[:machineDirEntryCap], true
	}
	out := make([]fsMachineDirEntry, 0, len(des))
	for _, de := range des {
		full := filepath.Join(path, de.Name())
		e := fsMachineDirEntry{Name: de.Name(), Path: full, IsDir: de.IsDir()}
		var fi fs.FileInfo
		if de.Type()&fs.ModeSymlink != 0 {
			e.IsSymlink = true
			// Never resolve a symlink for metadata. Separate policy and
			// identity checks cannot bind an attacker-mutable pathname to
			// an opened target, even if the link is restored between checks.
			machineSymlinkListed(full)
		} else {
			fi, _ = de.Info()
		}
		if fi != nil {
			e.IsDir = fi.IsDir()
			e.Size = fi.Size()
			e.Modified = fi.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, e)
	}
	return out, truncated, nil
}

// machineSymlinkListed runs when a symlink entry is encountered. Tests replace
// it to swap the target at the point where metadata used to be checked.
var machineSymlinkListed = func(string) {}

// sortDirEntries orders entries by name, as os.ReadDir does (File.ReadDir
// returns directory order).
func sortDirEntries(des []os.DirEntry) {
	slices.SortFunc(des, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
}
