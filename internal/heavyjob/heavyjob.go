// Copyright (c) 2025 Reliant Labs

// Package heavyjob queues the daemon's heavy commands behind the machine's
// heavy slots, so a machine full of agents runs a bounded number of builds,
// test lanes and codegen runs at once instead of all of them.
//
// Why. Every agent on a machine runs its shell commands through one daemon,
// and nothing bounded how many of them were heavy at once: a 32 CPU / 64 GiB
// cloud workspace ran load 100–300 with a dozen agents each running an
// unslotted `go build ./...` or `go test ./...` on every core, and every one of
// those jobs slowed down (reclaim, not CPU, is what past a handful of jobs
// costs). Forge already has the primitive — host-wide heavy slots
// (forge/pkg/hostslot), held by `forge generate`, `forge lint`, `forge slot
// run` and the gate's heavy checks — but only forge's own lanes used it.
//
// What. A Gate decides whether a command is heavy (a short, explicit
// list of command patterns, plus whatever the caller declares heavy) and, when
// it is, rewrites the command to run under this binary's slot wrapper
// (RunWrapper): the wrapper takes a slot from the SAME pool forge's lanes use —
// same directory, same capacity, one machine-wide budget — runs the command as
// its child, and holds the slot until the command exits.
//
// Why a wrapper process and not a lock taken in the daemon. A slot is an OS
// file lock owned by whoever holds it. Held by the wrapper, its lifetime is
// exactly the command's: a command adopted into the background, killed by its
// timeout, or orphaned by a daemon restart releases its slot when its process
// group dies, with no bookkeeping in the daemon. The wrapper marks itself a
// subtree holder and passes FORGE_SLOT_HELD to the command, so anything the
// command runs that wants a slot of its own — `forge generate`, `make test`
// under `forge slot run` — runs at once instead of queueing behind its own
// parent (which, with every slot held by such parents, would deadlock). Each
// command also appears in `forge slot status` under its own pid and command
// line, not as N anonymous holds by the daemon.
//
// Light commands never wait: they are not rewritten at all.
//
//forge:exclude-contract: one concrete policy over exec.Cmd values (classify, rewrite) plus the slot wrapper this binary runs as; the slots themselves are forge/pkg/hostslot, and there is no second implementation to substitute
package heavyjob

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// WrapperArg is the first argument that makes this binary run as the slot
// wrapper instead of the reliant CLI: `<self> __heavy-slot [flags] -- argv...`.
// It is a private protocol between a daemon and the binary it runs from, kept
// small and stable because a daemon upgraded in place execs the NEW binary at
// its old path.
const WrapperArg = "__heavy-slot"

// DefaultCommands are the commands that are heavy without being declared so:
// work that saturates several cores and gigabytes of memory for seconds to
// minutes. Each is a word list matched against the start of every simple
// command in a shell line; a word ending in * matches any word with that
// prefix.
//
// Deliberately absent: orchestrators that already take one heavy slot per
// heavy step of their own — `forge ci gate`, control-plane's `task ci` —
// because run inside a slot they can no longer bound their steps (each step's
// slot is granted at once), so wrapping them would run N heavy steps under one
// slot. Also absent: long-lived servers (`npm run dev`, `go run`, `forge env
// up`), which would hold a slot for as long as they run.
var DefaultCommands = []string{
	"go build",
	"go test",
	"go vet",
	"go install",
	"golangci-lint run",
	"vitest",
	"npx vitest",
	"npm test",
	"npm run test*",
	"npm run build*",
	"npm ci",
	"forge generate",
	"forge lint",
	"reliant forge generate",
	"reliant forge lint",
	"make test*",
	"task test*",
	"docker build",
}

