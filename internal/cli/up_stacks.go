// Package cli — the machine-local record of which stacks `forge env up` /
// `forge env up` started, and the teardown that acts on it.
//
// Three files are written per (project, env), under
// $HOME/.cache/forge/up/<project-id>/:
//
//	project.json     the project DIRECTORY <project-id> hashes from
//	<env>.pids       what this run started: "<name>\t<pid>" per line
//	<env>.env.json   the resolved discovery facts (live ports + DATABASE_URL)
//
// The <project-id> segment (see projectIDForDir) is what scopes every record to
// ONE checkout, so two projects bringing up the same env name never read each
// other's. Until project.json it was also ALL that was recorded — and a hash
// cannot be inverted. A stack whose project directory had since been deleted
// was therefore unreachable by every forge verb: `forge env down` derives the
// id from the forge.yaml above the working directory, and there was no
// forge.yaml left to derive it from. Eight rounds of the dogfood loop ended
// that way, leaving 38 orphaned processes, 7.5 GB resident and 15 listening
// ports that nothing could name. Recording the path is what makes that set
// enumerable (`forge env ps`) and stoppable (`forge env down --all`).
//
// These files are a RECORD, never an authority. Teardown proves ownership from
// the LIVE process markers (up_reclaim.go) and signals nothing else: a ledger
// drifts — a crashed run never persisted one, air re-execs under a new pid, and
// a pid the OS has since recycled belongs to a stranger — so "this file
// remembers pid 4242" is not proof that pid 4242 is ours to kill.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/procguard"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// upCacheDir is the machine-local root every stack record lives under.
func upCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "forge", "up"), nil
}

// upProjectCacheDir is the per-project record directory. An empty projectID is
// an error rather than a shared fallback directory: an unidentifiable project
// must never read or write another project's records.
func upProjectCacheDir(projectID string) (string, error) {
	if projectID == "" {
		return "", errors.New("no project id: cannot locate this project's stack records")
	}
	root, err := upCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, projectID), nil
}

// upStatePath returns the (project, env) PID ledger:
//
//	$HOME/.cache/forge/up/<project-id>/<env>.pids
//
// projectID is a parameter, not a global lookup: `forge env down --all` acts on
// projects OTHER than the one containing the working directory, and every
// caller that acts on its own project already holds its id.
func upStatePath(projectID, env string) (string, error) {
	dir, err := upProjectCacheDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, env+".pids"), nil
}

// upEnvStatePath returns the (project, env) discovery cache:
//
//	$HOME/.cache/forge/up/<project-id>/<env>.env.json
//
// Sibling of upStatePath's ledger — the resolved name→port map + DATABASE_URL
// a fresh `forge env status` render cannot recover (see resolvedEnvState).
func upEnvStatePath(projectID, env string) (string, error) {
	dir, err := upProjectCacheDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, env+".env.json"), nil
}

// projectRecordPath returns the per-project path record:
//
//	$HOME/.cache/forge/up/<project-id>/project.json
func projectRecordPath(projectID string) (string, error) {
	dir, err := upProjectCacheDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "project.json"), nil
}

// projectRecord is project.json: the directory <project-id> hashes from, so an
// enumerator can NAME a stack it did not start — including one whose directory
// no longer exists, which is exactly the case that had no name at all.
type projectRecord struct {
	Dir       string `json:"dir"`
	UpdatedAt string `json:"updated_at"`
}

