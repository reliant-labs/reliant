// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// cpuPeriod is the cpu.max period, cgroup v2's default: 100ms.
const cpuPeriod = 100_000

// cgroupFS is where cgroup v2 is mounted.
const cgroupFS = "/sys/fs/cgroup"

// commandCgroupPrefix names the cgroup the daemon starts each command in
// (internal/proctree's cmdPrefix): cmd-<daemon pid>-<n>.
const commandCgroupPrefix = "cmd-"

// cpuCap is the CPU cap a slotted command runs under: share CPUs as a hard
// cap (cpu.max), so a slotted command cannot use more than its share however
// many threads it starts. GOMAXPROCS bounds the Go processes that honour it,
// and cpu.max bounds everything else — cc1 and ld under cgo, a node build, a
// command that set its own GOMAXPROCS. Past the slots' total, work queues for
// a slot instead of piling onto the run queue, where it timed out and was
// killed (2026-11-03: load 150–320 on 31 CPUs).
//
// The cap goes on a cgroup of the wrapper's OWN making, never on the one it
// runs in: that cgroup's cpu.max is whoever made it's (the daemon's, or a
// test's), and rewriting it could raise a cap it inherited (5 CPUs became 7
// under a wrapper that derived its share from a 5-CPU cgroup). So, in the
// daemon's command cgroup C, the wrapper makes two children:
//
//	C/heavy-<pid>-caller   what ran in C: the shell, this wrapper, the rest of the line
//	C/heavy-<pid>-command  the command, capped
//
// Both are needed because of cgroup v2's no-internal-process rule: C can
// enable the cpu controller for its children (which is what gives the command
// cgroup a cpu.max) only once no process is left in C itself. The command is
// cloned straight into its cgroup (CLONE_INTO_CGROUP), so nothing of it runs
// uncapped. release moves everything back into C and removes both, so C is
// left as the daemon made it.
//
// The cap is never above what the wrapper inherited: the tightest cpu.max on
// the path from C to the root. Where that is already at or below the share,
// the wrapper makes no cgroup at all (the inherited cap binds the command
// already, and a second one equal to it would add nothing), and the share it
// exports as GOMAXPROCS is clamped to it.
//
// Only in the daemon's own command cgroup (cmd-*), which holds this command
// line and nothing else: a wrapper started anywhere else must not move
// whatever shares its cgroup. Where it cannot make the cgroups (cgroup v1, no
// cpu controller enabled for C, no permission) it sets no cap, and the share
// stays GOMAXPROCS alone.
type cpuCap struct {
	// share is the CPUs to export to the command (GOMAXPROCS and
	// UV_THREADPOOL_SIZE): the share asked for, clamped to the inherited cap.
	share int
	// why is why no cap was set; "" when one was.
	why string
	// dir is C; caller and command are the children made in it, "" when
	// none were.
	dir, caller, command string
	// fd is command, opened for CLONE_INTO_CGROUP; -1 when closed.
	fd int
}

// limitCPU caps the command this wrapper is about to start at share CPUs.
func limitCPU(share int) *cpuCap { return limitCPUAt("/proc/self/cgroup", cgroupFS, share) }

// limitCPUAt is limitCPU reading this process's cgroup from selfCgroup and
// the cgroup tree under fsRoot (tests).
func limitCPUAt(selfCgroup, fsRoot string, share int) *cpuCap {
	c := &cpuCap{share: share, fd: -1}
	if share < 1 {
		c.why = "no share"
		return c
	}
	rel, why := ownCgroup(selfCgroup)
	if why != "" {
		c.why = why
		return c
	}
	inherited, capped := inheritedCPUs(fsRoot, rel)
	if capped {
		c.share = min(share, max(1, int(math.Floor(inherited))))
	}
	if !isCommandCgroupName(filepath.Base(rel)) {
		c.why = "not in a daemon command cgroup"
		return c
	}
	if capped && float64(share) >= inherited {
		c.why = fmt.Sprintf("its cgroup is capped at %s CPUs already, no more than its share of %d", strconv.FormatFloat(inherited, 'f', -1, 64), share)
		return c
	}
	if err := c.split(selfCgroup, filepath.Join(fsRoot, rel), rel, share); err != nil {
		c.why = err.Error()
	}
	return c
}

