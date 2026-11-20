package pull_request_record

import (
	"context"
	"strconv"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

const (
	defaultPageSize = 100
	maxPageSize     = 200
)

// ListPullRequestRecords lists one repository's records in number order, each
// with its current head's checks and verdict. A repository the caller's own
// GitHub access does not reach lists nothing.
func (s *Service) ListPullRequestRecords(
	ctx context.Context,
	req *connect.Request[pb.ListPullRequestRecordsRequest],
) (*connect.Response[pb.ListPullRequestRecordsResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	repo, err := repositoryArg(req.Msg.GetRepository())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	state := req.Msg.GetState()
	switch state {
	case "", core.PRRecordOpen, core.PRRecordClosed, core.PRRecordMerged:
	default:
		return nil, svcerr.Wrap(svcerr.InvalidArgument("state must be open, closed or merged"))
	}
	size := int(req.Msg.GetPageSize())
	switch {
	case size < 0:
		return nil, svcerr.Wrap(svcerr.InvalidArgument("page_size must not be negative"))
	case size == 0:
		size = defaultPageSize
	case size > maxPageSize:
		size = maxPageSize
	}
	after := 0
	if tok := req.Msg.GetPageToken(); tok != "" {
		if after, err = strconv.Atoi(tok); err != nil || after < 0 {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("page_token is not one this service returned"))
		}
	}
	recs, err := s.deps.Store.ListPRRecords(ctx, core.PRRecordFilter{
		Provider: prrecords.ProviderGitHub, Repository: repo, State: state, AfterNumber: after, Limit: size + 1,
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	resp := &pb.ListPullRequestRecordsResponse{}
	if len(recs) > size {
		recs = recs[:size]
		resp.NextPageToken = strconv.Itoa(recs[size-1].Number)
	}
	policy := policyOf(req.Msg.GetPolicy())
	// Access is per repository id: a rename keeps the id, and records of one
	// name may span two ids only across a delete and re-create.
	allowed := map[string]bool{}
	for _, rec := range recs {
		key := rec.AccountKey + "/" + rec.RepositoryID
		ok, seen := allowed[key]
		if !seen {
			if ok, err = s.canRead(ctx, userID, rec); err != nil {
				return nil, svcerr.Wrap(err)
			}
			allowed[key] = ok
		}
		if !ok {
			continue
		}
		_, checks, verdict, err := s.currentHead(ctx, rec, policy)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		resp.Records = append(resp.Records, recordToProto(rec, verdict, checks))
	}
	if len(resp.Records) == 0 {
		resp.NextPageToken = ""
	}
	return connect.NewResponse(resp), nil
}
