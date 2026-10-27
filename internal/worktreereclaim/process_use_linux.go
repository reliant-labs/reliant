//go:build linux

package worktreereclaim

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// CheckProcessUse inspects same-owner Linux processes' cwd, executable, open
// files and mapped files. Unreadable state is a hold, never absence. This is a
// fresh observation, NOT an OS write freeze; callers still re-inventory under
// daemon admission immediately before removal.
func CheckProcessUse(ctx context.Context, paths []string) error {
	return checkProcessUseAt(ctx, "/proc", os.Geteuid(), paths)
}
func checkProcessUseAt(ctx context.Context, proc string, uid int, paths []string) error {
	if len(paths) == 0 {
		return errors.New("process-use paths required")
	}
	roots := make([]string, 0, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return errors.New("process-use requires absolute paths")
		}
		roots = append(roots, filepath.Clean(path))
	}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return fmt.Errorf("process enumeration unavailable: %w", err)
	}
	checked := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		checked++
		if checked > 32768 {
			return errors.New("process enumeration budget exceeded")
		}
		base := filepath.Join(proc, entry.Name())
		info, err := os.Stat(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot establish process owner")
		}
		if int(stat.Uid) != uid {
			continue
		}
		// Zombies have no cwd/exe/fds and cannot use the filesystem. Read stat
		// rather than interpreting a readlink failure as proof of zombie status.
		dead, err := processZombie(base)
		if err != nil {
			if processGone(base) {
				continue
			}
			return err
		}
		if dead {
			continue
		}
		for _, name := range []string{"cwd", "exe"} {
			target, err := os.Readlink(filepath.Join(base, name))
			if err != nil {
				if processGone(base) {
					break
				}
				return fmt.Errorf("process %s %s unreadable: %w", entry.Name(), name, err)
			}
			if processPathUses(target, roots) {
				return fmt.Errorf("workspace in use by process %s (%s)", entry.Name(), name)
			}
		}
		if processGone(base) {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(base, "fd"))
		if err != nil {
			if processGone(base) {
				continue
			}
			return fmt.Errorf("process fd enumeration unavailable: %w", err)
		}
		if len(fds) > 65536 {
			return errors.New("process fd budget exceeded")
		}
		for _, fd := range fds {
			// Only private trusted retirement code may pass its exact pinned
			// inspection descriptors. Never exempt a process or caller payload.
			if entry.Name() == strconv.Itoa(os.Getpid()) {
				if n, err := strconv.ParseUint(fd.Name(), 10, 64); err == nil && inspectionFile(ctx, uintptr(n)) {
					continue
				}
			}
			target, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
			// FDs legitimately close between enumeration and readlink. ENOENT is
			// safe for that one fd; permission/other failures are uncertainty.
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("process fd unreadable: %w", err)
			}
			if processPathUses(target, roots) {
				return fmt.Errorf("workspace open by process %s", entry.Name())
			}
		}
		maps, err := os.Open(filepath.Join(base, "maps"))
		if err != nil {
			if processGone(base) {
				continue
			}
			return fmt.Errorf("process mappings unavailable: %w", err)
		}
		scanner := bufio.NewScanner(maps)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		lines := 0
		for scanner.Scan() {
			lines++
			if lines > 100000 {
				maps.Close()
				return errors.New("process mapping budget exceeded")
			}
			if err := ctx.Err(); err != nil {
				maps.Close()
				return err
			}
			line := scanner.Text()
			// First five fields are address/perms/offset/dev/inode; the remainder
			// is a path that may itself contain spaces. Proc escapes newlines.
			for i := 0; i < 5; i++ {
				line = strings.TrimLeft(line, " \t")
				n := strings.IndexAny(line, " \t")
				if n < 0 {
					line = ""
					break
				}
				line = line[n:]
			}
			path := strings.TrimLeft(line, " \t")
			path = strings.ReplaceAll(path, `\012`, "\n")
			if processPathUses(path, roots) {
				maps.Close()
				return fmt.Errorf("workspace mapped by process %s", entry.Name())
			}
		}
		scanErr := scanner.Err()
		closeErr := maps.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return ctx.Err()
}
func processZombie(base string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return false, err
	}
	if len(data) > 1<<20 {
		return false, errors.New("process stat budget exceeded")
	}
	pos := strings.LastIndexByte(string(data), ')')
	if pos < 0 || len(data) < pos+4 {
		return false, errors.New("malformed process stat")
	}
	return data[pos+2] == 'Z' || data[pos+2] == 'X', nil
}
func processGone(base string) bool { _, err := os.Stat(base); return os.IsNotExist(err) }
func processPathUses(path string, roots []string) bool {
	path = strings.TrimSuffix(path, " (deleted)")
	if !filepath.IsAbs(path) {
		return false
	}
	for _, root := range roots {
		if overlapActivityPath(root, filepath.Clean(path)) {
			return true
		}
	}
	return false
}
