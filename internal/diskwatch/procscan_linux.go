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
	caps, err := readCapEff(filepath.Join(procRoot, "self"))
	if err != nil {
		return procSnapshot{}, fmt.Errorf("reading this process's capabilities: %w", err)
	}
	return scanProcRoot(procRoot, reader{euid: os.Geteuid(), capEff: caps}, boot)
}

// reader is who reads /proc: the user whose processes' references it must
// read (euid), and its effective capability set, which the kernel compares
// with a process's permitted set before it hands over that process's
// references (serviceManager).
type reader struct {
	euid   int
	capEff uint64
}

// scanProcRoot is scanProcesses over the /proc at root, as the reader as.
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
//
// One process is explained rather than failed: this user's own systemd
// service manager (serviceManager), whose references the kernel refuses to
// its own user because it holds a capability the reader lacks. It is known by
// identity, not by what it holds: it starts units and does no work in a
// GOTMPDIR itself, and everything it starts runs in a cgroup of its own, as a
// process this scan reads like any other. So it holds nothing beyond what its
// command line shows, and keeps no directory; it is named in
// procSnapshot.explained.
//
// A pid names a process only while it lives: one that exits mid-read can be
// replaced by another under the same pid, and reads either side of the
// replacement would mix the two. So each process is read between two reads
// of its start time (readProcess), and what it held counts only when both
// name the same process.
func scanProcRoot(root string, as reader, boot time.Time) (procSnapshot, error) {
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
		p, err := readProcessStable(filepath.Join(root, e.Name()), as)
		if errors.Is(err, errProcExited) {
			continue // holds nothing, and no process took its pid
		}
		if err != nil {
			return procSnapshot{}, fmt.Errorf("pid %d: %w", pid, err)
		}
		scanned++
		for n := range p.refs.names {
			if s.names == nil {
				s.names = map[string]bool{}
			}
			s.names[n] = true
		}
		at := boot.Add(time.Duration(p.start) * time.Second / clockTicks)
		if p.explained != "" {
			s.explained = append(s.explained, fmt.Sprintf("pid %d (%s): %s", pid, p.comm, p.explained))
		}
		isGo, isLink := creatorKinds(p.comm, p.exe, p.cmdline)
		if isGo {
			s.goStarts = append(s.goStarts, procStart{PID: pid, Comm: p.comm, Start: at})
		}
		if isLink {
			s.linkStarts = append(s.linkStarts, procStart{PID: pid, Comm: p.comm, Start: at})
		}
	}
	if scanned == 0 {
		return procSnapshot{}, errors.New("no process readable in " + root)
	}
	return s, nil
}

var (
	// errProcExited: the process exited, and no other has its pid.
	errProcExited = errors.New("process exited")
	// errProcReplaced: the process exited and another took its pid while
	// it was being read.
	errProcReplaced = errors.New("pid taken by another process while it was read")
)

// pidReadAttempts bounds how often readProcessStable reads a pid whose
// process keeps being replaced mid-read before the scan gives up.
const pidReadAttempts = 3

// readProcessStable is readProcess, read again when the pid's process was
// replaced mid-read: the replacement is a live process like any other, and
// is judged by a read of its own.
func readProcessStable(dir string, as reader) (procInfo, error) {
	var err error
	for range pidReadAttempts {
		var p procInfo
		if p, err = readProcess(dir, as); !errors.Is(err, errProcReplaced) {
			return p, err
		}
	}
	return procInfo{}, err
}

// procInfo is what one process holds and is, from one read of its /proc.
type procInfo struct {
	comm    string
	start   uint64 // clock ticks since boot
	exe     string
	cmdline []byte
	refs    procSnapshot // its references (names only)
	// explained says why one of the reader's processes whose references the
	// kernel refused holds nothing to read (serviceManager); "" for every
	// other process. refs then has its command line alone.
	explained string
}

// readProcess reads one process's /proc entry at dir, as the reader as: its
// identity (command name and start time) and every reference it holds.
//
// The start time is read first and again last. Between two reads that agree,
// every read was of that one process: its pid is not free for reuse until it
// is reaped, after which its stat is gone or names another start. A read that
// found an entry gone is then of a process that is exiting, whose references
// are going with it. When the second read finds the pid free, the process
// exited (errProcExited) and nothing read counts; when it finds another
// process, the reads may mix the two (errProcReplaced), and the replacement
// must be read afresh.
func readProcess(dir string, as reader) (procInfo, error) {
	comm, start, err := readStat(dir)
	if gone(err) {
		return procInfo{}, errProcExited
	}
	if err != nil {
		// stat is readable by everyone: an unreadable or malformed one may
		// be a go command or linker we would otherwise not see.
		return procInfo{}, err
	}
	p := procInfo{comm: comm, start: start}
	if err := p.read(dir, as); err != nil {
		if gone(err) {
			// Its status or cgroup vanished while a refusal was explained:
			// an exit, or another process taking its pid, only if stat
			// agrees.
			switch _, again, serr := readStat(dir); {
			case gone(serr):
				return procInfo{}, errProcExited
			case serr == nil && again != start:
				return procInfo{}, errProcReplaced
			}
		}
		return procInfo{}, err
	}
	_, again, err := readStat(dir)
	switch {
	case gone(err):
		return procInfo{}, errProcExited
	case err != nil:
		return procInfo{}, err
	case again != start:
		return procInfo{}, errProcReplaced
	}
	return p, nil
}

