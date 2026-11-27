// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/logging"
)

// cgroupFS is where cgroup v2 is mounted.
const cgroupFS = "/sys/fs/cgroup"

// cmdPrefix names the cgroups this package creates: cmd-<daemon pid>-<seq>.
const cmdPrefix = "cmd-"

// trackLive: the daemon's trees are listed for internal/procguard, which
// prunes the list on each /proc scan.
const trackLive = true

// commandsName names the cgroup, in the delegated directory, that holds every
// command cgroup and bounds them together.
const commandsName = "commands"

// New builds a limiter for this process: thread limits sized to this machine
// (Size), and the delegated cgroup subtree when there is one.
func New(cfg Config) *Limiter {
	l := &Limiter{}
	var rl unix.Rlimit
	nproc := 0
	// RLIMIT_NPROC does not apply to root.
	if unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur != unix.RLIM_INFINITY && os.Geteuid() != 0 {
		nproc = int(min(rl.Cur, 1<<31))
		l.nproc = uint64(float64(rl.Cur) * CommandsShare)
	}
	l.size = Size(runtime.NumCPU(), threadBudget(nproc, threadsMax()), cfg.CommandThreads)
	switch cfg.CgroupRoot {
	case "-":
		l.why = "per-command cgroups are switched off"
	case "":
		l.root, l.why = delegatedRoot()
	default:
		l.root = cfg.CgroupRoot
	}
	if l.root == "" {
		return l
	}
	// Leftovers of an earlier daemon, made before the commands cgroup
	// existed or in it. Never this process's own: another limiter of it may
	// hold cgroups prepared for commands not yet started (reliant #2826).
	sweep(l.root, false)
	commands, err := makeCommandsCgroup(l.root, commandsName, l.size.Shared)
	if err != nil {
		l.commands = l.root
		l.why = fmt.Sprintf("no limit on all commands together: %v", err)
		return l
	}
	l.commands = commands
	sweep(l.commands, false)
	if err := enableController(l.root, l.commands, "cpu"); err != nil {
		logging.Warn("proctree: cannot enable the cpu controller for command cgroups; heavy commands are bounded by GOMAXPROCS alone", "error", err)
	}
	// Separately from cpu: a machine that delegates one and not the other
	// keeps the one it has. RELIANT_HEAVY_MEMORY_HIGH=off does NOT undo
	// controller adoption; see dev-docs/heavy-bounds-rollout.md for the
	// daemon rollback and hierarchy recreation required for that.
	if err := enableController(l.root, l.commands, "memory"); err != nil {
		logging.Warn("proctree: cannot enable the memory controller for command cgroups; heavy commands run without a memory bound", "error", err)
	}
	return l
}

