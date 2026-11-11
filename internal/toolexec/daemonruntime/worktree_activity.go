// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

var errRecoveryAdmission = errors.New("workspace recovery finalization in progress; retry this operation")

// This gate is daemon-process scoped (commands and stream input share it).
// Every admission names its scope (admissionScope). An unknown scope conflicts
// with any finalization, so only work whose schema names neither a path nor a
// working directory is admitted that way. Only trusted handler code can obtain
// an exemption; payloads cannot supply one.
var recoveryAdmissions = recoveryAdmission{work: map[uint64]admissionScope{}, leases: map[uint64]*WorktreeRecoveryLease{}, managedUse: managedRecoveryUse, processUse: worktreereclaim.CheckProcessUse}

type recoveryAdmission struct {
	mu     sync.Mutex
	next   uint64
	work   map[uint64]admissionScope
	leases map[uint64]*WorktreeRecoveryLease
	// managedUse continues admission after a start command returns: it vetoes
	// a lease while a managed background process or terminal session runs
	// inside one of the leased paths. It samples the registries afresh, so a
	// process's whole lifetime counts, not just its start command.
	managedUse func(leased []string) error
	// processUse inspects every live process's cwd, executable, open and
	// mapped files. nil means worktreereclaim.CheckProcessUse.
	processUse func(context.Context, []string) error
}

// admissionScope is what one admitted piece of daemon work may touch.
type admissionScope struct {
	// paths are canonical. Empty means unknown, which conflicts with every
	// lease.
	paths []string
	// cwd says paths are the directories the work RUNS in, not paths it names.
	// Only work running inside a leased path conflicts: a shell started in an
	// ancestor ($HOME, /) or a sibling is elsewhere on the machine and must not
	// hold the worktree (owner decision, 2026-11-08). Whatever it does under
	// the worktree before a lease check (a cd, an open file) is seen by process
	// inspection there; what bounds what it does after one is the retirement
	// itself (see managedRecoveryUse), not that check.
	cwd bool
}

func (s admissionScope) conflicts(leased []string) bool {
	if !s.cwd {
		return admissionConflict(s.paths, leased)
	}
	if len(s.paths) == 0 || len(leased) == 0 {
		return true
	}
	for _, dir := range s.paths {
		for _, root := range leased {
			if admissionWithin(root, dir) {
				return true
			}
		}
	}
	return false
}

// cwdScope is the scope of work running in dir. Empty dir is the daemon's own
// working directory, which a child started with no Dir inherits. An error means
// where it runs cannot be established.
func cwdScope(dir string) (admissionScope, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return admissionScope{}, err
		}
		dir = wd
	}
	p, err := canonicalAdmissionPath(dir)
	if err != nil {
		return admissionScope{}, err
	}
	return admissionScope{paths: []string{p}, cwd: true}, nil
}

type WorktreeRecoveryLease struct {
	gate     *recoveryAdmission
	id       uint64
	paths    []string
	deadline time.Time
	released bool // gate.mu
}

// AcquireWorktreeRecovery excludes overlapping new work for a short final
// recheck/quarantine/removal window, NOT capture or network publication. Managed
// sessions/processes are checked afresh, including detached executions whose
// initial daemon command has already returned. Restart destroys all leases.
func AcquireWorktreeRecovery(ctx context.Context, paths []string) (*WorktreeRecoveryLease, error) {
	lease, err := recoveryAdmissions.acquire(paths)
	if err != nil {
		return nil, err
	}
	if err = lease.Check(ctx); err != nil {
		lease.Release()
		return nil, err
	}
	return lease, nil
}
func canonicalAdmissionPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty admission path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve existing ancestors too: a future child of a symlinked parent
	// must conflict with a lease on its canonical destination.
	cur := abs
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// admissionWithin reports whether path is root or inside it.
func admissionWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func admissionOverlap(a, b string) bool {
	return admissionWithin(a, b) || admissionWithin(b, a)
}
func admissionConflict(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if admissionOverlap(x, y) {
				return true
			}
		}
	}
	return false
}

// begin admits work that names the paths it touches (nil: unknown).
func (g *recoveryAdmission) begin(paths []string) (func(), error) {
	canonical := make([]string, 0, len(paths))
	for _, path := range paths {
		p, err := canonicalAdmissionPath(path)
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, p)
	}
	return g.admit(admissionScope{paths: canonical})
}

// beginIn admits work that runs in dir: a command, a tool call, a background
// process or a terminal. A dir whose location cannot be established is
// admitted as unknown, which keeps every finalization out while it runs.
func (g *recoveryAdmission) beginIn(dir string) (func(), error) {
	scope, err := cwdScope(dir)
	if err != nil {
		scope = admissionScope{}
	}
	return g.admit(scope)
}