// read fills in p's command line and, for one of the reader's processes, its
// references. Whatever is gone is skipped: readProcess decides afterwards
// whether that was the process exiting or another taking its pid.
func (p *procInfo) read(dir string, as reader) error {
	euid := as.euid
	// Whose references /proc gives this user is told by who owns them, not
	// the process: a non-dumpable process of this user's (a sandbox,
	// ssh-agent) has root's, and is withheld from its own user too.
	info, err := os.Lstat(filepath.Join(dir, "fd"))
	if incomplete(err) {
		// Any other failure (EACCES under hidepid, an I/O error) hides whose
		// process this is: a /proc we cannot trust.
		return fmt.Errorf("reading fd: %w", err)
	}
	own := err == nil && (euid == 0 || ownerUID(info) == euid)
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if incomplete(err) {
		// cmdline is readable by everyone: an error is a /proc we cannot
		// trust, whoever owns the process.
		return fmt.Errorf("reading cmdline: %w", err)
	}
	p.cmdline = cmdline
	p.refs.addRefs(string(cmdline))
	if own {
		if p.exe, err = readRefs(&p.refs, dir); err != nil {
			if !errors.Is(err, fs.ErrPermission) {
				return fmt.Errorf("(%s): %w", p.comm, err)
			}
			why, xerr := serviceManager(dir, p.comm, as)
			if xerr != nil {
				// The error names the refusal first, as it always has.
				return fmt.Errorf("(%s): %w; %w", p.comm, err, xerr)
			}
			p.exe, p.explained = "", why
		}
	}
	return nil
}

// serviceManager explains a refusal of one of the reader's processes'
// references (EACCES/EPERM): it returns why, and no error, only when the
// process is this user's own systemd service manager, known from what /proc
// shows everyone. All three must hold:
//
//  1. Its status: every uid is the reader's, and its permitted capability
//     set is not a subset of the reader's effective set. That is the kernel's
//     ptrace read check refusing what the uids would grant
//     (cap_ptrace_access_check): a process that may gain a capability its
//     reader lacks keeps its references from its own user. A lingering
//     `systemd --user` holding CAP_WAKE_ALARM is one (reliant#3522).
//  2. Its cgroup (the v2 "0::" line) is exactly
//     /user.slice/user-<uid>.slice/user@<uid>.service/init.scope. systemd
//     puts every unit it starts, and every process those spawn, in a cgroup
//     of its own, so the one process in init.scope is the manager itself.
//  3. Its command name is systemd.
//
// Such a process starts units and does no work of its own in a GOTMPDIR:
// everything it starts lands in another cgroup, as a process the scan reads.
// So it holds nothing beyond what its command line shows.
//
// Any other refusal, any other capability holder included, is unexplained
// (hidepid, an LSM, a /proc we cannot trust) and the error says why. An error
// for which gone is true means status or cgroup vanished: readProcess tells
// an exit from the rest.
func serviceManager(dir, comm string, as reader) (string, error) {
	if err := holdsCapabilityBeyond(dir, as); err != nil {
		return "", err
	}
	u := strconv.Itoa(as.euid)
	want := "/user.slice/user-" + u + ".slice/user@" + u + ".service/init.scope"
	b, err := os.ReadFile(filepath.Join(dir, "cgroup"))
	if err != nil {
		return "", fmt.Errorf("explaining the refusal: reading cgroup: %w", err)
	}
	got, found := "", false
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "0::"); ok {
			got, found = v, true
			break
		}
	}
	if !found {
		return "", errors.New("unexplained refusal: no cgroup v2 entry")
	}
	if got != want {
		return "", fmt.Errorf("unexplained refusal: cgroup %q is not this user's service manager's %q", got, want)
	}
	if comm != "systemd" {
		return "", fmt.Errorf("unexplained refusal: command %q in the service manager's cgroup is not systemd", comm)
	}
	return "this user's service manager (user@" + u + ".service/init.scope)", nil
}

// holdsCapabilityBeyond returns nil when the status at dir shows every uid is
// the reader's and a permitted capability set beyond the reader's effective
// set (serviceManager, rule 1), else an error saying which does not hold.
func holdsCapabilityBeyond(dir string, as reader) error {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return fmt.Errorf("explaining the refusal: reading status: %w", err)
	}
	var uids []string
	prm, havePrm := uint64(0), false
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "Uid":
			uids = strings.Fields(v)
		case "CapPrm":
			if prm, err = strconv.ParseUint(strings.TrimSpace(v), 16, 64); err != nil {
				return fmt.Errorf("explaining the refusal: malformed CapPrm %q", strings.TrimSpace(v))
			}
			havePrm = true
		}
	}
	if len(uids) != 4 || !havePrm {
		return errors.New("explaining the refusal: status has no Uid or CapPrm")
	}
	for _, u := range uids {
		if u != strconv.Itoa(as.euid) {
			return fmt.Errorf("unexplained refusal: uids %v are not all %d", uids, as.euid)
		}
	}
	if prm&^as.capEff == 0 {
		return fmt.Errorf("unexplained refusal: its permitted capabilities %016x are within ours %016x", prm, as.capEff)
	}
	return nil
}

// readCapEff reads the effective capability set from the status at dir
// (/proc/self for this process).
func readCapEff(dir string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	return 0, errors.New("status has no CapEff")
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
