// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// OwnerStore is what the Resolver reads and writes: the machines that may
// hold a project's checkouts, and the owner it records.
type OwnerStore interface {
	Installs
	// GetProject confirms the workspace is the caller's: only its owner's
	// machines are asked, and only its owner's row is written.
	GetProject(ctx context.Context, id string) (*db.Project, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	// AdoptWorktreeDaemon records daemonID as the owner of a row that names
	// none; a row that already names one is left alone.
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error
}

// OwnerProbe is what the Resolver asks the machines: whether one is
// connected right now, and a stat on it. *toolexec.NATSDaemonRouter
// implements it.
type OwnerProbe interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Package variables, not constants, so a test can shrink them.
var (
	// ownerProbeTimeout bounds one machine's stat. A connected machine
	// answers a stat in milliseconds; one that does not is not waited for.
	ownerProbeTimeout = 3 * time.Second
	// ownerRetryAfter is how long a row the machines could not settle is left
	// alone before it is asked about again, so a page that routes ten requests
	// for one unattributed workspace probes once.
	ownerRetryAfter = 2 * time.Minute
)

// Resolver finds the machine that holds a workspace's checkout.
//
// A row records its owner (worktrees.daemon_id) from creation, but rows made
// before that was recorded name none, and every request about one went to the
// user's default machine — the wrong disk for a workspace on another machine.
// The Resolver settles such a row the first time it is used: it asks each of
// the user's connected machines that have the project whether the
// workspace's directory exists there (one stat each, in parallel, bounded by
// ownerProbeTimeout), and records the owner when exactly one has it.
//
// It never wakes a machine and never waits for one: a machine that is asleep
// or starting is not asked, so a workspace that lives on one stays unattributed
// until the periodic sweep (worktreesweep) or a later use finds it. Any
// connected machine whose answer is unknown (an error, a timeout) also leaves
// the row unattributed, since that machine might be the one. A nil Resolver
// answers with the recorded owner only.
type Resolver struct {
	store OwnerStore
	probe OwnerProbe
	now   func() time.Time

	mu      sync.Mutex
	retryAt map[string]time.Time
	// inflight dedupes concurrent resolutions of one row: the others wait for
	// the first's answer instead of probing again.
	inflight map[string]*ownerCall
}

type ownerCall struct {
	done  chan struct{}
	owner string
}

// NewResolver builds a Resolver. A nil probe (a router that cannot report
// liveness) leaves every unattributed row unattributed.
func NewResolver(store OwnerStore, probe OwnerProbe) *Resolver {
	return &Resolver{
		store:    store,
		probe:    probe,
		now:      time.Now,
		retryAt:  map[string]time.Time{},
		inflight: map[string]*ownerCall{},
	}
}

// Owner returns the machine holding worktree's checkout, or "" when that is
// not known: the main checkout (every machine with the project has it), or a
// row no connected machine could settle. A row that records its owner answers
// at once; an unattributed branch workspace is resolved as the type describes.
func (r *Resolver) Owner(ctx context.Context, userID string, worktree *db.Worktree) string {
	if owner := WorktreeOwner(worktree); owner != "" || worktree == nil {
		return owner
	}
	if r == nil || r.probe == nil || r.store == nil || worktree.IsMain || worktree.DeletedAt != nil || worktree.Path == "" || userID == "" {
		return ""
	}

	r.mu.Lock()
	if call, ok := r.inflight[worktree.ID]; ok {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.owner
		case <-ctx.Done():
			return ""
		}
	}
	if until, ok := r.retryAt[worktree.ID]; ok && r.now().Before(until) {
		r.mu.Unlock()
		return ""
	}
	call := &ownerCall{done: make(chan struct{})}
	r.inflight[worktree.ID] = call
	r.mu.Unlock()

	owner := r.resolve(ctx, userID, worktree)

	r.mu.Lock()
	delete(r.inflight, worktree.ID)
	if owner == "" {
		r.retryAt[worktree.ID] = r.now().Add(ownerRetryAfter)
	} else {
		delete(r.retryAt, worktree.ID)
	}
	r.mu.Unlock()
	call.owner = owner
	close(call.done)
	return owner
}

