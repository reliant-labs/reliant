package cli

// Build-freshness + duplicate/orphan-process detection for `forge env status`.
//
// The status report's first half (up.go) answers "is the port up, and is the
// holder forge-owned?" from a port probe. This half answers the two questions
// that a port probe cannot:
//
//   - "Is the running server on my latest code?" — for each host service it
//     resolves the LIVE server process(es), their binary path + build mtime +
//     process start time, and flags a binary that predates the repo HEAD
//     commit (or that a rebuild has already superseded).
//
//   - "Is more than one process serving this service?" — the air-leak symptom.
//     air rebuilds a watched service by spawning a fresh server and reaping the
//     old one; when the reap fails, two server processes (different build
//     vintages) run at once and a stale one can answer — and fail — a request.
//     This sweeps the process table for THIS project+env's ownership marker,
//     groups by service, and FLAGS any service backed by more than one server
//     process.
//
// Both reuse the (project, env) ownership marker the reclaim guard stamps
// (up_reclaim.go): a marker sweep finds every server process regardless of pid
// churn or which one currently holds the port, which is exactly what a
// port-only probe misses when a stale straggler has lost the port to its
// replacement.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// enrichServing fills each HOST row's Serving list (the live server processes
// backing it, with build-freshness) and Duplicate flag, by sweeping the
// process table for this project+env's ownership marker. Frontends are left
// alone: their dev servers (next/vite) legitimately fork worker pools that a
// leaf count would misread as duplicates, and a JS dev server has no compiled
// binary whose mtime means anything. headCommit is the freshness yardstick
// (zero → no stale-vs-HEAD flags, but the straggler/deleted signals still
// apply). Best-effort: on a platform without process inspection the marker
// sweep is empty and every row's Serving stays nil, degrading the report to
// health-only without misfiring.
func enrichServing(rows []upServiceRow, projectID, env string, headCommit time.Time) {
	facts := newOSProcFacts()
	enrichServingWith(rows, projectID, env, headCommit, facts, facts.pidList(), procStartTimes)
}

// enrichServingWith is enrichServing's body with the OS seams injected: the
// procFacts (env/ppid/argv), the candidate pid set, and the start-time lookup.
// enrichServing supplies the real ones; tests supply fixtures, so the whole
// path — marker sweep → leaves → argv attribution → row fields — runs without a
// real process table.
func enrichServingWith(rows []upServiceRow, projectID, env string, headCommit time.Time, facts procFacts, pids []int, startTimes func([]int) map[int]time.Time) {
	groups := markedServiceGroups(projectID, env, pids, facts)
	if len(groups) == 0 {
		return
	}
	// Resolve leaves per host row first, gather every leaf pid, then fetch all
	// start times in ONE `ps` call (rather than one fork per pid).
	type pending struct {
		row    int
		leaves []int
	}
	var pend []pending
	var allPIDs []int
	for i := range rows {
		if rows[i].Kind != "host" {
			continue
		}
		group := groups[serviceMarkerName(rows[i])]
		if len(group) == 0 {
			continue
		}
		leaves := leafPIDs(group, facts)
		pend = append(pend, pending{row: i, leaves: leaves})
		allPIDs = append(allPIDs, leaves...)
	}
	if len(pend) == 0 {
		return
	}
	starts := startTimes(allPIDs)
	for _, pd := range pend {
		procs := make([]servingProc, 0, len(pd.leaves))
		for _, pid := range pd.leaves {
			sp := buildServingProc(pid, starts[pid], headCommit)
			if args, ok := facts.argv(pid); ok {
				sp.Argv = args
				sp.Command = processCommand(args)
			}
			procs = append(procs, sp)
		}
		rows[pd.row].Serving = procs
		dups, undetermined := attributeDuplicates(procs)
		rows[pd.row].Duplicates = dups
		rows[pd.row].Duplicate = len(dups) > 0
		rows[pd.row].AttributionUndetermined = undetermined
	}
}