// enableController enables controller ctl for the command cgroups. With cpu,
// a heavy command's slot wrapper can enable it in turn for a cgroup it makes
// under its command cgroup and cap that at its CPU share (cpu.max), and can
// lower its command cgroup's cpu.weight against the other commands'; with
// memory, it can bound its command cgroup's memory.high (internal/heavyjob).
// Both levels hold no process of their own (the daemon is in root's self/),
// as cgroup v2 requires.
func enableController(root, commands, ctl string) error {
	for _, dir := range []string{root, commands} {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+"+ctl), 0); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}

// threadsMax is kernel.threads-max, the machine's limit on threads of every
// user; 0 when unreadable.
func threadsMax() int {
	data, err := os.ReadFile("/proc/sys/kernel/threads-max")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

// makeCommandsCgroup makes (or reuses) the cgroup name under root that holds
// every command cgroup: the pids controller enabled for its children, and
// pids.max shared (no limit when 0). It holds no process of its own, which
// cgroup v2's no-internal-process rule requires of a cgroup whose children
// have controllers. Creating it needs nothing beyond what creating a command
// cgroup in root needs: it is the delegatee's own.
func makeCommandsCgroup(root, name string, shared int) (string, error) {
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+pids"), 0); err != nil {
		return "", fmt.Errorf("cannot enable the pids controller under %s: %w", dir, err)
	}
	limit := "max"
	if shared > 0 {
		limit = strconv.Itoa(shared)
	}
	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(limit), 0); err != nil {
		return "", fmt.Errorf("cannot set %s/pids.max: %w", dir, err)
	}
	return dir, nil
}

// delegatedRoot is the cgroup directory command cgroups go in: the parent of
// this process's own cgroup, when it is delegated to this user (writable, with
// the pids controller enabled for its children). The parent and not this
// process's own cgroup, because cgroup v2's no-internal-process rule forbids
// enabling controllers for the children of a cgroup that has processes — the
// daemon's. The cloud entrypoint sets this up: the daemon in /daemon/self,
// /daemon delegated (docker/workspace-docker-entrypoint.sh in control-plane).
// A systemd unit with Delegate=yes and DelegateSubgroup=self makes the same
// layout, but without pids enabled; delegatedDir enables it.
func delegatedRoot() (dir, why string) {
	data, err := os.ReadFile("/proc/self/cgroup")
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
	parent := filepath.Dir(own)
	if own == "/" || parent == "/" {
		return "", fmt.Sprintf("the daemon's cgroup %s has no delegated parent", own)
	}
	return delegatedDir(filepath.Join(cgroupFS, parent))
}

// delegatedDir is delegatedRoot's verdict on dir, the parent of the daemon's
// cgroup: dir itself when it is delegated to this user and enables the pids
// controller for its children. A delegated dir that does not yet enable it
// gets it enabled here. systemd leaves a delegated unit's subtree_control
// empty (Delegate=yes, DelegateSubgroup=self), so a self-hosted daemon's
// unit would otherwise need a step of its own to write it. The write is
// refused (EBUSY) while dir itself holds a process, such as one a previous
// run of the unit left behind; dir is then not used, as before.
func delegatedDir(dir string) (string, string) {
	ctl, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return "", fmt.Sprintf("cgroup %s is not readable: %v", dir, err)
	}
	for _, p := range []string{dir, filepath.Join(dir, "cgroup.procs")} {
		if err := unix.Access(p, unix.W_OK); err != nil {
			return "", fmt.Sprintf("cgroup %s is not delegated to this user (%s: %v)", dir, filepath.Base(p), err)
		}
	}
	if enablesPids(ctl) {
		return dir, ""
	}
	if err := enableSubtreePids(dir); err != nil {
		return "", fmt.Sprintf("cgroup %s does not enable the pids controller for its children, and enabling it failed: %v", dir, err)
	}
	if ctl, err = os.ReadFile(filepath.Join(dir, "cgroup.subtree_control")); err != nil || !enablesPids(ctl) {
		return "", fmt.Sprintf("cgroup %s does not enable the pids controller for its children", dir)
	}
	logging.Info("proctree: enabled the pids controller for the children of the daemon's delegated cgroup", "dir", dir)
	return dir, ""
}

// enablesPids reports whether a cgroup.subtree_control's content lists pids.
func enablesPids(ctl []byte) bool {
	return strings.Contains(" "+strings.TrimSpace(string(ctl))+" ", " pids ")
}

// enableSubtreePids enables the pids controller for dir's children. A var so
// a test can run delegatedDir over plain files.
var enableSubtreePids = func(dir string) error {
	return os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+pids"), 0)
}

