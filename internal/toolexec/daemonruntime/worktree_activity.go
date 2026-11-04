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
// Empty work paths mean unknown scope and conflict with any finalization. Only
// trusted handler code can obtain an exemption; payloads cannot supply one.
var recoveryAdmissions = recoveryAdmission{work: map[uint64][]string{}, leases: map[uint64]*WorktreeRecoveryLease{}, managedUse: managedRecoveryUse}

type recoveryAdmission struct {
	mu     sync.Mutex
	next   uint64
	work   map[uint64][]string
	leases map[uint64]*WorktreeRecoveryLease
	// managedUse continues unknown-scope admission after a start command
	// returns. It samples actual registry lifetimes, never initial cwd.
	managedUse func() error
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
func admissionOverlap(a, b string) bool {
	within := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	return within(a, b) || within(b, a)
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
func (g *recoveryAdmission) begin(paths []string) (func(), error) {
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
	for _, lease := range g.leases {
		if admissionConflict(canonical, lease.paths) {
			return nil, errRecoveryAdmission
		}
	}
	g.next++
	id := g.next
	g.work[id] = canonical
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
		if admissionConflict(canonical, work) {
			return nil, errors.New("workspace has active daemon work")
		}
	}
	if g.managedUse != nil {
		if err := g.managedUse(); err != nil {
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

// managedRecoveryUse retains the unknown-scope veto for the entire managed
// process/session lifetime. Arbitrary shells can sleep outside a workspace,
// then open an absolute path inside it; cwd/fd probes cannot exclude that.
// Start/creation admission remains held until registry publication, closing
// the handoff race. Registry completion releases the veto without a poller.
func managedRecoveryUse() error {
	for _, p := range shell.GetBackgroundManager().GetAllProcesses() {
		if p.StatusSafe() == "running" {
			return errors.New("managed background process has unknown filesystem scope")
		}
	}
	if tm := terminalManager(); tm != nil && len(tm.ListSessions()) != 0 {
		return errors.New("open terminal session has unknown filesystem scope")
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
		if err := l.gate.managedUse(); err != nil {
			return err
		}
	}
	// No broad PID exclusions, including our own: an fd/cwd we hold under a
	// recovery root is use too. Inspection uncertainty is not a safe idle result.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := worktreereclaim.CheckProcessUse(probeCtx, l.paths); err != nil {
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
	case "worktree.recovery_finalize", "worktree.reconcile":
		// Trusted reclamation acquires scoped admission itself for shedding or
		// finalization. Ordinary admission here would veto its own lease.
		// Reclaimer path/row locks serialize other lifecycle mutations.
		return true
	default:
		return false
	}
}

// Only schemas whose complete path scope is known are inferred. Exec and
// arbitrary tools are unknown: a cwd is not a confinement boundary. They are
// refused conservatively during the short finalization window only.
func beginRecoveryCommand(name string, payload []byte) (func(), error) {
	if recoveryLifecycleCommand(name) {
		return func() {}, nil
	}
	var paths []string
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
func beginRecoveryWork() (func(), error) { return recoveryAdmissions.begin(nil) }

// Shared daemon observer, lazily constructed from the daemon's private data
// directory. The caller owns Start and cancellation for each workspace.
// Paths never leave the daemon, and persisted records contain only hashes.
func NewWorktreeActivityObserver(dataDir string) (*worktreereclaim.ActivityObserver, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("daemon data directory required")
	}
	return worktreereclaim.NewActivityObserver(filepath.Join(dataDir, "worktree-activity.json"), time.Now)
}