// recordProjectDir stamps the project directory alongside the ledger. Written
// on every persist so a checkout that moved is re-recorded. The stored value is
// the CANONICAL directory — the same string projectIDForDir hashed — so
// projectIDForDir(rec.Dir) round-trips back to this id. Best-effort: losing it
// costs enumeration a name, never the run.
func recordProjectDir(projectID, dir string) {
	path, err := projectRecordPath(projectID)
	if err != nil || dir == "" {
		return
	}
	data, err := json.MarshalIndent(projectRecord{
		Dir:       canonicalProjectDir(dir),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := atomicfile.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// readProjectRecord reads a project's path record. Returns "" when the record
// is absent or unreadable — enumeration then reports the stack by id alone
// rather than failing.
func readProjectRecord(projectID string) string {
	path, err := projectRecordPath(projectID)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var rec projectRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ""
	}
	return rec.Dir
}

// stackKey identifies one stack: the (project, env) pair EVERY ownership
// decision in forge is keyed on.
type stackKey struct {
	projectID string
	env       string
}

// discoverMarkedStacks groups every process carrying BOTH ownership markers by
// the (project, env) pair those markers name. It is markedOrphanRoots' marked
// pass without the "which stack am I looking for" input — and that inversion is
// the whole point: a stack can be found without already knowing its project id,
// which is the only way to reach one whose project directory is gone. A process
// missing either marker (a pre-marker stack, anything forge did not start) is
// not grouped and so is never a teardown candidate. Pure over the injected
// facts.
func discoverMarkedStacks(pids []int, f procFacts) map[stackKey][]int {
	out := map[stackKey][]int{}
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		environ, ok := f.environ(pid)
		if !ok {
			continue
		}
		envName, _, projectID := markerFields(environ)
		if envName == "" || projectID == "" {
			continue
		}
		k := stackKey{projectID: projectID, env: envName}
		out[k] = append(out[k], pid)
	}
	return out
}

// runningStack is one live stack on this machine, as `forge env ps` reports it
// and `forge env down --all` acts on it.
type runningStack struct {
	stackKey
	// dir is the recorded project directory; "" when no project.json survives
	// (a stack started by a forge that predates the record).
	dir string
	// dirExists distinguishes "you can cd there and run `forge env down`" from
	// "this stack outlived its project and --all is the only way to reach it".
	dirExists bool
	// procs counts every live process carrying this stack's markers. The trees
	// a teardown signals are NOT recorded here: stopStack re-reads the process
	// table for each stack it stops, so it acts on the table as it is then, not
	// as enumeration found it.
	procs int
}

// label names a stack for output: its project directory when one was recorded,
// otherwise the raw id (which is still enough for `--all` to act on).
func (s runningStack) label() string {
	if s.dir == "" {
		return "(unrecorded project " + s.projectID + ")"
	}
	if !s.dirExists {
		return s.dir + "  (directory is gone)"
	}
	return s.dir
}

// discoverRunningStacks enumerates every forge-started stack alive on this
// machine, from the live process markers — not from the record files, which
// drift and which a deleted project directory takes with it in spirit if not on
// disk. The records only supply the NAME (project.json) for what the process
// table already found. Sorted for stable output.
func discoverRunningStacks() []runningStack {
	return discoverRunningStacksWithFacts(newOSProcFacts())
}

func discoverRunningStacksWithFacts(facts *osProcFacts) []runningStack {
	grouped := discoverMarkedStacks(facts.pidList(), facts)
	out := make([]runningStack, 0, len(grouped))
	for key, pids := range grouped {
		s := runningStack{
			stackKey: key,
			dir:      readProjectRecord(key.projectID),
			procs:    len(pids),
		}
		if s.dir != "" {
			if _, err := os.Stat(filepath.Join(s.dir, defaultProjectConfigFile)); err == nil {
				s.dirExists = true
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].label() != out[j].label() {
			return out[i].label() < out[j].label()
		}
		return out[i].env < out[j].env
	})
	return out
}

// stackStop is what one teardown did: how many process trees it signalled,
// and which trees it left running because they host this very command.
type stackStop struct {
	stopped int
	skipped []ancestorSkip
}

// ancestorSkip is a teardown root left running because it is in this
// command's own lineage (see procguard). Signalling it would end the process
// running `forge env down` — and with it whatever session that process hosts,
// which on an agent server is every session, not just this one.
type ancestorSkip struct {
	pid     int
	command string
}

func (s ancestorSkip) String() string {
	return fmt.Sprintf("skipped pid %d (%s): it is an ancestor of this command — stopping it would end the session running you", s.pid, s.command)
}

// stackStopPlan is the decision a teardown acts on, made before anything is
// signalled: the trees to signal, and the trees left alone because they host
// this command.
type stackStopPlan struct {
	roots   []int
	skipped []ancestorSkip
}

// planStackStop is the pure decision core of every stack teardown: select the
// (project, env) trees by ownership (stackTeardownRoots), narrow them to the
// scope, then set aside every root in this command's lineage.
//
// Setting aside a ROOT sets aside its whole tree. That is the only coherent
// unit: the tree under an ancestor contains this command, and its siblings —
// other sessions the same server hosts — are the ancestor's to manage, not
// processes forge started. A root that is NOT an ancestor cannot have one in
// its tree (anything below it would make it an ancestor), so every other tree
// is stopped exactly as before. The decision reads only the process table:
// no name, env var or flag can turn it off.
func planStackStop(projectID, env string, scope []string, tracked []trackedProc, pids []int, alive func(int) bool, f procFacts, lineage procguard.Lineage) stackStopPlan {
	roots := stackTeardownRoots(projectID, env, tracked, pids, alive, f)
	if len(scope) > 0 {
		roots = filterRootsByService(roots, scope, f)
	}
	var plan stackStopPlan
	hosting := map[int]bool{}
	for _, pid := range roots {
		if lineage.Contains(pid) {
			hosting[pid] = true
			plan.skipped = append(plan.skipped, ancestorSkip{pid: pid, command: commandLabel(f.argv(pid))})
		}
	}
	for _, pid := range roots {
		// A root BELOW a skipped one belongs to that tree. It only becomes a
		// root of its own when the marked process above it could not be read
		// — and then it is a sibling session of this command, which the
		// skipped tree's owner manages, not forge.
		if hosting[pid] || insideTree(pid, hosting, f) {
			continue
		}
		plan.roots = append(plan.roots, pid)
	}
	return plan
}

// insideTree reports whether any ancestor of pid is one of tops. Bounded and
// cycle-safe; an unknown parent ends the walk.
func insideTree(pid int, tops map[int]bool, f procFacts) bool {
	if len(tops) == 0 {
		return false
	}
	seen := map[int]bool{pid: true}
	for {
		ppid, ok := f.parent(pid)
		if !ok || ppid <= 1 || seen[ppid] {
			return false
		}
		if tops[ppid] {
			return true
		}
		seen[ppid] = true
		pid = ppid
	}
}

// commandLabelMax bounds a command line in a one-line report.
const commandLabelMax = 60

// commandLabel renders a process's argv for a one-line report: the program's
// base name, then its arguments, cut to commandLabelMax runes.
func commandLabel(argv []string, ok bool) string {
	if !ok || len(argv) == 0 {
		return "command unreadable"
	}
	parts := append([]string{filepath.Base(argv[0])}, argv[1:]...)
	label := []rune(strings.Join(parts, " "))
	if len(label) > commandLabelMax {
		return string(label[:commandLabelMax-1]) + "…"
	}
	return string(label)
}

// stopStack tears down the (projectID, env) stack and reports what it
// signalled and what it left running. A failed process scan is not an empty
// stack: preserve its records and report that teardown could not establish its
// scope.
func stopStack(projectID, env string) (stackStop, error) {
	facts := newOSProcFacts()
	return stopStackWithFacts(projectID, env, facts)
}

func stopStackWithFacts(projectID, env string, facts *osProcFacts) (stackStop, error) {
	if err := requireTeardownSnapshot(facts); err != nil {
		return stackStop{}, err
	}
	return stopStackScopedWithFacts(projectID, env, nil, facts)
}

func requireTeardownSnapshot(facts *osProcFacts) error {
	if facts == nil || len(facts.ppids) == 0 {
		return errors.New("cannot inspect the host process table; environment shutdown is unverified and stack records were preserved (check process-inspection permissions)")
	}
	return nil
}

// stopStackScopedWithFacts is stopStack narrowed to the services a run is
// about to REPLACE. With an empty scope it is stopStack: the whole (project,
// env) stack goes, and the records with it.
//
// A non-empty scope is the `forge env up --target` case, and the distinction
// is the whole point of the flag. "One stack per (project, env)" exists so a
// second run cannot leave a half-owned duplicate of a service behind — it is
// about the services being RESTARTED, not about the env as an indivisible
// unit. Tearing the whole env down to restart one frontend contradicts what
// the user asked for and, worse, does it silently: they get a stack with one
// service running and no indication the other five were stopped on the way.
//
// Scoping is by the FORGE_UP_SERVICE marker each child is stamped with,
// resolved through the same ancestry walk ownership uses, so a re-exec'd
// grandchild (air restarting its server) is still attributed to its service.
// A process whose service marker cannot be read is left ALONE under a scoped
// stop: an unattributable process is not evidence that the targeted service
// is running, and killing it would resurrect the very behaviour this
// prevents. It is still reclaimed by an unscoped stop, where the intent is
// "everything in this env".
//
// Records are only removed on an unscoped stop. Under a scope the ledger
// still describes live processes this run is not touching, and dropping it
// would strand them: `forge env down` and `forge env ps` read it. The
// registry rewrites the ledger for the services it starts (see
// procRegistry.persist), which merges rather than replaces for this reason.
// The same holds when a tree was left running because it hosts this command.
func stopStackScopedWithFacts(projectID, env string, scope []string, facts *osProcFacts) (stackStop, error) {
	if err := requireTeardownSnapshot(facts); err != nil {
		return stackStop{}, err
	}
	return executeStackStop(projectID, env, scope, planTeardown(projectID, env, scope, facts), facts, killTreesAndWait)
}

// replaceStack is the `forge env up` pre-flight's stop: the scoped stop
// above, except that a stack hosting this command is a REFUSAL rather than a
// skip.
// "One stack per (project, env)" cannot hold while the predecessor keeps
// running — on kernel-assigned ports nothing would even collide — and the
// predecessor cannot be stopped without ending this command. The decision is
// made before anything is signalled: half-stopping a stack and then refusing
// would be the worst of both.
func replaceStack(projectID, env string, scope []string) (stackStop, error) {
	facts := newOSProcFacts()
	if err := requireTeardownSnapshot(facts); err != nil {
		return stackStop{}, err
	}
	plan := planTeardown(projectID, env, scope, facts)
	if len(plan.skipped) > 0 {
		return stackStop{}, errStackHostsThisCommand(env, plan.skipped)
	}
	return executeStackStop(projectID, env, scope, plan, facts, killTreesAndWait)
}

// errStackHostsThisCommand is replaceStack's refusal.
func errStackHostsThisCommand(env string, skipped []ancestorSkip) error {
	lines := make([]string, 0, len(skipped))
	for _, s := range skipped {
		lines = append(lines, fmt.Sprintf("pid %d (%s)", s.pid, s.command))
	}
	return fmt.Errorf("the env=%s stack this project is already running hosts this command — replacing it would end the session running you; nothing was stopped:\n"+
		"    %s\n"+
		"  run `forge env up %s` from a shell outside that stack, or stop it there first with `forge env down %s`",
		env, strings.Join(lines, "\n    "), env, env)
}

// planTeardown is planStackStop over the live machine: the ledger, the
// process-table snapshot in facts, and this process's real lineage.
func planTeardown(projectID, env string, scope []string, facts *osProcFacts) stackStopPlan {
	return planStackStop(projectID, env, scope, trackedStack(projectID, env), facts.pidList(), processAlive, facts, procguard.Self())
}

// executeStackStop reports the plan, signals its roots, and settles the
// records.
func executeStackStop(projectID, env string, scope []string, plan stackStopPlan, f procFacts, stop func([]int) error) (stackStop, error) {
	for _, s := range plan.skipped {
		fmt.Printf("[up] %s\n", s)
	}
	for _, pid := range plan.roots {
		fmt.Printf("[up] %s: stopping (pid %d + tree)\n", serviceOfPID(pid, f), pid)
	}
	return completeStackStop(projectID, env, scope, plan, stop)
}

func completeStackStop(projectID, env string, scope []string, plan stackStopPlan, stop func([]int) error) (stackStop, error) {
	if err := stop(plan.roots); err != nil {
		return stackStop{}, fmt.Errorf("environment shutdown incomplete; stack records preserved: %w", err)
	}
	// A skipped tree is still running, so the records still describe it:
	// `forge env ps` must keep listing it and a later `forge env down` run
	// from OUTSIDE it must still reach it.
	if len(scope) == 0 && len(plan.skipped) == 0 {
		removeStackRecords(projectID, env)
	}
	return stackStop{stopped: len(plan.roots), skipped: plan.skipped}, nil
}

// filterRootsByService keeps only the teardown roots whose stamped service
// marker is in scope. Both the host name (`admin-server`) and the frontend
// form (`frontend:reliant-web`) are matched against the bare target name, so
// a caller passes app names and need not know how the registry spells a
// frontend's ledger entry.
//
// Unattributable roots are dropped, not kept — see stopStackScopedWithFacts.
func filterRootsByService(roots []int, scope []string, f procFacts) []int {
	wanted := make(map[string]bool, len(scope))
	for _, name := range scope {
		wanted[name] = true
	}
	kept := make([]int, 0, len(roots))
	for _, pid := range roots {
		service := serviceOfPID(pid, f)
		if service == "" || service == "process" {
			continue
		}
		if wanted[strings.TrimPrefix(service, "frontend:")] {
			kept = append(kept, pid)
		}
	}
	return kept
}

// stackTeardownRoots is the pure selection core of stopStack: every process
// tree that belongs to (projectID, env) and must be signalled.
//
// The LIVE markers are the authority. The sweep over pids finds exactly the
// processes that ARE this stack — including ones no ledger ever recorded (the
// run crashed before it persisted) and ones whose pid has since changed (air
// re-execs its server), which is why a teardown keyed on remembered pids leaked
// orphans for as long as it existed.
//
// The ledger is the FALLBACK, for when the authority is unavailable: the sweep
// needs a process table (`ps`) and readable process environments, and an
// environment can supply neither — a failed `ps`, a sandbox that blocks
// /proc, the Windows stubs. So one rule, no platform branch:
//
//   - a recorded pid that is dead is skipped;
//   - a recorded pid whose environment IS readable must carry our markers —
//     otherwise it is a pid the OS recycled into a stranger's process, and
//     forge does not kill what it cannot prove it owns;
//   - a recorded pid whose environment cannot be read at all is signalled: the
//     record is then the only evidence forge has that it started this process,
//     and refusing to act on it would mean no teardown at all.
//
// alive is injected (processAlive in production) so the rule is testable
// without a real process table.
func stackTeardownRoots(projectID, env string, tracked []trackedProc, pids []int, alive func(int) bool, f procFacts) []int {
	roots := markedOrphanRoots(projectID, env, pids, f)
	seen := make(map[int]bool, len(roots))
	for _, pid := range roots {
		seen[pid] = true
	}
	for _, t := range tracked {
		if t.pid <= 1 || seen[t.pid] || !alive(t.pid) {
			continue
		}
		if environ, ok := f.environ(t.pid); ok {
			if _, matched := markerMatches(environ, projectID, env); !matched {
				continue // recycled, or another project's — never ours to signal
			}
		}
		seen[t.pid] = true
		roots = append(roots, t.pid)
	}
	return roots
}

// serviceOfPID reads back the FORGE_UP_SERVICE a process was stamped with, so
// teardown output names the service instead of a bare pid. Falls back to
// "process" when the marker is unreadable.
func serviceOfPID(pid int, f procFacts) string {
	if environ, ok := f.environ(pid); ok {
		if _, service, _ := markerFields(environ); service != "" {
			return service
		}
	}
	return "process"
}

// removeStackRecords drops the (project, env) ledger + discovery cache, and
// prunes the project directory once no env under it is still recorded — so the
// cache does not accumulate one directory per project ever run.
func removeStackRecords(projectID, env string) {
	if statePath, err := upStatePath(projectID, env); err == nil {
		if rerr := os.Remove(statePath); rerr != nil && !os.IsNotExist(rerr) {
			fmt.Printf("[up] warning: remove state: %v\n", rerr)
		}
	}
	if envPath, err := upEnvStatePath(projectID, env); err == nil {
		if rerr := os.Remove(envPath); rerr != nil && !os.IsNotExist(rerr) {
			fmt.Printf("[up] warning: remove env state: %v\n", rerr)
		}
	}
	dir, err := upProjectCacheDir(projectID)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pids") {
			return // another env is still recorded here
		}
	}
	_ = os.RemoveAll(dir)
}

