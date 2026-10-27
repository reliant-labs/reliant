package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// projectDirHealConsumerName is the durable JetStream consumer name used by
// StartDaemonEventConsumer. Stable across api-server restarts so progress is
// preserved; safe across multiple api-server replicas because JetStream
// serializes delivery to a single instance per durable.
const projectDirHealConsumerName = "api-server-project-dir-heal"

// StartDaemonEventConsumer subscribes to daemon.v1.events.connected on the
// DAEMON_EVENTS JetStream stream and heals user project directories on each
// connect. Blocks until ctx is cancelled.
//
// Why this exists: the api-server's local ToolsDaemonService never sees
// daemon connections — daemons connect to the daemon-gateway process, not
// here. Previously a DaemonConnectionListener on the local service was used
// for the same heal job, but it never fired in cloud OR OSS mode. The
// daemon-gateway publishes connect events to JetStream
// (internal/daemonevents/publisher.go); we consume those instead.
//
// The synchronous fs.mkdir at the top of CreateProject and the
// EnqueueDaemonCommand fallback both require the daemon to be known to the
// api-server — but during onboarding the api-server's daemons table is
// empty until the cloud pod actually opens its bidi stream, which races
// CreateProject by several seconds. Reacting to the connect event lets us
// mkdir each of the user's project paths the moment the daemon comes
// online, healing those rows without depending on the api-server having
// the daemon ID at create time.
//
// mkdir is idempotent, so JetStream redelivery on a missed Ack is harmless.
func (s *Service) StartDaemonEventConsumer(ctx context.Context, nc *nats.Conn) error {
	if s == nil || s.deps.Router == nil {
		logging.WarnContext(ctx, "daemon-events consumer not started: ProjectService has no daemon router")
		return nil
	}
	if nc == nil {
		logging.WarnContext(ctx, "daemon-events consumer not started: nil NATS connection")
		return nil
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("create jetstream context: %w", err)
	}

	// Look up the stream — don't create it. The daemon-gateway owns stream
	// config (file storage, 7-day retention). If it hasn't started yet,
	// exit gracefully; the heal job will be re-attempted on the next
	// api-server restart.
	stream, err := js.Stream(ctx, daemonevents.StreamDaemonEvents)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			logging.WarnContext(ctx, "daemon-events consumer not started: DAEMON_EVENTS stream does not exist yet (daemon-gateway will create it)")
			return nil
		}
		return fmt.Errorf("look up %s stream: %w", daemonevents.StreamDaemonEvents, err)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       projectDirHealConsumerName,
		FilterSubject: daemonevents.SubjectConnected,
		// Don't replay historical connects — every daemon that's already
		// connected when this consumer starts is irrelevant for heal; we
		// only care about future connect events. The reconciler equivalent
		// for the existing-state case is the synchronous mkdir during
		// CreateProject + the EnqueueDaemonCommand fallback.
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("create/update %s consumer: %w", projectDirHealConsumerName, err)
	}

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		var evt daemonevents.Event
		if err := json.Unmarshal(msg.Data(), &evt); err != nil {
			logging.WarnContext(ctx, "daemon-events consumer: failed to unmarshal event, acking to avoid poison loop", "error", err)
			_ = msg.Ack()
			return
		}
		if evt.UserID == "" {
			_ = msg.Ack()
			return
		}
		s.ensureProjectDirsOnDaemon(evt.UserID, evt.DaemonID)
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("start consuming %s: %w", daemonevents.SubjectConnected, err)
	}
	defer cc.Stop()

	logging.InfoContext(ctx, "daemon-events consumer started",
		"stream", daemonevents.StreamDaemonEvents,
		"subject", daemonevents.SubjectConnected,
		"durable", projectDirHealConsumerName,
	)

	<-ctx.Done()
	logging.InfoContext(ctx, "daemon-events consumer stopped")
	return nil
}

