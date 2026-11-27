// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Go work directories: the scratch of builds that were killed.
//
// The go command builds in a work directory it creates under GOTMPDIR
// (go-build*, ~300 MB for a linked binary) and the linker in one under TMPDIR
// (go-link-*). Both remove their own on a normal exit. Neither can on
// SIGKILL, and the linker does not even on SIGINT or SIGTERM: it traps
// neither. On 2026-11-03 one machine held ~1,500 of them, 222 GB of /tmp, and
// its disk filled.
//
// Age alone does not say a work directory is dead: a long `go test` keeps one
// for an hour. So on Linux a directory is removed only once its being dead is
// PROVEN from /proc (procSnapshot):
//
//  1. no live process references it: as its working directory, root or
//     executable, through an open file or a mapping, on its command line, or
//     in its environment (GOTMPDIR, TMPDIR); and
//  2. it is older than every live process that could have created it: the go
//     command for go-build*, the linker for go-link-*. Its creator started
//     before it, so when every live one started after it was born, its
//     creator is dead.
//
// A per-command GOTMPDIR root the daemon made (proctree; gotmp-*) needs only
// the first: every process of its command has it in its environment.
//
// The scan is a proof only when it read everything this user's processes
// hold: one whose working directory, descriptors, mappings or environment
// could not be read fails it (scanProcRoot), as if there were no /proc. Each
// process is read between two reads of its start time, so reads that straddle
// a pid's reuse by another process are never taken for either (readProcess).
// A service manager's identity or capabilities cannot excuse unreadable
// references: neither proves that it holds no work directory.
//
// Where there is no /proc (macOS) nothing can be proven, and a go-build*
// directory is removed by age alone, GoWorkDirAge, as before.
//
// The per-command roots are the daemon's own: it makes one for every command
// it runs, and only a cgroup lets it remove one when its command ends
// (proctree, gotmp_linux.go). A daemon without cgroup delegation (a
// self-hosted systemd service) therefore leaves every root it makes, one per
// command: on 2026-11-22 one such daemon had left 105k of them, ~48 GiB with
// its killed builds inside. So the daemon reclaims its own roots on the
// Go-cache cadence whatever RELIANT_STORAGE_REAPER says (reaper.go,
// runReaperOff). With the reaper off that pass judges ONLY those roots
// (daemonOnly): a person's go-build* and go-link-* in their own /tmp are not
// the daemon's to remove there, the same line "auto runs on managed daemons
// only" draws for every other cache.

const (
	goBuildPrefix = "go-build"
	goLinkPrefix  = "go-link-"
	// commandGoTmpBasePrefix and commandGoTmpPrefix name the daemon's
	// per-command GOTMPDIR roots, <temp dir>/reliant-gotmp-<uid>/gotmp-<pid>-<n>,
	// or gotmp-<pid>-c<n> for one whose command has a cgroup, which proctree
	// frees when it retires that cgroup; this pass judges both by the same
	// /proc proof (proctree.GoTmpBasePrefix, proctree.GoTmpPrefix; a test holds
	// them equal).
	commandGoTmpBasePrefix = "reliant-gotmp-"
	commandGoTmpPrefix     = "gotmp-"

	// workDirProofMinAge is the youngest directory a proven pass removes: a
	// margin for clocks, not part of the proof.
	workDirProofMinAge = 10 * time.Minute
	// creatorSlack absorbs the precision of a process's start time (a clock
	// tick) against a directory's birth time.
	creatorSlack = time.Second
)

// procStart is one live process that could have created a work directory.
type procStart struct {
	PID   int
	Comm  string
	Start time.Time
}

func (p procStart) String() string {
	return fmt.Sprintf("pid %d (%s) started %s", p.PID, p.Comm, p.Start.UTC().Format(time.RFC3339))
}

// procSnapshot is what the live processes hold, from one scan of /proc.
type procSnapshot struct {
	// names are the work-directory names (go-build*, go-link-*, gotmp-*)
	// that appear in any path a live process holds. Matching by name rather
	// than by full path means a process that reaches a directory through
	// another mount of the same disk still counts.
	names map[string]bool
	// goStarts and linkStarts are the live go commands and linkers.
	goStarts, linkStarts []procStart
}

