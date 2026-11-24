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
	"github.com/reliant-labs/reliant/internal/ospath"
)

// ── Cloning a project's nested repositories ────────────────────────────
//
// Setting a project up on a machine (CreateProjectFromRepo) queues the clone
// of its root through the control plane, and records the project's other
// repositories with a remote — the ones below the root, like forge/ inside
// reliant-labs/ — as pending project_daemon_repos rows. A clone needs an
// empty target, so they can only run once the root has landed; the gateway
// holding the machine's stream runs them then, each into its relative path,
// with the same git.clone the root used (so the machine's git credential
// helper authenticates them the same way). The project_daemons row stays
// installing until every one is settled, then becomes installed, or failed
// with a reason naming each repository that did not clone and git's words.
//
// Two triggers, both on the machine's gateway: the machine's announcement
// that the root landed (handleFileSystemChanged), and every connect
// (resumeNestedClonesOnConnect), which picks up a run whose gateway went away
// and rows an older gateway left pending. A claim (ClaimProjectDaemonRepos)
// keeps two gateways off one repository.

const (
	// nestedCloneTimeoutMs bounds one clone on the machine.
	nestedCloneTimeoutMs = restoreCloneTimeoutMs
	// nestedClaimStaleAfter is when a claim no gateway settled may be taken
	// again: past one clone's timeout, its gateway is gone.
	nestedClaimStaleAfter = 20 * time.Minute
	// nestedCloneTotalTimeout bounds one project's nested clones.
	nestedCloneTotalTimeout = restoreTotalTimeout
	// nestedFailureReasonMax bounds the reason stored on the project's row.
	nestedFailureReasonMax = 1000
)

// NestedCloneStore is what cloning a checkout's nested repositories reads and
// writes: the nested rows (store.ProjectDaemonExtStore) and the settle of
// the checkout's own row (*db.Repo).
type NestedCloneStore interface {
	ClaimProjectDaemonRepos(ctx context.Context, projectID, daemonID string, staleBefore time.Time) ([]*core.ProjectDaemonRepo, error)
	SettleProjectDaemonRepo(ctx context.Context, projectID, daemonID, repoID, installErr string) error
	ListProjectDaemonReposForPair(ctx context.Context, projectID, daemonID string) ([]*core.ProjectDaemonRepo, error)
	MarkProjectDaemonInstalledInScope(ctx context.Context, projectID, daemonID string, scope *core.OrgScope) (bool, error)
	FailProjectDaemonInstallInScope(ctx context.Context, projectID, daemonID, installErr string, scope *core.OrgScope) (bool, error)
}

// nestedCloneAdapter joins the repository and the checkout rows into the
// NestedCloneStore the clone pass takes.
type nestedCloneAdapter struct {
	ProjectReconcileRepo
	ProjectDaemons
}

// rootLanded reports whether the checkout's root is on the machine's disk.
type rootLanded func(ctx context.Context) (bool, error)

// hasUnsettledNestedRepos reports whether rows have a clone still to run.
func hasUnsettledNestedRepos(rows []*core.ProjectDaemonRepo) bool {
	for _, r := range rows {
		if r != nil && r.State.Unsettled() {
			return true
		}
	}
	return false
}

// connCloneRunner runs a git.clone on the machine behind conn.
func (s *Service) connCloneRunner(conn *daemonConnection, timeoutMs int32) cloneRunner {
	return func(ctx context.Context, step cloneStep) error {
		payload, err := json.Marshal(step)
		if err != nil {
			return err
		}
		resp, err := s.sendCommandToConn(ctx, conn, &reliantv1.DaemonCommandRequest{
			RequestId:   uuid.NewString(),
			CommandType: "git.clone",
			Payload:     payload,
			TimeoutMs:   timeoutMs,
		})
		if err != nil {
			return err
		}
		if !resp.GetSuccess() {
			msg := strings.TrimSpace(resp.GetErrorMessage())
			if msg == "" {
				msg = "the machine reported no reason"
			}
			return fmt.Errorf("%s", msg)
		}
		return nil
	}
}

