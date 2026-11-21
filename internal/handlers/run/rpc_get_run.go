package run

import (
	"context"

	"github.com/reliant-labs/reliant/internal/rundiagnostics"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetRun returns one run's current state.
func (s *Service) GetRun(
	ctx context.Context,
	req *connect.Request[reliantv1.GetRunRequest],
) (*connect.Response[reliantv1.GetRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, _, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	rows, err := s.deps.Repo.ListRunNodeExecutions(ctx, run.ID, rundiagnostics.RecordLimit+1)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read run nodes"))
	}
	truncated := len(rows) > rundiagnostics.RecordLimit
	if truncated {
		rows = rows[:rundiagnostics.RecordLimit]
	}
	return connect.NewResponse(&reliantv1.GetRunResponse{
		Run: runToProto(run), Nodes: rundiagnostics.Nodes(rows), NodesTruncated: truncated,
	}), nil
}
