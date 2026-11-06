// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const worktreeDaemonCommandTimeoutMs int32 = 120_000

// worktreeReadCommandTimeoutMs bounds the interactive read-path commands the
// UI issues repeatedly (git_changes / git_status / git_commits). At the 120s
// mutation budget, one slow daemon answer pinned a browser connection per
// stacked poll for two minutes — enough to exhaust Chromium's 6-connection
// HTTP/1.1 pool in dev (renderer → Vite is plain http) and starve every other
// RPC behind it. Reads that exceed this budget should fail fast and let the
// next poll retry.
const worktreeReadCommandTimeoutMs int32 = 30_000

// wake is the step a command takes after failing to reach its machine. The
// router resumes an asleep machine when it can (machinewake.ResumerOf's rule:
// only a router that is also a machinewake.Resumer wakes anything).
func (s *Service) wake() machinewake.Service {
	resumer, _ := s.deps.Router.(machinewake.Resumer)
	return machinewake.NewInstrumented(machinewake.Deps{Resumer: resumer, Owners: s.deps.Repo})
}

// relockActive tells the owning daemon to lock this worktree's checkouts as
// active and to RETIRE the archive's fence, so nothing built from a read taken
// before the restore can claim or remove the directory afterwards.
//
// It is synchronous and its answer is checked:
//   - "gone": a removal already finished. The row is recorded as removed and the
//     unarchive is refused (restore it with recreate); flipping it to active over
//     a missing directory would leave a broken workspace.
//   - anything but "locked" (unreachable, held such as a parked checkout, error):
//     the row stays archived and an error is returned, never an active row with
//     no verified lock.
//
// It returns true only when the machine answered "locked". Its errors are the
// wire errors UnarchiveWorktree returns: Unavailable, since the row stays
// archived and the user retries, in the machine's words.
func (s *Service) relockActive(ctx context.Context, userID string, worktree *db.Worktree) (bool, error) {
	if worktree.Path == "" || worktree.DaemonID == nil || *worktree.DaemonID == "" {
		return true, nil
	}
	fence := ""
	if worktree.DeletedAt != nil {
		fence = strconv.FormatInt(worktree.DeletedAt.UnixNano(), 10)
	}
	req := struct {
		Worktrees []map[string]any `json:"worktrees"`
	}{Worktrees: []map[string]any{{"id": worktree.ID, "path": worktree.Path, "state": "active", "retire": fence}}}
	var resp struct {
		Results []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
			Detail  string `json:"detail"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, *worktree.DaemonID, "worktree.reconcile", req, &resp); err != nil {
		// Do not flip the row to active over a directory we could not re-lock:
		// it stays archived and the user retries when the machine is reachable.
		// The machine's state (and the "no daemon connected" marker the web
		// waits on) or the daemon's own words; never the transport's.
		return false, daemonerr.Error("re-lock the archived workspace", err)
	}
	if len(resp.Results) == 0 {
		return false, svcerr.Unavailable("the machine did not answer for this workspace")
	}
	res := resp.Results[0]
	switch res.Outcome {
	case "locked":
		return true, nil
	case "gone":
		_ = s.deps.Repo.MergeWorktreeCleanupMetadata(ctx, worktree.ID, func(m *db.CleanupMetadata, _ bool) bool {
			m.DirectoryDeleted = true
			return true
		})
		return false, nil
	case "held":
		return false, svcerr.Unavailable(fmt.Sprintf("could not restore: %s (%s)", res.Detail, res.Reason))
	case "error":
		return false, svcerr.Unavailable(fmt.Sprintf("the machine could not re-lock this workspace: %s", res.Error))
	}
	return false, svcerr.Unavailable(fmt.Sprintf("could not restore: the machine answered %q", res.Outcome))
}

// settleArchived asks the owning daemon to remove an archived worktree's
// directory if that is safe, on a context detached from the request: archiving
// has already succeeded, and a client that disconnects must not abandon a
// half-finished removal. The directory is the daemon's to judge; whatever it
// declines is recorded and offered in the storage inbox item.
func (s *Service) settleArchived(ctx context.Context, userID string, worktree *db.Worktree, projectPath string) {
	// projectPath is the project's checkout on the owner (ownerCheckout); ""
	// leaves the settle to the sweep.
	if s.deps.Settler == nil || worktree.Path == "" || projectPath == "" {
		return
	}
	archived, err := s.deps.Repo.GetWorktree(ctx, worktree.ID)
	if err != nil || archived == nil {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	go func() {
		defer cancel()
		s.deps.Settler.Settle(bg, userID, &core.ReclaimCandidate{Worktree: archived, OwnerUserID: userID, ProjectPath: projectPath})
	}()
}

// sendWorktreeDaemonCommand sends a command to daemonID ("" = the user's
// default daemon) and unmarshals the response, with the default (mutation)
// timeout budget.
//
// Pass the daemon explicitly. Worktree creation issues one command per nested
// repo, and all of them — plus the id recorded on the row — must name the same
// machine, so it resolves the daemon once and passes it to every send.
func (s *Service) sendWorktreeDaemonCommand(ctx context.Context, userID, daemonID, commandType string, payload interface{}, resp interface{}) error {
	return s.sendWorktreeDaemonCommandTimeout(ctx, userID, daemonID, commandType, payload, resp, worktreeDaemonCommandTimeoutMs)
}

// sendWorktreeDaemonCommandTimeout is sendWorktreeDaemonCommand with an
// explicit timeout — use worktreeReadCommandTimeoutMs for the polled read
// paths so a slow daemon can't pin connections for the full mutation budget.
func (s *Service) sendWorktreeDaemonCommandTimeout(ctx context.Context, userID, daemonID, commandType string, payload interface{}, resp interface{}, timeoutMs int32) error {
	return s.sendToMachine(ctx, userID, machinewake.Target{DaemonID: daemonID}, commandType, payload, resp, timeoutMs)
}

// sendToMachine sends a command to the target machine. Every worktree command
// goes through here, so a machine found asleep is woken (machinewake) and the
// request fails as waking, for the client to retry once it is up.
func (s *Service) sendToMachine(ctx context.Context, userID string, target machinewake.Target, commandType string, payload interface{}, resp interface{}, timeoutMs int32) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if s.deps.Router == nil {
		return fmt.Errorf("daemon command %s: no daemon router", commandType)
	}
	var respBytes []byte
	if target.DaemonID == "" {
		respBytes, err = s.deps.Router.SendDaemonCommand(ctx, userID, commandType, payloadBytes, timeoutMs)
	} else {
		respBytes, err = s.deps.Router.SendDaemonCommandToDaemon(ctx, userID, target.DaemonID, commandType, payloadBytes, timeoutMs)
	}
	if err != nil {
		err = s.wake().AfterFailure(ctx, userID, target, err)
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// =============================================================================
// Permission Helpers
// =============================================================================

// worktreeBelongsToUser checks if a worktree belongs to a user via its project.
// A worktree in someone else's project is "worktree not found", exactly like a
// missing one, so the answer never reveals that it exists. A failure to look
// either up is the failure it is: a deadline stays a deadline, an outage is
// Internal.
func (s *Service) worktreeBelongsToUser(ctx context.Context, worktreeID string, userID string) error {
	worktree, err := s.deps.Repo.GetWorktree(ctx, worktreeID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}
	if worktree == nil {
		return svcerr.NotFound("worktree")
	}

	if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, worktree.ProjectID, userID); err != nil {
		if svcerr.IsNotFound(err) {
			return svcerr.NotFound("worktree")
		}
		return svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	return nil
}

// projectBelongsToUser checks if a project belongs to a user. Missing and
// someone else's are the store's one NotFound (core.ErrProjectNotFound).
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	if _, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	return nil
}

// =============================================================================
// Conversion Helpers
// =============================================================================

// worktreeToProto converts a db.Worktree to proto Worktree
func worktreeToProto(w *db.Worktree) *reliantv1.Worktree {
	proto := &reliantv1.Worktree{
		Id:         w.ID,
		Name:       w.Name,
		Path:       w.Path,
		Branch:     w.Branch,
		BaseBranch: w.BaseBranch,
		ProjectId:  w.ProjectID,
		Status:     reliantv1.WorktreeStatus(w.Status), // the DB stores the enum's number
		IsMain:     w.IsMain,
		CreatedAt:  w.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  w.UpdatedAt.Format(time.RFC3339),
		LastActive: w.LastActive.Format(time.RFC3339),
		// Creation from the UI settles in the background, so the row is the
		// only place the reason reaches the user who asked.
		FailureReason: w.FailureReason,
	}

	if w.ChatID != nil {
		proto.ChatId = w.ChatID
	}

	// The machine holding the checkout, so the new-chat composer can run a
	// chat in this workspace on that machine and nowhere else.
	if owner := daemonplacement.WorktreeOwner(w); owner != "" {
		proto.DaemonId = &owner
	}

	if w.DeletedAt != nil {
		deletedAt := w.DeletedAt.Format(time.RFC3339)
		proto.DeletedAt = &deletedAt
	}

	if w.CleanupMetadata != nil {
		proto.CleanupMetadata = &reliantv1.CleanupMetadata{
			DirectoryDeleted: w.CleanupMetadata.DirectoryDeleted,
			BranchDeleted:    w.CleanupMetadata.BranchDeleted,
			HeldReason:       w.CleanupMetadata.HeldReason,
			HeldDetail:       w.CleanupMetadata.HeldDetail,
			SizeBytes:        w.CleanupMetadata.SizeBytes,
			SnapshotRefs:     w.CleanupMetadata.SnapshotRefs,
			StrippedPaths:    w.CleanupMetadata.StrippedPaths,
			StrippedBytes:    w.CleanupMetadata.StrippedBytes,
			Forgotten:        w.CleanupMetadata.Forgotten,
		}
	}

	return proto
}

// placed is the machine chosen for a workspace and the project's main
// checkout on it: the repos a workspace is made from are THAT machine's
// checkout, never projects.path (the creating machine's).
type placed struct {
	machine  machinewake.Target
	checkout projectcheckout.Target
}

// checkoutOn is the project's main checkout on daemonID — the machine a new
// workspace is placed on, or a workspace's owner, whose repos hold its branch
// and git metadata. "" (a legacy row with no recorded owner) is the project's
// placed machine. A machine with no checkout is projectcheckout's actionable
// refusal: which machines have it, or how to put it there.
func (s *Service) checkoutOn(ctx context.Context, userID string, project *db.Project, daemonID, purpose string) (projectcheckout.Target, error) {
	machines, _ := s.deps.Router.(daemonplacement.Machines)
	return projectcheckout.ForMachine(ctx, s.deps.Repo, machines, userID, project, daemonID, purpose)
}

// withCheckout resolves the project's checkout on target's machine and pins
// target to the machine that answered, so the path and the commands agree.
func (s *Service) withCheckout(ctx context.Context, userID string, project *db.Project, target machinewake.Target, purpose string) (placed, error) {
	checkout, err := s.checkoutOn(ctx, userID, project, target.DaemonID, purpose)
	if err != nil {
		return placed{}, err
	}
	target.DaemonID = checkout.DaemonID
	return placed{machine: target, checkout: checkout}, nil
}

// ownerCheckout is the project's checkout on a workspace's owner for the
// best-effort follow-ups of an archive or delete (the settle, branch
// cleanup), which skip rather than fail: "" when there is no project or the
// owner holds no checkout any more. The sweep retries the settle later.
func (s *Service) ownerCheckout(ctx context.Context, userID string, project *db.Project, worktree *db.Worktree) string {
	if project == nil {
		return ""
	}
	checkout, err := s.checkoutOn(ctx, userID, project, daemonplacement.WorktreeOwner(worktree), "clean up its workspaces")
	if err != nil {
		logging.WarnContext(ctx, "No checkout of the project on the workspace's machine; skipping its cleanup here",
			"error", err, "worktreeID", worktree.ID, "projectID", project.ID)
		return ""
	}
	return checkout.Path
}

// listProjectRepos returns the project's nested repos, rejecting projects with
// none. Empty list is a precondition failure rather than success-with-zero so
// callers can rely on at least one fan-out target.
//
// The registry trails the filesystem — a project cloned onto a cloud machine
// or announced by a daemon has no repo rows — so an empty registry asks the
// machine the worktree goes on (place, called only then) what exists in ITS
// checkout and adopts it. Default resolution is project-blind: it can pick a
// laptop with no checkout, whose "not here" used to surface as "project has
// no git repos" for a project that is one.
func (s *Service) listProjectRepos(ctx context.Context, userID string, project *db.Project, place func() (placed, error)) ([]*core.Repo, error) {
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}
	if len(repos) > 0 {
		return repos, nil
	}
	if s.deps.Router == nil {
		return nil, svcerr.FailedPrecondition("the project has no registered git repository, and no machine is connected to look for one")
	}
	target, err := place()
	if err != nil {
		return nil, err
	}
	repos, err = repopkg.AdoptFromDaemon(ctx, s.deps.Repo, repopkg.OnDaemon(s.deps.Router, target.machine.DaemonID), project, target.checkout.Path)
	if err != nil {
		return nil, s.repoDiscoveryError(ctx, userID, target, err)
	}
	if len(repos) == 0 {
		return nil, svcerr.FailedPrecondition(repopkg.NoReposMessage(target.checkout.Path))
	}
	return repos, nil
}

// repoDiscoveryError is the answer when the project's machine could not say
// which repos the project has. It never claims there are none: a missing
// folder is the user's to fix (FailedPrecondition, naming the path on that
// machine), an asleep machine is woken (the client retries), anything else is
// Unavailable.
func (s *Service) repoDiscoveryError(ctx context.Context, userID string, target placed, err error) error {
	if toolexec.DaemonReportedMissingPath(err) {
		return svcerr.FailedPrecondition(fmt.Sprintf("the project folder %s does not exist on the machine this workspace would be created on", target.checkout.Path))
	}
	var cutShort *repopkg.SearchCutShortError
	if errors.As(err, &cutShort) {
		// The machine answered: waking it or retrying will not change that.
		return svcerr.FailedPrecondition(cutShort.Error())
	}
	err = s.wake().AfterFailure(ctx, userID, target.machine, err)
	if machinewake.IsWaking(err) {
		return err
	}
	doing := fmt.Sprintf("could not check %s for git repositories", target.checkout.Path)
	if serr, ok := daemonerr.MachineState(doing, err); ok {
		return serr
	}
	return svcerr.WithCause(svcerr.Unavailable(doing+" on the project's machine; try again once it is connected"), err)
}

// resolveRepoPath resolves a per-RPC repo_id selector against the worktree's
// project repo set and returns the absolute git checkout path inside the
// workspace. The rules:
//
//   - empty repoID + project has 0 or 1 repos -> use worktree.Path as-is
//     (legacy single-repo behavior; works for both "project root is a repo"
//     and "no repos yet but daemon-side path is still a git checkout").
//   - empty repoID + project has 2+ repos -> InvalidArgument; multi-repo
//     callers must specify which repo they're acting on.
//   - non-empty repoID -> look up the repo, ensure it belongs to this
//     worktree's project, and return <worktree.Path>/<repo.relative_path>.
//   - repoID not in the project's repo set -> NotFound.
//
// Returns the resolved absolute path and the resolved repo (nil when falling
// through to legacy single-repo behavior with no Repo rows).
func (s *Service) resolveRepoPath(ctx context.Context, worktree *db.Worktree, repoID string) (string, *core.Repo, error) {
	repos, err := s.deps.Repo.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return "", nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}

	if repoID == "" {
		if len(repos) > 1 {
			return "", nil, svcerr.InvalidArgument("repo_id required in multi-repo projects")
		}
		// 0 repos: fall through to worktree path (e.g. project initialized
		// before nested-repo migration). 1 repo: prefer the registered repo's
		// relative_path so multi-repo workspace layouts still work for the
		// single-repo case (relative_path is "" for a project-root repo, so
		// this reduces to worktree.Path).
		if len(repos) == 1 {
			return filepath.Join(worktree.Path, repos[0].RelativePath), repos[0], nil
		}
		return worktree.Path, nil, nil
	}

	for _, r := range repos {
		if r.ID == repoID {
			return filepath.Join(worktree.Path, r.RelativePath), r, nil
		}
	}
	return "", nil, svcerr.WithMessage(svcerr.ErrNotFound, "repo not in project")
}

// resolveRepoBaseBranch picks the right base branch for a per-repo write
// operation (e.g. CreatePR). Lookup order:
//
//  1. worktree.BaseBranches[repo.ID] — the per-repo override captured at
//     create time (set when multi-repo workspaces have heterogeneous
//     defaults like main/master/develop).
//  2. worktree.BaseBranch — the legacy single value, canonical for
//     single-repo and a sane fallback when the per-repo map is empty/missing
//     this entry.
//  3. "" — let the daemon auto-detect via gh / git remote show.
//
// repo may be nil when the project has no Repo rows registered (legacy
// pre-migration shape); we fall through to the legacy single value.
func (s *Service) resolveRepoBaseBranch(worktree *db.Worktree, repo *core.Repo) string {
	if repo != nil {
		if b, ok := worktree.BaseBranches[repo.ID]; ok && b != "" {
			return b
		}
	}
	return worktree.BaseBranch
}

// validateWorktreeForGitOps validates that a worktree is suitable for git operations
func (s *Service) validateWorktreeForGitOps(ctx context.Context, userID string, worktree *db.Worktree) error {
	if worktree.DeletedAt != nil {
		return errWorktreeArchived()
	}

	var resp validatePathAnswer
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.validate_path", map[string]string{"path": worktree.Path}, &resp); err != nil {
		return daemonerr.Error("access the worktree directory", err)
	}
	return resp.verdict(worktree.Path)
}

// gitReadOnDisk runs read, the rest of a read-only git rpc on worktree's
// directory, with the worktree.validate_path probe that
// validateWorktreeForGitOps sends before a mutation running beside it rather
// than first. A read that succeeds found the directory, so its answer stands
// without waiting for the probe; when read fails, the probe's verdict on a
// missing or unreadable directory comes first, exactly as
// validateWorktreeForGitOps would have answered before read ran. The polled
// read paths (GetWorktreeChanges, ListWorktreeRepoStatuses, ...) so lose a
// serial daemon round trip: in prod the probe alone was p50 60ms, p95 1.1s,
// p99 5.1s (2026-11-06, gateway spans).
//
// A read that answered without proving the directory is there (a per-repo
// failure it reports in its answer, or no repo to read) returns
// errDirUnverified: the probe decides, and the caller treats the sentinel as
// success.
//
// The probe never wakes the machine and a failed send is no verdict: read's
// own send wakes it, and reports that, so one request wakes a machine once.
func (s *Service) gitReadOnDisk(ctx context.Context, userID string, worktree *db.Worktree, read func() error) error {
	if worktree.DeletedAt != nil {
		return errWorktreeArchived()
	}
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	verdict := make(chan error, 1)
	go func() { verdict <- s.probeWorktreeDir(probeCtx, userID, worktree) }()
	err := read()
	if err == nil {
		return nil
	}
	if v := <-verdict; v != nil {
		return v
	}
	return err
}

// errDirUnverified is what a gitReadOnDisk read returns when its answer does
// not prove the worktree directory exists.
var errDirUnverified = errors.New("the read did not reach the worktree directory")

// probeWorktreeDir is validateWorktreeForGitOps's directory check without the
// wake: nil when the daemon could not be asked, as well as when it found the
// directory.
func (s *Service) probeWorktreeDir(ctx context.Context, userID string, worktree *db.Worktree) error {
	if s.deps.Router == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]string{"path": worktree.Path})
	if err != nil {
		return nil
	}
	var raw []byte
	if daemonID := daemonplacement.WorktreeOwner(worktree); daemonID == "" {
		raw, err = s.deps.Router.SendDaemonCommand(ctx, userID, "worktree.validate_path", payload, worktreeReadCommandTimeoutMs)
	} else {
		raw, err = s.deps.Router.SendDaemonCommandToDaemon(ctx, userID, daemonID, "worktree.validate_path", payload, worktreeReadCommandTimeoutMs)
	}
	if err != nil {
		return nil
	}
	var resp validatePathAnswer
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	return resp.verdict(worktree.Path)
}

// validatePathAnswer is the daemon's answer to worktree.validate_path.
type validatePathAnswer struct {
	Exists bool   `json:"exists"`
	Error  string `json:"error,omitempty"`
}

// verdict is the error a git operation on path fails with, or nil when the
// directory is there.
func (a validatePathAnswer) verdict(path string) error {
	if a.Exists {
		return nil
	}
	if a.Error == "not_found" {
		return svcerr.FailedPrecondition(fmt.Sprintf("worktree directory does not exist: %s; the worktree may need to be recreated", path))
	}
	return svcerr.Internal(fmt.Sprintf("cannot access worktree directory: %s", a.Error))
}

func errWorktreeArchived() error {
	return svcerr.FailedPrecondition("worktree is archived; unarchive it before performing git operations")
}

// wakeIfAsleep asks the target machine a cheap question before a write that
// would otherwise meet an asleep machine too late: a create that persists its
// row and makes the checkouts in the background, or a delete whose cleanup is
// best-effort. An asleep machine is woken and the call fails as waking (see
// machinewake), so nothing is recorded until a retry finds the machine up.
//
// Only a wake is returned. Any other failure of the probe is left for the
// operation itself to meet, as before.
func (s *Service) wakeIfAsleep(ctx context.Context, userID string, target machinewake.Target, path string) error {
	var resp struct {
		Exists bool `json:"exists"`
	}
	err := s.sendToMachine(ctx, userID, target, "worktree.validate_path", map[string]string{"path": path}, &resp, worktreeReadCommandTimeoutMs)
	if machinewake.IsWaking(err) {
		return err
	}
	return nil
}

// worktreeMachine adapts the service's daemon send (which wakes a sleeping
// machine on failure) to workspacecreate.Machine, bound to the placement the
// worktree was made for. It carries the whole target, not just its daemon id:
// a no-machine chat's workspace must never wake its machine, in the
// background checkouts any more than in the request that placed them.
type worktreeMachine struct {
	s      *Service
	userID string
	target machinewake.Target
}

func (m worktreeMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	return m.s.sendToMachine(ctx, m.userID, m.target, commandType, payload, resp, timeoutMs)
}

// emitWorktreeChanged tells connected clients a worktree's state settled.
//
// This rides `user_updates`, which is a sequenced Postgres table replayed on
// reconnect via `sinceSeq` — not a fire-and-forget signal. That is what makes
// asynchronous creation safe on a phone: an app backgrounded through the whole
// 30-120s create reconnects and receives the completion it slept through.
func (s *Service) emitWorktreeChanged(ctx context.Context, userID, projectID, worktreeID string) {
	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchWorktreeChanges, db.RefetchOpts{
		ProjectID:  &projectID,
		WorktreeID: &worktreeID,
	}); err != nil {
		logging.ErrorContext(ctx, "Failed to emit worktree refetch", "error", err, "worktreeID", worktreeID)
	}
}