func (g *recoveryAdmission) admit(scope admissionScope) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, lease := range g.leases {
		if scope.conflicts(lease.paths) {
			return nil, errRecoveryAdmission
		}
	}
	g.next++
	id := g.next
	g.work[id] = scope
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); delete(g.work, id); g.mu.Unlock() }) }, nil
}
func (g *recoveryAdmission) acquire(paths []string) (*WorktreeRecoveryLease, error) {
	if len(paths) == 0 {
		return nil, errors.New("recovery paths required")
	}
	canonical := make([]string, 0, len(paths))
	for _, path := range paths {
		p, err := canonicalAdmissionPath(path)
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, p)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, work := range g.work {
		if work.conflicts(canonical) {
			return nil, errors.New("workspace has active daemon work")
		}
	}
	if g.managedUse != nil {
		if err := g.managedUse(canonical); err != nil {
			return nil, err
		}
	}
	for _, lease := range g.leases {
		if admissionConflict(canonical, lease.paths) {
			return nil, errRecoveryAdmission
		}
	}
	g.next++
	lease := &WorktreeRecoveryLease{gate: g, id: g.next, paths: canonical, deadline: time.Now().Add(2 * time.Minute)}
	g.leases[lease.id] = lease
	return lease, nil
}

// managedRecoveryUse vetoes a lease for the whole lifetime of a managed
// background process or terminal session started inside a leased path. Start
// and creation admission (beginIn) stays held until registry publication,
// closing the handoff race; registry completion releases the veto without a
// poller.
//
// One started anywhere else does NOT veto (owner decision, 2026-11-08; bug
// c4a30cf1). This used to refuse while ANY managed process or terminal was open
// on the machine, on the grounds that a shell can sleep outside a workspace and
// later open an absolute path inside it. An agent machine always has one, so no
// archived worktree ever freed its disk. A shell that has since moved into the
// worktree, or holds a file there, is seen by process inspection, which every
// Check runs right after this.
//
// A Check is point-in-time, for managed and unmanaged processes alike. What
// bounds a process that comes in after one is the retirement
// (worktreereclaim.retireWorkspace), and the bound is reachability, not an
// atomic test-and-unlink (none exists): the checkout is moved to a fresh,
// random quarantine path before the checks that authorize deletion, so its
// original path no longer exists to cd into or open; a full Check — every
// process's cwd, open and mapped files — runs again immediately before EACH
// unlink; and a process with no reference into the tree at that Check can get
// one only by naming the fresh quarantine path, or from a process that had
// one, which that Check saw. Changes that land before an entry's deletion
// boundary also stop it, with the remaining files and the git pointer kept
// (TestRetirementTreeChangeBeforeTheDeletionBoundaryStopsDeletion). A process
// that opens the quarantine path by name between a Check and the next unlink
// is not fenced; it never was for any process on the machine, managed or not,
// so scoping managed work to its own directory does not widen it.
//
// Residual risk (owner-accepted, 2026-11-08, question bccdb4da: "Land it now,
// race documented"): a process that writes into the random quarantine path
// between the last Check and the unlink loses that write. Main has the same
// window for every unmanaged process. A daemon without root cannot fence it
// (fanotify FAN_OPEN_PERM needs CAP_SYS_ADMIN; so does a mount-namespace
// barrier). Capture-before-delete, in the all-worktrees reclaim redesign,
// makes such a write recoverable rather than closing the window.
func managedRecoveryUse(leased []string) error {
	return managedUseIn(leased, managedWorkSnapshot())
}

// managedWork is one running background process or open terminal session, and
// the directory it was started in ("" = the daemon's own).
type managedWork struct{ kind, dir string }

func managedWorkSnapshot() []managedWork {
	var work []managedWork
	for _, p := range shell.GetBackgroundManager().GetAllProcesses() {
		if p.StatusSafe() == "running" {
			work = append(work, managedWork{kind: "background process", dir: p.WorkingDir})
		}
	}
	if tm := terminalManager(); tm != nil {
		for _, s := range tm.ListSessions() {
			work = append(work, managedWork{kind: "terminal session", dir: s.WorkingDir})
		}
	}
	return work
}

func managedUseIn(leased []string, work []managedWork) error {
	for _, w := range work {
		scope, err := cwdScope(w.dir)
		if err != nil {
			// Where it runs cannot be established, so it may run here.
			return fmt.Errorf("managed %s has an unknown working directory: %w", w.kind, err)
		}
		if scope.conflicts(leased) {
			return fmt.Errorf("a managed %s was started in this workspace", w.kind)
		}
	}
	return nil
}

