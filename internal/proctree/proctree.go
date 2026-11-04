// Copyright (c) 2026 Reliant Labs

// Package proctree bounds how many threads one command the daemon runs may
// have at once, so a runaway command fails alone instead of taking every agent
// on the machine down with it.
//
// Why. On 2026-10-30 a cloud machine hit its process limit (~5,200 bash, load
// ~1,900, fork EAGAIN everywhere) and had to be restarted, killing every
// agent's in-flight work: one agent's test re-ran a CI gate inside itself
// without bound. Every agent's shell runs as the same user, through one
// daemon, so the only limit that applied was the user's RLIMIT_NPROC (7,650
// there) — shared by all of them AND by the daemon. The first command to reach
// it took the rest down.
//
// Threads, not processes. Both limits below count tasks: the cgroup pids
// controller and RLIMIT_NPROC charge every thread, and a Go or Node process
// has about one thread per CPU (31 CPUs: ~35 threads per Go test binary). So
// every limit here is sized in threads, from the machine (Size). The fixed
// 2,000 this package started with, chosen as a process count, is ~57 Go
// processes on a 31-CPU machine and ~140 on an 8-CPU laptop: one `buf
// generate` running ~80 plugins reached it at once on the former.
//
// What. Each command the daemon starts is a Tree:
//
//   - On Linux with a delegated cgroup v2 subtree (the cloud workspace
//     entrypoint delegates the daemon's cgroup's parent to the workspace
//     user), the daemon keeps one commands cgroup beside its own (the layout
//     below), whose pids.max is CommandsShare of the machine's thread budget
//     (the user's RLIMIT_NPROC): the rest is the daemon's reserve, so however
//     many threads agents' commands hold, the daemon can still run
//     shell_kill, report and recover. Each command is born —
//     clone3(CLONE_INTO_CGROUP), so no fork escapes — into a cgroup of its
//     own inside it, sized for a parallel build or test at the machine's full
//     width and at most half the shared limit. A runaway command's next
//     thread fails; no other command's does. The cgroup also lets a kill
//     reach every process the command started, including any that left its
//     process group with setsid (cgroup.kill).
//   - A command that cannot have a cgroup (no delegated subtree, or a
//     command entering new namespaces) has its RLIMIT_NPROC lowered to
//     CommandsShare of the user's instead, applied with prlimit as soon as it
//     has started. RLIMIT_NPROC counts every thread of the user — every other
//     agent's too — so this is no per-command bound, only the daemon's
//     reserve; the cgroups replace it wherever they exist.
//
// The cgroups, on a cloud machine:
//
//	/sys/fs/cgroup/daemon/   delegated; the daemon's cgroup's parent
//	├── self/                the daemon
//	└── commands/            pids.max: Sizing.Shared, all commands together
//	    └── cmd-<pid>-<n>/   pids.max: Sizing.PerCommand, one command
//
// A command that reaches a limit is told so in its own output
// (Tree.LimitNote): which limit, how many threads it refused, and the setting
// that raises it. Without that, all the agent sees is "pthread_create:
// Resource temporarily unavailable" from whichever child lost.
//
// Both degrade to nothing (only the process group) where unavailable — macOS,
// Windows, a cgroup that is not delegated — and never stop a command from
// starting: a broken limiter must never stop work.
//
// A tree whose leader is credsandbox's init is also entered in
// internal/procledger for its lifetime, with the paths it holds: the init is
// undumpable, and the ledger is how a workspace retirement knows what it holds
// open without reading its descriptors (reliant#1998).
//
//forge:exclude-contract: one concrete policy over exec.Cmd values and the daemon's own cgroup v2 subtree; local /proc and /sys only, no second implementation
package proctree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/procledger"
)