// probeOutcome is one machine's answer about a directory.
type probeOutcome int

const (
	probeAbsent probeOutcome = iota
	probePresent
	probeUnknown
)

func (r *Resolver) resolve(ctx context.Context, userID string, worktree *db.Worktree) string {
	// Someone else's workspace is never settled by the caller's machines.
	project, err := r.store.GetProject(ctx, worktree.ProjectID)
	if err != nil || project == nil || project.UserID != userID {
		return ""
	}
	candidates, err := r.candidates(ctx, userID, worktree.ProjectID)
	if err != nil {
		logging.WarnContext(ctx, "[daemonplacement] could not list the machines that may hold a workspace",
			"worktreeID", worktree.ID, "error", err)
		return ""
	}
	if len(candidates) == 0 {
		return ""
	}

	outcomes := make([]probeOutcome, len(candidates))
	var wg sync.WaitGroup
	for i, daemonID := range candidates {
		wg.Add(1)
		go func(i int, daemonID string) {
			defer wg.Done()
			outcomes[i] = r.probeOne(ctx, userID, daemonID, worktree.Path)
		}(i, daemonID)
	}
	wg.Wait()

	owner := ""
	for i, outcome := range outcomes {
		switch outcome {
		case probeUnknown:
			// A connected machine that did not answer may be the owner.
			return ""
		case probePresent:
			if owner != "" {
				// Two machines have the path: neither is provably the owner.
				return ""
			}
			owner = candidates[i]
		}
	}
	if owner == "" {
		return ""
	}
	if err := r.store.AdoptWorktreeDaemon(ctx, worktree.ID, owner); err != nil {
		logging.WarnContext(ctx, "[daemonplacement] could not record the machine holding a workspace",
			"worktreeID", worktree.ID, "daemonID", owner, "error", err)
		return ""
	}
	logging.InfoContext(ctx, "[daemonplacement] recorded the machine holding an unattributed workspace",
		"worktreeID", worktree.ID, "daemonID", owner)
	return owner
}

// candidates lists the user's machines that may hold the project's checkouts
// and are connected now: the ones the project is installed on, or every
// machine of the user's when no install was ever recorded.
func (r *Resolver) candidates(ctx context.Context, userID, projectID string) ([]string, error) {
	ids, err := installedDaemonIDs(ctx, r.store, userID, projectID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		daemons, err := r.store.ListDaemonsByUserID(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, d := range daemons {
			if d != nil && d.UserID == userID {
				ids = append(ids, d.ID)
			}
		}
	}
	connected := ids[:0:0]
	for _, id := range ids {
		online, err := r.probe.IsDaemonOnline(ctx, userID, &toolexec.DaemonSelector{ID: id})
		if err == nil && online {
			connected = append(connected, id)
		}
	}
	return connected, nil
}

// probeOne asks one connected machine whether path is a directory on it.
func (r *Resolver) probeOne(ctx context.Context, userID, daemonID, path string) probeOutcome {
	ctx, cancel := context.WithTimeout(ctx, ownerProbeTimeout)
	defer cancel()
	payload, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return probeUnknown
	}
	raw, err := r.probe.SendDaemonCommandToDaemon(ctx, userID, daemonID, "fs.stat", payload, int32(ownerProbeTimeout/time.Millisecond))
	if err != nil {
		return probeUnknown
	}
	var stat struct {
		Exists bool `json:"exists"`
		IsDir  bool `json:"is_dir"`
	}
	if err := json.Unmarshal(raw, &stat); err != nil {
		return probeUnknown
	}
	if stat.Exists && stat.IsDir {
		return probePresent
	}
	return probeAbsent
}