// ensureProjectDirsOnDaemon mkdirs each of the user's project paths on the
// connected daemon. Called by StartDaemonEventConsumer on every daemon
// connect event. Idempotent.
func (s *Service) ensureProjectDirsOnDaemon(userID, daemonID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 200})
	if err != nil {
		logging.Warn("project-dir-heal: failed to list user projects", "error", err, "userID", userID, "daemonID", daemonID)
		return
	}
	if len(projects) == 0 {
		return
	}
	// The counts are logged, not just the total. The total alone read as
	// "ensured 2 project dirs" on 2026-10-07 when the heal had in fact
	// skipped both — they were repositories, whose directories only a clone
	// may create — and the log was the first place anyone looked for why the
	// new machine had no houndersclub directory.
	var healed, skippedRepository int
	defer func() {
		logging.Info("project-dir-heal: done",
			"userID", userID, "daemonID", daemonID, "projects", len(projects),
			"mkdir", healed, "skipped_repository", skippedRepository)
	}()
	for _, p := range projects {
		// Judged with ospath because the mkdir runs on the daemon, not here.
		// Under filepath.IsAbs this heal silently skipped every project
		// belonging to a Windows daemon, so their directories were never
		// created and the skip left no trace.
		if p == nil {
			continue
		}
		if !projectDirIsHealable(p) {
			skippedRepository++
			continue
		}
		if p.Path == "" || !ospath.IsAbs(p.Path) {
			logging.Warn("project-dir-heal: skipping project with a non-absolute path",
				"userID", userID, "daemonID", daemonID, "projectID", p.ID, "path", p.Path)
			continue
		}
		if err := s.sendProjectDaemonCommand(ctx, userID, "fs.mkdir", map[string]string{"path": p.Path}, nil); err != nil {
			logging.Warn("project-dir-heal: failed to mkdir project path", "error", err, "userID", userID, "daemonID", daemonID, "path", p.Path)
			continue
		}
		healed++
	}
}

// projectDirIsHealable reports whether project-dir-heal may create a
// project's directory on a daemon that does not have it.
//
// The heal exists for an EMPTY project created while its daemon was still
// booting, whose mkdir missed the machine. It must never create the directory
// of a project whose checkout comes from a repository. That directory is the
// clone's to create — atomically, and only once the clone succeeded — and an
// empty directory at the project path is read by every other surface as "the
// checkout is here". The heal running on daemon connect is exactly how a
// queued clone that never ran became an empty project the product called
// installed: the connect-time mkdir made the directory, and the connect-time
// reconcile then saw it and marked the install done.
//
// A remote URL is what marks a repository-backed project: CreateProjectFromRepo
// records it, and CreateProject records it when the directory it opened was
// already a checkout. A project with no remote is a plain directory, which an
// empty mkdir faithfully recreates.
func projectDirIsHealable(p *db.Project) bool {
	return p.RemoteURL == nil || strings.TrimSpace(*p.RemoteURL) == ""
}

// projectDaemonToProto converts a db.ProjectDaemon to its proto form.
func projectDaemonToProto(pd *db.ProjectDaemon) *reliantv1.ProjectDaemon {
	out := &reliantv1.ProjectDaemon{
		ProjectId:    pd.ProjectID,
		DaemonId:     pd.DaemonID,
		Path:         pd.Path,
		ClonedAt:     pd.ClonedAt.Format(time.RFC3339),
		InstallState: installStateToProto(pd.InstallState),
		InstallError: pd.InstallError,
	}
	if pd.DefaultBranch != nil {
		out.DefaultBranch = pd.DefaultBranch
	}
	return out
}

// installStateToProto maps the stored state onto the wire enum. An empty
// value means the row predates the column, and those rows are installed by
// definition: the old flow only ever wrote one after a completed clone.
func installStateToProto(state core.ProjectInstallState) reliantv1.ProjectInstallState {
	switch state {
	case core.ProjectInstallInstalling:
		return reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING
	case core.ProjectInstallFailed:
		return reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_FAILED
	default:
		return reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED
	}
}

// ListProjectDaemonsForDaemon returns the project_daemons rows installed on
// the given daemon, filtered to projects owned by the calling user.
func (s *Service) ListProjectDaemonsForDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ListProjectDaemonsForDaemonRequest],
) (*connect.Response[reliantv1.ListProjectDaemonsForDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.DaemonId == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}

	rows, err := s.deps.Repo.ListProjectDaemonsForDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project_daemons"))
	}

	// Filter by ownership: a project_daemons row is only visible to a user
	// if the underlying project belongs to them.
	out := make([]*reliantv1.ProjectDaemon, 0, len(rows))
	for _, pd := range rows {
		if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, pd.ProjectID, userID); err != nil {
			continue
		}
		out = append(out, projectDaemonToProto(pd))
	}

	return connect.NewResponse(&reliantv1.ListProjectDaemonsForDaemonResponse{
		ProjectDaemons: out,
	}), nil
}

// ListProjectDaemons returns every project_daemons row across all the
// caller's projects. Implemented as a fan-out over the user's projects (we
// already have ListProjectDaemonsForProject) rather than a new join query —
// the fan-out is bounded by project count (small) and keeps the SQL surface
// minimal. The picker uses this to tell users *which* daemon hosts a project
// instead of just "another one".
func (s *Service) ListProjectDaemons(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListProjectDaemonsRequest],
) (*connect.Response[reliantv1.ListProjectDaemonsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project_daemons"))
	}

	out := make([]*reliantv1.ProjectDaemon, 0)
	for _, p := range projects {
		rows, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, p.ID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list project_daemons"))
		}
		for _, pd := range rows {
			out = append(out, projectDaemonToProto(pd))
		}
	}

	return connect.NewResponse(&reliantv1.ListProjectDaemonsResponse{
		ProjectDaemons: out,
	}), nil
}

