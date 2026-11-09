package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/reliant-labs/forge/internal/hostlaunch"
	"github.com/reliant-labs/forge/internal/procguard"
)

// Stopping a supervised stack: SIGTERM, a grace period, then SIGKILL.
const (
	// defaultStopGrace is how long a stopping stack's processes get between
	// SIGTERM and SIGKILL when nothing in the stack declares a longer drain.
	defaultStopGrace = 10 * time.Second
	// maxStopGrace caps how far a runner's declared drain extends the grace,
	// so one misconfigured kill_delay cannot hold Ctrl-C for minutes.
	maxStopGrace = 30 * time.Second
	// stopGraceMargin is the time beyond a runner's own kill delay that it
	// needs to escalate to SIGKILL, reap its child and exit itself.
	stopGraceMargin = 3 * time.Second
)

// Shutdown timings a test shrinks. stopReapWait bounds the wait for the
// children's exit to be observed after SIGKILL, so a process holding one of
// their output pipes can never hang the command. stopSweepInterval is how often
// the grace period looks for processes of this run that left the tree.
// stopStragglerWait is how long the final check keeps re-sweeping while it
// still finds a process of this run, or one it cannot inspect.
var (
	stopReapWait      = 5 * time.Second
	stopSweepInterval = time.Second
	stopStragglerWait = time.Second
)