// GoTmpBasePrefix and GoTmpPrefix name each command's own GOTMPDIR,
// <temp dir>/reliant-gotmp-<uid>/gotmp-<daemon pid>-<n> (gotmp_linux.go), which
// internal/diskwatch reclaims once nothing holds it.
const (
	GoTmpBasePrefix = "reliant-gotmp-"
	GoTmpPrefix     = "gotmp-"
)

// BaseCommandThreads is the part of one command's thread limit that does not
// grow with the CPU count: the shell, the go command or a node toolchain, a
// headless browser (~200 threads with its renderers).
const BaseCommandThreads = 1024

// ThreadsPerCPU is the part that does. A parallel Go build or test at full
// width runs about one process per CPU, each with about a thread per CPU plus
// its runtime's own: measured at 31 CPUs, 31 test binaries of ~35 threads
// plus the go command and the compiler, ~40 per CPU. Twice that leaves room
// for vet, cgo's C compiler or a second lane in the same command. It is
// linear where the worst case is quadratic: past ~64 CPUs half of Shared
// binds first on any machine whose budget is the kernel default's.
const ThreadsPerCPU = 64

// CommandsShare is the share of the machine's thread budget all commands
// together may hold, and the RLIMIT_NPROC of a command with no cgroup; the
// rest is the daemon's reserve.
const CommandsShare = 0.85

// minCommandThreads keeps the per-command limit usable on a tiny budget.
const minCommandThreads = 256

// CommandThreadsSetting names the daemon setting that sets one command's
// thread limit (proto/reliant/config/v1/daemon_config.proto
// command_threads), for the message a command at its limit is shown.
const CommandThreadsSetting = "command_threads (env RELIANT_COMMAND_THREADS)"

// Sizing is the thread limits for one machine.
type Sizing struct {
	// CPUs is the machine's CPU count.
	CPUs int
	// Budget is the user's thread budget: RLIMIT_NPROC, or
	// kernel.threads-max when that is lower or RLIMIT_NPROC does not apply;
	// 0 when neither is known.
	Budget int
	// Shared is the thread limit of all commands together, CommandsShare of
	// Budget; 0 for none.
	Shared int
	// PerCommand is one command's thread limit.
	PerCommand int
	// Configured is whether PerCommand was set (command_threads) rather
	// than derived.
	Configured bool
}

// Size computes the thread limits for a machine of cpus CPUs whose thread
// budget is budget (0: unknown). configured, when positive, is one command's
// limit as set, taken as is; otherwise it is BaseCommandThreads plus
// ThreadsPerCPU per CPU, at most half of Shared — one runaway command leaves
// the other half to every other agent — and at least minCommandThreads.
func Size(cpus, budget, configured int) Sizing {
	s := Sizing{CPUs: max(cpus, 1), Budget: max(budget, 0)}
	s.Shared = int(float64(s.Budget) * CommandsShare)
	if configured > 0 {
		s.PerCommand, s.Configured = configured, true
		return s
	}
	s.PerCommand = BaseCommandThreads + ThreadsPerCPU*s.CPUs
	if s.Shared > 0 {
		s.PerCommand = min(s.PerCommand, s.Shared/2)
	}
	s.PerCommand = max(s.PerCommand, minCommandThreads)
	return s
}

// threadBudget is the user's thread budget: its RLIMIT_NPROC (0: does not
// apply), or the kernel's threads-max (0: unknown) when that is lower — it
// counts every user's threads, so it binds first if it is. 0 when neither is
// known.
func threadBudget(nproc, threadsMax int) int {
	switch {
	case nproc <= 0:
		return threadsMax
	case threadsMax <= 0:
		return nproc
	default:
		return min(nproc, threadsMax)
	}
}

// Limiter starts commands as bounded process trees.
type Limiter struct {
	// root is the delegated cgroup directory; "" when there is none (why
	// says so).
	root string
	// commands is the cgroup command cgroups are created in: root's
	// commands cgroup, whose pids.max is size.Shared, or root itself when
	// that could not be made (why says so).
	commands string
	why      string
	size     Sizing
	// nproc is the RLIMIT_NPROC a command with no cgroup gets; 0 leaves it
	// alone.
	nproc uint64

	mu  sync.Mutex
	seq uint64
}

