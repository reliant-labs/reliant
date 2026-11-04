//go:build !windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/internal/procguard"
)

// startInOwnProcessGroup makes the child the leader of a new process
// group (pgid == child pid) so the whole subtree it spawns can be
// signalled at once.
//
// The load-bearing case is `go run`: it compiles to a temp binary and
// execs it as a CHILD, and it does NOT forward signals to that child.
// SIGTERM to the `go run` parent alone leaves the real server orphaned
// (reparented to init) and still holding its port — exactly the stale-
// :3090 squatter that bumped reliant-api-server to :3091. Putting the
// parent in its own group and signalling the GROUP on shutdown takes the
// orphan-prone child down with it. Air/binary/delve benefit too (any
// runner that forks a grandchild).
func startInOwnProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalProcessGroup sends sig to the entire process group led by pid —
// a negative pid targets the group, per kill(2). Falls back to signalling
// just pid if the group send fails (e.g. the leader already reaped). A
// non-positive pid is a no-op so a stale 0/-1 in the state file can never
// fan a signal out to unrelated processes. A member of this command's own
// lineage is refused, and a group holding one is narrowed to pid alone (see
// procguard).
func signalProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	self := procguard.Self()
	if self.Contains(pid) {
		return &procguard.LineageError{PID: pid, Command: commandLabel(readProcArgv(pid))}
	}
	if self.ContainsGroup(pid) {
		return syscall.Kill(pid, sig)
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		return syscall.Kill(pid, sig)
	}
	return nil
}

// processAlive reports whether pid is a live process, via a signal-0 probe.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// killProcessTree signals pid, its process group, AND every transitive
// descendant. The tree walk is the robust part: a runner like Air re-execs
// its server in a NEW process group on every rebuild, so signalling only
// pid's group (signalProcessGroup) leaves the respawned child squatting its
// port. Walking ppid catches it regardless of group/session games — the
// parent/child relationship is the one stable handle. Descendants are
// collected BEFORE signalling so a dying tree's shifting ppids can't hide
// a child.
//
// It is also the last line of the lineage guard (procguard): the teardown
// planners already set aside every root that hosts this command, and this
// refuses one that reaches here anyway, skips a group-wide signal to a group
// this command belongs to, and never signals a lineage member found among the
// descendants.
func killProcessTree(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	self := procguard.Self()
	if self.Contains(pid) {
		return &procguard.LineageError{PID: pid, Command: commandLabel(readProcArgv(pid))}
	}
	descendants := descendantPIDs(pid)
	targets := make([]int, 0, len(descendants)+2)
	if !self.ContainsGroup(pid) {
		targets = append(targets, -pid)
	}
	targets = append(targets, pid)
	for _, d := range descendants {
		if !self.Contains(d) {
			targets = append(targets, d)
		}
	}
	var failures []error
	// A missing group is normal after its leader exits. Permission failures
	// are not proof of shutdown, including when a direct leader signal works.
	for _, target := range targets {
		if err := syscall.Kill(target, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			failures = append(failures, fmt.Errorf("signal %s to pid/group %d: %w", sig, target, err))
		}
	}
	return errors.Join(failures...)
}

// ppidMap returns a pid→ppid snapshot of the whole process table, read
// from `ps` (works on macOS and Linux without /proc). Used by the
// ownership resolver to walk a port-holder's ancestry looking for the
// FORGE_UP_ENV marker. A nil/empty map (ps failed) makes ancestry walks a
// no-op — the caller then only trusts the holder's own env.
func ppidMap() map[int]int {
	out, err := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	m := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		m[pid] = ppid
	}
	return m
}

// procStartTimes returns the wall-clock start time of each requested pid, read
// from `ps -o lstart=` (works on macOS and Linux without /proc) in ONE call so
// the whole set costs a single fork. Used by `forge env status` build-freshness
// to tell a freshly-launched server from a straggler running an older build
// (its start predates the on-disk binary's mtime). A pid missing from the
// result had no readable start time (dead, or `ps` unavailable) and the caller
// omits its started_at.
func procStartTimes(pids []int) map[int]time.Time {
	out := map[int]time.Time{}
	list := make([]string, 0, len(pids))
	for _, p := range pids {
		if p > 0 {
			list = append(list, strconv.Itoa(p))
		}
	}
	if len(list) == 0 {
		return out
	}
	o, err := exec.CommandContext(context.Background(),
		"ps", "-o", "pid=,lstart=", "-p", strings.Join(list, ",")).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(o), "\n") {
		fields := strings.Fields(line)
		// "<pid> Dow Mon DD HH:MM:SS YYYY" → pid + 5 lstart fields. Fields()
		// collapses the padded single-digit day, so a single-space layout
		// parses uniformly.
		if len(fields) < 6 {
			continue
		}
		pid, perr := strconv.Atoi(fields[0])
		if perr != nil {
			continue
		}
		ts := strings.Join(fields[1:6], " ")
		t, terr := time.ParseInLocation("Mon Jan 2 15:04:05 2006", ts, time.Local)
		if terr != nil {
			continue
		}
		out[pid] = t
	}
	return out
}

// descendantPIDs returns every transitive child of root, reading the
// (pid, ppid) table from `ps` so it works on macOS and Linux alike without
// depending on /proc.
func descendantPIDs(root int) []int {
	out, err := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	var result []int
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				result = append(result, c)
				queue = append(queue, c)
			}
		}
	}
	return result
}