func (t *Tree) prepareCgroup(cmd *exec.Cmd, seq uint64) {
	l := t.l
	// A command entering new namespaces (credsandbox) is left in the
	// daemon's cgroup: clone3 into a cgroup together with CLONE_NEWUSER is
	// not a combination worth risking on a command that must start. It gets
	// the RLIMIT_NPROC reserve instead.
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Cloneflags != 0 {
		l.sweepOwn()
		return
	}
	dir := filepath.Join(l.commands, fmt.Sprintf("%s%d-%d", cmdPrefix, os.Getpid(), seq))
	if err := l.hold(dir); err != nil {
		logging.Warn("proctree: cannot create a command cgroup; the command runs bounded only by RLIMIT_NPROC", "dir", dir, "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(strconv.Itoa(l.size.PerCommand)), 0); err != nil {
		logging.Warn("proctree: cannot set pids.max; the command runs bounded only by RLIMIT_NPROC", "dir", dir, "error", err)
		l.drop(dir)
		return
	}
	if l.commands != l.root {
		t.sharedRefused, _ = refusals(l.commands)
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logging.Warn("proctree: cannot open a command cgroup; the command runs unbounded", "dir", dir, "error", err)
		l.drop(dir)
		return
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = fd
	t.dir, t.fd = dir, fd
}

// reattachCgroup takes back dir as the tree's cgroup (Limiter.Reattach): only
// a cgroup v2 directory named cmd-<this pid>-<n>, which this process made
// before its exec, and only while it still holds a process (an empty one is
// the sweep's to remove). The sequence moves past n, so this image never
// names a new command's cgroup after one still in use.
func (t *Tree) reattachCgroup(dir string) {
	rest, ok := strings.CutPrefix(filepath.Base(dir), fmt.Sprintf("%s%d-", cmdPrefix, os.Getpid()))
	if !ok || !filepath.IsAbs(dir) || !strings.HasPrefix(dir, "/sys/fs/cgroup/") {
		return
	}
	seq, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return
	}
	for {
		cur := cgroupSeq.Load()
		if cur >= seq || cgroupSeq.CompareAndSwap(cur, seq) {
			break
		}
	}
	if populated(dir) {
		t.dir = dir
	}
}

// cgroupVanished reports whether err, from starting the tree's command, is
// the kernel refusing the clone into the tree's cgroup because the cgroup was
// removed: ENOENT for a removed cgroup, ENODEV for one dying, and the cgroup
// directory gone. A missing program or working directory is ENOENT too, but
// leaves the cgroup in place.
func (t *Tree) cgroupVanished(err error) bool {
	if t == nil || t.dir == "" {
		return false
	}
	if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ENODEV) {
		return false
	}
	_, statErr := os.Stat(t.dir)
	return errors.Is(statErr, os.ErrNotExist)
}

// recreateCgroup gives the tree a new cgroup, under a new name, in place of
// the one that vanished before its command started (cgroupVanished). One that
// cannot be made leaves the command uncgrouped, as Prepare does.
func (t *Tree) recreateCgroup(cmd *exec.Cmd, err error) {
	gone := t.dir
	t.closeFD()
	t.dir = ""
	cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = false, 0
	t.l.drop(gone)
	t.prepareCgroup(cmd, cgroupSeq.Add(1))
	logging.Warn("proctree: a command's cgroup was removed before the command started in it; starting it again in a new one",
		"chat_id", t.owner.ChatID, "command", t.owner.Command, "removed", gone, "cgroup", t.dir, "error", err)
}

// The command cgroups of the trees prepared and not yet released, which every
// sweep of this process's empty command cgroups leaves alone: a tree's cgroup
// is empty from Prepare until its command starts in it, and a start into a
// removed cgroup fails ENOENT, which os/exec reports as "fork/exec <path>: no
// such file or directory" (reliant #2416). Process-wide, like the names
// (cgroupSeq): a second limiter's sweep must spare the first's holds too
// (reliant #2826).
var (
	heldMu sync.Mutex
	held   = map[string]struct{}{}
)

// hold sweeps this process's empty command cgroups and creates dir, held
// until drop: under one lock, so a concurrent Prepare's sweep can never take
// a cgroup between its creation and its command's start.
func (l *Limiter) hold(dir string) error {
	heldMu.Lock()
	defer heldMu.Unlock()
	sweep(l.commands, true)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	held[dir] = struct{}{}
	return nil
}

// drop releases a cgroup hold made, and removes the cgroup once empty. One
// its command left processes in stays until they exit; a later sweep takes it.
func (l *Limiter) drop(dir string) {
	heldMu.Lock()
	delete(held, dir)
	heldMu.Unlock()
	if err := removeCgroup(dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.EBUSY) {
		logging.Debug("proctree: cannot remove a command cgroup", "dir", dir, "error", err)
	}
}

// removeCgroup removes the cgroup dir and, first, the empty cgroups under it:
// a heavy command's slot wrapper makes two in its command's cgroup and removes
// them when the command exits (internal/heavyjob), but a wrapper that was
// killed leaves them, and a cgroup with children cannot be removed. One still
// holding a process stays, and so does dir.
func removeCgroup(dir string) error {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			_ = removeCgroup(filepath.Join(dir, e.Name()))
		}
	}
	return os.Remove(dir)
}