// cloneNestedReposAsync clones the checkout's nested repositories off the
// caller's path: this runs on the connection's inbound worker, and clones
// take minutes. conn.done bounds it.
func (s *Service) cloneNestedReposAsync(ctx context.Context, conn *daemonConnection, projectID, rootPath string, scope *core.OrgScope, landed rootLanded) {
	if conn == nil {
		return
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nestedCloneTotalTimeout)
		defer cancel()
		go func() {
			select {
			case <-conn.done:
				cancel()
			case <-runCtx.Done():
			}
		}()
		settled := cloneNestedRepos(runCtx, nestedCloneAdapter{s.deps.Repo, s.deps.Installs},
			s.connCloneRunner(conn, nestedCloneTimeoutMs), landed, projectID, conn.daemonID, rootPath, scope)
		if settled {
			if err := s.deps.Repo.EmitUserRefetch(runCtx, conn.userID, db.RefetchFileTree, db.RefetchOpts{ProjectID: &projectID}); err != nil {
				logging.WarnContext(runCtx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not tell clients the checkout settled",
					"error", err, "projectID", projectID, "daemonID", conn.daemonID)
			}
		}
	}()
}

// resumeNestedClonesOnConnect resumes, on the machine behind conn, the nested
// clones of every checkout that has some still to run: one whose gateway went
// away mid-way, or whose root landed while no gateway of this release held
// the machine. A checkout whose root clone failed waits for the user to set
// it up again; one whose root has not landed yet waits for it
// (cloneNestedRepos checks).
func (s *Service) resumeNestedClonesOnConnect(ctx context.Context, conn *daemonConnection, scope *core.OrgScope, check daemonPathChecker) {
	if conn == nil {
		return
	}
	projectIDs, err := s.deps.Installs.ListProjectsWithUnsettledDaemonRepos(ctx, conn.daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not list the machine's checkouts with nested repositories to clone",
			"error", err, "daemonID", conn.daemonID)
		return
	}
	if len(projectIDs) == 0 {
		return
	}
	rows, err := s.deps.Installs.ListProjectDaemonsForDaemon(ctx, conn.daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not read the machine's checkouts",
			"error", err, "daemonID", conn.daemonID)
		return
	}
	byProject := make(map[string]*core.ProjectDaemon, len(rows))
	for _, r := range rows {
		if r != nil {
			byProject[r.ProjectID] = r
		}
	}
	for _, projectID := range projectIDs {
		row := byProject[projectID]
		if row == nil || row.InstallState == core.ProjectInstallFailed || strings.TrimSpace(row.Path) == "" {
			continue
		}
		path := row.Path
		landed := func(ctx context.Context) (bool, error) {
			state, err := check(ctx, path)
			return state.Exists, err
		}
		s.cloneNestedReposAsync(ctx, conn, projectID, path, scope, landed)
	}
}

