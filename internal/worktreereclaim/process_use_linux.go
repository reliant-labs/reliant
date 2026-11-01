//go:build linux

package worktreereclaim

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
				if processGone(base) {
					break
				}
				// A task inside exit has dropped its mm and fs, so these
				// links read ENOENT before it is a zombie. It never touches
				// the filesystem again; anything else unreadable holds.
				if os.IsNotExist(err) && processExiting(base) {
					continue procs
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
			if mapsVanished(base, err) {
				continue
			}
			return fmt.Errorf("process mappings unavailable: %w", err)
		}
		mapped, scanErr := mapsUse(ctx, maps, roots)
		closeErr := maps.Close()
		if scanErr != nil {
			// The process exited between the open and the read: the read
			// fails ESRCH (seen under parallel git, where every short-lived
			// child exits mid-scan). Its mappings are gone with it.
			if mapsVanished(base, scanErr) {
				continue
			}
			return scanErr
		}
		if mapped {
			return fmt.Errorf("workspace mapped by process %s", entry.Name())
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return ctx.Err()
}

// mapsUse reports whether any mapping read from maps (a /proc/<pid>/maps
// stream) is a file under roots.
func mapsUse(ctx context.Context, maps io.Reader, roots []string) (bool, error) {
	scanner := bufio.NewScanner(maps)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	lines := 0
	for scanner.Scan() {
		lines++
		if lines > 100000 {
			return false, errors.New("process mapping budget exceeded")
		}
		if err := ctx.Err(); err != nil {
			return false, err
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
			return true, nil
		}
	}
	return false, scanner.Err()
}

// mapsVanished reports whether a failure to open or read base's maps means the
// process has no mappings left, rather than that they could not be read. The
// kernel answers ESRCH for a task that is gone or has dropped its mm on the way
// out of exit, and a task without an mm maps nothing. Any other failure, with
// the process still listed, is uncertainty and holds.
func mapsVanished(base string, err error) bool {
	return errors.Is(err, syscall.ESRCH) || processGone(base)
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