// Config is the limiter's policy. The zero value detects everything.
type Config struct {
	// CommandThreads sets one command's thread limit (0: derived from the
	// machine, Size).
	CommandThreads int
	// CgroupRoot overrides the detected delegated cgroup directory; tests
	// use it. "-" turns cgroups off.
	CgroupRoot string
}

var (
	defaultMu sync.RWMutex
	defaultL  *Limiter
)

// SetDefault installs the limiter the daemon's exec paths use.
func SetDefault(l *Limiter) {
	defaultMu.Lock()
	defaultL = l
	defaultMu.Unlock()
}

// Default is the limiter SetDefault installed, or nil (which bounds nothing:
// a nil *Limiter's Prepare only sets the process group).
func Default() *Limiter {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultL
}

// String describes the limiter for a startup log line.
func (l *Limiter) String() string {
	if l == nil {
		return "commands run unbounded (no process limiter)"
	}
	s := "commands run unbounded per tree: " + l.why
	if l.commands != "" {
		s = fmt.Sprintf("each command runs in a cgroup of its own under %s, limited to %d threads (%s)",
			l.commands, l.size.PerCommand, l.size.perCommandBasis())
		if l.why != "" {
			s += "; " + l.why
		} else if l.size.Shared > 0 {
			s += fmt.Sprintf("; all commands together to %d threads (%.0f%% of the user's budget of %d; the rest is the daemon's reserve)",
				l.size.Shared, CommandsShare*100, l.size.Budget)
		}
	}
	if l.nproc > 0 {
		if l.commands != "" {
			s += fmt.Sprintf("; a command that cannot have a cgroup gets RLIMIT_NPROC %d instead", l.nproc)
		} else {
			s += fmt.Sprintf("; RLIMIT_NPROC %d per command (the rest of the user's threads are the daemon's reserve)", l.nproc)
		}
	}
	return s
}

// perCommandBasis says where PerCommand came from, for the startup line and
// the message a command at its limit is shown.
func (s Sizing) perCommandBasis() string {
	if s.Configured {
		return "set by " + CommandThreadsSetting
	}
	switch {
	case s.PerCommand == BaseCommandThreads+ThreadsPerCPU*s.CPUs:
		return fmt.Sprintf("%d + %d per CPU x %d CPUs", BaseCommandThreads, ThreadsPerCPU, s.CPUs)
	case s.Shared > 0 && s.PerCommand == s.Shared/2:
		return fmt.Sprintf("half the %d all commands share", s.Shared)
	default:
		return "the minimum"
	}
}

// LimitNote explains, when the kernel refused the command threads or
// processes at one of its limits while it ran, which limit, how many, and what
// raises it; "" otherwise. Valid after Release (which reads it before the
// command's cgroup goes) or while the command runs.
func (t *Tree) LimitNote() string {
	if t == nil {
		return ""
	}
	if t.noted {
		return t.note
	}
	return t.limitNote()
}

// refused counts the threads or processes the kernel refused a command.
type refused struct {
	// own were refused at the command's own limit.
	own uint64
	// shared were refused, to any command, at the limit all commands share
	// while this one ran.
	shared uint64
	// either were refused to this command at one of the two, not known
	// which (a kernel before 6.13).
	either uint64
}

// What a refused thread looks like in a command's output, so the reader can
// match the note to the error it explains.
const refusedSymptoms = "(\"Resource temporarily unavailable\", \"pthread_create\", \"fork\" or \"failed to create new OS thread\" above)"

