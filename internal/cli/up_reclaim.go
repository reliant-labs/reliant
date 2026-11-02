package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Ownership markers stamped onto every host process / frontend `forge env up`
// spawns (see stampForgeOwnership). They ride the child's environment, so
// they PROPAGATE to every descendant — air's re-exec'd server, npm's
// next/vite grandchild — regardless of pid churn or a stale/absent ledger.
// This makes process ownership a property of the LIVE process, discoverable
// by inspecting whoever actually holds a wanted port, rather than a fact
// that lives only in the drift-prone per-env .pids file.
//
// Ownership is keyed on the PAIR (project, env), never env alone. FORGE_UP_ENV
// names the environment; FORGE_UP_PROJECT names the specific project directory
// whose `forge env up` spawned the process. A reclaim (the pre-flight guard /
// `env down`) only ever touches a process whose BOTH markers match the
// reclaiming project+env, so two different projects sharing an env name (e.g.
// both "dev") can never kill each other's host processes — the incident. A
// process that carries FORGE_UP_ENV but NO FORGE_UP_PROJECT (a stack spawned by
// a pre-fix forge, or any non-forge process) can never match and is always
// treated as FOREIGN — the reclaim only ever NARROWS what counts as "ours".
const (
	forgeUpEnvVar     = "FORGE_UP_ENV"
	forgeUpServiceVar = "FORGE_UP_SERVICE"
	forgeUpProjectVar = "FORGE_UP_PROJECT"
)

// projectIDForDir hashes a project directory into a short, stable id that is
// safe both as an environment-variable value AND as a single filesystem path
// component (the per-project ledger dir under ~/.cache/forge/up/). The dir is
// canonicalised first — made absolute and de-symlinked — so `.`, `./`, and a
// symlinked alias of the same checkout all map to ONE id. A dir that can't be
// resolved yields an empty id, which fails ownership matching CLOSED (nothing
// is reclaimed) — the safe direction.
func projectIDForDir(dir string) string {
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(canonicalProjectDir(dir)))
	return hex.EncodeToString(sum[:8]) // 16 hex chars — ample against collisions
}

// canonicalProjectDir is the absolute, de-symlinked form of dir — the exact
// string projectIDForDir hashes, and the one recorded in project.json so the
// record round-trips back to the id it sits under.
func canonicalProjectDir(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	return abs
}

// procFacts is the process-inspection seam the ownership resolver reads:
// a pid's environment and its parent. The real implementation reads the OS
// (sysctl KERN_PROCARGS2 on darwin, /proc on linux, `ps` for the ppid
// table); unit tests inject a map-backed fake so the ancestry logic runs
// without spawning real processes.
type procFacts interface {
	// environ returns pid's environment as KEY=VALUE strings. ok is false
	// when the env is unreadable (dead pid, permission, SIP-redacted system
	// binary) — an unreadable holder is treated as NOT forge-owned.
	environ(pid int) (env []string, ok bool)
	// parent returns pid's parent pid. ok is false when unknown.
	parent(pid int) (ppid int, ok bool)
	// argv returns pid's command line. ok is false when argv is unreadable —
	// on a platform with no argv source at all, or for a process whose
	// cmdline the kernel withholds. Duplicate attribution reports an
	// unreadable argv as UNDETERMINED rather than guessing a row.
	argv(pid int) (args []string, ok bool)
}

// osProcFacts is the production procFacts: it reads real process env via
// the platform readProcEnviron and answers parent() from a single ppid
// snapshot taken at construction (one `ps` call, then pure map lookups for
// the ancestry walk).
type osProcFacts struct {
	ppids map[int]int
}

func newOSProcFacts() *osProcFacts {
	return &osProcFacts{ppids: ppidMap()}
}

func (o *osProcFacts) environ(pid int) ([]string, bool) {
	return readProcEnviron(pid)
}

func (o *osProcFacts) parent(pid int) (int, bool) {
	ppid, ok := o.ppids[pid]
	return ppid, ok
}

func (o *osProcFacts) argv(pid int) ([]string, bool) {
	return readProcArgv(pid)
}

// pidList is every pid in the snapshot this instance was built from — the scan
// surface for the ownership sweeps. Read off the SAME snapshot the ancestry
// walk uses, so a sweep cannot select a pid whose parent the walk has never
// heard of, and so one process-table read serves the whole decision.
func (o *osProcFacts) pidList() []int {
	out := make([]int, 0, len(o.ppids))
	for pid := range o.ppids {
		out = append(out, pid)
	}
	return out
}

