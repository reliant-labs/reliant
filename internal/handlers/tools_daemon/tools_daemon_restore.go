// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// ── Restoring a pool member's checkouts ────────────────────────────────
//
// A machine pool member is declared interchangeable: every run starts from the
// remote and makes its own worktree. A member whose disk is the pod's own
// (control-plane daemons.disk_mode = ephemeral) therefore wakes with nothing
// on it while its project_daemons rows still say installed. Placement keeps
// runs off it until its checkout is seen again (project_daemons.verified_at,
// dev-docs/design/machine-pools.md "Wake, then place"); this puts the
// checkout back, so the member becomes placeable on its own.
//
// Only members of one of the owner's pools are restored. On any other
// machine a missing checkout is the owner's doing (they deleted it), and
// re-cloning it behind their back would be wrong.

const (
	// restoreTotalTimeout bounds one connect's restore pass: several clones
	// of large repositories.
	restoreTotalTimeout = 30 * time.Minute
	// restoreCloneTimeoutMs bounds one clone on the daemon.
	restoreCloneTimeoutMs = int32(15 * time.Minute / time.Millisecond)
)

// CheckoutRestoreStore is what restoring a pool member's checkouts reads and
// writes.
type CheckoutRestoreStore interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListMachinePoolsForUser(ctx context.Context, userID string) ([]*db.MachinePool, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string) error
	ResolveQueuedProjectDaemon(ctx context.Context, requestID, installErr string) error
}

// cloneStep is one `git.clone` a restore runs.
type cloneStep struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// cloneRunner runs one clone on the daemon being restored.
type cloneRunner func(ctx context.Context, step cloneStep) error

// restoreCheckoutsOnConnect restores, on the machine behind conn, the
// checkouts the reconcile pass found missing. Runs off the connect path.
func (s *Service) restoreCheckoutsOnConnect(conn *daemonConnection, missing []*db.Project) {
	if conn == nil || len(missing) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), restoreTotalTimeout)
	defer cancel()
	go func() {
		select {
		case <-conn.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	run := func(ctx context.Context, step cloneStep) error {
		payload, err := json.Marshal(step)
		if err != nil {
			return err
		}
		resp, err := s.sendCommandToConn(ctx, conn, &reliantv1.DaemonCommandRequest{
			RequestId:   uuid.NewString(),
			CommandType: "git.clone",
			Payload:     payload,
			TimeoutMs:   restoreCloneTimeoutMs,
		})
		if err != nil {
			return err
		}
		if !resp.GetSuccess() {
			return fmt.Errorf("%s", strings.TrimSpace(resp.GetErrorMessage()))
		}
		return nil
	}
	restorePoolMemberCheckouts(ctx, s.deps.Repo, run, conn.daemonID, conn.userID, missing)
}

// restorePoolMemberCheckouts is the testable core: when daemonID is a member
// of one of userID's pools, re-clone every project in missing that the
// machine's project_daemons rows say is installed.
//
// While a restore runs the row stays installed but unverified, which
// placement reads as "connected; checking its checkout": the member is
// waited for, not passed over. Success stamps the row verified; a failure
// marks it failed with the reason, which placement then reports.
func restorePoolMemberCheckouts(ctx context.Context, store CheckoutRestoreStore, run cloneRunner, daemonID, userID string, missing []*db.Project) {
	member, err := isPoolMember(ctx, store, daemonID, userID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" restore: could not tell whether the machine is a pool member",
			"error", err, "daemonID", daemonID)
		return
	}
	if !member {
		return
	}
	rows, err := store.ListProjectDaemonsForDaemon(ctx, daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" restore: failed to list the machine's projects",
			"error", err, "daemonID", daemonID)
		return
	}
	installed := map[string]*core.ProjectDaemon{}
	for _, r := range rows {
		if r != nil && r.InstallState == core.ProjectInstallInstalled {
			installed[r.ProjectID] = r
		}
	}
	for _, project := range missing {
		if ctx.Err() != nil {
			return
		}
		row := installed[project.ID]
		if row == nil {
			continue
		}
		repos, err := store.ListReposByProject(ctx, project.ID)
		if err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" restore: failed to list the project's repositories",
				"error", err, "daemonID", daemonID, "projectID", project.ID)
			continue
		}
		steps := restorePlan(project, row, repos)
		if len(steps) == 0 {
			failRestore(ctx, store, project, row, daemonID, "its checkout is gone and the project has no remote to clone it from")
			continue
		}
		var failure error
		for _, step := range steps {
			if err := run(ctx, step); err != nil {
				failure = fmt.Errorf("clone %s into %s: %w", step.Repo, step.Path, err)
				break
			}
		}
		if failure != nil {
			failRestore(ctx, store, project, row, daemonID, failure.Error())
			continue
		}
		if err := store.UpsertProjectDaemon(ctx, project.ID, daemonID, row.Path, row.DefaultBranch); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" restore: cloned, but could not record it",
				"error", err, "daemonID", daemonID, "projectID", project.ID)
			continue
		}
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" restore: put a pool member's checkout back",
			"daemonID", daemonID, "projectID", project.ID, "path", row.Path, "repos", len(steps))
	}
}