// requireProjectDir resolves the directory of the forge.yaml governing the
// working directory, and FAILS when there is none.
//
// It exists because projectDirForKCL() falls back to "." — harmless for a KCL
// render (kcl reports the missing file), fatal for a teardown: "." hashes to a
// project id no stack was ever recorded under, the lookup finds nothing, and
// `forge env down` prints "no tracked stack" and exits 0. "I cannot tell which
// project you mean" must never render as "nothing was running" — that is a
// guard that cannot fail, and it reported success over a machine carrying 38
// orphaned processes.
func requireProjectDir(verb string) (string, error) {
	cfgPath, err := findProjectConfigFile()
	if err != nil {
		cwd, _ := os.Getwd()
		return "", fmt.Errorf("%s: no %s found in %s or any parent — cannot tell which project's stack to act on.\n"+
			"     run it from inside the project, or reach every forge stack on this machine with:\n"+
			"       forge env ps          # what is running, and where\n"+
			"       forge env down --all  # stop all of it",
			verb, defaultProjectConfigFile, cwd)
	}
	return filepath.Dir(cfgPath), nil
}

// runUpStopAll stops every forge-started stack on this machine, whatever
// project it belongs to. It is the escape hatch for the one state
// `forge env down <env>` structurally cannot reach: a stack whose project
// directory no longer exists, so no forge.yaml can be found to derive its id.
//
// It is not a broader authority — only processes carrying forge's OWN ownership
// markers are ever signalled, exactly as in the single-project path. What
// changes is enumeration: the stacks are discovered FROM the markers instead of
// being named by the caller.
func runUpStopAll() error {
	facts := newOSProcFacts()
	if err := requireTeardownSnapshot(facts); err != nil {
		return err
	}
	stacks := discoverRunningStacksWithFacts(facts)
	if len(stacks) == 0 {
		fmt.Println("[down] no owned Forge host processes were found on this machine; Docker containers and Kubernetes clusters were not stopped.")
		return nil
	}
	res, err := stopDiscoveredStacks(stacks)
	hosting := ""
	if len(res.skipped) > 0 {
		hosting = fmt.Sprintf(", and left %d running because it hosts this command", len(res.skipped))
	}
	fmt.Printf("[down] signalled %d host process tree(s) across %d stack(s)%s; Docker containers and Kubernetes clusters were not stopped.\n", res.stopped, len(stacks), hosting)
	return err
}

