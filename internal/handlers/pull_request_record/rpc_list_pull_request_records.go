package pull_request_record

import (
	"context"
	"strconv"
	"strings"

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
// with its current head's checks and verdict. Only the repository identities
// the caller's own GitHub access reaches are listed (a name can have named a
// deleted repository before), and they are chosen before paging, so a page
// is never cut short by records the caller cannot see.
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
	afterNumber, afterRepo, err := parsePageToken(req.Msg.GetPageToken())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	allowed, err := s.readableRepositories(ctx, userID, repo)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	resp := &pb.ListPullRequestRecordsResponse{}
	if len(allowed) == 0 {
		return connect.NewResponse(resp), nil
	}
	recs, err := s.deps.Store.ListPRRecords(ctx, core.PRRecordFilter{
		Provider: prrecords.ProviderGitHub, Repository: repo, RepositoryIDs: allowed, State: state,
		AfterNumber: afterNumber, AfterRepositoryID: afterRepo, Limit: size + 1,
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if len(recs) > size {
		recs = recs[:size]
		last := recs[size-1]
		resp.NextPageToken = strconv.Itoa(last.Number) + ":" + last.RepositoryID
	}
	policy := policyOf(req.Msg.GetPolicy())
	for _, rec := range recs {
		_, checks, verdict, err := s.currentHead(ctx, rec, policy)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		resp.Records = append(resp.Records, recordToProto(rec, verdict, checks))
	}
	return connect.NewResponse(resp), nil
}

// parsePageToken reads a next_page_token: "<number>:<repository id>", the
// last record of the previous page.
func parsePageToken(tok string) (int, string, error) {
	if tok == "" {
		return 0, "", nil
	}
	num, repo, ok := strings.Cut(tok, ":")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || n < 0 || repo == "" {
		return 0, "", svcerr.InvalidArgument("page_token is not one this service returned")
	}
	return n, repo, nil
}
