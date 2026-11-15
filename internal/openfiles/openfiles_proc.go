package openfiles

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// procRoot is where Linux mounts the process table.
const procRoot = "/proc"

// deletedSuffix is what the kernel appends to the target of a link whose file
// has been unlinked.
const deletedSuffix = " (deleted)"

// takeProc records one machine-wide snapshot from a Linux procfs mounted at
// root: every process's cwd, root and exe links, every fd link that resolves
// to a path, and the file-backed mappings in maps — the same classes lsof
// lists by default, read from the same source lsof itself reads on Linux, so
// the answer is identical to lsof run as the same user and needs no binary.
//
// Visibility is the kernel's, not ours: an unprivileged user cannot read the
// fds, cwd or maps of another user's process (EACCES). Those processes are
// skipped, exactly as lsof skips them (its routine exit 1), so a path held
// only by another user's process is invisible here as it is to lsof. Run as
// root to see them all.
//
// A process that exits mid-scan (ENOENT, ESRCH) or denies access is skipped.
// Anything else, a context that ends first, or a procfs that yields no paths
// at all is an error: a partial snapshot would report the unreached
// processes as idle.
func takeProc(ctx context.Context, root string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	entries, err := os.ReadDir(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("reading %s: %w", root, err)
	}
	var paths []string
	for _, e := range entries {
		if _, err := strconv.ParseUint(e.Name(), 10, 64); err != nil {
			continue // not a pid: /proc/meminfo, /proc/sys, ...
		}
		if err := scanProcess(ctx, filepath.Join(root, e.Name()), &paths); err != nil {
			return Snapshot{}, err
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Snapshot{}, fmt.Errorf("%s scan did not finish: %w", root, ctxErr)
	}
	if len(paths) == 0 {
		return Snapshot{}, fmt.Errorf("%s reported no open paths (is procfs mounted?)", root)
	}
	return FromPaths(paths), nil
}

// scanProcess appends the paths one process holds. It returns an error only
// for a failure that makes the snapshot untrustworthy; a vanished or
// inaccessible process is skipped without one.
func scanProcess(ctx context.Context, dir string, paths *[]string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s scan did not finish: %w", filepath.Dir(dir), err)
	}
	for _, link := range []string{"cwd", "root", "exe"} {
		target, err := os.Readlink(filepath.Join(dir, link))
		if err != nil {
			if skippable(err) {
				continue
			}
			return err
		}
		appendLinkTarget(paths, target)
	}

	fdDir, err := os.Open(filepath.Join(dir, "fd"))
	if err == nil {
		names, err := fdDir.Readdirnames(-1)
		_ = fdDir.Close()
		for i, name := range names {
			if i%256 == 255 && ctx.Err() != nil {
				return fmt.Errorf("%s scan did not finish: %w", filepath.Dir(dir), ctx.Err())
			}
			target, err := os.Readlink(filepath.Join(dir, "fd", name))
			if err != nil {
				if skippable(err) {
					continue
				}
				return err
			}
			appendLinkTarget(paths, target)
		}
		if err != nil && !skippable(err) {
			return err
		}
	} else if !skippable(err) {
		return err
	}

	maps, err := os.Open(filepath.Join(dir, "maps"))
	if err != nil {
		if skippable(err) {
			return nil
		}
		return err
	}
	defer func() { _ = maps.Close() }()
	scanner := bufio.NewScanner(maps)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// address perms offset dev inode [spaces] pathname — the pathname
		// may itself contain spaces, so split off the five fixed fields.
		parts := strings.SplitN(scanner.Text(), " ", 6)
		if len(parts) < 6 {
			continue
		}
		appendLinkTarget(paths, strings.TrimLeft(parts[5], " "))
	}
	if err := scanner.Err(); err != nil && !skippable(err) {
		return err
	}
	return nil
}

// appendLinkTarget records target when it names a file. Sockets, pipes,
// anonymous inodes and mapping labels ([heap], [stack], socket:[…],
// anon_inode:…) are not paths and are dropped; a trailing " (deleted)" is
// stripped so the directory the process still holds a file in stays covered.
func appendLinkTarget(paths *[]string, target string) {
	target = strings.TrimSuffix(target, deletedSuffix)
	if !strings.HasPrefix(target, "/") {
		return
	}
	*paths = append(*paths, target)
}

// skippable reports whether err means the process is gone or is not ours to
// read — the routine outcomes of scanning a live machine.
func skippable(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.ESRCH) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM)
}
