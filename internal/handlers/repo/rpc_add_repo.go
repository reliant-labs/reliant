package repo

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// =============================================================================
// AddRepo: persists a new nested repo for a project.
//
// Validates the on-disk path exists + has a .git via the daemon. Reads the
// remote URL opportunistically. Rejects duplicates by (project_id, relative_path).
// =============================================================================

func (s *Service) AddRepo(
	ctx context.Context,
	req *connect.Request[reliantv1.AddRepoRequest],
) (*connect.Response[reliantv1.AddRepoResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, err := s.authorizeProject(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	relPath := req.Msg.RelativePath
	target, err := s.checkout(ctx, userID, project, "add a repo to it")
	if err != nil {
		return nil, err
	}
	repoPath := target.Join(relPath)

	// Validate via daemon: confirm path is a git repo. We piggyback on
	// repo.discover with max_depth=0 to scan just the requested path —
	// it returns one Found if the path is a git repo, zero otherwise.
	type discoveredRepo struct {
		RelativePath string `json:"relative_path"`
		RemoteURL    string `json:"remote_url,omitempty"`
	}
	var daemonResp struct {
		Discovered []discoveredRepo `json:"discovered"`
	}
	if err := s.sendToCheckout(ctx, userID, target, "repo.discover", map[string]interface{}{
		"path":      repoPath,
		"max_depth": 1,
	}, &daemonResp); err != nil {
		return nil, daemonerr.Error("validate the repo path", err)
	}
	var remoteURL *string
	foundGit := false
	for _, d := range daemonResp.Discovered {
		if d.RelativePath == "" || d.RelativePath == "." {
			foundGit = true
			if d.RemoteURL != "" {
				rurl := d.RemoteURL
				remoteURL = &rurl
			}
			break
		}
	}
	if !foundGit {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("path %q is not a git repository", relPath))
	}

	// Reject duplicates.
	if existing, err := s.deps.Repo.GetRepoByProjectAndPath(ctx, project.ID, relPath); err == nil && existing != nil {
		return nil, svcerr.AlreadyExists(fmt.Sprintf("repo at %q already registered for this project", relPath))
	}

	name := ""
	if req.Msg.Name != nil {
		name = *req.Msg.Name
	}
	if name == "" {
		name = filepath.Base(repoPath)
		if name == "" || name == "." || name == "/" {
			name = project.Name
		}
	}

	now := time.Now().UTC()
	record := &core.Repo{
		ID:           uuid.New().String(),
		ProjectID:    project.ID,
		Name:         name,
		RelativePath: relPath,
		RemoteURL:    remoteURL,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.deps.Repo.CreateRepo(ctx, record); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to add repo"))
	}

	// Flip is_git_repo true if it wasn't already — adding the first repo to
	// a previously empty project makes it git-bearing.
	if !project.IsGitRepo {
		project.IsGitRepo = true
		project.UpdatedAt = now
		if err := s.deps.Repo.UpdateProject(ctx, project, userID); err != nil {
			logging.WarnContext(ctx, "Failed to flip project.is_git_repo", "error", err, "projectID", project.ID)
		}
	}

	return connect.NewResponse(&reliantv1.AddRepoResponse{Repo: repoToProto(record, nil)}), nil
}
