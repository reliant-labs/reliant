package repo

import (
	"context"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
)

// =============================================================================
// DiscoverRepos: scans the project directory for nested git repos via daemon.
// Results are NOT persisted — the caller decides which to add via AddRepo.
// =============================================================================

func (s *Service) DiscoverRepos(
	ctx context.Context,
	req *connect.Request[reliantv1.DiscoverReposRequest],
) (*connect.Response[reliantv1.DiscoverReposResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, err := s.authorizeProject(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	type discoveredRepo struct {
		RelativePath string `json:"relative_path"`
		Name         string `json:"name"`
		RemoteURL    string `json:"remote_url,omitempty"`
	}
	var daemonResp struct {
		Discovered      []discoveredRepo `json:"discovered"`
		Truncated       bool             `json:"truncated"`
		TruncatedReason string           `json:"truncated_reason"`
	}
	target, err := s.checkout(ctx, userID, project, "scan it for repos")
	if err != nil {
		return nil, err
	}
	payload := map[string]interface{}{
		"path":      target.Path,
		"max_depth": req.Msg.MaxDepth,
	}
	if err := s.sendToCheckout(ctx, userID, target, "repo.discover", payload, &daemonResp); err != nil {
		return nil, daemonerr.Error("scan the project for repos", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]*reliantv1.Repo, len(daemonResp.Discovered))
	for i, d := range daemonResp.Discovered {
		out[i] = &reliantv1.Repo{
			ProjectId:    project.ID,
			Name:         d.Name,
			RelativePath: d.RelativePath,
			RemoteUrl:    d.RemoteURL,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	return connect.NewResponse(&reliantv1.DiscoverReposResponse{
		Discovered:      out,
		Truncated:       daemonResp.Truncated,
		TruncatedReason: daemonResp.TruncatedReason,
	}), nil
}