// cloneNestedRepos is the testable core: once the checkout's root has landed
// at rootPath, clone each nested repository it can claim into its relative
// path, shallowest first (a clone needs an empty target, and a nested
// repository may hold another), and record each outcome. A repository inside
// one that failed is not attempted: its clone would create the parent's
// directory and the parent could never be cloned there again. When no
// repository of the checkout is left to clone, the checkout's own row is
// settled: installed, or failed naming each repository that did not clone.
// It reports whether it settled that row.
func cloneNestedRepos(ctx context.Context, store NestedCloneStore, run cloneRunner, landed rootLanded, projectID, daemonID, rootPath string, scope *core.OrgScope) bool {
	ok, err := landed(ctx)
	if err != nil || !ok {
		// Not yet: the root's own clone lands first, and its announcement
		// (or the next connect) runs this again.
		return false
	}
	claimed, err := store.ClaimProjectDaemonRepos(ctx, projectID, daemonID, time.Now().Add(-nestedClaimStaleAfter))
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not claim the nested repositories",
			"error", err, "projectID", projectID, "daemonID", daemonID)
		return false
	}
	sort.SliceStable(claimed, func(i, j int) bool {
		di, dj := depthOf(claimed[i].RelativePath), depthOf(claimed[j].RelativePath)
		if di != dj {
			return di < dj
		}
		return claimed[i].RelativePath < claimed[j].RelativePath
	})
	var failedRels []string
	for _, repo := range claimed {
		rel := strings.Trim(strings.TrimSpace(repo.RelativePath), "/")
		var cloneErr string
		switch {
		case ctx.Err() != nil:
			// Left cloning: a later pass re-claims it once the claim is
			// stale. Settling it failed would report a clone that never ran.
			continue
		case insideAny(rel, failedRels):
			cloneErr = "not cloned, because the repository it sits in did not clone"
		case rel == "" || strings.TrimSpace(repo.RemoteURL) == "":
			cloneErr = "no remote is recorded for it"
		default:
			if err := run(ctx, cloneStep{Repo: strings.TrimSpace(repo.RemoteURL), Path: ospath.Join(rootPath, rel)}); err != nil {
				if ctx.Err() != nil {
					continue
				}
				cloneErr = boundReason(err.Error(), nestedFailureReasonMax)
			}
		}
		if cloneErr != "" {
			failedRels = append(failedRels, rel)
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: a nested repository did not clone",
				"projectID", projectID, "daemonID", daemonID, "repoID", repo.RepoID, "relativePath", rel, "reason", cloneErr)
		}
		if err := store.SettleProjectDaemonRepo(ctx, projectID, daemonID, repo.RepoID, cloneErr); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not record a nested repository's clone",
				"error", err, "projectID", projectID, "daemonID", daemonID, "repoID", repo.RepoID)
		}
	}
	return settleNestedCheckout(ctx, store, projectID, daemonID, scope)
}

// settleNestedCheckout settles the checkout's row once none of its nested
// repositories is left to clone. Both settles are no-ops on a row already
// settled, and on a project moved out of the machine's org.
func settleNestedCheckout(ctx context.Context, store NestedCloneStore, projectID, daemonID string, scope *core.OrgScope) bool {
	rows, err := store.ListProjectDaemonReposForPair(ctx, projectID, daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not read the nested repositories",
			"error", err, "projectID", projectID, "daemonID", daemonID)
		return false
	}
	if hasUnsettledNestedRepos(rows) {
		return false // another gateway, or a later pass, finishes them
	}
	if reason := nestedFailureReason(rows); reason != "" {
		failed, err := store.FailProjectDaemonInstallInScope(ctx, projectID, daemonID, reason, scope)
		if err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not record the failure",
				"error", err, "projectID", projectID, "daemonID", daemonID)
			return false
		}
		return failed
	}
	installed, err := store.MarkProjectDaemonInstalledInScope(ctx, projectID, daemonID, scope)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" nested clones: could not settle the checkout",
			"error", err, "projectID", projectID, "daemonID", daemonID)
		return false
	}
	return installed
}

// nestedFailureReason names each nested repository that did not clone and
// why, for the checkout's install_error; "" when every one cloned.
func nestedFailureReason(rows []*core.ProjectDaemonRepo) string {
	var parts []string
	for _, r := range rows {
		if r == nil || r.State != core.NestedRepoFailed {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.RelativePath
		}
		parts = append(parts, fmt.Sprintf("%s (%s/): %s", name, strings.Trim(r.RelativePath, "/"), r.Error))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return boundReason("the project cloned, but its nested repository "+parts[0], nestedFailureReasonMax)
	default:
		return boundReason(fmt.Sprintf("the project cloned, but %d of its nested repositories did not: %s",
			len(parts), strings.Join(parts, "; ")), nestedFailureReasonMax)
	}
}

func depthOf(rel string) int {
	rel = strings.Trim(strings.ReplaceAll(rel, `\`, "/"), "/")
	if rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

// insideAny reports whether rel is one of dirs or below one.
func insideAny(rel string, dirs []string) bool {
	for _, d := range dirs {
		if d != "" && (rel == d || strings.HasPrefix(rel, d+"/")) {
			return true
		}
	}
	return false
}

func boundReason(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}
