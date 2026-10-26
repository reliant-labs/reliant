package tools_daemon

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReportToolResult is a unary RPC handler that was previously used by daemons
// to report tool execution results when the bidirectional stream was unavailable.
// This path has been removed in favour of the bidi stream.
func (s *Service) ReportToolResult(ctx context.Context, req *connect.Request[reliantv1.ReportToolResultRequest]) (*connect.Response[reliantv1.ReportToolResultResponse], error) {
	return nil, svcerr.Unimplemented("ReportToolResult is no longer supported; use the bidi stream")
}