// ownCgroup is this process's cgroup v2 path, relative to the cgroup root,
// from selfCgroup (/proc/self/cgroup's unified "0::" line). why says why
// there is none to use.
func ownCgroup(selfCgroup string) (rel, why string) {
	data, err := os.ReadFile(selfCgroup)
	if err != nil {
		return "", "no /proc/self/cgroup"
	}
	own := ""
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			own = p
		}
	}
	if own == "" {
		return "", "not on cgroup v2"
	}
	// The kernel writes an absolute, clean path; anything else (a relative
	// path, a ".." that would leave the root) is not a cgroup to use. Nor
	// is the root itself.
	rel, ok := strings.CutPrefix(own, "/")
	if !ok || !filepath.IsLocal(rel) || filepath.Clean(own) != own {
		return "", "not in a daemon command cgroup"
	}
	return rel, ""
}

// inheritedCPUs is the tightest cpu.max on the path from the cgroup rel up to
// fsRoot, in CPUs: the most this process may use, whatever its own cgroup
// says. capped is false when nothing on the path is readable and limited
// ("max" is no limit). When rel is not under fsRoot (a cgroup namespace mounts
// the process's cgroup as the root) the walk starts at fsRoot, as
// forge/pkg/hostslot's does.
func inheritedCPUs(fsRoot, rel string) (cpus float64, capped bool) {
	root := filepath.Clean(fsRoot)
	dir := filepath.Join(root, rel)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir = root
	}
	for {
		if n, ok := readCPUMax(filepath.Join(dir, "cpu.max")); ok && (!capped || n < cpus) {
			cpus, capped = n, true
		}
		if dir == root {
			return cpus, capped
		}
		dir = filepath.Dir(dir)
	}
}

// readCPUMax parses a cpu.max ("<quota> <period>", "max <period>" for no
// limit) into CPUs.
func readCPUMax(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) == 0 || f[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseFloat(f[0], 64)
	if err != nil || quota <= 0 {
		return 0, false
	}
	period := float64(cpuPeriod)
	if len(f) > 1 {
		if p, err := strconv.ParseFloat(f[1], 64); err == nil && p > 0 {
			period = p
		}
	}
	return quota / period, true
}

// split makes dir's two children (see cpuCap), moves what runs in dir into
// the caller one, and caps the command one at share CPUs. On failure it
// leaves dir as it found it.
func (c *cpuCap) split(selfCgroup, dir, rel string, share int) (err error) {
	ctl, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return err
	}
	if !hasWord(string(ctl), "cpu") {
		return errors.New("the cpu controller is not enabled for its cgroup")
	}
	unlock, err := lockCgroup(dir)
	if err != nil {
		return err
	}
	defer unlock()
	// Another wrapper of the same line may have split dir while this one
	// waited for the lock, moving this one into its caller cgroup: then dir
	// is that wrapper's to restore, and this command runs under its cgroup.
	if now, _ := ownCgroup(selfCgroup); now != rel {
		return errors.New("its cgroup was split by another heavy command's wrapper")
	}
	// Controllers already enabled for dir's children: dir is structured by
	// something else, which is not this wrapper's to change.
	if sc, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control")); err != nil || strings.TrimSpace(string(sc)) != "" {
		return fmt.Errorf("its cgroup already has controllers for children (%q, %v)", strings.TrimSpace(string(sc)), err)
	}
	pid := os.Getpid()
	caller := filepath.Join(dir, fmt.Sprintf("heavy-%d-caller", pid))
	command := filepath.Join(dir, fmt.Sprintf("heavy-%d-command", pid))
	if err := os.Mkdir(caller, 0o755); err != nil {
		return err
	}
	c.dir, c.caller = dir, caller
	defer func() {
		if err != nil {
			c.restore()
		}
	}()
	if err := os.Mkdir(command, 0o755); err != nil {
		return err
	}
	c.command = command
	// The enable is refused (EBUSY) while dir counts a task: one the line
	// forked between the move and the enable, or one that has exited but
	// is not yet dead to the kernel's count, which cgroup.procs no longer
	// lists (seen on 6.18 beside a busy `go test`). Move again, and retry
	// for a moment.
	for try := 0; ; try++ {
		if err = moveAll(dir, caller); err != nil {
			return err
		}
		if err = enableCPU(dir); err == nil {
			break
		}
		if !errors.Is(err, unix.EBUSY) || try == busyRetries {
			return err
		}
		time.Sleep(busyBackoff)
	}
	if err := writeCgroupFile(command, "cpu.max", fmt.Sprintf("%d %d", share*cpuPeriod, cpuPeriod)); err != nil {
		return err
	}
	fd, err := unix.Open(command, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	c.fd = fd
	return nil
}