// processCommand derives a stable COMMAND IDENTITY from a process's argv — the
// key duplicate attribution groups by. It is the executable's base name plus
// every leading non-flag argument, so `/usr/local/bin/reliant server worker
// --queue=x` and `/opt/reliant server worker --queue=y` both identify as
// "reliant server worker".
//
// Stopping at the first flag is what makes the identity stable across the two
// processes a duplicate consists of: a rebuild's replacement is the same
// subcommand, but its flags routinely differ (a kernel-assigned port, a
// per-run temp dir), and folding those in would split a genuine duplicate into
// two singletons — a duplicate that stops being reported. Empty when argv is
// empty or carries no executable, which the caller reads as "unattributable".
func processCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	exe := strings.TrimSpace(argv[0])
	if exe == "" {
		return ""
	}
	parts := []string{filepath.Base(exe)}
	for _, a := range argv[1:] {
		if a == "" || strings.HasPrefix(a, "-") {
			break
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// attributeDuplicates decides WHICH command a service row's duplicate is, from
// the argv-derived identity of each serving process — not from the row's name.
//
// This is the fix for the defect that a marker group is not a command group.
// The FORGE_UP_SERVICE marker rides the environment and therefore PROPAGATES to
// every descendant, so a `reliant server worker` spawned under the api-server's
// marker lands in the api-server group. Counting that group's leaves reports
// "2 processes serving api-server" — detection right, attribution wrong. A
// duplicated worker is the case that actually corrupts a run (two workers
// polling one Temporal task queue), so naming it as a service it is not is the
// shape that gets a true warning dismissed.
//
// Returns one group per command that has MORE THAN ONE live process, sorted by
// command for a deterministic report. undetermined is true when the row has
// several processes and at least one of them has no readable argv: that
// process could be a duplicate of any other, and there is no honest way to say
// which. Callers must report undetermined as an explicit unknown — a
// confidently wrong attribution is worse than an admitted one.
func attributeDuplicates(procs []servingProc) (groups []duplicateGroup, undetermined bool) {
	if len(procs) <= 1 {
		return nil, false
	}
	byCommand := map[string][]int{}
	for _, sp := range procs {
		if sp.Command == "" {
			// Unattributable: it may or may not duplicate one of the others.
			undetermined = true
			continue
		}
		byCommand[sp.Command] = append(byCommand[sp.Command], sp.PID)
	}
	for cmd, pids := range byCommand {
		if len(pids) <= 1 {
			continue
		}
		sort.Ints(pids)
		groups = append(groups, duplicateGroup{Command: cmd, PIDs: pids})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Command < groups[j].Command })
	return groups, undetermined
}

// serviceMarkerName maps a report row back to the FORGE_UP_SERVICE marker value
// its process was stamped with at launch: a host service is stamped with its
// bare name, a frontend with the "frontend:<name>" label procRegistry.start
// uses. This is the key the marker sweep groups by.
func serviceMarkerName(r upServiceRow) string {
	if r.Kind == "frontend" {
		return "frontend:" + r.Name
	}
	return r.Name
}

// buildServingProc gathers the build-freshness facts for one server pid: its
// executable path, that binary's mtime, the process start time, and whether it
// is stale. "Stale" folds three ways a process can be running non-current code:
//
//   - its binary was deleted out from under it (Linux marks the exe link
//     "(deleted)" when air rebuilds over a still-running process),
//   - its binary predates the repo HEAD commit (headCommit non-zero), or
//   - the on-disk binary was rebuilt AFTER this process started (a newer build
//     exists; this process is the straggler) — the cross-platform signal that
//     survives even when the binary path resolves to the fresh file.
func buildServingProc(pid int, start, headCommit time.Time) servingProc {
	sp := servingProc{PID: pid}
	if !start.IsZero() {
		sp.StartedAt = start.UTC().Format(time.RFC3339)
	}
	path, ok := procExecPath(pid)
	if !ok || path == "" {
		return sp
	}
	sp.Path = path
	// Linux tags a replaced-under-a-running-process binary; that alone means
	// the process is running a build no longer on disk.
	real := path
	deleted := false
	if strings.HasSuffix(real, " (deleted)") {
		real = strings.TrimSuffix(real, " (deleted)")
		deleted = true
	}
	var mtime time.Time
	if info, err := os.Stat(real); err == nil {
		mtime = info.ModTime()
		sp.BuiltAt = mtime.UTC().Format(time.RFC3339)
	}
	sp.Stale = binaryStale(mtime, start, headCommit, deleted)
	return sp
}

// binaryStale decides whether a server process is running non-current code
// from its binary mtime, its own start time, the repo HEAD commit time, and
// whether the binary was deleted under it. Split out from buildServingProc so
// the classification is unit-testable without a real pid / filesystem. Any one
// of the three conditions is sufficient:
//
//   - deleted: the binary no longer exists on disk (air rebuilt over it),
//   - mtime before headCommit: the binary predates the latest commit, or
//   - mtime after start: a newer build exists than the one this process
//     started with (this process is the straggler).
//
// A zero mtime (unstatable) leaves the last two checks off; a zero headCommit
// (no git) or zero start (no `ps`) individually disables only its own check.
func binaryStale(mtime, start, headCommit time.Time, deleted bool) bool {
	if deleted {
		return true
	}
	if mtime.IsZero() {
		return false
	}
	if !headCommit.IsZero() && mtime.Before(headCommit) {
		return true
	}
	if !start.IsZero() && mtime.After(start) {
		return true
	}
	return false
}

// markedServiceGroups sweeps a candidate pid set and groups the processes owned
// by (projectID, envName) — those whose environment carries BOTH matching
// ownership markers, the same predicate the reclaim guard uses — keyed by their
// FORGE_UP_SERVICE marker. A process with no/foreign markers is skipped, so a
// different project's stack on the same env name never enters a group. Pure
// (procFacts injected) → unit-testable without a real process table.
func markedServiceGroups(projectID, envName string, pids []int, f procFacts) map[string][]int {
	groups := map[string][]int{}
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		env, ok := f.environ(pid)
		if !ok {
			continue
		}
		if svc, matched := markerMatches(env, projectID, envName); matched {
			groups[svc] = append(groups[svc], pid)
		}
	}
	return groups
}