// addRefs records the work-directory names in s, a path, argument or
// environment value some live process holds.
func (s *procSnapshot) addRefs(v string) {
	if !strings.Contains(v, goBuildPrefix) && !strings.Contains(v, goLinkPrefix) && !strings.Contains(v, commandGoTmpPrefix) {
		return
	}
	for _, f := range strings.FieldsFunc(v, func(r rune) bool {
		return r == '/' || r == '\\' || r == 0 || r == ' ' || r == '=' || r == ':' || r == ',' || r == ';' || r == '"' || r == '\'' || r == '\n' || r == '\t'
	}) {
		if workDirKind(f) != "" {
			if s.names == nil {
				s.names = map[string]bool{}
			}
			s.names[f] = true
		}
	}
}

// workDirKind is the kind of work directory name is, "" for none.
func workDirKind(name string) string {
	switch {
	case strings.HasPrefix(name, goBuildPrefix):
		return goBuildPrefix
	case strings.HasPrefix(name, goLinkPrefix):
		return goLinkPrefix
	case strings.HasPrefix(name, commandGoTmpPrefix):
		return commandGoTmpPrefix
	}
	return ""
}

// creatorAlive returns the oldest live process that could have created a
// directory of kind born at birth: one that started no later than birth.
func (s *procSnapshot) creatorAlive(kind string, birth time.Time) (procStart, bool) {
	var starts []procStart
	switch kind {
	case goBuildPrefix:
		starts = s.goStarts
	case goLinkPrefix:
		starts = s.linkStarts
	default:
		return procStart{}, false
	}
	var oldest procStart
	found := false
	for _, p := range starts {
		if p.Start.Before(birth.Add(creatorSlack)) && (!found || p.Start.Before(oldest.Start)) {
			oldest, found = p, true
		}
	}
	return oldest, found
}

// workDirAudit is the go_work_dirs pass's entry in the trim audit log.
type workDirAudit struct {
	// Proof is how a directory was shown dead: "proc_scan", or "age_only"
	// where there is no /proc.
	Proof string `json:"proof"`
	// Scope is "daemon_roots" for a pass over only the daemon's per-command
	// GOTMPDIR roots (the storage reaper is off), "" for every work dir.
	Scope   string `json:"scope,omitempty"`
	Dirs    string `json:"dirs"`
	Scanned int    `json:"scanned"`
	Removed int    `json:"removed"`
	Freed   uint64 `json:"freed_bytes"`
	// Kept, by the reason a directory was not proven dead.
	KeptReferenced   int `json:"kept_referenced,omitempty"`
	KeptCreatorAlive int `json:"kept_creator_alive,omitempty"`
	KeptYoung        int `json:"kept_young,omitempty"`
	KeptForeign      int `json:"kept_foreign,omitempty"`
	// OldestCreator is the live process that kept the most directories by
	// rule 2: until it exits, nothing born after it started can be proven
	// dead.
	OldestCreator string `json:"oldest_creator,omitempty"`
	// ExplainedUnreadable is retained for audit compatibility but never populated:
	// unreadable references now always fail the proof, regardless of identity.
	ExplainedUnreadable []string `json:"explained_unreadable,omitempty"`
	Stopped             string   `json:"stopped,omitempty"`
	Error               string   `json:"error,omitempty"`
}