// limitMessage words what limitNote found; "" when nothing was refused.
func limitMessage(s Sizing, r refused) string {
	raise := fmt.Sprintf("If the command really needs more, lower its parallelism (go test -p, GOMAXPROCS, test runner workers), "+
		"or raise the limit with the daemon setting %s.", CommandThreadsSetting)
	shared := fmt.Sprintf("%d threads, %.0f%% of the user's budget of %d; the rest is held back so the daemon can always recover",
		s.Shared, CommandsShare*100, s.Budget)
	var msgs []string
	if r.own > 0 {
		msgs = append(msgs, fmt.Sprintf("This command reached its thread limit (%d threads at once, %s): "+
			"the kernel refused it %d new threads or processes, and whatever needed them failed %s. "+
			"The limit counts threads, not processes: a Go or Node process has about one per CPU. %s",
			s.PerCommand, s.perCommandBasis(), r.own, refusedSymptoms, raise))
	}
	if r.shared > 0 {
		msgs = append(msgs, fmt.Sprintf("While this command ran, this machine's agent commands together reached their shared thread limit (%s): "+
			"the kernel refused %d new threads or processes across them, and this command's may be among them. "+
			"Wait for other commands to finish, or stop the ones no longer needed (shell_list, shell_kill); "+
			"the shared limit rises only with the machine's process limit (ulimit -u).",
			shared, r.shared))
	}
	if r.either > 0 {
		msgs = append(msgs, fmt.Sprintf("The kernel refused this command %d new threads or processes at a thread limit, and whatever needed them failed %s: "+
			"its own (%d threads at once, %s) or the one all agent commands share (%s). "+
			"The limits count threads, not processes: a Go or Node process has about one per CPU. %s",
			r.either, refusedSymptoms, s.PerCommand, s.perCommandBasis(), shared, raise))
	}
	return strings.Join(msgs, "\n")
}

// Owner is who a command runs for, so a machine running out of processes can
// say whose processes they are.
type Owner struct {
	// ChatID is the agent (chat or sub-agent thread) whose tool call started
	// the command; "" when the caller did not say.
	ChatID string
	// Command is the command line as the caller gave it.
	Command string
}

// Tree is one command's process tree. Its methods are safe on a nil *Tree.
type Tree struct {
	l     *Limiter
	owner Owner
	dir   string // the command's cgroup, "" when it has none
	fd    int    // the cgroup directory, open until the command starts; -1 when none
	pid   int
	// sharedRefused is the commands cgroup's refusal count when the command
	// was prepared, so LimitNote can tell refusals at the shared limit made
	// while it ran.
	sharedRefused uint64
	// note is LimitNote's answer, kept by Release before the cgroup goes.
	note  string
	noted bool
	// footprint is what the tree's leader holds, for a credsandbox init
	// (accountedFootprint); nil for any other command, whose leader /proc can
	// show.
	footprint []string
	// accounted is the ledger entry made at Started, until Release forgets it.
	accounted *procledger.Process
	// goTmp is the command's own GOTMPDIR (gotmp_linux.go); "" when it has
	// none.
	goTmp string
}

// Prepare readies cmd, which must not have been started, to run as a bounded
// tree: its own process group always, and its own cgroup when the limiter has
// one. Call Started once cmd.Start returns, and Release once the command is
// done with. A failure to set up the cgroup leaves the command unbounded
// rather than failing it.
func (l *Limiter) Prepare(cmd *exec.Cmd, owner Owner) *Tree {
	t := &Tree{l: l, owner: owner, fd: -1}
	setProcessGroup(cmd)
	t.prepareGoTmp(cmd)
	if credsandbox.IsInit(cmd) {
		t.footprint, _ = accountedFootprint(cmd)
	}
	if l == nil || l.commands == "" {
		return t
	}
	l.mu.Lock()
	l.seq++
	seq := l.seq
	l.mu.Unlock()
	t.prepareCgroup(cmd, seq)
	return t
}