// markerEnvName extracts the FORGE_UP_ENV value from a process's environment
// (empty when absent). The environ slice is KEY=VALUE strings; a later
// duplicate wins, matching exec's last-wins env semantics.
func markerEnvName(env []string) string {
	v, _, _ := markerFields(env)
	return v
}

// markerFields extracts (FORGE_UP_ENV, FORGE_UP_SERVICE, FORGE_UP_PROJECT) from
// a process's environment. Any may be empty when its marker is absent — in
// particular projectID is empty for a process spawned by a pre-fix forge that
// only stamped the env marker, which markerMatches then treats as foreign.
func markerFields(env []string) (envName, service, projectID string) {
	prefixEnv := forgeUpEnvVar + "="
	prefixSvc := forgeUpServiceVar + "="
	prefixProj := forgeUpProjectVar + "="
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, prefixEnv):
			envName = kv[len(prefixEnv):]
		case strings.HasPrefix(kv, prefixSvc):
			service = kv[len(prefixSvc):]
		case strings.HasPrefix(kv, prefixProj):
			projectID = kv[len(prefixProj):]
		}
	}
	return envName, service, projectID
}

// markerMatches reports whether a process's environment identifies it as owned
// by (projectID, envName) — the single ownership predicate every resolver
// below funnels through. BOTH markers must match, AND the process's project
// marker must be non-empty: a process with no FORGE_UP_PROJECT (a pre-fix
// stack, or any non-forge process) can NEVER match, so it is always foreign
// and never reclaimed. Because the reclaiming projectID is a concrete hash, an
// empty reclaiming id (unresolvable project dir) also matches nothing — the
// predicate fails closed in both directions, only ever NARROWING "ours".
func markerMatches(env []string, projectID, envName string) (service string, ok bool) {
	name, svc, proj := markerFields(env)
	if name == envName && proj != "" && proj == projectID {
		return svc, true
	}
	return "", false
}

// maxAncestryDepth bounds the parent walk so a pathological ppid cycle (or a
// deeply nested tree) can't spin. Real forge chains are shallow
// (forge-up → go/air/npm → server/next), so a handful of levels is ample.
const maxAncestryDepth = 8

// forgeOwnerOfPID reports whether pid — or any ancestor up to init (pid 1) —
// is owned by (projectID, envName), i.e. whether the process is one THIS
// project's `forge env up` for THIS env spawned. Returns the matching
// FORGE_UP_SERVICE marker too.
//
// Walking ancestry is the grandchild-reparented-to-launchd safeguard: even
// if the port-holder's own env were unreadable, a marked ancestor still
// identifies it as ours. It cannot produce a false positive — a process not
// descended from THIS project's forge has no matching marked ancestor. On Unix
// orphans re-parent to 1; on Windows ppids are never re-parented, so the
// guarantee comes from ppidMap, which drops any parent link whose parent is
// dead, unreadable, or created after the child. A genuinely
// foreign holder (including another project's stack on the same env
// name) is never misclassified as reclaimable. The walk stops at pid 1
// (launchd/init, everyone's ancestor) which is never inspected.
func forgeOwnerOfPID(pid int, projectID, envName string, f procFacts) (service string, owned bool) {
	for depth := 0; pid > 1 && depth < maxAncestryDepth; depth++ {
		if env, ok := f.environ(pid); ok {
			if svc, matched := markerMatches(env, projectID, envName); matched {
				return svc, true
			}
		}
		ppid, ok := f.parent(pid)
		if !ok || ppid == pid || ppid <= 1 {
			break
		}
		pid = ppid
	}
	return "", false
}

// classifyPortConflicts splits the pre-flight port conflicts into the ones
// held by a forge-owned process for env (OURS — reclaimable) and the ones
// held by anything else (FOREIGN — never touched). The holder pid is
// resolved via resolvePID (portListenerPID); a holder that can't be resolved, or whose
// ancestry carries no marker, lands in foreign — the conservative default
// that preserves the "never reclaim an unidentifiable process" safety
// property.
func classifyPortConflicts(projectID, envName string, conflicts []portConflict, resolvePID func(int) int, f procFacts) (owned, foreign []portConflict) {
	for _, c := range conflicts {
		pid := resolvePID(c.port)
		if pid > 0 {
			if _, ok := forgeOwnerOfPID(pid, projectID, envName, f); ok {
				owned = append(owned, c)
				continue
			}
		}
		foreign = append(foreign, c)
	}
	return owned, foreign
}