// leafPIDs reduces a service's marked-process group to its LEAF processes — the
// members that are not an ancestor of any other member. For a healthy air/
// go-run service the tree is runner → server, so the runner (air / go) is
// internal and the single server is the one leaf. The air leak adds a second
// server (child of the runner, or an orphan reparented to init when the runner
// stopped waiting on it); neither is an ancestor of the other, so BOTH are
// leaves and the count rises to 2 — the duplicate signal. Pure (procFacts
// injected); leaves are returned sorted for a deterministic report.
func leafPIDs(group []int, f procFacts) []int {
	inGroup := make(map[int]bool, len(group))
	for _, p := range group {
		inGroup[p] = true
	}
	// A group member is "internal" (not a leaf) when it appears as an ancestor
	// of some other member. Walk each member's ancestry, marking any ancestor
	// that is itself in the group.
	internal := map[int]bool{}
	for _, p := range group {
		pid := p
		for depth := 0; depth < maxAncestryDepth; depth++ {
			ppid, ok := f.parent(pid)
			if !ok || ppid <= 1 || ppid == pid {
				break
			}
			if inGroup[ppid] {
				internal[ppid] = true
			}
			pid = ppid
		}
	}
	var leaves []int
	for _, p := range group {
		if !internal[p] {
			leaves = append(leaves, p)
		}
	}
	sort.Ints(leaves)
	return leaves
}

// headCommitTime returns the commit time of the project's git HEAD — the
// yardstick build-freshness measures a binary's mtime against. Zero time when
// dir is not a git repo or git is unavailable, in which case the stale-vs-HEAD
// check is simply skipped.
func headCommitTime(dir string) time.Time {
	out, err := exec.CommandContext(context.Background(),
		"git", "-C", dir, "log", "-1", "--format=%ct", "HEAD").Output()
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// anyDuplicateServing reports whether any row carries the duplicate-server flag
// — the trigger for the loud table-header banner.
func anyDuplicateServing(rows []upServiceRow) bool {
	for _, r := range rows {
		if r.Duplicate {
			return true
		}
	}
	return false
}

// anyAttributionUndetermined reports whether any row had several serving
// processes it could NOT attribute for lack of argv. It gets its own banner
// rather than folding into the duplicate one: "there may be a duplicate and I
// cannot tell" is a different claim from "there is one, and it is this
// command", and collapsing the two is how an unknown starts reading as a
// finding.
func anyAttributionUndetermined(rows []upServiceRow) bool {
	for _, r := range rows {
		if r.AttributionUndetermined {
			return true
		}
	}
	return false
}

// joinInts renders a pid list for the duplicate warning ("150, 205").
func joinInts(vals []int) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}

// servingLine renders one serving process for the text table: pid, binary
// path, start + build times, and a STALE marker when the process isn't running
// current code.
func servingLine(sp servingProc) string {
	var b strings.Builder
	b.WriteString("pid ")
	b.WriteString(strconv.Itoa(sp.PID))
	if sp.Path != "" {
		b.WriteString("  ")
		b.WriteString(sp.Path)
	}
	if sp.StartedAt != "" {
		b.WriteString("  started ")
		b.WriteString(shortTime(sp.StartedAt))
	}
	if sp.BuiltAt != "" {
		b.WriteString("  built ")
		b.WriteString(shortTime(sp.BuiltAt))
	}
	if sp.Stale {
		b.WriteString("  STALE")
	}
	return b.String()
}

// shortTime trims an RFC3339 timestamp to its "HH:MM:SS" wall-clock portion for
// the compact text table, falling back to the full string if it doesn't parse.
func shortTime(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Local().Format("15:04:05")
}
