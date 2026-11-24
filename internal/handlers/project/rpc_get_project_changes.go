package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GetProjectChanges returns recent git changes for a project
func (s *Service) GetProjectChanges(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectChangesRequest],
) (*connect.Response[reliantv1.GetProjectChangesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Check if project is a git repository
	if !project.IsGitRepo {
		return connect.NewResponse(&reliantv1.GetProjectChangesResponse{
			Branch:     "",
			Files:      []*reliantv1.FileChange{},
			TotalFiles: 0,
		}), nil
	}

	repoRel, _, err := s.resolveProjectRepo(ctx, project, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}
	target, err := s.checkout(ctx, userID, project, "see its git status")
	if err != nil {
		return nil, err
	}
	repoPath := target.Join(repoRel)

	// Route git changes through daemon
	type daemonFileChange struct {
		Path            string `json:"path"`
		Status          string `json:"status"`
		IsNew           bool   `json:"is_new"`
		Diff            string `json:"diff,omitempty"`
		Content         string `json:"content,omitempty"`
		OriginalContent string `json:"original_content,omitempty"`
	}
	var changesResp struct {
		Branch     string             `json:"branch"`
		Files      []daemonFileChange `json:"files"`
		TotalFiles int32              `json:"total_files"`
	}
	if err := s.sendToDaemon(ctx, userID, target.DaemonID, "project.git_changes", map[string]string{"path": repoPath}, &changesResp); err != nil {
		logging.ErrorContext(ctx, "Failed to get git changes via daemon", "error", err, "projectID", req.Msg.ProjectId, "path", repoPath)
		return connect.NewResponse(&reliantv1.GetProjectChangesResponse{
			Branch:     "",
			Files:      []*reliantv1.FileChange{},
			TotalFiles: 0,
		}), nil
	}

	files := make([]*reliantv1.FileChange, len(changesResp.Files))
	for i, f := range changesResp.Files {
		files[i] = &reliantv1.FileChange{
			Path:            f.Path,
			Status:          fileChangeStatusFromString(f.Status),
			IsNew:           f.IsNew,
			Diff:            f.Diff,
			Content:         f.Content,
			OriginalContent: f.OriginalContent,
		}
	}

	return connect.NewResponse(&reliantv1.GetProjectChangesResponse{
		Branch:     changesResp.Branch,
		Files:      files,
		TotalFiles: changesResp.TotalFiles,
	}), nil
}
