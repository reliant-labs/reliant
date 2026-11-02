// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitremote"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// reconcileTotalTimeout bounds the entire per-connect reconcile pass so a
// slow or unresponsive daemon can't leave a goroutine running forever.
const reconcileTotalTimeout = 60 * time.Second

// reconcilePerCommandTimeoutMs bounds each per-project daemon round-trip. A
// var so tests can shrink it.
var reconcilePerCommandTimeoutMs int32 = 10_000

// projectPathState is the reconcile-relevant on-disk state of a project path on
// a specific daemon, as reported by that daemon.
type projectPathState struct {
	// Exists is true when the path is a real directory on the daemon's disk.
	Exists bool
	// RemoteURL is the git remote the project root resolves to, or "" when the
	// root is not a git repo, has no remote, or could not be resolved.
	RemoteURL string
}

// daemonPathChecker asks a daemon whether a project path exists on its disk and,
// when it does, what git remote the root resolves to. It returns an error only
// for transport/daemon failures — a path that simply isn't on the daemon is
// reported as {Exists: false}, not an error. Factored as an interface so the
// reconcile core can be unit-tested without the bidi streaming plumbing.
type daemonPathChecker func(ctx context.Context, path string) (projectPathState, error)

// reconcileProjectDaemonsOnConnect reconciles project_daemons rows against the
// filesystem of the daemon behind conn. runConnectWork calls it, off the hot
// connect path, once the link answered a probe; ctx ends when the connection
// does. The existence signal is repo.discover: repo.Discover fails
// (Success=false) only when the requested path is not a directory, so a
// successful response means the clone really exists on this daemon's disk.
//
// It returns errReconcileInterrupted when a check met a transport failure:
// the pass stopped there, and runConnectWork retries it once the link proves
// ready again. Only a complete pass goes on to restore missing checkouts.
func (s *Service) reconcileProjectDaemonsOnConnect(ctx context.Context, conn *daemonConnection) error {
	if conn == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, reconcileTotalTimeout)
	defer cancel()

	check := func(ctx context.Context, path string) (projectPathState, error) {
		payload, err := json.Marshal(map[string]any{"path": path, "max_depth": 1})
		if err != nil {
			return projectPathState{}, err
		}
		resp, err := s.sendCommandToConn(ctx, conn, &reliantv1.DaemonCommandRequest{
			RequestId:   uuid.NewString(),
			CommandType: "repo.discover",
			Payload:     payload,
			TimeoutMs:   reconcilePerCommandTimeoutMs,
		})
		if err != nil {
			return projectPathState{}, err
		}
		if !resp.GetSuccess() {
			// repo.discover only fails when the path is not a directory — a
			// clean "not on this daemon" signal, not a transport failure.
			return projectPathState{Exists: false}, nil
		}
		return projectPathState{
			Exists:    true,
			RemoteURL: rootRemoteFromDiscover(resp.GetPayload()),
		}, nil
	}

	missing, err := s.reconcileAndWatch(ctx, conn, check)
	if err != nil {
		return err
	}
	// A pool member whose disk came up empty gets its checkouts back
	// (tools_daemon_restore.go). Its own, longer, budget: clones are slow.
	s.restoreCheckoutsOnConnect(conn, missing)
	return nil
}

// reconcileAndWatch runs the reconcile pass for conn's machine and starts the
// project config watcher (skills, memory, MCP servers) for each checkout it
// recorded. Registration asked the machine to watch the checkouts it had on
// record BEFORE this pass; one the pass just recorded is watched from now,
// not from the next reconnect, including by a pass that was interrupted
// after recording it. It returns the projects absent from the disk and the
// pass's error (errReconcileInterrupted).
func (s *Service) reconcileAndWatch(ctx context.Context, conn *daemonConnection, check daemonPathChecker) ([]*db.Project, error) {
	missing, added, err := reconcileProjectDaemons(ctx, s.deps.Repo, conn.daemonID, conn.userID, check)
	for _, path := range added {
		if err := s.sendLoadAndWatchProjectConfig(ctx, conn, path, true); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to start the project config watcher for a recorded checkout",
				"error", err, "daemonID", conn.daemonID, "path", path)
		}
	}
	return missing, err
}

