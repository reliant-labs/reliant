// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// reconcileTotalTimeout bounds the entire per-connect reconcile pass so a
	// slow or unresponsive daemon can't leave a goroutine running forever.
	reconcileTotalTimeout = 60 * time.Second

	// reconcilePerCommandTimeoutMs bounds each per-project daemon round-trip.
	// Kept short so one unreachable path doesn't starve the rest.
	reconcilePerCommandTimeoutMs = 10_000
)

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
// filesystem of the daemon behind conn. It runs in its own goroutine off the
// hot connect path. The existence signal is repo.discover: repo.Discover fails
// (Success=false) only when the requested path is not a directory, so a
// successful response means the clone really exists on this daemon's disk.
func (s *ToolsDaemonService) reconcileProjectDaemonsOnConnect(conn *daemonConnection) {
	if conn == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTotalTimeout)
	defer cancel()

	// Cancel promptly if the daemon disconnects mid-reconcile.
	go func() {
		select {
		case <-conn.done:
			cancel()
		case <-ctx.Done():
		}
	}()

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

	reconcileProjectDaemons(ctx, s.database, conn.daemonID, conn.userID, check)
}

// reconcileProjectDaemons is the testable core of the reconcile pass. For each
// of the user's projects it asks check whether the path exists on the daemon;
// if so it upserts a project_daemons row and, when the project has no remote
// URL yet, backfills one from the discovered root remote. Individual failures
// are logged and skipped, never fatal.
func reconcileProjectDaemons(
	ctx context.Context,
	database db.Repository,
	daemonID, userID string,
	check daemonPathChecker,
) {
	projects, err := database.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 1000, Offset: 0})
	if err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to list projects",
			"error", err, "userID", userID, "daemonID", daemonID)
		return
	}

	var reconciled, backfilled, skipped int
	for _, project := range projects {
		if ctx.Err() != nil {
			break
		}
		if project == nil || strings.TrimSpace(project.ID) == "" || strings.TrimSpace(project.Path) == "" {
			continue
		}

		state, err := check(ctx, project.Path)
		if err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" reconcile: path check failed",
				"error", err, "userID", userID, "daemonID", daemonID,
				"projectID", project.ID, "path", project.Path)
			skipped++
			continue
		}
		if !state.Exists {
			// Not on this daemon — must not write a row.
			skipped++
			continue
		}

		if err := database.UpsertProjectDaemon(ctx, project.ID, daemonID, project.Path, project.DefaultBranch); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to upsert project_daemon",
				"error", err, "userID", userID, "daemonID", daemonID, "projectID", project.ID)
			skipped++
			continue
		}
		reconciled++

		// Backfill projects.remote_url when it's unset and discover resolved a
		// root remote for this clone.
		if projectRemoteEmpty(project) && strings.TrimSpace(state.RemoteURL) != "" {
			if backfillProjectRemoteURL(ctx, database, project, strings.TrimSpace(state.RemoteURL)) {
				backfilled++
			}
		}
	}

	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" reconcile: complete",
		"userID", userID, "daemonID", daemonID,
		"reconciled", reconciled, "backfilled", backfilled, "skipped", skipped)
}

// backfillProjectRemoteURL sets project.remote_url to remoteURL, respecting the
// partial unique index projects_user_remote_url_uniq (user_id, remote_url): if
// another of the user's projects already holds that remote, it skips and logs
// rather than colliding. Returns true when the row was updated.
func backfillProjectRemoteURL(ctx context.Context, database db.Repository, project *db.Project, remoteURL string) bool {
	existing, err := database.GetProjectByRemoteURLAndUser(ctx, remoteURL, project.UserID)
	switch {
	case err == nil && existing != nil && existing.ID != project.ID:
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" reconcile: skipping remote backfill, remote already claimed by another project",
			"userID", project.UserID, "projectID", project.ID,
			"otherProjectID", existing.ID, "remoteURL", remoteURL)
		return false
	case err != nil && !isNotFoundErr(err):
		// Couldn't verify uniqueness — don't risk violating the index.
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" reconcile: remote uniqueness check failed, skipping backfill",
			"error", err, "userID", project.UserID, "projectID", project.ID, "remoteURL", remoteURL)
		return false
	}

	updated := *project
	ru := remoteURL
	updated.RemoteURL = &ru
	if err := database.UpdateProject(ctx, &updated, project.UserID); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" reconcile: failed to backfill remote_url",
			"error", err, "userID", project.UserID, "projectID", project.ID, "remoteURL", remoteURL)
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