// sweepOwn sweeps this process's empty command cgroups, sparing the held ones.
func (l *Limiter) sweepOwn() {
	heldMu.Lock()
	defer heldMu.Unlock()
	sweep(l.commands, true)
}

func (t *Tree) closeFD() {
	if t.fd >= 0 {
		_ = unix.Close(t.fd)
		t.fd = -1
	}
}

// limitNproc lowers the started command's RLIMIT_NPROC to the limiter's
// share. Applied right after Start, before the command has had a chance to
// fork anything worth the name (a shell is still starting up); everything the
// command forks afterwards inherits it.
func (t *Tree) limitNproc() {
	n := t.l.nproc
	if err := unix.Prlimit(t.pid, unix.RLIMIT_NPROC, &unix.Rlimit{Cur: n, Max: n}, nil); err != nil && !errors.Is(err, unix.ESRCH) {
		logging.Debug("proctree: cannot lower the command's RLIMIT_NPROC", "pid", t.pid, "error", err)
	}
}

// Kill SIGKILLs every process in the tree's cgroup, wherever it went: a member
// that left the process group with setsid is still in the cgroup. A tree with
// no cgroup reports false and kills nothing (the caller kills the group).
//
// It writes cgroup.kill, which the kernel applies to the cgroup and every
// cgroup under it at once (Linux 5.14+). Before 5.14 there is no cgroup.kill,
// and the cgroup is frozen instead and every process in it SIGKILLed by pid
// (freezeAndKill).
func (t *Tree) Kill() bool {
	if t == nil || t.dir == "" {
		return false
	}
	err := writeCgroupFile(filepath.Join(t.dir, cgroupKillFile), "1")
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(t.dir); statErr == nil {
			err = freezeAndKill(t.dir)
			if err == nil {
				return true
			}
		}
	}
	logging.Warn("proctree: cannot kill a command cgroup", "dir", t.dir, "error", err)
	return false
}

// cgroupKillFile is the cgroup file Kill writes. A var so a test can take the
// path a kernel without it takes.
var cgroupKillFile = "cgroup.kill"

// freezeTimeout bounds how long freezeAndKill waits for a cgroup to freeze.
const freezeTimeout = 2 * time.Second

// freezeAndKill SIGKILLs every process in cgroup dir and the cgroups under it
// by pid, while the cgroup is frozen. A frozen process cannot exit or fork, so
// each pid listed stays its process until it is sent its SIGKILL (a fatal
// signal is delivered to a frozen process), and nothing is born after the
// listing to escape it. A cgroup that does not freeze within freezeTimeout is
// thawed and not signalled: its pids would not be pinned.
func freezeAndKill(dir string) error {
	freeze := filepath.Join(dir, "cgroup.freeze")
	if err := writeCgroupFile(freeze, "1"); err != nil {
		return fmt.Errorf("freezing: %w", err)
	}
	defer func() { _ = writeCgroupFile(freeze, "0") }()
	if !awaitFrozen(dir, freezeTimeout) {
		return fmt.Errorf("the cgroup did not freeze within %v", freezeTimeout)
	}
	for _, pid := range cgroupPids(dir) {
		if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			logging.Debug("proctree: cannot SIGKILL a process of a frozen command cgroup", "pid", pid, "error", err)
		}
	}
	return nil
}

// awaitFrozen waits up to d for cgroup dir to report itself frozen.
func awaitFrozen(dir string, d time.Duration) bool {
	giveUp := time.After(d)
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for !cgroupEvent(dir, "frozen") {
		select {
		case <-giveUp:
			return false
		case <-poll.C:
		}
	}
	return true
}

// cgroupPids lists the processes of cgroup dir and of every cgroup under it.
func cgroupPids(dir string) []int {
	var pids []int
	data, _ := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	for _, f := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			pids = append(pids, cgroupPids(filepath.Join(dir, e.Name()))...)
		}
	}
	return pids
}

// writeCgroupFile writes value to an existing cgroup control file. It never
// creates one: a file the kernel does not have reads as os.ErrNotExist.
func writeCgroupFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Populated reports whether any process of the tree's cgroup is alive. False
// for a tree with no cgroup.
func (t *Tree) Populated() bool {
	if t == nil || t.dir == "" {
		return false
	}
	return populated(t.dir)
}

