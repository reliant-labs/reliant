package pull_request_record

import (
	"context"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

// callerID is the signed-in caller, or Unauthenticated.
func callerID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

// repositoryArg is a request's owner/name, or InvalidArgument.
func repositoryArg(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", svcerr.InvalidArgument("repository must be owner/name")
	}
	return repo, nil
}

// canRead is whether the caller's own GitHub credential recently reported the
// record's repository as visible: the grant that routes its events to their
// triggers. A record the caller cannot see is reported as not found.
func (s *Service) canRead(ctx context.Context, userID string, rec *core.PRRecord) (bool, error) {
	return s.deps.Store.HasFreshIntegrationAccess(ctx, userID, rec.Provider, rec.AccountKey, rec.RepositoryID,
		s.now().Add(-core.IntegrationAccessFreshness))
}

// readableRepositories is the repository identities records were kept under
// the name repo that the caller may read. A name can have named a deleted
// repository before, so both reads resolve them before they select a record:
// a record the caller cannot see never stands in for one it can.
func (s *Service) readableRepositories(ctx context.Context, userID, repo string) ([]string, error) {
	identities, err := s.deps.Store.ListPRRecordRepositories(ctx, prrecords.ProviderGitHub, repo)
	if err != nil {
		return nil, err
	}
	var allowed []string
	for _, id := range identities {
		ok, err := s.canRead(ctx, userID, &core.PRRecord{Provider: prrecords.ProviderGitHub, AccountKey: id.AccountKey, RepositoryID: id.RepositoryID})
		if err != nil {
			return nil, err
		}
		if ok && !slices.Contains(allowed, id.RepositoryID) {
			allowed = append(allowed, id.RepositoryID)
		}
	}
	return allowed, nil
}

func policyOf(p *pb.VerdictPolicy) prrecords.Policy {
	return prrecords.Policy{TrustedAuthors: p.GetTrustedAuthors(), RereviewAuthors: p.GetRereviewAuthors()}
}

// currentHead reads the current head's review markers and checks and
// computes its verdict.
func (s *Service) currentHead(ctx context.Context, rec *core.PRRecord, policy prrecords.Policy) ([]core.PRRecordReview, []core.PRRecordCheck, prrecords.Verdict, error) {
	if rec.HeadSHA == "" {
		return nil, nil, prrecords.Verdict{}, nil
	}
	reviews, err := s.deps.Store.ListPRRecordReviews(ctx, rec.ID, []string{rec.HeadSHA})
	if err != nil {
		return nil, nil, prrecords.Verdict{}, err
	}
	checks, err := s.deps.Store.ListPRRecordChecks(ctx, rec.Provider, rec.RepositoryID, []string{rec.HeadSHA})
	if err != nil {
		return nil, nil, prrecords.Verdict{}, err
	}
	return reviews, checks, prrecords.HeadVerdict(reviews, rec.HeadSHA, policy), nil
}

func recordToProto(rec *core.PRRecord, v prrecords.Verdict, checks []core.PRRecordCheck) *pb.PullRequestRecord {
	out := &pb.PullRequestRecord{
		Provider: rec.Provider, Repository: rec.Repository, RepositoryId: rec.RepositoryID,
		Number: int32(rec.Number), State: rec.State, Draft: rec.Draft, Title: rec.Title,
		AuthorLogin: rec.AuthorLogin, HeadSha: rec.HeadSHA, HeadRef: rec.HeadRef,
		BaseRef: rec.BaseRef, BaseSha: rec.BaseSHA, Labels: rec.Labels,
		MergeCommitSha: rec.MergeCommit,
		SnapshotAt:     timestamppb.New(rec.SnapshotAt),
		ObservedAt:     timestamppb.New(rec.ObservedAt),
		Verdict: &pb.PullRequestVerdict{
			Kind: v.Kind, ReviewId: v.ReviewID, AuthorLogin: v.AuthorLogin, Rereview: v.Rereview,
		},
	}
	if rec.MergedAt != nil {
		out.MergedAt = timestamppb.New(*rec.MergedAt)
	}
	if !v.SubmittedAt.IsZero() {
		out.Verdict.SubmittedAt = timestamppb.New(v.SubmittedAt)
	}
	for _, c := range checks {
		pc := &pb.PullRequestCheck{
			Name: c.Name, AppSlug: c.AppSlug, CheckRunId: c.CheckRunID, Status: c.Status, Conclusion: c.Conclusion,
		}
		if c.CompletedAt != nil {
			pc.CompletedAt = timestamppb.New(*c.CompletedAt)
		}
		out.Checks = append(out.Checks, pc)
	}
	return out
}
