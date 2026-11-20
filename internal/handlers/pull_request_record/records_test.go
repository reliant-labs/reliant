package pull_request_record_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/pull_request_record"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/pkg/config"
)

const (
	head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	old  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func seed(t *testing.T, env *storetest.Env, number int, labels ...string) {
	t.Helper()
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	key := fmt.Sprintf("d-%d", number)
	_, err := env.Repo.ApplyPRRecordChange(context.Background(), core.PRRecordChange{
		Delivery: core.PRRecordDelivery{Provider: "github", DeliveryKey: key, EventType: "pull_request.synchronize", AppliedAt: at},
		Snapshot: &core.PRRecordSnapshot{
			ID: key, Provider: "github", AccountKey: "inst", RepositoryID: "42", Repository: "acme/widgets",
			Number: number, State: core.PRRecordOpen, HeadSHA: head, Labels: labels, SnapshotAt: at, ObservedAt: at, Full: true,
		},
		HeadID:          key + "-h",
		PreviousHeadSHA: old,
		Reviews: []core.PRRecordReview{
			{ID: key + "-r1", ReviewID: "100", CommitSHA: head, MarkerSHA: head, Kind: core.PRReviewChanges, AuthorLogin: "reliant-labs[bot]", SubmittedAt: at, ObservedAt: at},
			{ID: key + "-r2", ReviewID: "101", CommitSHA: head, MarkerSHA: head, Kind: core.PRReviewPassed, AuthorLogin: "reliant-labs[bot]", SubmittedAt: at.Add(time.Minute), ObservedAt: at},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newService(t *testing.T) (*pull_request_record.Service, *storetest.Env) {
	t.Helper()
	env := storetest.New(t)
	svc, err := pull_request_record.New(pull_request_record.Deps{
		Logger: slog.New(slog.DiscardHandler), Config: &config.Config{}, Store: env.Repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, env
}

func asUser(userID string) context.Context {
	return auth.WithUser(context.Background(), userID, "user", userID+"@example.com")
}

func TestRecordsReadNeedsAccess(t *testing.T) {
	svc, env := newService(t)
	seed(t, env, 7, "review:changes")
	get := &pb.GetPullRequestRecordRequest{Repository: "Acme/Widgets", Number: 7}

	// No grant for the repository: the record does not exist for the caller.
	if _, err := svc.GetPullRequestRecord(asUser(storetest.SeedUserID), connect.NewRequest(get)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("without access: err = %v, want NotFound", err)
	}
	list, err := svc.ListPullRequestRecords(asUser(storetest.SeedUserID), connect.NewRequest(&pb.ListPullRequestRecordsRequest{Repository: "acme/widgets"}))
	if err != nil || len(list.Msg.GetRecords()) != 0 {
		t.Fatalf("without access: list = %v, %v", list, err)
	}
	if _, err := svc.GetPullRequestRecord(context.Background(), connect.NewRequest(get)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("signed out: err = %v", err)
	}
	if _, err := svc.GetPullRequestRecord(asUser(storetest.SeedUserID), connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "widgets", Number: 7})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad repository: err = %v", err)
	}
}

func TestRecordsReadComputesTheVerdict(t *testing.T) {
	svc, env := newService(t)
	for n := 1; n <= 3; n++ {
		seed(t, env, n)
	}
	if err := env.Repo.ReplaceIntegrationAccess(context.Background(), storetest.SeedUserID, "github", "99", time.Now(),
		[]core.IntegrationAccessGrant{{AccountKey: "inst", ResourceKey: "42"}}); err != nil {
		t.Fatal(err)
	}
	ctx := asUser(storetest.SeedUserID)
	policy := &pb.VerdictPolicy{TrustedAuthors: []string{"reliant-labs[bot]"}}

	got, err := svc.GetPullRequestRecord(ctx, connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "acme/widgets", Number: 2, Policy: policy}))
	if err != nil {
		t.Fatal(err)
	}
	// The first marker on the head decides it; the later pass is history.
	if v := got.Msg.GetRecord().GetVerdict(); v.GetKind() != core.PRReviewChanges || v.GetReviewId() != "100" {
		t.Fatalf("verdict = %v", v)
	}
	if len(got.Msg.GetReviews()) != 2 || len(got.Msg.GetHeads()) != 1 || got.Msg.GetHeads()[0].GetPreviousSha() != old {
		t.Fatalf("response = %v", got.Msg)
	}
	// With no trusted author there is no verdict.
	got, err = svc.GetPullRequestRecord(ctx, connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "acme/widgets", Number: 2}))
	if err != nil || got.Msg.GetRecord().GetVerdict().GetKind() != "" {
		t.Fatalf("no policy: %v, %v", got, err)
	}

	// Paging.
	page, err := svc.ListPullRequestRecords(ctx, connect.NewRequest(&pb.ListPullRequestRecordsRequest{Repository: "acme/widgets", PageSize: 2, Policy: policy}))
	if err != nil || len(page.Msg.GetRecords()) != 2 || page.Msg.GetNextPageToken() != "2" {
		t.Fatalf("page 1 = %v, %v", page, err)
	}
	page, err = svc.ListPullRequestRecords(ctx, connect.NewRequest(&pb.ListPullRequestRecordsRequest{Repository: "acme/widgets", PageSize: 2, PageToken: "2", Policy: policy}))
	if err != nil || len(page.Msg.GetRecords()) != 1 || page.Msg.GetRecords()[0].GetNumber() != 3 || page.Msg.GetNextPageToken() != "" {
		t.Fatalf("page 2 = %v, %v", page, err)
	}
	if page.Msg.GetRecords()[0].GetVerdict().GetKind() != core.PRReviewChanges {
		t.Fatalf("listed verdict = %v", page.Msg.GetRecords()[0].GetVerdict())
	}
	if _, err := svc.ListPullRequestRecords(ctx, connect.NewRequest(&pb.ListPullRequestRecordsRequest{Repository: "acme/widgets", PageToken: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
}