func populated(dir string) bool {
	return cgroupEvent(dir, "populated")
}

// cgroupEvent reports whether cgroup dir's cgroup.events sets key ("populated",
// "frozen"). False when it cannot be read.
func cgroupEvent(dir, key string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+" "); ok {
			return v != "0"
		}
	}
	return false
}

// Release removes the tree's cgroup once it is empty. One the command left
// processes in stays until they exit; the next Prepare sweeps it. What
// LimitNote reports is read first, while the cgroup is still there.
func (t *Tree) Release() {
	if t == nil {
		return
	}
	t.forget()
	t.closeFD()
	defer t.releaseGoTmp()
	if t.dir == "" {
		return
	}
	if !t.noted {
		t.note, t.noted = t.limitNote(), true
		if t.note != "" {
			logging.Warn("proctree: a command was refused threads at a thread limit",
				"chat_id", t.owner.ChatID, "command", t.owner.Command, "cgroup", t.dir,
				"command_limit", t.l.size.PerCommand, "shared_limit", t.l.size.Shared)
		}
	}
	t.l.drop(t.dir)
}

// limitNote reads the refusals at the command's own limit and, made while it
// ran, at the limit all commands share, and words them (limitMessage).
func (t *Tree) limitNote() string {
	if t.dir == "" || t.l == nil {
		return ""
	}
	own, exact := refusals(t.dir)
	if !exact {
		// Before Linux 6.13 a cgroup counts every fork of its own that was
		// refused, at its limit or an ancestor's: which one is not known.
		return limitMessage(t.l.size, refused{either: own})
	}
	var shared uint64
	if t.l.commands != t.l.root {
		if now, _ := refusals(t.l.commands); now > t.sharedRefused {
			shared = now - t.sharedRefused
		}
	}
	return limitMessage(t.l.size, refused{own: own, shared: shared})
}

// refusals is how many forks (threads or processes) the kernel refused at
// dir's own pids.max: pids.events.local's max (Linux 6.13+; pids.events then
// counts the descendants' too). exact is false on an older kernel, which has
// only pids.events, counting instead every fork of dir's own processes that
// was refused at any limit. 0 when neither is readable.
func refusals(dir string) (n uint64, exact bool) {
	exact = true
	data, err := os.ReadFile(filepath.Join(dir, "pids.events.local"))
	if err != nil {
		exact = false
		if data, err = os.ReadFile(filepath.Join(dir, "pids.events")); err != nil {
			return 0, false
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "max "); ok {
			n, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, exact
		}
	}
	return 0, exact
}

// sweep removes this package's empty command cgroups in dir. At start-up
// (!own, New) it takes those of earlier daemons that have exited, though not
// those of a daemon still running (a daemon an agent started for development
// makes its command cgroups beside the ones of the daemon that runs the
// agent), and never this process's own. Afterwards (own) it takes only this
// process's, whose commands may have left processes behind when they were
// released, sparing the held ones: those of trees not yet released, which are
// empty until their command starts. With own, the caller holds heldMu. Each
// cgroup it retires takes its command's GOTMPDIR with it (removePairedGoTmp).
func sweep(dir string, own bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, e := range entries {
		name := e.Name()
		rest, ok := strings.CutPrefix(name, cmdPrefix)
		if !e.IsDir() || !ok {
			continue
		}
		pidText, _, _ := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			continue
		}
		sub := filepath.Join(dir, name)
		if own {
			if _, isHeld := held[sub]; pid != self || isHeld {
				continue
			}
		} else if pid == self || !errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			continue
		}
		if !populated(sub) && removeCgroup(sub) == nil {
			// Retired: none of its command's processes is left, so its
			// GOTMPDIR goes too, if the watcher did not take it.
			removePairedGoTmp(name)
		}
	}
}

// processIdentity reads pid's process group and start time (fields 5 and 22
// of /proc/<pid>/stat), which /proc shows for an undumpable process too.
func processIdentity(pid int) (pgid int, start uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	// The comm may hold spaces and parentheses: fields start after the last ')'.
	pos := bytes.LastIndexByte(data, ')')
	if pos < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(string(data[pos+1:]))
	if len(fields) < 20 {
		return 0, 0, false
	}
	pgid, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return pgid, start, true
}