// reconcileProjectDaemons is the testable core of the reconcile pass. For each
// of the user's projects it asks check whether the path exists on the daemon;
// if so it upserts a project_daemons row and, when the project has no remote
// URL yet, backfills one from the discovered root remote. A database failure
// for one project is logged and skipped. A check failure is a transport
// failure (check reports a path that is simply absent as {Exists: false}), and
// stops the pass with errReconcileInterrupted: every later check would meet
// the same dead link, one timeout at a time. It returns the projects whose
// path the daemon reported absent, and the paths of the checkouts it recorded
// that the machine had no row for or whose lost clone it settled installed.
//
// It never overwrites what the machine's own row already says without
// evidence: a directory at the creation path is evidence of a checkout there
// only when the row has no other path and no clone is landing or failed. A
// machine whose recorded checkout is elsewhere keeps it, and a clone in
// flight settles through its own outcome — unless that outcome is lost: a
// clone installing for longer than staleInstallAfter since it could start
// (stuckInstall) is settled from its row's own path (settleStaleInstall).
func reconcileProjectDaemons(
	ctx context.Context,
	database ProjectReconcileStore,
	daemonID, userID string,
	check daemonPathChecker,
) (missing []*db.Project, added []string, _ error) {
	projects, err := database.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 1000, Offset: 0})
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to list projects",
			"error", err, "userID", userID, "daemonID", daemonID)
		return nil, nil, nil
	}
	// Clones queued while the machine was away start now: their stuck-clone
	// clock starts with them. Without the stamp a row is never judged stuck,
	// so a failure here only defers that to the next connect.
	if err := database.StartQueuedProjectDaemonClones(ctx, daemonID); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to start the clock of the machine's queued clones",
			"error", err, "userID", userID, "daemonID", daemonID)
	}
	recorded := map[string]*core.ProjectDaemon{}
	rows, err := database.ListProjectDaemonsForDaemon(ctx, daemonID)
	if err != nil {
		// Without the machine's rows nothing can be told from new: write
		// nothing rather than overwrite a path it may already have.
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to read the machine's checkouts",
			"error", err, "userID", userID, "daemonID", daemonID)
		return nil, nil, nil
	}
	for _, row := range rows {
		if row != nil {
			recorded[row.ProjectID] = row
		}
	}

	var reconciled, backfilled, skipped int
	for i, project := range projects {
		if ctx.Err() != nil {
			break
		}
		if project == nil || strings.TrimSpace(project.ID) == "" || strings.TrimSpace(project.Path) == "" {
			continue
		}

		prior := recorded[project.ID]
		if stuckInstall(prior) {
			// A clone that could run this long ago and whose outcome never
			// arrived (the machine suspended mid-clone, the notification
			// was lost) would hold the row in "installing" for good, and
			// every machine call refused with "still being cloned". Settle
			// it from the row's OWN path.
			settled, err := settleStaleInstall(ctx, database, check, prior)
			if err != nil {
				logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: interrupted; a path check met a transport failure, so the pass stops here and is retried once the link is ready",
					"error", err, "userID", userID, "daemonID", daemonID,
					"projectID", project.ID, "path", prior.Path,
					"reconciled", reconciled, "checked", i, "projects", len(projects))
				return missing, added, fmt.Errorf("%w: %w", errReconcileInterrupted, err)
			}
			if settled {
				// Installed now: watched from here, as a new checkout is.
				added = append(added, prior.Path)
				reconciled++
			} else {
				skipped++
			}
			continue
		}
		if prior != nil && (prior.InstallState != core.ProjectInstallInstalled || ospath.Clean(prior.Path) != ospath.Clean(project.Path)) {
			// The machine's row says something the creation path cannot
			// overrule: its checkout is elsewhere, or a clone is landing or
			// failed there.
			skipped++
			continue
		}

		state, err := check(ctx, project.Path)
		if err != nil {
			logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: interrupted; a path check met a transport failure, so the pass stops here and is retried once the link is ready",
				"error", err, "userID", userID, "daemonID", daemonID,
				"projectID", project.ID, "path", project.Path,
				"reconciled", reconciled, "checked", i, "projects", len(projects))
			return missing, added, fmt.Errorf("%w: %w", errReconcileInterrupted, err)
		}
		if !state.Exists {
			// Not on this daemon — must not write a row.
			missing = append(missing, project)
			skipped++
			continue
		}

		if err := database.UpsertProjectDaemon(ctx, project.ID, daemonID, project.Path, project.DefaultBranch); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to upsert project_daemon",
				"error", err, "userID", userID, "daemonID", daemonID, "projectID", project.ID)
			skipped++
			continue
		}
		reconciled++
		if prior == nil {
			added = append(added, project.Path)
		}

		// Backfill projects.remote_url when it's unset and discover resolved a
		// root remote for this clone.
		if projectRemoteEmpty(project) && strings.TrimSpace(state.RemoteURL) != "" {
			if backfillProjectRemoteURL(ctx, database, project, strings.TrimSpace(state.RemoteURL)) {
				backfilled++
			}
		}
	}

	if ctx.Err() != nil {
		// The pass's own budget ran out, or the connection ended: not a
		// complete pass, so it must not read as one.
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: stopped before it finished",
			"userID", userID, "daemonID", daemonID, "error", ctx.Err(),
			"reconciled", reconciled, "backfilled", backfilled, "skipped", skipped, "projects", len(projects))
		return missing, added, fmt.Errorf("%w: %w", errReconcileInterrupted, ctx.Err())
	}
	logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: complete",
		"userID", userID, "daemonID", daemonID,
		"reconciled", reconciled, "backfilled", backfilled, "skipped", skipped)
	return missing, added, nil
}

// staleInstallAfter is how long a clone may stay installing before the
// connect reconcile settles it from the row's own path. A clone lands
// atomically (the directory appears only once it succeeded), so a directory
// there is a finished clone; none, this long after it could start, is one
// whose outcome was lost. Settling a clone that is in fact still running is
// recoverable: its outcome, matched by request id, or the machine's
// filesystem announcement still marks it installed.
const staleInstallAfter = 30 * time.Minute