// Config is the heavy-command policy. The zero value is the default policy.
type Config struct {
	// Commands are the heavy-command patterns. Nil is DefaultCommands; an
	// empty, non-nil slice declares nothing heavy by pattern (only commands
	// their caller declares heavy queue).
	Commands []string
	// Env is the environment the slot pool is resolved from — normally the
	// daemon's own, os.Environ(). It decides the slot directory
	// (FORGE_SLOTS_DIR, else the user cache dir) and the capacity
	// (FORGE_HEAVY_SLOTS, else the machine's default, min(CPUs/4, GiB/8));
	// FORGE_HEAVY_SLOTS=off turns the gate off. These are forge's own knobs on
	// purpose: one machine, one budget, shared with forge's lanes.
	Env []string
	// Self is the command that runs the wrapper: this binary followed by
	// WrapperArg. Nil resolves os.Executable(). Tests point it at their own
	// test binary.
	Self []string
	// Admission, when set, may refuse a heavy command before it starts
	// (internal/diskguard refuses them on a nearly full disk). It is asked
	// whether or not the slots are on.
	Admission Admission
	// BackgroundAdmission, when set, may refuse a heavy command started in
	// the background (internal/procguard refuses them when the machine is
	// near its process limit). Foreground heavy commands are not asked: the
	// agent waits on those, so they cannot pile up the way background ones
	// did on 2026-10-30.
	BackgroundAdmission Admission
}

// Admission decides whether a heavy command may start now.
type Admission interface {
	// HeavyRefusal is why a heavy command may not start now, or "" when it
	// may.
	HeavyRefusal() string
}

// RefusedExitCode is the exit status a refused heavy command reports:
// EX_TEMPFAIL, "try again later" — the command never ran.
const RefusedExitCode = 75

// RefusedError is a heavy command an Admission refused. Its message is what
// the caller tells the agent.
type RefusedError struct {
	Command string
	Reason  string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("reliant: `%s` was not started. %s", e.Command, e.Reason)
}

// Gate queues heavy commands behind the machine's heavy slots, and asks its
// Admission before any starts. A nil *Gate does neither, so callers outside a
// daemon need no special case.
type Gate struct {
	patterns    []pattern
	admission   Admission
	bgAdmission Admission
	// self is the wrapper command; nil when heavy commands run unqueued
	// (unqueued says why).
	self     []string
	unqueued string
	dir      string
	// slots is the capacity passed to the wrapper when it came from the
	// environment ("" when it is the machine's default, which the wrapper
	// derives identically).
	slots    string
	capacity hostslot.Capacity
}

// New builds a Gate. When heavy commands cannot be queued here — the limit is
// switched off, or this binary cannot be found to run the wrapper — the gate
// still classifies them and asks its Admission, and runs them unqueued (see
// String): a broken limiter must never stop work. The error is only for a
// configuration it cannot read.
func New(cfg Config) (*Gate, error) {
	capy, err := hostslot.CapacityFor(hostslot.Heavy, cfg.Env)
	if err != nil {
		return nil, err
	}
	commands := cfg.Commands
	if commands == nil {
		commands = DefaultCommands
	}
	g := &Gate{
		patterns:    parsePatterns(commands),
		admission:   cfg.Admission,
		bgAdmission: cfg.BackgroundAdmission,
		dir:         hostslot.Options{Env: cfg.Env}.BaseDir(),
		capacity:    capy,
	}
	if _, ok := lookup(cfg.Env, hostslot.SlotsEnv(hostslot.Heavy)); ok {
		g.slots = strconv.Itoa(capy.Slots)
	}
	switch self := cfg.Self; {
	case capy.Unlimited:
		g.unqueued = fmt.Sprintf("heavy slots are off (%s)", capy.Source)
	case len(self) > 0:
		g.self = self
	default:
		exe, err := os.Executable()
		if err != nil {
			g.unqueued = fmt.Sprintf("own executable for the heavy-slot wrapper not found: %v", err)
		} else {
			g.self = []string{exe, WrapperArg}
		}
	}
	return g, nil
}

// String describes the gate for a startup log line.
func (g *Gate) String() string {
	switch {
	case g == nil:
		return "heavy commands run unqueued"
	case g.self == nil:
		return "heavy commands run unqueued: " + g.unqueued
	}
	return fmt.Sprintf("heavy commands queue for %s (%s)", g.capacity, g.dir)
}