// MarkProjectInstalled records that a project has a clone on a daemon. The
// project must belong to the calling user; the daemon_id is trusted because
// the user-facing flow that calls this just successfully cloned through the
// gateway, which already authenticated the daemon binding.
func (s *Service) MarkProjectInstalled(
	ctx context.Context,
	req *connect.Request[reliantv1.MarkProjectInstalledRequest],
) (*connect.Response[reliantv1.MarkProjectInstalledResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" || req.Msg.DaemonId == "" || req.Msg.Path == "" {
		return nil, svcerr.InvalidArgument("project_id, daemon_id, and path are required")
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	var defaultBranch *string
	if req.Msg.DefaultBranch != nil && *req.Msg.DefaultBranch != "" {
		db := *req.Msg.DefaultBranch
		defaultBranch = &db
	}

	if err := s.deps.Repo.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, req.Msg.Path, defaultBranch); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to record project installation"))
	}

	// The caller just cloned onto this daemon, so forge.yaml is on its disk
	// now. Learn the project's forge name here rather than leaving the row
	// saying "not read yet" until someone opens the Forge tab with a daemon
	// online — see forge_name_learn.go. Non-forge repos are left unmarked,
	// and a failed read is never the RPC's failure: the install is recorded.
	projectfacts.LearnForgeName(ctx, s.deps.Router, s.deps.Repo,
		userID, req.Msg.DaemonId, req.Msg.ProjectId, req.Msg.Path)

	// Return the resolved row so the client can populate UI state without
	// a follow-up list call.
	pd := &db.ProjectDaemon{
		ProjectID:     req.Msg.ProjectId,
		DaemonID:      req.Msg.DaemonId,
		Path:          req.Msg.Path,
		DefaultBranch: defaultBranch,
		ClonedAt:      time.Now().UTC(),
	}
	return connect.NewResponse(&reliantv1.MarkProjectInstalledResponse{
		ProjectDaemon: projectDaemonToProto(pd),
	}), nil
}

// ListRepositoriesForDaemon returns the projects cloned on the given daemon,
// joined with project metadata (name + remote_url) so admin callers can
// render a table without per-row GetProject calls. Filtered to projects
// owned by the calling user.
func (s *Service) ListRepositoriesForDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRepositoriesForDaemonRequest],
) (*connect.Response[reliantv1.ListRepositoriesForDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.DaemonId == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}
	rows, err := s.deps.Repo.ListProjectDaemonsForDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repositories"))
	}
	out := make([]*reliantv1.DaemonRepository, 0, len(rows))
	for _, pd := range rows {
		project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, pd.ProjectID, userID)
		if err != nil {
			// Either the project belongs to a different user or it was
			// hard-deleted while still leaving the FK-cascaded row. Skip
			// quietly so the admin table doesn't show orphans.
			continue
		}
		branch := ""
		if pd.DefaultBranch != nil {
			branch = *pd.DefaultBranch
		} else if project.DefaultBranch != nil {
			branch = *project.DefaultBranch
		}
		remoteURL := ""
		if project.RemoteURL != nil {
			remoteURL = *project.RemoteURL
		}
		out = append(out, &reliantv1.DaemonRepository{
			ProjectId: project.ID,
			Name:      project.Name,
			RemoteUrl: remoteURL,
			Branch:    branch,
			Path:      pd.Path,
			ClonedAt:  pd.ClonedAt.Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListRepositoriesForDaemonResponse{
		Repositories: out,
	}), nil
}

// resolveProjectDaemonRow loads the project_daemons row for (project_id,
// daemon_id) and the parent project, enforcing user ownership. Returns
// NotFound when there's no clone of that project on that daemon.
func (s *Service) resolveProjectDaemonRow(ctx context.Context, projectID, daemonID, userID string) (*db.Project, *core.ProjectDaemon, error) {
	if projectID == "" || daemonID == "" {
		return nil, nil, svcerr.InvalidArgument("project_id and daemon_id are required")
	}
	// A project owned by someone else is the store's NotFound, the same as a
	// missing one, so the answer never reveals that it exists.
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	rows, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to look up project_daemons"))
	}
	for _, pd := range rows {
		if pd.DaemonID == daemonID {
			return project, pd, nil
		}
	}
	return nil, nil, svcerr.WithMessage(svcerr.ErrNotFound, "project is not cloned on that daemon")
}

