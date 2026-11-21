package system

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/version"
)

// Info returns general API information including version, build info, and worktree
func (s *Service) Info(
	ctx context.Context,
	req *connect.Request[reliantv1.InfoRequest],
) (*connect.Response[reliantv1.InfoResponse], error) {
	buildInfo := version.Get()

	// Extract worktree name from working directory
	// Pattern: ~/.reliant/worktrees/<repo_id>/<worktree-name>
	worktreeName := "dev"
	if wd, err := os.Getwd(); err == nil {
		worktreeName = filepath.Base(wd)
	}

	resp := &reliantv1.InfoResponse{
		Version:        buildInfo.Version,
		Commit:         buildInfo.Commit,
		Date:           buildInfo.Date,
		Branch:         buildInfo.Branch,
		Api:            "v2",
		Timestamp:      time.Now().UTC().Unix(),
		WorktreeName:   worktreeName,
		ClientMinimums: clientMinimums(s.deps.ClientVersions),
	}
	return connect.NewResponse(resp), nil
}