// stuckInstall reports whether row is a clone installing for longer than
// staleInstallAfter, counted from when it could first run
// (core.ProjectDaemon.CloneStartedAt): the queue time when its machine was
// connected then, otherwise the connect that let it start. Counted from the
// queue time, a machine asleep for longer than the timeout woke to its
// just-starting clone marked failed. A clone with no start yet is not stuck:
// its machine has not connected since it was queued.
//
// The queue time (ClonedAt) must be past the timeout too. A clone never
// starts before it is queued, so this changes nothing for rows this release
// writes; it covers a re-clone queued by the previous release during a
// rollout, which re-stamps cloned_at but leaves an earlier clone's
// clone_started_at in place.
func stuckInstall(row *core.ProjectDaemon) bool {
	return row != nil && row.InstallState == core.ProjectInstallInstalling &&
		row.CloneStartedAt != nil && time.Since(*row.CloneStartedAt) >= staleInstallAfter &&
		time.Since(row.ClonedAt) >= staleInstallAfter
}

// staleInstallReason is the clone error a lost clone settles with: the
// refusal then offers to clone again instead of "try again once the clone
// finishes".
const staleInstallReason = "the clone did not report back; it may not have finished"

// settleStaleInstall settles row, a clone installing since before
// staleInstallAfter, from the machine's disk at the row's own path: installed
// when the checkout is there, failed when it is not. It reports whether the
// row is now installed. A check failure (transport) is returned and settles
// nothing.
func settleStaleInstall(ctx context.Context, database ProjectReconcileStore, check daemonPathChecker, row *core.ProjectDaemon) (bool, error) {
	if strings.TrimSpace(row.Path) == "" {
		return false, nil
	}
	state, err := check(ctx, row.Path)
	if err != nil {
		return false, err
	}
	if state.Exists {
		if err := database.MarkProjectDaemonInstalled(ctx, row.ProjectID, row.DaemonID); err != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to settle a lost clone that landed",
				"error", err, "daemonID", row.DaemonID, "projectID", row.ProjectID)
			return false, nil
		}
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: settled a clone whose outcome was lost; its checkout is there",
			"daemonID", row.DaemonID, "projectID", row.ProjectID, "path", row.Path)
		return true, nil
	}
	if err := database.FailProjectDaemonInstall(ctx, row.ProjectID, row.DaemonID, staleInstallReason); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to settle a lost clone that never landed",
			"error", err, "daemonID", row.DaemonID, "projectID", row.ProjectID)
		return false, nil
	}
	logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: settled a clone whose outcome was lost as failed; nothing is at its path",
		"daemonID", row.DaemonID, "projectID", row.ProjectID, "path", row.Path)
	return false, nil
}

// backfillProjectRemoteURL sets project.remote_url to remoteURL, respecting the
// partial unique index projects_user_remote_url_uniq (user_id, remote_url): if
// another of the user's projects already holds that remote, it skips and logs
// rather than colliding. Returns true when the row was updated.
func backfillProjectRemoteURL(ctx context.Context, database ProjectReconcileStore, project *db.Project, remoteURL string) bool {
	existing, err := database.GetProjectByRemoteURLAndUser(ctx, remoteURL, project.UserID)
	switch {
	case err == nil && existing != nil && existing.ID != project.ID:
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: skipping remote backfill, remote already claimed by another project",
			"userID", project.UserID, "projectID", project.ID,
			"otherProjectID", existing.ID, "remoteURL", gitremote.Redacted(remoteURL))
		return false
	case err != nil && !isNotFoundErr(err):
		// Couldn't verify uniqueness — don't risk violating the index.
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: remote uniqueness check failed, skipping backfill",
			"error", err, "userID", project.UserID, "projectID", project.ID, "remoteURL", gitremote.Redacted(remoteURL))
		return false
	}

	updated := *project
	ru := remoteURL
	updated.RemoteURL = &ru
	if err := database.UpdateProject(ctx, &updated, project.UserID); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to backfill remote_url",
			"error", err, "userID", project.UserID, "projectID", project.ID, "remoteURL", gitremote.Redacted(remoteURL))
		return false
	}
	project.RemoteURL = &ru
	return true
}

func projectRemoteEmpty(project *db.Project) bool {
	return project.RemoteURL == nil || strings.TrimSpace(*project.RemoteURL) == ""
}

// rootRemoteFromDiscover extracts the root repo's remote URL from a repo.discover
// response payload. The root repo is the entry with an empty relative_path;
// returns "" when the payload is unparseable or the root has no remote.
func rootRemoteFromDiscover(payload []byte) string {
	var resp struct {
		Discovered []struct {
			RelativePath string `json:"relative_path"`
			RemoteURL    string `json:"remote_url"`
		} `json:"discovered"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return ""
	}
	for _, d := range resp.Discovered {
		if d.RelativePath == "" && strings.TrimSpace(d.RemoteURL) != "" {
			return d.RemoteURL
		}
	}
	return ""
}