// Started records the started command (cmd.Process.Pid) and applies what can
// only be applied to a running process. Call it whether or not Start
// succeeded (pid 0 when it did not).
func (t *Tree) Started(pid int) {
	if t == nil {
		return
	}
	t.closeFD()
	t.pid = pid
	if pid <= 0 {
		t.Release()
		return
	}
	// The cgroups bound a command that has one; RLIMIT_NPROC, which counts
	// every agent's threads, is only the fallback for one that has not.
	if t.dir == "" && t.l != nil && t.l.nproc > 0 {
		t.limitNproc()
	}
	t.account()
	register(t)
}

// account enters a credsandbox init in the ledger. The child is not reaped
// until the caller's Wait, so the pid read here is still this process's, and
// its start time pins the entry to it.
func (t *Tree) account() {
	if t.footprint == nil {
		return
	}
	pgid, start, ok := processIdentity(t.pid)
	if !ok {
		return
	}
	p := procledger.Process{Pid: t.pid, Pgid: pgid, Start: start, Paths: t.footprint}
	procledger.Record(p)
	t.accounted = &p
}

// forget drops the ledger entry once the command is done with.
func (t *Tree) forget() {
	if t != nil && t.accounted != nil {
		procledger.Forget(t.accounted.Pid, t.accounted.Start)
		t.accounted = nil
	}
}

// accountedFootprint lists every path a credsandbox init started by cmd holds:
// its working directory, its executable and each file on its descriptors, in
// the resolved form /proc reports. ok is false when one of those cannot be
// named, and the init then goes unaccounted: a retirement holds for it.
func accountedFootprint(cmd *exec.Cmd) (paths []string, ok bool) {
	dir := cmd.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, false
		}
		dir = wd
	}
	named := []string{dir, cmd.Path}
	files := slices.Clone(cmd.ExtraFiles)
	for _, std := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
		if f, isFile := std.(*os.File); isFile {
			files = append(files, f)
		}
	}
	for _, f := range files {
		if f == nil {
			continue
		}
		name := f.Name()
		// os.Pipe's ends are "|0" and "|1": no path at all.
		if name == "|0" || name == "|1" {
			continue
		}
		named = append(named, name)
	}
	for _, name := range named {
		if !filepath.IsAbs(name) {
			return nil, false
		}
		if resolved, err := filepath.EvalSymlinks(name); err == nil {
			name = resolved
		}
		paths = append(paths, filepath.Clean(name))
	}
	return paths, true
}

// Cgroup is the tree's cgroup directory, "" when it has none.
func (t *Tree) Cgroup() string {
	if t == nil {
		return ""
	}
	return t.dir
}

// Info describes a live tree: its process group (the leader's pid), cgroup
// and owner.
type Info struct {
	Pgid   int
	Cgroup string
	Owner  Owner
}

// The trees started and not yet forgotten, by process group. A tree stays
// here after its command exits for as long as anything it left is alive:
// those processes are still its owner's. Forget drops it.
var (
	liveMu sync.Mutex
	live   = map[int]*Tree{}
)

func register(t *Tree) {
	// Only where a capacity guard scans /proc and prunes the list
	// (internal/procguard); elsewhere nothing would ever take entries out.
	if !trackLive {
		return
	}
	liveMu.Lock()
	live[t.pid] = t
	liveMu.Unlock()
}

// Live lists the trees started and not forgotten.
func Live() []Info {
	liveMu.Lock()
	defer liveMu.Unlock()
	out := make([]Info, 0, len(live))
	for pgid, t := range live {
		out = append(out, Info{Pgid: pgid, Cgroup: t.dir, Owner: t.owner})
	}
	return out
}

// Prune forgets every tree whose process group alive reports gone and whose
// cgroup, if it has one, is empty: nothing of the command is left to own.
func Prune(alive func(pgid int) bool) {
	liveMu.Lock()
	defer liveMu.Unlock()
	for pgid, t := range live {
		if !alive(pgid) && !t.Populated() {
			delete(live, pgid)
		}
	}
}
