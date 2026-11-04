// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// procRoot is where scanProcesses reads processes from.
const procRoot = "/proc"

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat's start time: 100 on
// every Linux architecture Go supports.
const clockTicks = 100

// scanProcesses reads every live process's references to work directories
// and the start times of the go commands and linkers (procSnapshot). A
// process whose descriptors or environment are not readable (another user's)
// contributes only what /proc shows of it to everyone: its command line, and
// its start time when it is a go command or a linker.
func scanProcesses() (procSnapshot, error) {
	boot, err := bootTime()
	if err != nil {
		return procSnapshot{}, err
	}
	entries, err := os.ReadDir(procRoot)
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
		dir := filepath.Join(procRoot, e.Name())
		comm, start, ok := readStat(dir)
		if !ok {
			continue // exited
		}
		scanned++
		at := boot.Add(time.Duration(start) * time.Second / clockTicks)
		switch comm {
		case "go":
			s.goStarts = append(s.goStarts, procStart{PID: pid, Comm: comm, Start: at})
		case "link":
			s.linkStarts = append(s.linkStarts, procStart{PID: pid, Comm: comm, Start: at})
		}
		for _, l := range []string{"cwd", "root", "exe"} {
			if t, err := os.Readlink(filepath.Join(dir, l)); err == nil {
				s.addRefs(t)
			}
		}
		if fds, err := os.ReadDir(filepath.Join(dir, "fd")); err == nil {
			for _, fd := range fds {
				if t, err := os.Readlink(filepath.Join(dir, "fd", fd.Name())); err == nil {
					s.addRefs(t)
				}
			}
		}
		for _, f := range []string{"cmdline", "environ", "maps"} {
			if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
				s.addRefs(string(b))
			}
		}
	}
	if scanned == 0 {
		return procSnapshot{}, errors.New("no process readable in " + procRoot)
	}
	return s, nil
}

// readStat reads a process's command name and start time (clock ticks since
// boot) from /proc/<pid>/stat.
func readStat(dir string) (comm string, start uint64, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return "", 0, false
	}
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return "", 0, false
	}
	fields := bytes.Fields(b[end+1:])
	// Fields after the name start at field 3 (state); starttime is field 22.
	if len(fields) < 20 {
		return "", 0, false
	}
	start, err = strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return string(b[open+1 : end]), start, true
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