// requireStopGrace extends the shutdown grace so a runner that declares its
// own drain (Air's kill_delay) can finish it before forge escalates. A drain
// that cannot fit under maxStopGrace is reported once, at start, rather than
// discovered as a SIGKILL at the next Ctrl-C.
func (p *procRegistry) requireStopGrace(who string, drain time.Duration) {
	if drain <= 0 {
		return
	}
	need := drain + stopGraceMargin
	if need > maxStopGrace {
		fmt.Printf("[up] %s: its runner drains for %s on stop, longer than forge waits (%s): stopping the stack will SIGKILL it before that drain ends — lower its kill_delay\n",
			who, drain, maxStopGrace-stopGraceMargin)
		need = maxStopGrace
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if need > p.stopGrace && need > defaultStopGrace {
		p.stopGrace = need
		p.graceFor = fmt.Sprintf("%s drains for %s", who, drain)
	}
}

// shutdown stops every process this run started, and everything they started:
//
//  1. SIGTERM each runner's whole tree (killProcessTree), while the runners
//     are still alive to anchor it — and every process carrying this run's
//     FORGE_UP_RUN marker that already left the tree (a runner that died, or
//     detached its child into a session of its own).
//  2. Wait up to the grace for all of it to exit, sweeping for escapees once a
//     stopSweepInterval and signalling any new one.
//  3. At the deadline, SIGKILL the trees and every marked process left.
//  4. Wait, bounded, for the exits to be observed; report anything still
//     running and keep the stack's records so `forge env down` can reach it.
//
// The marker sweep is what makes this hold for any runner: the tree walk sees
// only what is still attached to a live runner, but every descendant inherits
// the marker however it was started. The sweep never selects a process in
// this command's own lineage (procguard), and the marker is unique to this
// run, so a stack another run started is never touched.
//
// The stack's records are removed only when the stack is PROVEN down. A
// process table that cannot be read, or a process started during this run
// whose environment cannot be read (so whether it carries the marker is
// unknown), is not proof of absence: shutdown then keeps the records, so
// `forge env down` and `forge env ps` still reach the stack, and returns an
// error naming what it could not check.
func (p *procRegistry) shutdown() error {
	p.mu.Lock()
	p.isStopped = true
	procs := make([]*managedProcess, len(p.processes))
	copy(procs, p.processes)
	grace, graceFor := p.stopGrace, p.graceFor
	p.mu.Unlock()
	if len(procs) == 0 {
		// Nothing was started, so nothing can carry this run's marker.
		removeStackRecords(p.projectID, p.env)
		return nil
	}
	if grace <= 0 {
		grace = defaultStopGrace
	}
	if graceFor != "" {
		fmt.Printf("[up] allowing %s for a graceful stop (%s)\n", grace.Round(time.Second), graceFor)
	}

	managed := map[int]bool{}
	for _, mp := range procs {
		if pid := procPID(mp); pid > 0 {
			managed[pid] = true
		}
	}
	// termed is every process the tree pass below SIGTERMs. One that later
	// leaves its tree (its runner gave up waiting for it) is still draining,
	// and a second signal could cut that drain short.
	termed := map[int]bool{}
	// Reverse order so later-started frontends die before the host services
	// they may have spoken to — keeps the user's last log lines clean.
	for i := len(procs) - 1; i >= 0; i-- {
		mp := procs[i]
		pid := procPID(mp)
		if pid <= 0 || managedProcessExited(mp) {
			continue
		}
		fmt.Printf("[up] stopping %s (pid=%d)...\n", mp.name, pid)
		for _, d := range descendantPIDs(pid) {
			termed[d] = true
		}
		_ = killProcessTree(pid, syscall.SIGTERM)
	}
	escaped := map[int]bool{}
	p.signalEscaped(managed, escaped, termed, syscall.SIGTERM)

	exited := allExited(procs)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	tick := time.NewTicker(stopSweepInterval)
	defer tick.Stop()
	timedOut := false
	for !timedOut {
		select {
		case <-exited:
			exited = nil
			p.signalEscaped(managed, escaped, termed, syscall.SIGTERM)
		case <-tick.C:
			p.signalEscaped(managed, escaped, termed, syscall.SIGTERM)
		case <-deadline.C:
			timedOut = true
			continue
		}
		if exited == nil && !anyAlive(escaped) {
			break
		}
	}

	if timedOut {
		for _, mp := range procs {
			pid := procPID(mp)
			if pid <= 0 || managedProcessExited(mp) {
				continue
			}
			fmt.Printf("[up] %s: did not exit, killing.\n", mp.name)
			_ = killProcessTree(pid, syscall.SIGKILL)
		}
		for _, pid := range sortedPIDs(escaped) {
			if processAlive(pid) {
				fmt.Printf("[up] pid %d did not exit, killing.\n", pid)
				_ = killProcessTree(pid, syscall.SIGKILL)
			}
		}
		if sw, table, err := p.sweep(managed); err == nil {
			for _, pid := range sw.roots {
				if escaped[pid] {
					continue
				}
				fmt.Printf("[up] pid %d (%s) did not exit, killing.\n", pid, serviceOfPID(pid, table))
				_ = killProcessTree(pid, syscall.SIGKILL)
			}
		}
	}

	left, unverified := p.awaitStopped(procs, managed)
	switch {
	case len(left) > 0:
		fmt.Printf("[up] warning: still running after SIGKILL: %s — the stack's records are kept; `forge env down %s` retries\n",
			strings.Join(left, ", "), p.env)
		return fmt.Errorf("environment shutdown incomplete; stack records preserved: still running after SIGKILL: %s — run `forge env down %s` to retry",
			strings.Join(left, ", "), p.env)
	case unverified != "":
		fmt.Printf("[up] warning: could not verify the stack stopped: %s — the stack's records are kept; `forge env down %s` retries\n",
			unverified, p.env)
		return fmt.Errorf("environment shutdown unverified; stack records preserved: %s — run `forge env down %s` to retry",
			unverified, p.env)
	}
	removeStackRecords(p.projectID, p.env)
	return nil
}

// awaitStopped waits (bounded by stopReapWait) for every managed process's
// exit to be observed, then reports what is still alive — a managed process
// whose exit never came, or a process carrying this run's marker — and, when
// it cannot tell, why not. Both empty means the stack is proven down.
func (p *procRegistry) awaitStopped(procs []*managedProcess, managed map[int]bool) (left []string, unverified string) {
	select {
	case <-allExited(procs):
	case <-time.After(stopReapWait):
	}
	for _, mp := range procs {
		if pid := procPID(mp); pid > 0 && !managedProcessExited(mp) && processAlive(pid) {
			left = append(left, fmt.Sprintf("%s (pid %d)", mp.name, pid))
		}
	}
	// A process SIGKILLed a moment ago can still be on its way out, and an
	// unreadable one may be a process that is just exiting.
	sw, table, err := p.sweep(managed)
	for until := time.Now().Add(stopStragglerWait); (err != nil || !sw.empty()) && time.Now().Before(until); {
		time.Sleep(100 * time.Millisecond)
		sw, table, err = p.sweep(managed)
	}
	if err != nil {
		return left, fmt.Sprintf("the process table could not be read (%v), so processes this run's runners left behind cannot be ruled out", err)
	}
	for _, pid := range sw.roots {
		left = append(left, fmt.Sprintf("%s (pid %d)", serviceOfPID(pid, table), pid))
	}
	if len(sw.unknown) > 0 {
		labels := make([]string, 0, len(sw.unknown))
		for _, pid := range sw.unknown {
			labels = append(labels, fmt.Sprintf("pid %d (%s)", pid, commandLabel(table.argv(pid))))
		}
		unverified = fmt.Sprintf("the environment of %s, started during this run, could not be read, so whether it belongs to this stack is unknown",
			strings.Join(labels, ", "))
	}
	return left, unverified
}

// signalEscaped records in escaped every process of this run that is outside
// the runners' trees, so the grace waits for it and its deadline kills it, and
// sends sig to the tree of each one newly found — unless the tree pass already
// signalled it (termed) before it left. A sweep that cannot read the process
// table signals nothing; the final check in awaitStopped reports it.
func (p *procRegistry) signalEscaped(managed, escaped, termed map[int]bool, sig syscall.Signal) {
	sw, table, err := p.sweep(managed)
	if err != nil {
		return
	}
	for _, pid := range sw.roots {
		if escaped[pid] {
			continue
		}
		escaped[pid] = true
		if termed[pid] {
			continue
		}
		fmt.Printf("[up] stopping %s's detached process (pid=%d)...\n", serviceOfPID(pid, table), pid)
		_ = killProcessTree(pid, sig)
	}
}

// sweep reads the process table once and selects this run's processes that
// left the runners' trees (sweepRun), dropping the runners themselves and
// anything no longer alive. An error means the table could not be read: an
// absence of roots then proves nothing.
func (p *procRegistry) sweep(managed map[int]bool) (runSweep, *stopTable, error) {
	read := p.readStopTable
	if read == nil {
		read = readOSStopTable
	}
	table, err := read()
	if err != nil {
		return runSweep{}, nil, err
	}
	if p.runID == "" || p.projectID == "" {
		return runSweep{}, nil, errors.New("this run has no ownership marker to sweep for")
	}
	sw := sweepRun(p.projectID, p.env, p.runID, p.startedAt, table, procguard.Self())
	roots := sw.roots[:0]
	for _, pid := range sw.roots {
		if !managed[pid] && table.alive(pid) {
			roots = append(roots, pid)
		}
	}
	unknown := sw.unknown[:0]
	if len(sw.unknown) > 0 {
		// An environment also reads as unreadable for the instant a process
		// is exiting. Re-read the table and keep only what is still there,
		// still not a zombie, and still unreadable.
		again, err := read()
		if err != nil {
			return runSweep{}, nil, err
		}
		for _, pid := range sw.unknown {
			if row, ok := again.procs[pid]; ok && !row.exited && again.alive(pid) {
				if _, readable := again.environ(pid); !readable {
					unknown = append(unknown, pid)
				}
			}
		}
	}
	return runSweep{roots: roots, unknown: unknown}, table, nil
}

// runSweep is one sweep's outcome. roots are the subtree roots of this run's
// marked processes. unknown are processes that could be this run's — alive,
// not provably another user's, started during the run — whose environment
// could not be read, so whether they carry the marker is undetermined.
type runSweep struct {
	roots   []int
	unknown []int
}

func (s runSweep) empty() bool { return len(s.roots) == 0 && len(s.unknown) == 0 }

// stopTable is one read of the process table, as the shutdown sweep needs it.
// It is a procFacts, so the ownership helpers (subtreeRoots, serviceOfPID)
// read it directly. Readers are injected so a test can model a table, an
// unreadable environment or a dead pid without real processes.
type stopTable struct {
	procs    map[int]stopProc
	readEnv  func(pid int) ([]string, bool)
	readArgv func(pid int) ([]string, bool)
	isAlive  func(pid int) bool
}

// stopProc is one process-table row.
type stopProc struct {
	ppid    int
	started time.Time // zero when unknown
	// foreign: provably another user's (or, on Windows, another session's)
	// process, which no child of this run can be.
	foreign bool
	// exited: a zombie — dead, holding nothing, awaiting its parent's reap.
	exited bool
}

func (t *stopTable) environ(pid int) ([]string, bool) { return t.readEnv(pid) }

func (t *stopTable) parent(pid int) (int, bool) {
	row, ok := t.procs[pid]
	return row.ppid, ok
}

func (t *stopTable) argv(pid int) ([]string, bool) {
	if t.readArgv == nil {
		return nil, false
	}
	return t.readArgv(pid)
}

func (t *stopTable) alive(pid int) bool {
	if row, ok := t.procs[pid]; ok && row.exited {
		return false
	}
	return t.isAlive(pid)
}

// sweepRun is the pure selection core of the shutdown sweep. Of every process
// in the table it selects the subtree roots of those stamped with this run's
// FORGE_UP_RUN AND this stack's (project, env) markers, and lists as unknown
// every process whose environment cannot be read but which could be this
// run's: alive, not provably another user's, and not started before the run
// (since). A process in this command's lineage is never selected.
func sweepRun(projectID, env, runID string, since time.Time, t *stopTable, lineage procguard.Lineage) runSweep {
	pids := make([]int, 0, len(t.procs))
	for pid := range t.procs {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	marked := map[int]bool{}
	var unknown []int
	for _, pid := range pids {
		row := t.procs[pid]
		if pid <= 1 || lineage.Contains(pid) || row.exited {
			continue
		}
		environ, ok := t.environ(pid)
		if !ok {
			if !row.foreign && (row.started.IsZero() || !row.started.Before(since)) {
				unknown = append(unknown, pid)
			}
			continue
		}
		if markerValue(environ, forgeUpRunVar) != runID {
			continue
		}
		if _, matched := markerMatches(environ, projectID, env); matched {
			marked[pid] = true
		}
	}
	return runSweep{roots: subtreeRoots(marked, t), unknown: unknown}
}

// markerValue is the last value of key in a KEY=VALUE environment (exec's
// last-wins semantics), or "".
func markerValue(environ []string, key string) string {
	prefix := key + "="
	v := ""
	for _, kv := range environ {
		if strings.HasPrefix(kv, prefix) {
			v = kv[len(prefix):]
		}
	}
	return v
}

// allExited returns a channel closed once every managed process's exit has
// been observed (its Wait returned).
func allExited(procs []*managedProcess) chan struct{} {
	ch := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, mp := range procs {
			mp := mp
			wg.Add(1)
			go func() {
				defer wg.Done()
				if mp.done != nil {
					<-mp.done
				} else if mp.cmd != nil {
					_ = mp.cmd.Wait()
				}
			}()
		}
		wg.Wait()
		close(ch)
	}()
	return ch
}

func anyAlive(pids map[int]bool) bool {
	for pid := range pids {
		if processAlive(pid) {
			return true
		}
	}
	return false
}

func sortedPIDs(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// airStopDrain is how long the air runner of spec waits for its server after
// interrupting it (`[build] kill_delay`, read with air's own rule: a value
// under a millisecond is a count of milliseconds). Zero when the config cannot
// be read, or when air does not interrupt at all (send_interrupt = false: it
// SIGKILLs the server at once). dir is the directory air runs in.
func airStopDrain(spec hostlaunch.RunnerSpec, dir string) time.Duration {
	if strings.TrimSpace(spec.Runner) != "air" {
		return 0
	}
	path := spec.AirConfig
	if path == "" {
		path = hostlaunch.DefaultAirConfig
	}
	if !filepath.IsAbs(path) && dir != "" {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return parseAirStopDrain(data)
}

func parseAirStopDrain(data []byte) time.Duration {
	var cfg struct {
		Build struct {
			SendInterrupt bool          `toml:"send_interrupt"`
			KillDelay     time.Duration `toml:"kill_delay"`
		} `toml:"build"`
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil || !cfg.Build.SendInterrupt {
		return 0
	}
	d := cfg.Build.KillDelay
	if d > 0 && d < time.Millisecond {
		d *= time.Millisecond
	}
	return d
}
