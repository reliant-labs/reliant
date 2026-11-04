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
// files and mapped files. Unreadable state is a hold, never absence, except for
// an undumpable process the daemon accounts for or a known agent that never
// holds a worktree (judgeUninspectable). This is a fresh observation, NOT an
// OS write freeze; callers still re-inventory under daemon admission
// immediately before removal.
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
procs:
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
				if processDone(base) {
					continue procs
				}
				if err := judgeUninspectable(ctx, base, entry.Name(), name, roots, err); err != nil {
					return err
				}
				continue procs
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
			if processDone(base) {
				continue
			}
			if err := judgeUninspectable(ctx, base, entry.Name(), "fd", roots, err); err != nil {
				return err
			}
			continue
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
			// safe for that one fd; permission/other failures are uncertainty
			// unless the process has gone or is inside exit, whose teardown
			// makes its descriptors read EACCES.
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				if processDone(base) {
					continue procs
				}
				if err := judgeUninspectable(ctx, base, entry.Name(), "fd "+fd.Name(), roots, err); err != nil {
					return err
				}
				continue procs
			}
			if processPathUses(target, roots) {
				return fmt.Errorf("workspace open by process %s", entry.Name())
			}
		}
		maps, err := os.Open(filepath.Join(base, "maps"))
		if err != nil {
			if processDone(base) {
				continue
			}
			if err := judgeUninspectable(ctx, base, entry.Name(), "maps", roots, err); err != nil {
				return err
			}
			continue
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
			// A task that exits mid-read drops its mm: the read fails ESRCH.
			if processDone(base) {
				continue
			}
			return fmt.Errorf("process %s mappings unreadable: %w", entry.Name(), scanErr)
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

// processExiting reports whether base is a single-threaded process that has
// entered exit: stat's flags carry PF_EXITING and its group has one thread.
// A multi-threaded group is not proven: its leader can exit while other
// threads run on, and the leader's links then read ENOENT too. Unreadable or
// short stat is not proof either.
func processExiting(base string) bool {
	data, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil || len(data) > 1<<20 {
		return false
	}
	pos := strings.LastIndexByte(string(data), ')')
	if pos < 0 {
		return false
	}
	// Fields after the comm, from field 3 (state): flags is field 9,
	// num_threads field 20.
	fields := strings.Fields(string(data[pos+1:]))
	if len(fields) < 18 {
		return false
	}
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		return false
	}
	return flags&pfExiting != 0 && fields[17] == "1"
}

// pfExiting is the kernel's PF_EXITING task flag (include/linux/sched.h).
const pfExiting = 0x00000004

func processGone(base string) bool { _, err := os.Stat(base); return os.IsNotExist(err) }

// processDone reports whether an unreadable piece of base's state is explained
// by the process having gone, or being a single-threaded task inside exit.
// Exit drops the task's mm and fs (cwd and exe read ENOENT, a maps read fails
// ESRCH) and tears down its descriptors (fd links read EACCES) before it is a
// zombie; such a task never touches the filesystem again. PF_EXITING stays set
// on the zombie, so this holds through reaping. Anything else unreadable holds.
func processDone(base string) bool { return processGone(base) || processExiting(base) }
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