// markedOrphanRoots is the pure selection core of every teardown (stopStack,
// and through it `forge env down`, `forge env down --all`, and the `up`/`run`
// pre-flight): from a candidate pid set it keeps every process owned by
// (projectID, envName) whose marked ancestors are NOT also in the set — the
// subtree roots — so a parent+child pair collapses to a single tree-kill. A
// process owned by a DIFFERENT project (or carrying no project marker at all)
// never enters the marked set, so it is never a root and never killed. Split
// out so the sweep is unit-testable without a real process table.
//
// It answers "is my stack running, and what exactly is it" in ONE pass, keyed
// on nothing but the ownership markers. That is what makes it correct for a dev
// loop on kernel-assigned ports, where a second stack conflicts with nothing
// and so a port probe can no longer detect a predecessor at all.
func markedOrphanRoots(projectID, envName string, pids []int, f procFacts) []int {
	marked := map[int]bool{}
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		if env, ok := f.environ(pid); ok {
			if _, matched := markerMatches(env, projectID, envName); matched {
				marked[pid] = true
			}
		}
	}
	return subtreeRoots(marked, f)
}

// subtreeRoots reduces a marked pid set to the processes with no marked
// ancestor in the same set — the minimal set of trees to signal. Shared by the
// (project, env)-scoped sweep and the machine-wide enumeration, which differ
// only in how the marked set was chosen. Sorted so output and tests are stable
// (map iteration is not).
func subtreeRoots(marked map[int]bool, f procFacts) []int {
	roots := make([]int, 0, len(marked))
	for pid := range marked {
		if !hasMarkedAncestor(pid, marked, f) {
			roots = append(roots, pid)
		}
	}
	sort.Ints(roots)
	return roots
}

// hasMarkedAncestor reports whether any ancestor of pid is in the marked
// set — used to reduce the marker sweep to subtree roots.
func hasMarkedAncestor(pid int, marked map[int]bool, f procFacts) bool {
	for depth := 0; depth < maxAncestryDepth; depth++ {
		ppid, ok := f.parent(pid)
		if !ok || ppid <= 1 || ppid == pid {
			return false
		}
		if marked[ppid] {
			return true
		}
		pid = ppid
	}
	return false
}

// killTreesAndWait SIGTERMs each pid's process tree, polls for exit up to a
// bounded grace, then SIGKILLs any straggler. It WAITS, so the `up`/`run`
// pre-flight that calls it knows the predecessor's ports are released before it
// launches the replacement. No-op on an empty list.
func killTreesAndWait(pids []int) error {
	return killTreesAndWaitWith(pids, killProcessTree, processAlive)
}

func killTreesAndWaitWith(pids []int, signal func(int, syscall.Signal) error, alive func(int) bool) error {
	if len(pids) == 0 {
		return nil
	}
	var failures []error
	for _, pid := range pids {
		if pid > 1 {
			if err := signal(pid, syscall.SIGTERM); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	wait := func(grace time.Duration) bool {
		deadline := time.Now().Add(grace)
		for {
			anyAlive := false
			for _, pid := range pids {
				if pid > 1 && alive(pid) {
					anyAlive = true
					break
				}
			}
			if !anyAlive {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if wait(8 * time.Second) {
		return nil
	}
	for _, pid := range pids {
		if pid > 1 && alive(pid) {
			if err := signal(pid, syscall.SIGKILL); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if !wait(2 * time.Second) {
		return fmt.Errorf("host processes remain alive after shutdown signals")
	}
	return nil
}

// stampForgeOwnership marks cmd's child (and, via env inheritance, every
// descendant) as forge-`up`-owned for (projectID, envName)/service. Stamping
// projectID alongside envName is what keys ownership per-project: a later
// reclaim in a different project won't match this marker and so can't kill the
// child. withForcedEnv dedups, so a re-stamp / an inherited marker from a
// nested forge-up (a different project's, even) is OVERWRITTEN with this
// project's id rather than duplicated. A nil cmd.Env is seeded from the current
// process env so the child doesn't lose its inherited environment.
func stampForgeOwnership(cmd *exec.Cmd, projectID, envName, service string) {
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	base = withForcedEnv(base, forgeUpEnvVar, envName)
	base = withForcedEnv(base, forgeUpServiceVar, service)
	base = withForcedEnv(base, forgeUpProjectVar, projectID)
	cmd.Env = base
}
