package run

import (
	"context"

	"github.com/reliant-labs/reliant/internal/logging"
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
	resp := &reliantv1.GetRunResponse{
		Run: runToProto(run), Nodes: rundiagnostics.Nodes(rows), NodesTruncated: truncated,
	}
	resp.Observations, resp.ObservationsTruncated = s.runObservations(ctx, run.ID)
	return connect.NewResponse(resp), nil
}

// runObservations is what the run tree's nodes recorded with observe:, newest
// first, bounded like the node attempts. A failed read leaves them out rather
// than failing the run's page: observability degrades, it never breaks the
// read it rides on.
func (s *Service) runObservations(ctx context.Context, workflowID string) ([]*reliantv1.NodeObservation, bool) {
	rows, err := s.deps.Repo.ListRunNodeObservations(ctx, workflowID, rundiagnostics.RecordLimit+1)
	if err != nil {
		logging.WarnContext(ctx, "[GetRun] Could not read the run's node observations; returning the run without them",
			"runID", workflowID, "error", err)
		return nil, false
	}
	truncated := len(rows) > rundiagnostics.RecordLimit
	if truncated {
		rows = rows[:rundiagnostics.RecordLimit]
	}
	out, cut := rundiagnostics.Observations(rows, rundiagnostics.ObservationTextBudget)
	return out, truncated || cut
}
