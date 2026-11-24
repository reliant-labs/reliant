package project

import (
	"context"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonliveness"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// unmovedWorkCommand is the daemon's read of what a fresh clone elsewhere
// would not carry (daemonruntime cmd_git_unmoved.go).
const unmovedWorkCommand = "git.unmoved_work"

// PreviewProjectMachineMove says what of the project's checkout on one
// machine stays there when the project is set up on another: a set-up is a
// fresh clone of each repository with a remote (owner, 2026-11-24: "a fresh
// clone including nested repos, plus a clear list of what won't move"), so
// uncommitted changes, unpushed branches, stashes, git worktrees and branch
// workspaces do not come along, and a repository with no remote does not
// either.
//
// The machine is asked only when it is connected: waking a sleeping cloud
// machine to answer a preview would bill it for nothing. Offline, the answer
// is what Reliant records (the repositories and the branch workspaces) with
// check MACHINE_OFFLINE, which the UI says it could not check.
func (s *Service) PreviewProjectMachineMove(
	ctx context.Context,
	req *connect.Request[pb.PreviewProjectMachineMoveRequest],
) (*connect.Response[pb.PreviewProjectMachineMoveResponse], error) {
	userID := auth.MustGetUserID(ctx)
	projectID, daemonID := strings.TrimSpace(req.Msg.GetProjectId()), strings.TrimSpace(req.Msg.GetDaemonId())
	if projectID == "" || daemonID == "" {
		return nil, svcerr.InvalidArgument("project_id and daemon_id are required")
	}
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	if _, err := s.ownedMachine(ctx, userID, daemonID); err != nil {
		return nil, err
	}
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the project's repositories"))
	}
	out := &pb.PreviewProjectMachineMoveResponse{
		DaemonId:   daemonID,
		DaemonName: strings.Trim(s.machineName(ctx, daemonID), `"`),
		Repos:      moveRepos(project, repos),
	}
	var workspacePaths []string
	if out.Workspaces, workspacePaths, err = s.liveWorkspacesOn(ctx, project, daemonID); err != nil {
		return nil, err
	}

	row, err := s.checkoutOn(ctx, project.ID, daemonID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		out.Check = pb.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_NOT_ON_MACHINE
		return connect.NewResponse(out), nil
	}
	out.Path = row.Path

	live, err := s.deps.Repo.IsDaemonIDAttached(ctx, daemonID, daemonliveness.DefaultStaleThreshold)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read whether the machine is connected"))
	}
	if !live {
		out.Check = pb.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_MACHINE_OFFLINE
		return connect.NewResponse(out), nil
	}

	rels := make([]string, len(out.Repos))
	for i, r := range out.Repos {
		rels[i] = r.RelativePath
	}
	var reply unmovedWorkReply
	err = s.sendToDaemon(ctx, userID, daemonID, unmovedWorkCommand, map[string]any{"path": row.Path, "repos": rels}, &reply)
	switch {
	case err == nil:
		out.Check = pb.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_CHECKED
		applyUnmovedWork(out, reply, workspacePaths)
	case daemonerr.IsOutdated(err):
		out.Check = pb.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_MACHINE_OUTDATED
	case daemonerr.Unreachable(err):
		// It went away between the lease read and the ask.
		out.Check = pb.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_MACHINE_OFFLINE
	default:
		logging.WarnContext(ctx, "PreviewProjectMachineMove: the machine could not check its checkout",
			"error", err, "project_id", project.ID, "daemon_id", daemonID)
		out.Check = pb.ProjectMachineMoveCheck_PROJECT_MACHINE_MOVE_CHECK_FAILED
		out.CheckError = daemonCheckError(err)
	}
	return connect.NewResponse(out), nil
}

// unmovedWorkReply is git.unmoved_work's answer.
type unmovedWorkReply struct {
	Repos []struct {
		RelativePath     string   `json:"relative_path"`
		ChangedFileCount int32    `json:"changed_file_count"`
		ChangedFiles     []string `json:"changed_files"`
		UnpushedBranches []struct {
			Name    string `json:"name"`
			Commits int32  `json:"commits"`
		} `json:"unpushed_branches"`
		StashCount int32 `json:"stash_count"`
		Worktrees  []struct {
			Path   string `json:"path"`
			Branch string `json:"branch"`
		} `json:"worktrees"`
		Error string `json:"error"`
	} `json:"repos"`
}