// goWorkDirRoots are the directories go work dirs are created in: GOTMPDIR
// and TMPDIR (or /tmp), and inside each the daemon's per-command root base.
func (w *Watcher) goWorkDirRoots() []string {
	var dirs []string
	for _, k := range []string{"GOTMPDIR", "TMPDIR"} {
		if v := strings.TrimSpace(w.env.Getenv(k)); v != "" && filepath.IsAbs(v) {
			dirs = append(dirs, filepath.Clean(v))
		}
	}
	// The machine's own /tmp only outside tests: a test's Watcher names its
	// temp directory, and one that does not must not reclaim this machine's.
	if len(dirs) == 0 && w.env.GOOS != "windows" && !testing.Testing() {
		dirs = append(dirs, "/tmp")
	}
	out := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// trimGoWorkDirs removes the work directories of killed builds (see the top
// of this file) and reports what it did; nil when it found none. Only this
// user's: /tmp is shared. daemonOnly judges only the daemon's per-command
// GOTMPDIR roots, and only on a /proc proof: no age rule, and no /proc scan at
// all where no root base exists.
func (w *Watcher) trimGoWorkDirs(ctx context.Context, age time.Duration, p *pacer, daemonOnly bool) (*CacheTrimResult, workDirAudit) {
	uid := w.currentUID()
	roots := w.goWorkDirRoots()
	audit := workDirAudit{Proof: "proc_scan", Dirs: strings.Join(roots, ", ")}
	if daemonOnly {
		audit.Scope = "daemon_roots"
		if !w.hasCommandGoTmpBase(roots, uid) {
			return nil, audit
		}
	}
	snap, scanErr := w.scanProcs()
	if scanErr != nil {
		audit.Proof, audit.Error = "age_only", scanErr.Error()
	}
	if scanErr != nil && (age <= 0 || daemonOnly) {
		// A root is removed only on a proof; without one, nothing is.
		return nil, audit
	}
	res := &CacheTrimResult{Kind: "go_work_dirs", Path: audit.Dirs}
	creatorKept := map[procStart]int{}

	consider := func(dir string, e os.DirEntry, kinds ...string) {
		kind := workDirKind(e.Name())
		if !e.IsDir() || kind == "" || !containsString(kinds, kind) {
			return
		}
		path := filepath.Join(dir, e.Name())
		audit.Scanned++
		// Another user's directory in a shared /tmp is theirs to remove, and
		// anything they control is a way to redirect ours.
		if info, lerr := os.Lstat(path); uid < 0 || lerr != nil || ownerUID(info) != uid {
			audit.KeptForeign++
			return
		}
		if scanErr != nil {
			// No /proc: the age rule, for go-build* alone, as before.
			if kind != goBuildPrefix || !olderThan(path, w.now().Add(-age)) {
				audit.KeptYoung++
				return
			}
		} else {
			birth := w.birthTime(path)
			if w.now().Sub(birth) < workDirProofMinAge {
				audit.KeptYoung++
				return
			}
			if snap.names[e.Name()] {
				audit.KeptReferenced++
				return
			}
			if c, alive := snap.creatorAlive(kind, birth); alive {
				audit.KeptCreatorAlive++
				creatorKept[c]++
				return
			}
		}
		n, rerr := p.removeTree(ctx, path, uid)
		res.Freed += n
		if rerr == nil {
			res.Removed++
		}
	}

	for _, dir := range roots {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			continue
		}
		for _, e := range entries {
			if ctx.Err() != nil || p.stopped != nil {
				break
			}
			if e.IsDir() && e.Name() == commandGoTmpBasePrefix+fmt.Sprint(uid) {
				base := filepath.Join(dir, e.Name())
				if sub, err := os.ReadDir(base); err == nil {
					for _, s := range sub {
						if ctx.Err() != nil || p.stopped != nil {
							break
						}
						// A per-command root holds the go command's go-build*;
						// it goes whole once its command is gone.
						consider(base, s, commandGoTmpPrefix)
					}
				}
				continue
			}
			if !daemonOnly {
				consider(dir, e, goBuildPrefix, goLinkPrefix)
			}
		}
	}
	audit.Removed, audit.Freed = res.Removed, res.Freed
	audit.Stopped = stopReason(ctx, p)
	best := 0
	for c, n := range creatorKept {
		if n > best {
			best, audit.OldestCreator = n, c.String()
		}
	}
	res.Size = res.Freed
	res.Stopped = audit.Stopped
	if res.Removed == 0 && res.Freed == 0 {
		return nil, audit
	}
	return res, audit
}

