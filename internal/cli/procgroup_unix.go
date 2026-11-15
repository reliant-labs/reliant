//go:build !windows

package cli

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"sort"
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
//
// A zombie is not alive. kill(2) still finds a process that has exited and
// not been reaped — on Linux and macOS alike signal 0 succeeds on it — but it
// runs nothing, holds no port and no file, and no signal can change that. A
// stop that waited for one to "exit" waited for its parent's wait(2) instead:
// forever, when the parent is a hung `forge env up` that never reaps, and then
// reported a stopped stack as still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	return !processExited(pid)
}

// killProcessTree signals pid, its process group, every transitive
// descendant, AND every process group a descendant leads. The tree walk is
// the robust part: a runner like Air starts its server in a NEW process group
// (Setpgid) on every rebuild, so signalling only pid's group
// (signalProcessGroup) leaves that server squatting its port. Walking ppid
// catches it regardless of group/session games — the parent/child
// relationship is the one stable handle — and signalling the group it leads
// catches what IT forked (a `sh -c` wrapper's children). Descendants are
// collected BEFORE signalling so a dying tree's shifting ppids can't hide a
// child.
//
// The walk only sees what is still attached: a runner that has already died
// left its children with init. Teardown therefore keeps every runner alive
// until it is signalled here (procRegistry.start) and sweeps the run's
// ownership markers for what escaped anyway (procRegistry.shutdown).
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
	// The mark is read BEFORE the snapshot: everything the snapshot lists
	// started before it, and a process that took over a listed pid later
	// started after it.
	mark, markOK := startMark()
	table := processTable()
	targets := treeSignalTargets(pid, table, self.Contains, self.ContainsGroup)
	v := stampVerifier{mark: mark, ok: markOK, read: readProcStamp}
	return signalTreeTargets(targets, table, v, self.Contains, syscall.Kill, nothingLiveAt, sig)
}

// signalTreeTargets sends sig to each of treeSignalTargets' targets, verifying
// every one against the process table right before its kill (stampVerifier):
//
//   - a pid is signalled only if it is still the process the snapshot listed;
//   - a group (a negative target) is signalled only if its leader still is,
//     AND still leads it. Otherwise the group is never signalled — its number
//     may since have been taken by an unrelated group — and each process the
//     snapshot listed in it is signalled on its own instead, each verified as
//     still that process and still in that group.
//
// Where the platform cannot verify (stampVerifier.ok false), pids are
// signalled as listed and groups never are.
//
// A kill that fails is a failure, except ESRCH (the target is already gone,
// normal mid-teardown) and an EPERM after which settled reports that nothing
// live remains at the target. macOS answers kill(-pgid) with EPERM when every
// member of the group is a zombie — exited, not yet reaped — so a runner that
// exited unreaped turned its own process group into a permanent "permission"
// failure and `forge env down` kept the records of a stack that was already
// gone. EPERM on anything still running (another user's process) stays a
// failure: permission failures are not proof of shutdown.
func signalTreeTargets(targets []int, table map[int]procRow, v stampVerifier, inLineage func(int) bool, kill func(int, syscall.Signal) error, settled func(target int) bool, sig syscall.Signal) error {
	var failures []error
	sent := map[int]bool{}
	send := func(target int) {
		if sent[target] {
			return
		}
		sent[target] = true
		err := kill(target, sig)
		switch {
		case err == nil, errors.Is(err, syscall.ESRCH):
		case errors.Is(err, syscall.EPERM) && settled(target):
		default:
			failures = append(failures, fmt.Errorf("signal %s to pid/group %d: %w", sig, target, err))
		}
	}
	for _, target := range targets {
		if target > 0 {
			if v.samePID(target) {
				send(target)
			}
			continue
		}
		group := -target
		if v.leads(group) {
			send(target)
			continue
		}
		for _, member := range groupMembers(group, table) {
			if !inLineage(member) && v.inGroup(member, group) {
				send(member)
			}
		}
	}
	return errors.Join(failures...)
}