// Job is one command a Gate is asked about.
type Job struct {
	// Command is the shell line; ignored when Argv is set.
	Command string
	// Argv is a command run with no shell.
	Argv []string
	// Declared is the caller saying the command is heavy whatever its
	// words are (a project script that builds everything, say). There is
	// no way to declare a command light: the patterns are the floor.
	Declared bool
	// Background is a command started in the background (run_in_background),
	// which Config.BackgroundAdmission is also asked about.
	Background bool
}

// label is what the slot records as this job's command.
func (j Job) label() string {
	if len(j.Argv) > 0 {
		return strings.Join(j.Argv, " ")
	}
	return j.Command
}

// Heavy reports whether job queues for a heavy slot: it is declared heavy, or
// one of its simple commands starts with a pattern's words. A shell line is
// split at unquoted ; & | ( ) ` and newlines, and the words that only say how
// a command runs — VAR=value, env, time, nice, timeout 5m, nohup, command,
// exec, and keywords such as do and then — are skipped first; quotes are
// honoured, so `git commit -m "fix go test"` is not a test run. It recognizes
// the plain ways agents spell a build, not every shell program: a command
// behind `bash -c '…'`, a variable or a script is not seen, which is what
// Job.Declared is for.
func (g *Gate) Heavy(job Job) bool {
	if g == nil {
		return false
	}
	if job.Declared {
		return true
	}
	if len(job.Argv) > 0 {
		return matchWords(g.patterns, job.Argv)
	}
	return classify(g.patterns, job.Command)
}

// Prepare readies cmd, which must not have been started, for job. A light
// job is left alone: (false, nil). A heavy one is first put to the Admission
// — a refusal is (true, *RefusedError), and cmd must not be started — and
// then rewritten to run under the slot wrapper. cmd keeps its directory,
// environment, I/O and process attributes; only what it execs changes, so
// callers set those up exactly as for a light command.
func (g *Gate) Prepare(cmd *exec.Cmd, job Job) (heavy bool, err error) {
	if !g.Heavy(job) {
		return false, nil
	}
	if g.admission != nil {
		if why := g.admission.HeavyRefusal(); why != "" {
			return true, &RefusedError{Command: job.label(), Reason: why}
		}
	}
	if job.Background && g.bgAdmission != nil {
		if why := g.bgAdmission.HeavyRefusal(); why != "" {
			return true, &RefusedError{Command: job.label(), Reason: why}
		}
	}
	if g.self == nil || cmd == nil || cmd.Process != nil || cmd.Err != nil || len(cmd.Args) == 0 {
		return true, nil
	}
	// A daemon whose binary was removed under it (an upgrade in progress)
	// runs the command unqueued rather than failing it.
	if _, err := os.Stat(g.self[0]); err != nil {
		return true, nil
	}
	args := append([]string{}, g.self[1:]...)
	args = append(args, "--dir", g.dir, "--label", job.label())
	if g.slots != "" {
		args = append(args, "--slots", g.slots)
	}
	args = append(args, "--", cmd.Path)
	args = append(args, cmd.Args[1:]...)
	cmd.Path = g.self[0]
	cmd.Args = append([]string{g.self[0]}, args...)
	return true, nil
}

// TimeoutNote is appended to a heavy command's output when its timeout fired
// after it had waited for a slot: the timeout counted the wait, which is not
// obvious from a command that "only" needed a minute.
const TimeoutNote = "This heavy command waited for a machine-wide heavy slot before it ran, and its timeout counted the wait (see the ⏳ lines). Run long builds and test suites with run_in_background, or with a longer timeout."

// WaitedFor reports whether output shows the wrapper waited for a slot.
func WaitedFor(output string) bool {
	return strings.Contains(output, waitingMarker)
}

func lookup(env []string, key string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val, found = v, true // the last assignment wins, as it does for exec
		}
	}
	return val, found
}