// Check must run immediately before each destructive action and after parking.
// Callers include BOTH original and quarantine paths when acquiring the lease.
func (l *WorktreeRecoveryLease) Check(ctx context.Context) error {
	l.gate.mu.Lock()
	valid := !l.released && l.gate.leases[l.id] == l && time.Now().Before(l.deadline)
	l.gate.mu.Unlock()
	if !valid {
		return errors.New("recovery admission expired or released")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.gate.managedUse != nil {
		if err := l.gate.managedUse(l.paths); err != nil {
			return err
		}
	}
	// No broad PID exclusions, including our own: an fd/cwd we hold under a
	// recovery root is use too. Inspection uncertainty (an unreadable /proc)
	// is not a safe idle result: it holds the worktree.
	inspect := l.gate.processUse
	if inspect == nil {
		inspect = worktreereclaim.CheckProcessUse
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := inspect(probeCtx, l.paths); err != nil {
		return err
	}
	l.gate.mu.Lock()
	defer l.gate.mu.Unlock()
	if l.released || l.gate.leases[l.id] != l || !time.Now().Before(l.deadline) {
		return errors.New("recovery admission expired or released during inspection")
	}
	return nil
}
func (l *WorktreeRecoveryLease) Release() {
	if l == nil {
		return
	}
	l.gate.mu.Lock()
	defer l.gate.mu.Unlock()
	delete(l.gate.leases, l.id)
	l.released = true
}
func recoveryLifecycleCommand(name string) bool {
	switch name {
	case "worktree.recovery_finalize", "worktree.reconcile", worktreereclaim.MarkCommand, worktreereclaim.UnmarkCommand:
		// Trusted reclamation acquires scoped admission itself for shedding or
		// finalization. Ordinary admission here would veto its own lease.
		// Reclaimer path/row locks serialize other lifecycle mutations.
		return true
	default:
		return false
	}
}

// selfAdmittedCommand names the commands whose handler takes its own
// admission, scoped to the working directory it resolves (beginRecoveryWorkIn)
// and held until the process or session it starts is in its registry.
// Admitting them here as well would add an unknown-scope admission that vetoes
// every finalization on the machine for as long as the command runs.
func selfAdmittedCommand(name string) bool {
	switch name {
	case "exec.run", "exec.bg_start", "terminal.create":
		return true
	default:
		return false
	}
}

// Only schemas whose complete path scope is known are inferred; the rest are
// unknown and refused conservatively during the short finalization window
// only. Exec and terminals are scoped to where they run by their handlers.
func beginRecoveryCommand(name string, payload []byte) (func(), error) {
	if usesPathsCommand(name) {
		noteWorktreeUse(string(payload))
	}
	if recoveryLifecycleCommand(name) || selfAdmittedCommand(name) {
		return func() {}, nil
	}
	var paths []string
	if name == WorktreeReclaimDecideCommand || name == WorktreeReclaimApproveCommand {
		// It writes only the reclaim's own state (approvals.json), never a
		// worktree: scoped there, it never waits on a reclaim in progress.
		// The approve command's removal runs after it answers, under a
		// recovery lease of its own (Reclaimer.Acquire), like any pass.
		if dir, err := reclaimStateDir(); err == nil {
			paths = []string{dir}
		}
	}
	if strings.HasPrefix(name, "fs.") {
		var req struct {
			Path        string `json:"path"`
			Source      string `json:"source"`
			Destination string `json:"destination"`
		}
		if json.Unmarshal(payload, &req) == nil {
			switch name {
			case "fs.read_file", "fs.read_binary_file", "fs.pdf_page_count", "fs.read_pdf_pages", "fs.write_file", "fs.write_binary_file", "fs.patch_file", "fs.stat", "fs.list_dir", "fs.mkdir", "fs.delete", "fs.get_tree":
				if req.Path != "" {
					paths = []string{req.Path}
				}
			}
		}
	}
	return recoveryAdmissions.begin(paths)
}

// beginRecoveryWorkIn admits work that runs in dir ("" = the daemon's own
// working directory): it conflicts only with a finalization of a worktree dir
// is inside.
func beginRecoveryWorkIn(dir string) (func(), error) { return recoveryAdmissions.beginIn(dir) }

// beginRecoveryWorkAnywhere admits work whose reach cannot be bounded (terminal
// input): it conflicts with every finalization while it is held.
func beginRecoveryWorkAnywhere() (func(), error) { return recoveryAdmissions.begin(nil) }

// Shared daemon observer, lazily constructed from the daemon's private data
// directory. The caller owns Start and cancellation for each workspace.
// Paths never leave the daemon, and persisted records contain only hashes.
func NewWorktreeActivityObserver(dataDir string) (*worktreereclaim.ActivityObserver, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("daemon data directory required")
	}
	return worktreereclaim.NewActivityObserver(filepath.Join(dataDir, "worktree-activity.json"), time.Now)
}