// groupMembers is every pid the snapshot lists in process group pgid, sorted.
func groupMembers(pgid int, table map[int]procRow) []int {
	var out []int
	for pid, row := range table {
		if row.pgid == pgid {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}

// nothingLiveAt reports whether a kill(2) target has nothing left running —
// a pid that is gone or a zombie, a group (negative) with no member that is
// not a zombie — read from a fresh `ps`. An unreadable table is not proof:
// false.
func nothingLiveAt(target int) bool {
	out, err := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,pgid=,stat=").Output()
	if err != nil {
		return false
	}
	states := parseProcStates(string(out))
	if len(states) == 0 {
		return false
	}
	return nothingLiveIn(target, states)
}

// procState is one `ps -axo pid=,pgid=,stat=` row.
type procState struct {
	pgid   int
	exited bool // a zombie
}

// parseProcStates parses `ps -axo pid=,pgid=,stat=`. Lines that are not two
// integers and a state are skipped. A state starting with Z is a zombie.
func parseProcStates(out string) map[int]procState {
	m := map[int]procState{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		pgid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		m[pid] = procState{pgid: pgid, exited: strings.HasPrefix(f[2], "Z")}
	}
	return m
}

// nothingLiveIn is nothingLiveAt's decision over a read table.
func nothingLiveIn(target int, states map[int]procState) bool {
	if target > 0 {
		st, ok := states[target]
		return !ok || st.exited
	}
	for _, st := range states {
		if st.pgid == -target && !st.exited {
			return false
		}
	}
	return true
}

// parseProcStatState extracts the state letter (field 3) from a Linux
// /proc/<pid>/stat, read after the LAST ')' as parseProcStatStamp does.
func parseProcStatState(data []byte) (string, bool) {
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// procStamp identifies one live process: its process group and its start
// time, in the platform's own unit and clock (startMark's: microseconds of
// wall clock on macOS, clock ticks since boot on Linux). A pid is
// unique among LIVE processes, so a process alive now whose start precedes a
// mark is the very process that held that pid at the mark.
type procStamp struct {
	pgid  int
	start int64
}

// stampVerifier re-checks, right before a kill, that a pid from the snapshot
// still names the process the snapshot saw. mark is startMark() read before
// the snapshot; ok is false where the platform cannot read stamps.
//
// The check is not atomic with the kill: no Unix offers a handle on a process
// GROUP (Linux's pidfd covers one process), so a few syscalls separate the
// read from kill(2). In that window the leader would have to exit, every
// member of its group exit, the pid be allocated again and the new process
// lead a group — a full pid-space wrap in microseconds. Before this check the
// window was the whole teardown (up to the stop grace).
type stampVerifier struct {
	mark int64
	ok   bool
	read func(pid int) (procStamp, bool)
}

// samePID reports whether pid still names the process the snapshot listed.
// Unverifiable platforms answer true: a pid kill there is what it always was.
func (v stampVerifier) samePID(pid int) bool {
	if !v.ok {
		return true
	}
	st, ok := v.read(pid)
	return ok && st.start < v.mark
}

// inGroup reports whether pid is still the snapshot's process AND still in
// process group pgid. Unverifiable platforms answer false.
func (v stampVerifier) inGroup(pid, pgid int) bool {
	if !v.ok {
		return false
	}
	st, ok := v.read(pid)
	return ok && st.pgid == pgid && st.start < v.mark
}

// leads reports whether process group pgid is still the one the snapshot saw:
// its leader (pid == pgid) is still the snapshot's process and still leads
// it. While the leader lives — even as an unreaped zombie — its pid, and so
// the group number, cannot be reused, so the group signalled is the group
// verified. Unverifiable platforms answer false.
func (v stampVerifier) leads(pgid int) bool {
	return v.inGroup(pgid, pgid)
}

// parseProcStatStamp extracts the process group and start time from a Linux
// /proc/<pid>/stat. Fields are read after the LAST ')' (comm may contain
// spaces and parentheses): fields[0] is field 3 (state), fields[2] field 5
// (pgrp), fields[19] field 22 (starttime). starttime is kept as the kernel
// reports it, in clock ticks since boot, and compared against a mark in the
// same ticks (linuxStartMark): no conversion, so no rounding to reason about.
func parseProcStatStamp(data []byte) (procStamp, bool) {
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return procStamp{}, false
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return procStamp{}, false
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return procStamp{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return procStamp{}, false
	}
	return procStamp{pgid: pgid, start: ticks}, true
}

// linuxStartMark turns /proc/uptime ("12345.67 54321.00") into a mark in the
// unit of /proc/<pid>/stat's starttime — clock ticks since boot at hz, the
// kernel's real USER_HZ (linuxClockTicks) — such that NO process started at
// or after the uptime read has a starttime below it. stampVerifier accepts a
// stamp only when start < mark.
//
// Proof. Let T be the boot-time instant, in ns, at which the kernel formatted
// /proc/uptime, and S >= T the boot-time instant a later process started.
//
//  1. Uptime truncation: fs/proc/uptime.c prints tv_sec and tv_nsec/10^7 of
//     ktime_get_boottime, truncated, so U = sec·10^9 + cs·10^7 <= T.
//  2. Starttime truncation: fs/proc/array.c reports
//     nsec_to_clock_t(start_boottime), and start_boottime = S (boot time,
//     ns, taken at fork; boot time on both sides since Linux 5.3 — before,
//     starttime used the monotonic clock, which stops in suspend, so on such
//     a kernel after a suspend the check is weaker by the time suspended).
//     nsec_to_clock_t (kernel/time/time.c) is one of three floors of x times
//     a positive constant; kernelClockTicks reproduces it branch for branch,
//     in 128-bit arithmetic so no product overflows and no division precedes
//     a multiplication. Every branch is non-decreasing in x.
//  3. So starttime = c(S) >= c(U), as S >= T >= U and c is non-decreasing.
//  4. mark = c(U) - m with m = ceil(hz/100) + 1 >= 1, so starttime >= mark + m
//     > mark: the process is refused. QED.
//
// Step 4 needs only m >= 1. The rest of m is margin against an input off by a
// quantum — an uptime rounded rather than truncated (up to 10ms, ceil(hz/100)
// ticks) — so the bound does not rest on that kernel detail. The cost: a
// process younger than about 10ms plus m+1 ticks at the read
// (linuxVerifiableAge) cannot verify. Its pid and group are then not
// signalled by that pass; the later passes of a stop (the grace sweeps, the
// SIGKILL pass) re-read the table, by when it is old enough.
func linuxStartMark(uptime []byte, hz int64) (int64, bool) {
	if hz <= 0 {
		return 0, false
	}
	f := strings.Fields(string(uptime))
	if len(f) == 0 {
		return 0, false
	}
	whole, frac, _ := strings.Cut(f[0], ".")
	secs, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	// Centiseconds, read as written: "47" is 47, "5" is 50, "123" is 12.
	frac = (frac + "00")[:2]
	cs, err := strconv.ParseUint(frac, 10, 64)
	if err != nil || secs > (1<<63-1)/1_000_000_000-1 {
		return 0, false
	}
	u := secs*1_000_000_000 + cs*10_000_000
	return int64(kernelClockTicks(u, uint64(hz))) - linuxMarkMargin(hz), true
}

// linuxMarkMargin is m in linuxStartMark's proof: one 10ms uptime quantum in
// ticks, rounded up, plus one tick.
func linuxMarkMargin(hz int64) int64 {
	return (hz+99)/100 + 1
}

// linuxVerifiableAge is how long before the uptime read a process must have
// started to be sure to verify: the uptime truncation (10ms) plus m+1 ticks
// (m for the margin, one for the starttime truncation), in ns, rounded up.
func linuxVerifiableAge(hz int64) int64 {
	return 10_000_000 + ((linuxMarkMargin(hz)+1)*1_000_000_000+hz-1)/hz
}

// kernelClockTicks is the kernel's nsec_to_clock_t at USER_HZ hz, branch for
// branch (kernel/time/time.c):
//
//	NSEC_PER_SEC % hz == 0:  x / (NSEC_PER_SEC/hz)
//	hz % 512 == 0:           x*hz/512 / (NSEC_PER_SEC/512)
//	otherwise:               x*9 / ((9*NSEC_PER_SEC + hz/2) / hz)
//
// The products are taken in 128 bits; the kernel's own 64-bit products only
// overflow after decades of uptime, where it is the kernel that is wrong.
func kernelClockTicks(x, hz uint64) uint64 {
	const nsPerSec = 1_000_000_000
	mulDiv := func(a, b, d uint64) uint64 {
		hi, lo := bits.Mul64(a, b)
		q, _ := bits.Div64(hi%d, lo, d) // hi < d for any x below 2^63
		return q
	}
	switch {
	case nsPerSec%hz == 0:
		return x / (nsPerSec / hz)
	case hz%512 == 0:
		return mulDiv(x, hz/512, nsPerSec/512)
	default:
		return mulDiv(x, 9, (9*nsPerSec+hz/2)/hz)
	}
}

// atClkTck is AT_CLKTCK, the auxiliary-vector key under which the kernel
// passes USER_HZ to every process (the source sysconf(_SC_CLK_TCK) reads).
// Its value, 17, is fixed by the ELF ABI on every Linux architecture.
const atClkTck = 17

// clockTicksFromAuxv returns AT_CLKTCK from an auxiliary vector, or false
// when it is absent or not a positive rate.
func clockTicksFromAuxv(auxv [][2]uintptr) (int64, bool) {
	for _, kv := range auxv {
		if kv[0] == atClkTck {
			if hz := int64(kv[1]); hz > 0 {
				return hz, true
			}
			return 0, false
		}
	}
	return 0, false
}

// parseAuxv decodes a raw auxiliary vector (/proc/self/auxv): pairs of
// native-endian machine words, ending at AT_NULL (0).
func parseAuxv(data []byte, wordSize int, order binary.ByteOrder) [][2]uintptr {
	var out [][2]uintptr
	word := func(b []byte) uintptr {
		if wordSize == 4 {
			return uintptr(order.Uint32(b))
		}
		return uintptr(order.Uint64(b))
	}
	for off := 0; off+2*wordSize <= len(data); off += 2 * wordSize {
		key := word(data[off:])
		if key == 0 {
			break
		}
		out = append(out, [2]uintptr{key, word(data[off+wordSize:])})
	}
	return out
}

// treeSignalTargets is killProcessTree's pure decision: the kill(2) targets
// for root's tree, read from one process-table snapshot. Negative entries are
// process groups. It names root's group, root, every descendant, and the group
// of every descendant that leads one (pgid == pid). A group is only ever
// signalled when its leader is in the tree, so no group a stranger leads is
// reached; and nothing in this command's lineage, nor a group it belongs to,
// is ever a target.
//
// These are CANDIDATES from a snapshot: signalTreeTargets re-verifies each one
// at kill time, because a pid or group number can change hands in between.
func treeSignalTargets(root int, table map[int]procRow, inLineage, lineageGroup func(int) bool) []int {
	descendants := descendantsIn(root, table)
	targets := make([]int, 0, 2*len(descendants)+2)
	if !lineageGroup(root) {
		targets = append(targets, -root)
	}
	targets = append(targets, root)
	for _, d := range descendants {
		if inLineage(d) {
			continue
		}
		targets = append(targets, d)
		if row, ok := table[d]; ok && row.pgid == d && !lineageGroup(d) {
			targets = append(targets, -d)
		}
	}
	return targets
}

// procRow is one process-table entry: a pid's parent and process group.
type procRow struct {
	ppid int
	pgid int
}

// processTable returns a pid→(ppid, pgid) snapshot of the whole process table,
// read from `ps` (works on macOS and Linux without /proc). Nil when ps fails:
// tree walks then see no descendants, and a caller signals the root alone.
func processTable() map[int]procRow {
	out, err := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,ppid=,pgid=").Output()
	if err != nil {
		return nil
	}
	return parseProcessTable(string(out))
}

// parseProcessTable parses `ps -axo pid=,ppid=,pgid=` output. Lines that are
// not three integers are skipped.
func parseProcessTable(out string) map[int]procRow {
	m := map[int]procRow{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		m[pid] = procRow{ppid: ppid, pgid: pgid}
	}
	return m
}

// descendantsIn returns every transitive child of root in table, breadth
// first. Cycle-safe.
func descendantsIn(root int, table map[int]procRow) []int {
	children := map[int][]int{}
	for pid, row := range table {
		children[row.ppid] = append(children[row.ppid], pid)
	}
	for _, c := range children {
		sort.Ints(c)
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
// process table from `ps` so it works on macOS and Linux alike without
// depending on /proc.
func descendantPIDs(root int) []int {
	return descendantsIn(root, processTable())
}

// readOSStopTable reads the process table the shutdown sweep needs — parent,
// owner, state and start time of every process — in one `ps` call. An error
// (ps failed, or printed nothing parseable) means the table is unknown, which
// the sweep must never read as empty.
func readOSStopTable() (*stopTable, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,ppid=,uid=,stat=,lstart=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	procs := parseStopTable(string(out), os.Getuid())
	if len(procs) == 0 {
		return nil, errors.New("ps printed no processes")
	}
	return &stopTable{procs: procs, readEnv: readProcEnviron, readArgv: readProcArgv, isAlive: processAlive}, nil
}

// parseStopTable parses `ps -axo pid=,ppid=,uid=,stat=,lstart=`. A process
// owned by another uid is foreign (root, uid 0, may own anything it started,
// so nothing is foreign to it). A state starting with Z is a zombie. An
// unparseable start time is left zero: unknown, never "before the run".
func parseStopTable(out string, uid int) map[int]stopProc {
	procs := map[int]stopProc{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		owner, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		row := stopProc{ppid: ppid, foreign: uid != 0 && owner != uid, exited: strings.HasPrefix(f[3], "Z")}
		if len(f) >= 9 {
			if t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[4:9], " "), time.Local); err == nil {
				row.started = t
			}
		}
		procs[pid] = row
	}
	return procs
}