// hasCommandGoTmpBase reports whether any of roots holds this user's
// per-command root base (reliant-gotmp-<uid>) as a real directory.
func (w *Watcher) hasCommandGoTmpBase(roots []string, uid int) bool {
	if uid < 0 {
		return false
	}
	for _, dir := range roots {
		if fi, err := os.Lstat(filepath.Join(dir, commandGoTmpBasePrefix+fmt.Sprint(uid))); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// birthTime is when path was created: its birth time where the filesystem
// records one, else its change time, which is never earlier (so the proof
// stays sound, only weaker).
func (w *Watcher) birthTime(path string) time.Time {
	w.mu.Lock()
	f := w.trim.birth
	w.mu.Unlock()
	if f != nil {
		return f(path)
	}
	return fileBirth(path)
}

// scanProcs takes one snapshot of the live processes.
func (w *Watcher) scanProcs() (procSnapshot, error) {
	w.mu.Lock()
	f := w.trim.procs
	w.mu.Unlock()
	if f != nil {
		return f()
	}
	return scanProcesses()
}

// auditGoWorkDirs appends the go_work_dirs pass to the trim audit log, when
// it found any work directory to judge.
func (w *Watcher) auditGoWorkDirs(ctx context.Context, trigger string, a workDirAudit) {
	if a.Scanned == 0 {
		return
	}
	verdict := verdictReclaimed
	switch {
	case a.Proof != "proc_scan" && a.Removed == 0:
		verdict = verdictUnproven
	case a.Removed == 0:
		verdict = verdictNothingDead
	}
	w.appendTrimAudit(ctx, w.reaperPolicyPath(w.reaperConfig()), trimAuditRecord{ //nolint:gosec // G101: Pass names the trim pass, not a credential
		At: w.now(), Pass: "go_work_dirs", Trigger: trigger, Verdict: verdict, Ran: true, RecordFresh: true, WorkDirs: &a,
	})
}

// logWorkDirPass logs one line for a go_work_dirs pass that found any work
// directory to judge: what it scanned, removed and freed, and what it kept by
// reason. At INFO when it removed something, so a person can see the daemon
// reclaiming its own roots on a self-hosted box; a cut-short pass is a WARN;
// a pass that removed nothing is DEBUG, one every 10 minutes.
func logWorkDirPass(ctx context.Context, trigger string, a workDirAudit) {
	if a.Scanned == 0 && a.Error == "" {
		return
	}
	scope := a.Scope
	if scope == "" {
		scope = "all"
	}
	fields := []any{
		"trigger", trigger, "scope", scope, "proof", a.Proof, "dirs", a.Dirs,
		"scanned", a.Scanned, "removed", a.Removed, "freed", FormatBytes(a.Freed),
		"kept_referenced", a.KeptReferenced, "kept_creator_alive", a.KeptCreatorAlive,
		"kept_young", a.KeptYoung, "kept_foreign", a.KeptForeign,
	}
	if a.OldestCreator != "" {
		fields = append(fields, "oldest_creator", a.OldestCreator)
	}
	if len(a.ExplainedUnreadable) > 0 {
		fields = append(fields, "explained_unreadable", strings.Join(a.ExplainedUnreadable, "; "))
	}
	if a.Error != "" {
		fields = append(fields, "error", a.Error)
	}
	switch {
	case a.Scope != "" && a.Error != "":
		// Only a /proc proof removes a root, so none goes until this clears.
		logging.WarnContext(ctx, "[diskwatch] Go work dir pass could not prove any GOTMPDIR root dead: the /proc scan failed", fields...)
	case a.Stopped != "":
		logging.WarnContext(ctx, "[diskwatch] Go work dir pass stopped early; the next pass resumes", append(fields, "stopped", a.Stopped)...)
	case a.Removed > 0:
		logging.InfoContext(ctx, "[diskwatch] Go work dir pass reclaimed dead work directories", fields...)
	default:
		logging.DebugContext(ctx, "[diskwatch] Go work dir pass found nothing dead", fields...)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
