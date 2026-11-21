package system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/version"
)

// Version returns detailed version information
func (s *Service) Version(
	ctx context.Context,
	req *connect.Request[reliantv1.VersionRequest],
) (*connect.Response[reliantv1.VersionResponse], error) {
	buildInfo := version.Get()

	resp := &reliantv1.VersionResponse{
		Version:        buildInfo.Version,
		Commit:         buildInfo.Commit,
		Date:           buildInfo.Date,
		Branch:         buildInfo.Branch,
		ClientMinimums: clientMinimums(s.deps.ClientVersions),
	}
	return connect.NewResponse(resp), nil
}