// busyRetries and busyBackoff bound how long a cgroup operation refused with
// EBUSY by a task on its way out is retried: 50 x 10ms.
const (
	busyRetries = 50
	busyBackoff = 10 * time.Millisecond
)

// enableCPU enables the cpu controller for dir's children, which gives each
// child a cpu.max. A variable so a test's fake cgroup tree can play the
// kernel's part.
var enableCPU = func(dir string) error { return writeCgroupFile(dir, "cgroup.subtree_control", "+cpu") }

// into makes cmd start in the capped cgroup, and reports whether it will:
// false when there is none.
func (c *cpuCap) into(cmd *exec.Cmd) bool {
	if c.fd < 0 {
		return false
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = c.fd
	return true
}

// release undoes split once the command has exited: everything in the two
// children (the line's processes, and any the command left behind) goes back
// into dir, and both are removed. Best effort: a child it cannot remove is
// removed by the daemon with the command cgroup (internal/proctree).
func (c *cpuCap) release() {
	if c.fd >= 0 {
		_ = unix.Close(c.fd)
		c.fd = -1
	}
	if c.caller == "" {
		return
	}
	if unlock, err := lockCgroup(c.dir); err == nil {
		defer unlock()
	}
	c.restore()
}

// restore is release's work, under the lock: the cpu controller off for
// dir's children first, since dir takes no process back while it is on.
func (c *cpuCap) restore() {
	if c.fd >= 0 {
		_ = unix.Close(c.fd)
		c.fd = -1
	}
	_ = writeCgroupFile(c.dir, "cgroup.subtree_control", "-cpu")
	for _, child := range []string{c.command, c.caller} {
		if child == "" {
			continue
		}
		for try := 0; ; try++ {
			_ = moveAll(child, c.dir)
			err := os.Remove(child)
			if err == nil || errors.Is(err, os.ErrNotExist) || !errors.Is(err, unix.EBUSY) || try == busyRetries {
				break
			}
			time.Sleep(busyBackoff)
		}
	}
	c.caller, c.command = "", ""
}

// moveAll moves every process in cgroup from into cgroup to, until from is
// empty: a process forked while it moves lands in from, and is moved next.
func moveAll(from, to string) error {
	for try := 0; try < 100; try++ {
		data, err := os.ReadFile(filepath.Join(from, "cgroup.procs"))
		if err != nil {
			return err
		}
		pids := strings.Fields(string(data))
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			// One pid per write: the kernel takes one at a time. A
			// process that exited meanwhile is gone, not a failure.
			if err := writeCgroupFile(to, "cgroup.procs", pid); err != nil && !errors.Is(err, unix.ESRCH) {
				return err
			}
		}
	}
	return fmt.Errorf("processes kept appearing in %s", from)
}

// writeCgroupFile writes val to dir's cgroup interface file name. Never
// created: a cgroup interface file the kernel did not make (a cpu.max
// without the cpu controller) means the operation is unavailable, not a file
// to make.
func writeCgroupFile(dir, name, val string) error {
	fh, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	_, werr := fh.WriteString(val)
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// lockCgroup takes an exclusive flock on dir, which serializes the split and
// restore of every wrapper of one command line (`go build ./a & go test ./b`
// in one shell starts two in the same cgroup). Closing the file releases it.
func lockCgroup(dir string) (unlock func(), err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

func hasWord(s, w string) bool {
	for _, f := range strings.Fields(s) {
		if f == w {
			return true
		}
	}
	return false
}

// isCommandCgroupName reports whether name is internal/proctree's command
// cgroup name: cmd-<daemon pid>-<seq>.
func isCommandCgroupName(name string) bool {
	rest, ok := strings.CutPrefix(name, commandCgroupPrefix)
	if !ok {
		return false
	}
	pid, seq, ok := strings.Cut(rest, "-")
	return ok && allDigits(pid) && allDigits(seq)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
