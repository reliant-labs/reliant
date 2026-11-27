package pipeline

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListQueue lists an enrolled repository's waiting and batched entries in
// pick order, and its batch still building, gating or passed.
func (s *Service) ListQueue(
	ctx context.Context,
	req *connect.Request[pb.ListQueueRequest],
) (*connect.Response[pb.ListQueueResponse], error) {
	if err := s.enabled(); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	repo, err := s.readRepo(ctx, userID, strings.TrimSpace(req.Msg.GetRepoId()))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	entries, err := s.deps.Store.ListActiveQueueEntries(ctx, repo.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	current, err := s.deps.Store.CurrentQueueBatch(ctx, repo.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	out := &pb.ListQueueResponse{CurrentBatch: batchProto(current)}
	for _, e := range entries {
		out.Entries = append(out.Entries, entryProto(e))
	}
	return connect.NewResponse(out), nil
}
