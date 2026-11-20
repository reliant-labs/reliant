package pull_request_record

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

// GetPullRequestRecord returns one pull request's record, its heads, and its
// current head's review markers, checks and verdict under the request's
// policy. A record the caller's own GitHub access does not reach is NotFound,
// like one that does not exist.
func (s *Service) GetPullRequestRecord(
	ctx context.Context,
	req *connect.Request[pb.GetPullRequestRecordRequest],
) (*connect.Response[pb.GetPullRequestRecordResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	repo, err := repositoryArg(req.Msg.GetRepository())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if req.Msg.GetNumber() <= 0 {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("number must be positive"))
	}
	rec, err := s.deps.Store.GetPRRecord(ctx, prrecords.ProviderGitHub, repo, int(req.Msg.GetNumber()))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	notFound := svcerr.NotFound("no record of that pull request")
	if rec == nil {
		return nil, svcerr.Wrap(notFound)
	}
	if ok, err := s.canRead(ctx, userID, rec); err != nil {
		return nil, svcerr.Wrap(err)
	} else if !ok {
		return nil, svcerr.Wrap(notFound)
	}
	reviews, checks, verdict, err := s.currentHead(ctx, rec, policyOf(req.Msg.GetPolicy()))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	heads, err := s.deps.Store.ListPRRecordHeads(ctx, rec.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	resp := &pb.GetPullRequestRecordResponse{Record: recordToProto(rec, verdict, checks)}
	for _, h := range heads {
		ph := &pb.PullRequestHead{Sha: h.HeadSHA, PreviousSha: h.PreviousHeadSHA, FirstSeenAt: timestamppb.New(h.FirstSeenAt)}
		if h.SupersededAt != nil {
			ph.SupersededAt = timestamppb.New(*h.SupersededAt)
		}
		resp.Heads = append(resp.Heads, ph)
	}
	for _, r := range reviews {
		resp.Reviews = append(resp.Reviews, &pb.PullRequestReviewMarker{
			ReviewId: r.ReviewID, CommitSha: r.CommitSHA, MarkerSha: r.MarkerSHA, Kind: r.Kind,
			OfReviewId: r.OfReviewID, AuthorLogin: r.AuthorLogin, SubmittedAt: timestamppb.New(r.SubmittedAt),
		})
	}
	return connect.NewResponse(resp), nil
}