// failRestore marks the row failed with why, so the member's install state
// and placement both say so instead of waiting on it forever.
func failRestore(ctx context.Context, store CheckoutRestoreStore, project *db.Project, row *core.ProjectDaemon, daemonID, reason string) {
	logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" restore: could not put a pool member's checkout back",
		"daemonID", daemonID, "projectID", project.ID, "reason", reason)
	requestID := uuid.NewString()
	if err := store.UpsertQueuedProjectDaemon(ctx, project.ID, daemonID, row.Path, row.DefaultBranch, requestID); err != nil {
		return
	}
	_ = store.ResolveQueuedProjectDaemon(ctx, requestID, "restoring the checkout on this pool member failed: "+reason)
}

// isPoolMember reports whether daemonID's labels match one of userID's pools.
func isPoolMember(ctx context.Context, store CheckoutRestoreStore, daemonID, userID string) (bool, error) {
	d, err := store.GetDaemon(ctx, daemonID)
	if err != nil {
		return false, err
	}
	if d == nil || len(d.Labels) == 0 {
		return false, nil
	}
	pools, err := store.ListMachinePoolsForUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, p := range pools {
		if machinepool.Matches(d.Labels, p.Selector) {
			return true, nil
		}
	}
	return false, nil
}

// restorePlan is the clones that put a project's checkout back at row.Path:
// each of its repositories with a remote, at its relative path, the root
// first and every repository before the ones nested inside it (a clone needs
// an empty target). A project with no repository rows but a remote is one
// clone of that remote at the root. The root clones the branch the machine's
// row (or the project) records; a nested repository its remote's default.
func restorePlan(project *db.Project, row *core.ProjectDaemon, repos []*core.Repo) []cloneStep {
	rootBranch := ""
	switch {
	case row.DefaultBranch != nil:
		rootBranch = *row.DefaultBranch
	case project.DefaultBranch != nil:
		rootBranch = *project.DefaultBranch
	}
	var steps []cloneStep
	for _, r := range repos {
		if r == nil || r.RemoteURL == nil || strings.TrimSpace(*r.RemoteURL) == "" {
			continue
		}
		rel := strings.Trim(strings.TrimSpace(r.RelativePath), "/")
		step := cloneStep{Repo: strings.TrimSpace(*r.RemoteURL), Path: row.Path}
		if rel == "" || rel == "." {
			step.Branch = rootBranch
		} else {
			step.Path = ospath.Join(row.Path, rel)
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 && project.RemoteURL != nil && strings.TrimSpace(*project.RemoteURL) != "" {
		steps = append(steps, cloneStep{Repo: strings.TrimSpace(*project.RemoteURL), Branch: rootBranch, Path: row.Path})
	}
	sort.SliceStable(steps, func(i, j int) bool {
		di, dj := strings.Count(steps[i].Path, "/"), strings.Count(steps[j].Path, "/")
		if di != dj {
			return di < dj
		}
		return steps[i].Path < steps[j].Path
	})
	return steps
}