// moveRepos is every repository of the project, the root first, as Reliant
// records them. A project with no repository rows but a remote is its root.
func moveRepos(project *db.Project, repos []*core.Repo) []*pb.ProjectMachineMoveRepo {
	out := make([]*pb.ProjectMachineMoveRepo, 0, len(repos)+1)
	for _, r := range repos {
		if r == nil {
			continue
		}
		rel := strings.Trim(strings.TrimSpace(r.RelativePath), "/")
		if rel == "." {
			rel = ""
		}
		out = append(out, &pb.ProjectMachineMoveRepo{
			RepoId:       r.ID,
			Name:         r.Name,
			RelativePath: rel,
			Clonable:     r.RemoteURL != nil && strings.TrimSpace(*r.RemoteURL) != "",
		})
	}
	if len(out) == 0 {
		out = append(out, &pb.ProjectMachineMoveRepo{
			Name:     project.Name,
			Clonable: project.RemoteURL != nil && strings.TrimSpace(*project.RemoteURL) != "",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].RelativePath == "") != (out[j].RelativePath == "") {
			return out[i].RelativePath == ""
		}
		return out[i].RelativePath < out[j].RelativePath
	})
	return out
}

// liveWorkspacesOn is the project's live branch workspaces owned by the
// machine: what stays there with its chats, and what a detach refuses over
// (requireNoWorkspacesOn). paths are their directories on the machine.
func (s *Service) liveWorkspacesOn(ctx context.Context, project *db.Project, daemonID string) (out []*pb.ProjectMachineMoveWorkspace, paths []string, _ error) {
	const page = 100
	for offset := 0; ; offset += page {
		worktrees, err := s.deps.Repo.ListWorktrees(ctx, db.WorktreeFilters{
			ProjectID: &project.ID, DaemonID: &daemonID, Limit: page, Offset: offset,
		})
		if err != nil {
			return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the project's workspaces"))
		}
		for _, wt := range worktrees {
			if wt == nil || wt.IsMain || wt.DeletedAt != nil || wt.DaemonID == nil || *wt.DaemonID != daemonID {
				continue
			}
			out = append(out, &pb.ProjectMachineMoveWorkspace{WorktreeId: wt.ID, Name: wt.Name, Branch: wt.Branch})
			if p := ospath.Clean(strings.TrimSpace(wt.Path)); p != "" {
				paths = append(paths, p)
			}
		}
		if len(worktrees) < page {
			break
		}
	}
	return out, paths, nil
}

// applyUnmovedWork fills each repository from the machine's reply. A git
// worktree inside one of the branch workspaces is that workspace, listed
// once, as a workspace.
func applyUnmovedWork(out *pb.PreviewProjectMachineMoveResponse, reply unmovedWorkReply, workspacePaths []string) {
	byRel := make(map[string]*pb.ProjectMachineMoveRepo, len(out.Repos))
	for _, r := range out.Repos {
		byRel[r.RelativePath] = r
	}
	for _, got := range reply.Repos {
		r := byRel[strings.Trim(got.RelativePath, "/")]
		if r == nil {
			continue
		}
		r.CheckError = got.Error
		r.ChangedFileCount = got.ChangedFileCount
		r.ChangedFiles = got.ChangedFiles
		r.StashCount = got.StashCount
		for _, b := range got.UnpushedBranches {
			r.UnpushedBranches = append(r.UnpushedBranches, &pb.ProjectMachineMoveBranch{Name: b.Name, UnpushedCommits: b.Commits})
		}
		for _, wt := range got.Worktrees {
			if inAnyPath(ospath.Clean(wt.Path), workspacePaths) {
				continue
			}
			r.OtherWorktrees = append(r.OtherWorktrees, &pb.ProjectMachineMoveWorktree{Path: wt.Path, Branch: wt.Branch})
		}
	}
}

func inAnyPath(p string, dirs []string) bool {
	for _, d := range dirs {
		if pathWithin(p, d) {
			return true
		}
	}
	return false
}

// daemonCheckError is the machine's reason, bounded.
func daemonCheckError(err error) string {
	msg := strings.TrimSpace(err.Error())
	const maxLen = 500
	if len(msg) > maxLen {
		msg = msg[:maxLen]
	}
	return msg
}
