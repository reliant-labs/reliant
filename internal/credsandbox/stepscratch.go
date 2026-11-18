// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"errors"
	"io/fs"
	"os"
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
// through an open file). One still held then stays until nothing holds it: its
// own reclaim keeps looking, every stepScratchLingerEvery.
//
// Every directory a daemon hands out carries an ownership lock, a .lock file
// inside it on which the daemon holds flock(LOCK_EX), taken before the
// directory gets its step- name and held until the step's own removal finished
// (the descriptor is close-on-exec, so no command inherits it). The kernel drops
// the lock when the daemon exits however it exits. This daemon's directories are
// also in its registry (stepRegistry), with that descriptor. The sweep, which the daemon runs
// when it starts and then at most every stepScratchSweepEvery whenever a
// credentialed command starts, takes only the directories no live daemon owns:
// never one in the registry, never one younger than stepScratchMinAge, never one
// whose lock another process holds (flock LOCK_NB fails: a daemon, this one or
// another on the same home, owns it). One whose lock it takes is re-checked
// while it holds the lock: removed only when a fresh, complete walk of /proc
// shows nothing holds it, by an atomic rename to a .trash- name and then its
// removal. A directory with no lock file (made before locks, or a step that
// removed its own) is judged by that walk and the age gate alone. A process
// whose holdings cannot be read makes the walk incomplete, and then the sweep
// removes nothing more and says what it kept.
//
// It is an addition: a command that never reads StepScratchEnv keeps working as
// before, and one written for it falls back to its own `mktemp -d` where the
// variable is unset (an older daemon, another platform, a host without the
// sandbox).

// StepScratchEnv names, to a credentialed command, a directory of its own in
// CredScratchDir that the daemon removes once the command's processes are all
// gone, on every path: an exit, a kill, a timeout, a cancelled run, a daemon
// restart. Set only beside SignalEnv: it is as hidden from credentials-none
// commands as CredScratchDir is. It holds .lock, the daemon's ownership lock
// (a step that removes it leaves its directory to the age-gated /proc proof
// alone).
const StepScratchEnv = "RELIANT_STEP_SCRATCH"

// stepScratchPrefix starts the name of every command's own scratch directory.
const stepScratchPrefix = "step-"

// Tunables of the reclaim, package variables so tests can shorten them.
var (
	// stepScratchSettleTries and stepScratchSettleEvery are how often, and
	// how long, Done first looks for the processes a killed command leaves
	// dying (a SIGKILLed process group is not reaped at once); after that it
	// looks every stepScratchLingerEvery, until nothing holds the directory.
	stepScratchSettleTries = 20
	stepScratchSettleEvery = 500 * time.Millisecond
	stepScratchLingerEvery = time.Minute
	// stepScratchSweepEvery is how often the sweep runs while credentialed
	// commands start.
	stepScratchSweepEvery = 10 * time.Minute
	// stepScratchRetryEvery is how soon the sweep runs again after a pass
	// whose walk of /proc was incomplete and removed nothing.
	stepScratchRetryEvery = time.Minute
	// stepScratchMinAge spares a directory made this recently: the moment
	// between its making and its command's start, when nothing holds it yet.
	stepScratchMinAge = time.Minute
	// stepScratchProcRoot is the /proc the sweep reads live processes from.
	stepScratchProcRoot = "/proc"
	// stepScratchNow is the clock the sweep judges ages and its own cadence
	// by.
	stepScratchNow = time.Now
)

// stepRegistry is every scratch directory this daemon has handed out and not
// yet removed, with the descriptor of the ownership lock it holds on each. A
// directory enters it when it is made, under mu, before any command can see its
// name, and leaves it only when its own reclaim removed it (removeStepScratch),
// which then closes the descriptor and so releases the lock. The sweep checks it under mu, after it has listed the
// root: a directory listed then was made before the listing, so it is either
// registered already or not this daemon's.
var stepRegistry struct {
	mu   sync.Mutex
	dirs map[string]*os.File
}

// registerStepScratch runs mk, which creates a directory and returns its path
// and the held lock, under the registry's lock, and registers both.
func registerStepScratch(mk func() (string, *os.File, error)) (string, error) {
	stepRegistry.mu.Lock()
	defer stepRegistry.mu.Unlock()
	dir, lock, err := mk()
	if err != nil {
		return "", err
	}
	if stepRegistry.dirs == nil {
		stepRegistry.dirs = map[string]*os.File{}
	}
	stepRegistry.dirs[dir] = lock
	return dir, nil
}

// stepScratchRegistered reports whether dir is one this daemon handed out and
// has not removed.
func stepScratchRegistered(dir string) bool {
	stepRegistry.mu.Lock()
	defer stepRegistry.mu.Unlock()
	_, ok := stepRegistry.dirs[dir]
	return ok
}

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
// the directory goes once no live process holds it. It keeps looking until
// then: the sweep never takes a directory this daemon handed out.
func (s *StepScratch) Done(gone func() bool) {
	if s == nil || s.dir == "" {
		return
	}
	go s.reclaim(gone)
}

// reclaim removes the directory once the command's processes are gone,
// looking every stepScratchSettleEvery at first and every
// stepScratchLingerEvery after stepScratchSettleTries, for as long as it takes.
func (s *StepScratch) reclaim(gone func() bool) {
	for i := 0; !s.tryReclaim(gone); i++ {
		wait := stepScratchSettleEvery
		if i >= stepScratchSettleTries {
			if i == stepScratchSettleTries {
				logging.Debug("credsandbox: a command's scratch is still held after it ended; removed once nothing holds it", "dir", s.dir)
			}
			wait = stepScratchLingerEvery
		}
		time.Sleep(wait)
	}
}

// tryReclaim removes the directory if the command's processes are proven gone,
// and reports whether it is gone. gone (the command's cgroup is empty) is the
// proof whenever the command has one, and then /proc is not read at all;
// otherwise the proof is a complete walk of /proc in which no process holds
// it, and a walk with any read error is no proof.
func (s *StepScratch) tryReclaim(gone func() bool) bool {
	if gone == nil || !gone() {
		refs, err := stepScratchRefs(stepScratchProcRoot)
		if err != nil || refs[baseName(s.dir)] {
			return false
		}
	}
	return removeStepScratch(s.dir)
}

// removeStepScratch removes dir, one this daemon handed out, and once it is
// gone takes it out of the registry and releases its ownership lock: the lock
// is held for the whole removal, so no sweep can judge a half-removed tree.
func removeStepScratch(dir string) bool {
	if err := removeTree(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logging.Warn("credsandbox: cannot remove a command's scratch directory; retrying", "dir", dir, "error", err)
		return false
	}
	stepRegistry.mu.Lock()
	lock := stepRegistry.dirs[dir]
	delete(stepRegistry.dirs, dir)
	stepRegistry.mu.Unlock()
	if lock != nil {
		_ = lock.Close()
	}
	return true
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