// PullProjectOnDaemon runs `git pull` for the project's clone on a daemon.
// Reuses the user's default daemon command channel; control-plane callers
// are expected to invoke this with the daemon's owner user identity.
func (s *Service) PullProjectOnDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.PullProjectOnDaemonRequest],
) (*connect.Response[reliantv1.PullProjectOnDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	_, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		return nil, err
	}
	branch := ""
	if pd.DefaultBranch != nil {
		branch = *pd.DefaultBranch
	}
	var pullResp struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	}
	payload := map[string]string{"path": pd.Path, "branch": branch}
	if err := s.sendProjectDaemonCommand(ctx, userID, "git.pull", payload, &pullResp); err != nil {
		return nil, daemonerr.Error("git pull", err)
	}
	// Refresh cloned_at so the admin UI shows "just now" after a successful
	// pull. Errors here are non-fatal — the pull already landed.
	if err := s.deps.Repo.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, pd.Path, pd.DefaultBranch); err != nil {
		logging.WarnContext(ctx, "PullProjectOnDaemon: failed to refresh cloned_at", "error", err, "project_id", req.Msg.ProjectId, "daemon_id", req.Msg.DaemonId)
	}
	return connect.NewResponse(&reliantv1.PullProjectOnDaemonResponse{
		Output: pullResp.Output,
	}), nil
}

// RemoveProjectFromDaemon deletes the on-disk clone AND the
// project_daemons row. The Project row itself is untouched (it may still be
// cloned on other daemons). Idempotent — calling on an absent clone still
// succeeds so the UI can clean up half-broken state.
func (s *Service) RemoveProjectFromDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.RemoveProjectFromDaemonRequest],
) (*connect.Response[reliantv1.RemoveProjectFromDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	_, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		// NotFound is treated as success — the row's already gone and
		// there's nothing on disk to delete from our perspective. Anything
		// else is a real error.
		if svcerr.IsNotFound(err) {
			return connect.NewResponse(&reliantv1.RemoveProjectFromDaemonResponse{}), nil
		}
		return nil, err
	}
	var removeResp struct {
		Success bool   `json:"success"`
		Removed bool   `json:"removed"`
		Error   string `json:"error"`
	}
	payload := map[string]string{"path": pd.Path}
	if err := s.sendProjectDaemonCommand(ctx, userID, "git.remove", payload, &removeResp); err != nil {
		// On-disk remove failure: still try to drop the DB row so the UI
		// stops listing a ghost. Log so an operator can investigate.
		logging.WarnContext(ctx, "RemoveProjectFromDaemon: on-disk remove failed (will still drop project_daemons row)", "error", err, "path", pd.Path)
	}
	if err := s.deps.Repo.DeleteProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete project_daemons row"))
	}
	return connect.NewResponse(&reliantv1.RemoveProjectFromDaemonResponse{}), nil
}

// RecloneProjectOnDaemon blows away the existing clone and re-clones from the
// project's remote_url at the recorded default branch. Refuses to run when
// the project has no remote_url (non-git project — nothing to clone from).
func (s *Service) RecloneProjectOnDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.RecloneProjectOnDaemonRequest],
) (*connect.Response[reliantv1.RecloneProjectOnDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		return nil, err
	}
	if project.RemoteURL == nil || *project.RemoteURL == "" {
		return nil, svcerr.FailedPrecondition("project has no remote_url; cannot reclone")
	}
	branch := ""
	if pd.DefaultBranch != nil {
		branch = *pd.DefaultBranch
	} else if project.DefaultBranch != nil {
		branch = *project.DefaultBranch
	}
	var recloneResp struct {
		Success bool   `json:"success"`
		Path    string `json:"path"`
		Error   string `json:"error"`
	}
	payload := map[string]string{
		"path":   pd.Path,
		"repo":   *project.RemoteURL,
		"branch": branch,
	}
	if err := s.sendProjectDaemonCommand(ctx, userID, "git.reclone", payload, &recloneResp); err != nil {
		return nil, daemonerr.Error("git reclone", err)
	}
	// Refresh cloned_at — the row's path/branch are unchanged but the
	// "Last cloned/pulled" column should now show "just now".
	if err := s.deps.Repo.UpsertProjectDaemon(ctx, req.Msg.ProjectId, req.Msg.DaemonId, pd.Path, pd.DefaultBranch); err != nil {
		logging.WarnContext(ctx, "RecloneProjectOnDaemon: failed to refresh cloned_at", "error", err, "project_id", req.Msg.ProjectId, "daemon_id", req.Msg.DaemonId)
	}
	return connect.NewResponse(&reliantv1.RecloneProjectOnDaemonResponse{
		Path: recloneResp.Path,
	}), nil
}
