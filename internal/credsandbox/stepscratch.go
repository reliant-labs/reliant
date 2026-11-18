// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Each credentialed command's own scratch directory.
//
// A credentialed step keeps what it fetches and builds in CredScratchDir, where
// no credentials-none command can reach it. The PR pipeline's bootstrap makes
// one with `mktemp -d` there and removes it from an EXIT trap, which does not
// run when the step is SIGKILLed: a node timeout, a cancelled run, a daemon
// restart, the OOM killer. Nothing else could tell such a directory from a live
// step's, so they stayed: on 2026-11-18 the pipeline machine held 537 of them,
// ~253 MB each, 131 GB, and had 2.6% of its disk free.
//
// So every credentialed command that gets SignalEnv also gets a directory of
// its own in CredScratchDir, step-<daemon pid>-<random>, mode 0700, named by
// StepScratchEnv. The daemon removes it once the command's processes are gone,
// however the command ended: proctree hands it back when it releases the
// command's tree (Done), and it goes as soon as the command's cgroup is proven
// empty, or no live process holds it (stepScratchRefs: in its environment,
// which every process of the command inherits, as its working directory or
// through an open file). One still held then, and one an earlier daemon left
// (it was killed, or restarted under its running commands), is removed by the
// sweep, which the daemon runs when it starts and then at most every
// stepScratchSweepEvery, whenever a credentialed command starts: once no live
// process holds it.
//
// It is an addition: a command that never reads StepScratchEnv keeps working as
// before, and one written for it falls back to its own `mktemp -d` where the
// variable is unset (an older daemon, another platform, a host without the
// sandbox).

// StepScratchEnv names, to a credentialed command, a directory of its own in
// CredScratchDir that the daemon removes once the command's processes are all
// gone, on every path: an exit, a kill, a timeout, a cancelled run, a daemon
// restart. Set only beside SignalEnv: it is as hidden from credentials-none
// commands as CredScratchDir is.
const StepScratchEnv = "RELIANT_STEP_SCRATCH"

// stepScratchPrefix starts the name of every command's own scratch directory.
const stepScratchPrefix = "step-"

// Tunables of the reclaim, package variables so tests can shorten them.
var (
	// stepScratchSettleTries and stepScratchSettleEvery bound how long Done
	// waits for the processes a killed command leaves dying (a SIGKILLed
	// process group is not reaped at once) before it leaves the directory to
	// the sweep.
	stepScratchSettleTries = 20
	stepScratchSettleEvery = 500 * time.Millisecond
	// stepScratchSweepEvery is how often the sweep runs while credentialed
	// commands start.
	stepScratchSweepEvery = 10 * time.Minute
	// stepScratchMinAge spares a directory made this recently: the moment
	// between its making and its command's start, when nothing holds it yet.
	stepScratchMinAge = time.Minute
	// stepScratchProcRoot is the /proc the sweep reads live processes from.
	stepScratchProcRoot = "/proc"
)

// StepScratch is one credentialed command's own scratch directory
// (StepScratchEnv). Its methods are safe on a nil *StepScratch.
type StepScratch struct {
	dir string
}

// stepScratches holds the directory wrap made for a command until its tree
// takes it (TakeStepScratch), keyed by the *exec.Cmd.
var stepScratches sync.Map // *exec.Cmd -> *StepScratch

// TakeStepScratch hands over the scratch directory Wrap made for cmd, nil when
// it made none. The caller removes it with Done once the command is done with;
// proctree does, when it releases the command's tree.
func TakeStepScratch(cmd *exec.Cmd) *StepScratch {
	v, ok := stepScratches.LoadAndDelete(cmd)
	if !ok {
		return nil
	}
	return v.(*StepScratch)
}

// Dir is the directory, "" for none.
func (s *StepScratch) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Done removes the directory once the command's processes are gone, in the
// background: a killed build's scratch holds hundreds of megabytes. gone, when
// not nil, proves it the cheap way (the command's cgroup is empty); otherwise
// the directory goes once no live process holds it. One still held after
// stepScratchSettleTries is left to the sweep.
func (s *StepScratch) Done(gone func() bool) {
	if s == nil || s.dir == "" {
		return
	}
	go s.reclaim(gone)
}

func (s *StepScratch) reclaim(gone func() bool) {
	for i := 0; i < stepScratchSettleTries; i++ {
		if i > 0 {
			time.Sleep(stepScratchSettleEvery)
		}
		if gone != nil && gone() {
			removeStepScratch(s.dir)
			return
		}
		refs, err := stepScratchRefs(stepScratchProcRoot)
		if err != nil {
			continue
		}
		if !refs[baseName(s.dir)] {
			removeStepScratch(s.dir)
			return
		}
	}
	logging.Debug("credsandbox: a command's scratch is still held after it ended; the sweep removes it once nothing holds it", "dir", s.dir)
}

func removeStepScratch(dir string) {
	if err := removeTree(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logging.Warn("credsandbox: cannot remove a command's scratch directory; the sweep retries", "dir", dir, "error", err)
	}
}

// withStepScratch gives cmd, a credentialed command whose env (already set)
// carries SignalEnv when signal, a scratch directory of its own and names it in
// StepScratchEnv. Without the signal, or when the directory cannot be made, the
// command runs without one (it falls back to its own mktemp).
func withStepScratch(cmd *exec.Cmd, signal bool) {
	if !signal {
		return
	}
	dir, err := makeStepScratch(CredScratchDir())
	if err != nil {
		logging.Warn("credsandbox: cannot make a credentialed command's own scratch directory; it runs without "+StepScratchEnv,
			"root", CredScratchDir(), "error", err)
		return
	}
	cmd.Env = append(cmd.Env, StepScratchEnv+"="+dir)
	stepScratches.Store(cmd, &StepScratch{dir: dir})
}

// withoutEnv is env without name.
func withoutEnv(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func baseName(dir string) string {
	if i := strings.LastIndexByte(dir, '/'); i >= 0 {
		return dir[i+1:]
	}
	return dir
}
