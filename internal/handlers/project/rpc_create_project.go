package project

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitref"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// CreateProject creates a new project
func (s *Service) CreateProject(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateProjectRequest],
) (*connect.Response[reliantv1.CreateProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Name == "" || req.Msg.Path == "" {
		return nil, svcerr.InvalidArgument("name and path are required")
	}

	// ospath, not filepath: the path names a directory on the user's DAEMON,
	// which may run on Windows while this server runs on Linux. filepath.IsAbs
	// compiles to the host's rules and judges every `C:\...` path relative.
	if req.Msg.Path != "" && !ospath.IsAbs(req.Msg.Path) {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("project path must be absolute, got: %q", req.Msg.Path))
	}

	// pinned is the machine the request names (daemon_id); "" is today's
	// default resolution. A named machine is resolved, and an existing
	// project attached, before anything below (findOrAttachOn).
	pinned := req.Msg.GetDaemonId()
	if pinned != "" {
		resp, err := s.findOrAttachOn(ctx, userID, pinned, req.Msg.Path)
		if err != nil || resp != nil {
			return resp, err
		}
	}
	// send runs a command on the pinned machine, or by default resolution.
	send := func(commandType string, payload, resp any) error {
		if pinned != "" {
			return s.sendToDaemon(ctx, userID, pinned, commandType, payload, resp)
		}
		return s.sendProjectDaemonCommand(ctx, userID, commandType, payload, resp)
	}

	// Check if project already exists for this user and path. Path-only
	// lookups are nondeterministic in multi-user setups (the projects table
	// allows the same path under different user_ids), so scope the check.
	//
	// AlreadyExists is the "find" half of find-or-create: every --project-path
	// resolution lands here on the second and later runs. A row is not a
	// directory, so before handing the caller an id to bind a run to, confirm
	// the directory the row names is still there.
	if existing, err := s.deps.Repo.GetProjectByPathAndUser(ctx, req.Msg.Path, userID); pinned == "" && err == nil && existing != nil {
		// The row was found BY this path, so the path asked about is the
		// one the caller named — on the machine the request runs on.
		machine := s.requestMachine(ctx, userID, existing.ID, req.Msg.Path)
		if verr := s.verifyProjectDirectoryExists(ctx, userID, existing.ID, req.Msg.Path, machine); verr != nil {
			return nil, verr
		}
		return nil, svcerr.AlreadyExists("a project already exists at this path")
	}

	// Ensure the project directory exists on the daemon. The cloud
	// workspace pod ships with an empty /home/workspace/projects/, and
	// repo.discover / project.init_files below silently no-op when their
	// target path is missing, leaving the DB row pointing at a phantom
	// directory. MkdirAll is idempotent so this is safe for repeat opens.
	//
	// The onboarding flow races daemon provisioning by ~10s: CreateDaemon
	// fires first, then CreateProject hits this code before the workspace
	// pod has registered. The synchronous mkdir fails with "no daemon
	// connected" in that window. To heal it, queue the same command on the
	// DAEMON_PENDING_COMMANDS JetStream so the gateway dispatches it the
	// moment the daemon's bidi stream comes up.
	//
	// Resolve the target daemon up front so, when the synchronous mkdir
	// below succeeds (proving a connected daemon actually ran it), we can
	// record a project_daemons row against the SAME daemon the command
	// routed to. Resolution is deterministic (connected/local preferred),
	// so the resolved id matches what sendProjectDaemonCommand targeted.
	// A resolve failure is non-fatal: it just means no daemon is reachable
	// right now, and the mkdir will fall through to the pending-command
	// enqueue path (the reconcile-on-connect flow writes the row later).
	resolvedDaemonID, resolveErr := pinned, error(nil)
	if pinned == "" {
		resolvedDaemonID, resolveErr = s.resolveDaemonID(ctx, userID)
	}
	if resolveErr != nil {
		logging.InfoContext(ctx, "No daemon resolvable for new project; will rely on pending-command + reconcile", "path", req.Msg.Path, "error", resolveErr)
	}

	// daemonConnected is set only when the synchronous mkdir succeeds. That
	// is the authoritative signal a live daemon handled the command — a
	// resolved id alone can come from a stale DB fallback. We gate the
	// project_daemons row write on this so a phantom row is never recorded.
	daemonConnected := false
	mkdirPayload := map[string]string{"path": req.Msg.Path}
	if err := send("fs.mkdir", mkdirPayload, nil); err != nil {
		if pinned != "" {
			// The user picked this machine and its folder: queueing the
			// mkdir for whichever machine connects next is not what they
			// asked for. Nothing is recorded yet.
			return nil, daemonerr.Error("create the project's folder on "+s.machineName(ctx, pinned), err)
		}
		logging.WarnContext(ctx, "Failed to mkdir project path via daemon; enqueueing for delivery on next connect", "error", err, "path", req.Msg.Path)
		payloadBytes, marshalErr := json.Marshal(mkdirPayload)
		if marshalErr != nil {
			logging.WarnContext(ctx, "Failed to marshal mkdir payload for enqueue", "error", marshalErr, "path", req.Msg.Path)
		} else if n, enqErr := s.enqueueDaemonCommand(ctx, userID, "fs.mkdir", payloadBytes); enqErr != nil {
			logging.WarnContext(ctx, "Failed to enqueue mkdir for daemon", "error", enqErr, "path", req.Msg.Path)
		} else {
			logging.InfoContext(ctx, "Queued fs.mkdir on DAEMON_PENDING_COMMANDS", "path", req.Msg.Path, "daemons", n)
		}
	} else {
		daemonConnected = true
	}

	// Discover nested git repos under the project path. A project may
	// contain 0..N repos (a docs folder is a valid project with zero).
	// IsGitRepo is derived from "any repos discovered". If the daemon is
	// unreachable, project creation still succeeds with zero repos —
	// repos can be added later via AddRepo.
	var discoverResp struct {
		Discovered       []discoveredRepo `json:"discovered"`
		HasForge         bool             `json:"has_forge"`
		ForgeProjectName string           `json:"forge_project_name"`
		Truncated        bool             `json:"truncated"`
		TruncatedReason  string           `json:"truncated_reason"`
	}
	if err := send("repo.discover", map[string]interface{}{"path": req.Msg.Path}, &discoverResp); err != nil {
		logging.WarnContext(ctx, "Failed to discover repos via daemon, project will start with zero repos", "error", err, "path", req.Msg.Path)
	} else if discoverResp.Truncated {
		// The repos found are registered; the rest are adopted the first time
		// a workspace needs them (repo.AdoptFromDaemon), which says so if its
		// own scan is cut short too.
		logging.WarnContext(ctx, "Repo discovery scanned part of the new project", "reason", discoverResp.TruncatedReason,
			"found", len(discoverResp.Discovered), "path", req.Msg.Path)
	}
	isGitRepo := len(discoverResp.Discovered) > 0

	// On a named machine, a folder that is a checkout of one of the user's
	// projects (its root remote) is that project here, not a new one: the
	// same rule CreateProjectFromRepo applies (existingProjectForRepo).
	if pinned != "" {
		if root, ok := rootRepo(discoverResp.Discovered); ok && strings.TrimSpace(root.RemoteURL) != "" {
			if existing, err := s.deps.Repo.GetProjectByRemoteURLAndUser(ctx, strings.TrimSpace(root.RemoteURL), userID); err == nil && existing != nil {
				return s.attachExisting(ctx, userID, pinned, existing, req.Msg.Path)
			}
		}
	}

	// Set defaults. Normalize + validate the caller-supplied branch so an
	// untrimmed or invalid value (e.g. "main " with a trailing space) is
	// never persisted or handed to git init.
	defaultBranch := "main"
	if req.Msg.DefaultBranch != nil {
		defaultBranch = gitref.NormalizeBranchName(*req.Msg.DefaultBranch)
		if err := gitref.ValidateBranchName(defaultBranch); err != nil {
			// The validator's sentence names the branch and the rule it broke.
			return nil, svcerr.InvalidArgument(err.Error())
		}
	}

	// Auto-initialize a brand-new EMPTY project as a git repo so the user
	// isn't nagged by the "Initialize Git" popup on a fresh project (the
	// popup keys solely off is_git_repo, i.e. ".git exists"). Bare `git init`
	// with NO initial commit: the popup only needs .git, and a fresh cloud
	// workspace pod has no git identity for a commit. OnlyIfEmpty gates it on
	// the daemon so an existing non-empty folder (e.g. a local dir the user
	// opened) is left untouched and still prompts. Best-effort + idempotent
	// (the daemon refuses if .git already exists); if the daemon isn't
	// connected yet (the onboarding provisioning race), enqueue it after the
	// queued fs.mkdir so it runs FIFO once the daemon's stream comes up.
	if !isGitRepo {
		gitInitPayload := struct {
			Path              string   `json:"path"`
			InitialBranch     string   `json:"initial_branch"`
			GitignorePatterns []string `json:"gitignore_patterns"`
			InitialCommit     bool     `json:"initial_commit"`
			OnlyIfEmpty       bool     `json:"only_if_empty"`
		}{
			Path:          req.Msg.Path,
			InitialBranch: defaultBranch,
			InitialCommit: false,
			OnlyIfEmpty:   true,
		}
		var gitInitResp struct {
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		if err := send("project.init_git_repo", gitInitPayload, &gitInitResp); err != nil {
			logging.WarnContext(ctx, "Failed to auto git-init project via daemon; enqueueing for delivery on next connect", "error", err, "path", req.Msg.Path)
			if pinned != "" {
				// The queue delivers to whichever machine connects next,
				// not to the one named; the user is offered "Initialize
				// Git" instead.
				logging.InfoContext(ctx, "Not queueing git-init: the project is on a named machine", "daemonID", pinned)
			} else if payloadBytes, marshalErr := json.Marshal(gitInitPayload); marshalErr != nil {
				logging.WarnContext(ctx, "Failed to marshal git-init payload for enqueue", "error", marshalErr, "path", req.Msg.Path)
			} else if n, enqErr := s.enqueueDaemonCommand(ctx, userID, "project.init_git_repo", payloadBytes); enqErr != nil {
				logging.WarnContext(ctx, "Failed to enqueue git-init for daemon", "error", enqErr, "path", req.Msg.Path)
			} else {
				logging.InfoContext(ctx, "Queued project.init_git_repo on DAEMON_PENDING_COMMANDS", "path", req.Msg.Path, "daemons", n)
			}
		} else if gitInitResp.Success {
			isGitRepo = true
			// Register the freshly initialized root repo. Discovery ran
			// BEFORE init (empty dir -> zero results), so without this the
			// persist loop below records nothing and worktree creation
			// refuses with "project has no git repos" despite .git existing.
			discoverResp.Discovered = append(discoverResp.Discovered,
				discoveredRepo{RelativePath: "", Name: req.Msg.Name})
		} else {
			logging.InfoContext(ctx, "Auto git-init skipped (non-empty dir or precondition)", "reason", gitInitResp.Error, "path", req.Msg.Path)
		}
	}

	// Derive the project's canonical remote URL from the root repo (the
	// discovered repo at RelativePath == "" or "."). For a single-repo
	// project this is the repo's git remote; multi-repo workspaces fall
	// back to whichever repo sits at the project root, if any. NULL when
	// no root remote is resolvable (non-git or daemon offline).
	var projectRemoteURL *string
	for _, found := range discoverResp.Discovered {
		rel := strings.TrimSpace(found.RelativePath)
		if (rel == "" || rel == ".") && found.RemoteURL != "" {
			rurl := found.RemoteURL
			projectRemoteURL = &rurl
			break
		}
	}

	// forge's name for the project is the join key to its control-plane
	// environments; NULL until a daemon reads one (GetTopology backfills).
	var forgeProjectName *string
	if name := strings.TrimSpace(discoverResp.ForgeProjectName); discoverResp.HasForge && name != "" {
		forgeProjectName = &name
	}

	now := time.Now().UTC()
	project := &db.Project{
		ID:               uuid.New().String(),
		UserID:           userID,
		Name:             req.Msg.Name,
		Path:             req.Msg.Path,
		Description:      req.Msg.Description,
		IsGitRepo:        isGitRepo,
		DefaultBranch:    &defaultBranch,
		RemoteURL:        projectRemoteURL,
		IsForge:          discoverResp.HasForge,
		ForgeProjectName: forgeProjectName,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActive:       now,
	}

	if err := s.deps.Repo.CreateProject(ctx, project); err != nil {
		// Race-window safety: if a concurrent request inserted the same
		// (user_id, path) pair after our check above, the DB rejects with a
		// UNIQUE violation. Surface that as AlreadyExists so the client can
		// open the existing project instead of seeing an opaque Internal.
		if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "duplicate key") {
			logging.InfoContext(ctx, "Project already exists at path (unique constraint)", "userID", userID, "path", req.Msg.Path)
			return nil, svcerr.AlreadyExists("a project already exists at this path")
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create project"))
	}

	// Record which daemon this project was provisioned on so the picker can
	// mark it immediately openable without waiting for a daemon reconnect.
	// Only write when a live daemon actually handled the mkdir above AND we
	// resolved its id — a resolved-but-unreachable daemon (mkdir enqueued
	// for later) is the "pending" case, where the reconcile-on-connect flow
	// creates the row once the daemon comes up. A failure here is logged but
	// never fails creation; the reconcile path heals it.
	if daemonConnected && resolvedDaemonID != "" {
		if err := s.deps.Repo.UpsertProjectDaemon(ctx, project.ID, resolvedDaemonID, project.Path, project.DefaultBranch); err != nil {
			logging.WarnContext(ctx, "Failed to record project_daemons row at creation; reconcile will heal it",
				"error", err, "project_id", project.ID, "daemon_id", resolvedDaemonID, "path", project.Path)
		} else {
			logging.InfoContext(ctx, "Recorded project_daemons row at creation",
				"project_id", project.ID, "daemon_id", resolvedDaemonID, "path", project.Path)
		}
	}

	// Persist discovered repos. Failures here are logged but don't fail
	// project creation — the project is still usable, repos can be added
	// later, and re-running discovery will pick them up.
	for _, found := range discoverResp.Discovered {
		repoName := found.Name
		if repoName == "" {
			repoName = filepath.Base(found.RelativePath)
			if repoName == "" || repoName == "." {
				repoName = req.Msg.Name
			}
		}
		var remoteURL *string
		if found.RemoteURL != "" {
			rurl := found.RemoteURL
			remoteURL = &rurl
		}
		repoRecord := &core.Repo{
			ID:           uuid.New().String(),
			ProjectID:    project.ID,
			Name:         repoName,
			RelativePath: found.RelativePath,
			RemoteURL:    remoteURL,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.deps.Repo.CreateRepo(ctx, repoRecord); err != nil {
			logging.WarnContext(ctx, "Failed to persist discovered repo", "error", err, "project_id", project.ID, "relative_path", found.RelativePath)
		}
	}

	// Create reliant.md with default instructions if it doesn't exist (via daemon)
	var initFilesResp struct {
		CreatedReliantMD  bool   `json:"created_reliant_md"`
		CreatedReliantDir bool   `json:"created_reliant_dir"`
		Error             string `json:"error,omitempty"`
	}
	initPayload := map[string]string{
		"path":            req.Msg.Path,
		"default_content": defaultProjectReliantMD,
	}
	if err := send("project.init_files", initPayload, &initFilesResp); err != nil {
		logging.WarnContext(ctx, "Failed to initialize project files via daemon", "error", err, "path", req.Msg.Path)
	} else if initFilesResp.Error != "" {
		logging.WarnContext(ctx, "Failed to initialize project files", "error", initFilesResp.Error, "path", req.Msg.Path)
	}

	// In multi-repo projects, also init .reliant/ in each nested repo so
	// downstream config loaders find the standard layout. Repo-level
	// reliant.md is intentionally not created (opt-in via skip_reliant_md).
	// The repo at RelativePath == "" shares the project-root .reliant/.
	for _, found := range discoverResp.Discovered {
		rel := strings.TrimSpace(found.RelativePath)
		if rel == "" || rel == "." {
			continue
		}
		repoPath := filepath.Join(req.Msg.Path, rel)
		var repoInitResp struct {
			Error string `json:"error,omitempty"`
		}
		repoInitPayload := map[string]interface{}{
			"path":            repoPath,
			"skip_reliant_md": true,
		}
		if err := send("project.init_files", repoInitPayload, &repoInitResp); err != nil {
			logging.WarnContext(ctx, "Failed to initialize repo files via daemon", "error", err, "path", repoPath)
		} else if repoInitResp.Error != "" {
			logging.WarnContext(ctx, "Failed to initialize repo files", "error", repoInitResp.Error, "path", repoPath)
		}
	}

	// Create main worktree for this project
	mainWorktree := &db.Worktree{
		ID:         uuid.New().String(),
		Name:       defaultBranch,
		Path:       req.Msg.Path,
		Branch:     defaultBranch,
		BaseBranch: defaultBranch,
		ProjectID:  project.ID,
		ChatID:     nil,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
		DeletedAt:  nil,
	}

	if err := s.deps.Repo.CreateWorktree(ctx, mainWorktree); err != nil {
		logging.ErrorContext(ctx, "Failed to create main worktree", "error", err, "project_id", project.ID)
		// Don't fail the entire project creation if main worktree fails
	}

	// Seed an empty project_configs row so workflows can start immediately
	// without waiting for the daemon to sync. The daemon will overwrite this
	// with real config data via ON CONFLICT ... DO UPDATE when it syncs.
	seedRecord := &db.ProjectConfigRecord{
		ProjectID: project.ID,
		DaemonID:  config.SeedDaemonID,
	}
	if err := s.deps.Repo.UpsertProjectConfigRecord(ctx, seedRecord); err != nil {
		logging.ErrorContext(ctx, "Failed to seed project config record", "error", err, "project_id", project.ID)
	}

	// Tell the daemon to load real configs for this project path.
	// This is fire-and-forget — the seed row above ensures the workflow
	// can start even if the daemon hasn't responded yet.
	if pinned != "" {
		s.syncConfigOn(ctx, userID, pinned, project.ID, req.Msg.Path)
	} else if s.deps.Router != nil {
		if err := s.deps.Router.SendLoadProjectConfigs(ctx, userID, req.Msg.Path, uuid.New().String()); err != nil {
			logging.WarnContext(ctx, "Failed to request daemon config load for new project",
				"error", err,
				"project_id", project.ID,
				"path", req.Msg.Path,
			)
		}
		// Set up a watcher so config changes are pushed continuously.
		// Without this, projects created after daemon startup never receive
		// config snapshots — only the one-shot load above fires (and may
		// silently fail if the daemon hasn't indexed the path yet).
		if err := s.deps.Router.SendWatchProjectConfigs(ctx, userID, req.Msg.Path, true); err != nil {
			logging.WarnContext(ctx, "Failed to request daemon config watch for new project",
				"error", err,
				"project_id", project.ID,
				"path", req.Msg.Path,
			)
		}
	}

	resp := &reliantv1.CreateProjectResponse{Project: projectToProto(project)}
	if pinned != "" {
		// The path the caller named on this machine (the project row was just
		// made from it): the checkout it records.
		resp.ProjectDaemon = s.checkoutProto(ctx, justRecorded(project.ID, pinned, req.Msg.Path, project.DefaultBranch))
	}
	return connect.NewResponse(resp), nil
}

// findOrAttachOn is CreateProject's find half on a named machine (daemon_id).
// It returns a response when the folder is an existing project — attached to
// it, when the project is not on the machine yet — an error when the request
// must stop, and (nil, nil) when a new project is to be created.
//
// "Existing" is decided as today, by path, extended to the named machine:
//   - a folder that is already a project's checkout on this machine is
//     AlreadyExists, as a path that is a project's is today (after the same
//     check that its directory is still there);
//   - a folder at the creation path of one of the user's projects is that
//     project. The connect reconcile already reads it so — any machine with a
//     directory at a project's creation path is recorded as holding it — so
//     attaching it here only does now what the next connect would. A project
//     already on this machine at another path is AlreadyExists: one machine
//     holds one checkout of a project.
//
// The third rule, a git checkout of a project's remote, needs the folder's
// remote, which repo.discover reads in CreateProject itself.
func (s *Service) findOrAttachOn(ctx context.Context, userID, daemonID, path string) (*connect.Response[reliantv1.CreateProjectResponse], error) {
	if _, err := s.ownedMachine(ctx, userID, daemonID); err != nil {
		return nil, err
	}
	if here, err := s.projectAtPathOn(ctx, userID, daemonID, path); err != nil {
		return nil, err
	} else if here != nil {
		if verr := s.verifyProjectDirectoryExists(ctx, userID, here.ID, path, daemonID); verr != nil {
			return nil, verr
		}
		return nil, svcerr.AlreadyExists(fmt.Sprintf("a project already exists at this path: %q on %s", here.Name, s.machineName(ctx, daemonID)))
	}
	existing, err := s.deps.Repo.GetProjectByPathAndUser(ctx, path, userID)
	if err != nil || existing == nil {
		return nil, nil
	}
	// Attaching a found project never creates its folder: an empty
	// directory dressed as the project is the phantom
	// verifyProjectDirectoryExists refuses.
	if err := s.requireFolder(ctx, userID, daemonID, path); err != nil {
		return nil, err
	}
	return s.attachExisting(ctx, userID, daemonID, existing, path)
}

// attachExisting records the folder at path on the machine as existing's
// checkout there (CreateProject with daemon_id found the project), unless the
// project is already on that machine.
func (s *Service) attachExisting(ctx context.Context, userID, daemonID string, existing *db.Project, path string) (*connect.Response[reliantv1.CreateProjectResponse], error) {
	prior, err := s.checkoutOn(ctx, existing.ID, daemonID)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return nil, svcerr.AlreadyExists(fmt.Sprintf("a project already exists for this folder: %q is on %s at %s",
			existing.Name, s.machineName(ctx, daemonID), prior.Path))
	}
	if err := s.recordCheckout(ctx, userID, daemonID, existing, path, existing.DefaultBranch); err != nil {
		return nil, err
	}
	projectfacts.LearnForgeName(ctx, s.deps.Router, s.deps.Repo, userID, daemonID, existing.ID, path)
	return connect.NewResponse(&reliantv1.CreateProjectResponse{
		Project:       projectToProto(existing),
		ProjectDaemon: s.checkoutProto(ctx, justRecorded(existing.ID, daemonID, path, existing.DefaultBranch)),
		Attached:      true,
	}), nil
}
