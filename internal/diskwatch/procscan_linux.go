// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// procRoot is where scanProcesses reads processes from.
const procRoot = "/proc"

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat's start time: 100 on
// every Linux architecture Go supports.
const clockTicks = 100

// scanProcesses reads every live process's references to work directories
// and the start times of the go commands and linkers (procSnapshot).
func scanProcesses() (procSnapshot, error) {
	boot, err := bootTime()
	if err != nil {
		return procSnapshot{}, err
	}
	return scanProcRoot(procRoot, os.Geteuid(), boot)
}

// scanProcRoot is scanProcesses over the /proc at root, as the user euid.
//
// The snapshot is a proof only if it is complete for every process that can
// hold this user's work directories: this user's own (those whose /proc
// references euid owns; every process when euid is root). So a reference
// source of such a process that cannot be read, for any reason but the
// process having exited, fails the whole scan, and the caller falls back to
// its age rule rather than judge from a partial picture. Another user's
// process cannot create this user's work directories and its descriptors are
// not ours to read; nor are a non-dumpable process's, which the kernel hands
// to root (go, the toolchain and what they build and run are dumpable). Such
// a process contributes what /proc shows everyone: its command line and,
// when it is a go command or a linker, its start time.
func scanProcRoot(root string, euid int, boot time.Time) (procSnapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return procSnapshot{}, err
	}
	var s procSnapshot
	scanned := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		// Whose references /proc gives this user is told by who owns them,
		// not the process: a non-dumpable process of this user's (a sandbox,
		// ssh-agent) has root's, and is withheld from its own user too.
		info, err := os.Lstat(filepath.Join(dir, "fd"))
		if gone(err) {
			continue // exited
		}
		if err != nil {
			// Any other failure (EACCES under hidepid, an I/O error) hides
			// whose process this is: a /proc we cannot trust.
			return procSnapshot{}, fmt.Errorf("pid %d: reading fd: %w", pid, err)
		}
		own := euid == 0 || ownerUID(info) == euid
		comm, start, err := readStat(dir)
		if gone(err) {
			continue // exited
		}
		if err != nil {
			// stat is readable by everyone: an unreadable or malformed one
			// may be a go command or linker we would otherwise not see.
			return procSnapshot{}, fmt.Errorf("pid %d: %w", pid, err)
		}
		scanned++
		cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if incomplete(err) {
			// cmdline is readable by everyone: an error is a /proc we
			// cannot trust, whoever owns the process.
			return procSnapshot{}, fmt.Errorf("pid %d: reading cmdline: %w", pid, err)
		}
		s.addRefs(string(cmdline))
		var exe string
		if own {
			if exe, err = readRefs(&s, dir); err != nil {
				return procSnapshot{}, fmt.Errorf("pid %d (%s): %w", pid, comm, err)
			}
		}
		at := boot.Add(time.Duration(start) * time.Second / clockTicks)
		isGo, isLink := creatorKinds(comm, exe, cmdline)
		if isGo {
			s.goStarts = append(s.goStarts, procStart{PID: pid, Comm: comm, Start: at})
		}
		if isLink {
			s.linkStarts = append(s.linkStarts, procStart{PID: pid, Comm: comm, Start: at})
		}
	}
	if scanned == 0 {
		return procSnapshot{}, errors.New("no process readable in " + root)
	}
	return s, nil
}

// readRefs adds every reference one of this user's processes holds to s: its
// working directory, root, executable, open files, mappings and environment.
// It returns the executable's path, and an error when any of them could not
// be read for a reason other than the process having exited.
func readRefs(s *procSnapshot, dir string) (exe string, err error) {
	for _, l := range []string{"cwd", "root", "exe"} {
		t, err := os.Readlink(filepath.Join(dir, l))
		if incomplete(err) {
			return "", fmt.Errorf("reading %s: %w", l, err)
		}
		s.addRefs(t)
		if l == "exe" {
			exe = t
		}
	}
	fds, err := os.ReadDir(filepath.Join(dir, "fd"))
	if incomplete(err) {
		return "", fmt.Errorf("reading fd: %w", err)
	}
	for _, fd := range fds {
		t, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
		if incomplete(err) { // a descriptor closed since the listing is fine
			return "", fmt.Errorf("reading fd %s: %w", fd.Name(), err)
		}
		s.addRefs(t)
	}
	for _, f := range []string{"environ", "maps"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if incomplete(err) {
			return "", fmt.Errorf("reading %s: %w", f, err)
		}
		s.addRefs(string(b))
	}
	return exe, nil
}

// incomplete reports whether err leaves a process's references unknown: any
// error but the process (or one descriptor of it) having gone, which holds
// nothing.
func incomplete(err error) bool {
	return err != nil && !gone(err)
}

// gone reports whether err positively establishes that a process (or one
// descriptor of it) no longer exists: its /proc entry vanished (ENOENT) or the
// kernel reports no such process (ESRCH). Nothing else proves an exit.
func gone(err error) bool {
	return err != nil && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH))
}

// creatorKinds reports whether a process could be a go command or a Go
// linker, from every name /proc gives it: its comm (the executable's file
// name, truncated), its executable's path, and the first two words of its
// command line (the second covers a -toolexec wrapper, which is handed the
// tool's path first). It errs toward yes: a false creator only keeps a
// directory longer.
func creatorKinds(comm, exe string, cmdline []byte) (isGo, isLink bool) {
	names := []string{comm, filepath.Base(strings.TrimSuffix(exe, " (deleted)"))}
	args := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
	for i := 0; i < len(args) && i < 2; i++ {
		// A process that rewrote its title separates words with spaces.
		f := strings.Fields(string(args[i]))
		for j := 0; j < len(f) && j < 2; j++ {
			names = append(names, filepath.Base(f[j]))
		}
	}
	for _, n := range names {
		switch n {
		case "go":
			isGo = true
		case "link":
			isLink = true
		}
	}
	return isGo, isLink
}

// readStat reads a process's command name and start time (clock ticks since
// boot) from /proc/<pid>/stat. A read error is returned as is (gone tells an
// exit from the rest); a malformed stat is an error that is not gone.
func readStat(dir string) (comm string, start uint64, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return "", 0, fmt.Errorf("reading stat: %w", err)
	}
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return "", 0, errors.New("malformed stat: no command name")
	}
	fields := bytes.Fields(b[end+1:])
	// Fields after the name start at field 3 (state); starttime is field 22.
	if len(fields) < 20 {
		return "", 0, fmt.Errorf("malformed stat: %d fields after the command name", len(fields))
	}
	start, err = strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("malformed stat start time: %w", err)
	}
	return string(b[open+1 : end]), start, nil
}

// bootTime is when the clock /proc's start times count from began: now on
// the wall clock minus now on the boot clock.
func bootTime() (time.Time, error) {
	var bt, rt unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &bt); err != nil {
		return time.Time{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &rt); err != nil {
		return time.Time{}, err
	}
	return time.Unix(rt.Unix()).Add(-time.Duration(bt.Nano())), nil
}

// fileBirth is path's birth time, or its change time where the filesystem
// records no birth time (never earlier than its birth); now when neither can
// be read, which is never old enough to remove.
func fileBirth(path string) time.Time {
	var sx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME|unix.STATX_CTIME, &sx); err != nil {
		return time.Now()
	}
	if sx.Mask&unix.STATX_BTIME != 0 {
		return time.Unix(sx.Btime.Sec, int64(sx.Btime.Nsec))
	}
	return time.Unix(sx.Ctime.Sec, int64(sx.Ctime.Nsec))
}