// stopDiscoveredStacks tears down each enumerated stack, naming it first, and
// returns the totals. Split from runUpStopAll's discovery so the teardown loop
// is testable against a chosen set of stacks — a test that discovered for
// itself would tear down every stack on the developer's machine.
func stopDiscoveredStacks(stacks []runningStack) (stackStop, error) {
	var total stackStop
	var failures []error
	for _, s := range stacks {
		fmt.Printf("[down] %s · env=%s\n", s.label(), s.env)
		res, err := stopStack(s.projectID, s.env)
		total.stopped += res.stopped
		total.skipped = append(total.skipped, res.skipped...)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s env=%s: %w", s.label(), s.env, err))
		}
	}
	return total, errors.Join(failures...)
}

// newEnvPsCmd lists every forge stack running on this machine — across ALL
// projects, including ones whose directory is gone.
//
// `forge env status <env>` answers "how is THIS project's env doing" and needs
// a project to stand in; this answers "what is forge running on this box",
// which is the question you have when something is holding a port, eating
// memory, or serving a tree you deleted. It reads the live ownership markers,
// so it sees stacks no record survived.
func newEnvPsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List every forge stack running on this machine, across all projects",
		Args:  cobra.NoArgs,
		Long: `List every stack ` + "`forge env up`" + ` has running on this
machine, across every project — not just the one in the working directory.

Each row is one (project, env) stack, with the number of live processes and
the project directory it was started from. A stack whose directory has since
been DELETED is still listed and still stoppable: it is discovered from the
ownership markers its processes carry, not from anything on disk.

Stop one:   cd <project> && forge env down <env>
Stop all:   forge env down --all

Examples:
  forge env ps
  forge env ps --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvPs(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON (project/project_id/env/processes/dir_exists)")
	return cmd
}

// envPsStack is one row of the `forge env ps` report. JSON tags are the
// machine-readable contract; additive only.
type envPsStack struct {
	// Project is the recorded project directory; empty when no record survives.
	Project   string `json:"project,omitempty"`
	ProjectID string `json:"project_id"`
	Env       string `json:"env"`
	Processes int    `json:"processes"`
	// DirExists is false when the stack outlived its project directory — the
	// state in which `forge env down <env>` cannot reach it and `--all` must.
	DirExists bool `json:"dir_exists"`
}

// envPsReport is the `forge env ps --json` envelope.
type envPsReport struct {
	Stacks []envPsStack `json:"stacks"`
}

func runEnvPs(jsonOut bool) error {
	stacks := discoverRunningStacks()
	if jsonOut {
		rep := envPsReport{Stacks: make([]envPsStack, 0, len(stacks))}
		for _, s := range stacks {
			rep.Stacks = append(rep.Stacks, envPsStack{
				Project:   s.dir,
				ProjectID: s.projectID,
				Env:       s.env,
				Processes: s.procs,
				DirExists: s.dirExists,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	if len(stacks) == 0 {
		fmt.Println("no forge stacks are running on this machine.")
		return nil
	}
	labelW := len("PROJECT")
	for _, s := range stacks {
		if len(s.label()) > labelW {
			labelW = len(s.label())
		}
	}
	fmt.Printf("%-*s  %-8s  %s\n", labelW, "PROJECT", "ENV", "PROCS")
	for _, s := range stacks {
		fmt.Printf("%-*s  %-8s  %d\n", labelW, s.label(), s.env, s.procs)
	}
	fmt.Println()
	fmt.Println("stop one:  cd <project> && forge env down <env>")
	fmt.Println("stop all:  forge env down --all")
	return nil
}
