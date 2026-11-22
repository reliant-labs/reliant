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
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/pkg/config"
)

const (
	head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	old  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	alice = "user-alice" // granted the repository
	bob   = "user-bob"   // a member of the same org, never granted it
)

// now is the service's clock in every test: grant freshness is judged by it.
var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// seed records pull request number of the repository with id repoID under
// the name acme/widgets.
func seed(t *testing.T, env *storetest.Env, repoID string, number int, labels ...string) {
	t.Helper()
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	key := fmt.Sprintf("d-%s-%d", repoID, number)
	_, err := env.Repo.ApplyPRRecordChange(context.Background(), core.PRRecordChange{
		Delivery: core.PRRecordDelivery{Provider: "github", DeliveryKey: key, EventType: "pull_request.synchronize", AppliedAt: at},
		Snapshot: &core.PRRecordSnapshot{
			ID: key, Provider: "github", AccountKey: "inst", RepositoryID: repoID, Repository: "acme/widgets",
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

func grant(t *testing.T, env *storetest.Env, userID string, at time.Time, repoIDs ...string) {
	t.Helper()
	var grants []core.IntegrationAccessGrant
	for _, id := range repoIDs {
		grants = append(grants, core.IntegrationAccessGrant{AccountKey: "inst", ResourceKey: id})
	}
	if err := store.ReplaceIntegrationAccess(context.Background(), env.Repo.ORM(), userID, "github", "gh-"+userID, at, grants); err != nil {
		t.Fatal(err)
	}
}

func newService(t *testing.T) (*pull_request_record.Service, *storetest.Env) {
	t.Helper()
	env := storetest.New(t)
	svc, err := pull_request_record.New(pull_request_record.Deps{
		Logger: slog.New(slog.DiscardHandler), Config: &config.Config{}, Store: env.Repo,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, env
}

func asUser(userID string) context.Context {
	return auth.WithUser(context.Background(), userID, "user", userID+"@example.com")
}

func get(svc *pull_request_record.Service, userID string, number int32) error {
	_, err := svc.GetPullRequestRecord(asUser(userID), connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "Acme/Widgets", Number: number}))
	return err
}

func list(t *testing.T, svc *pull_request_record.Service, userID string, size int32, token string) ([]int32, string) {
	t.Helper()
	resp, err := svc.ListPullRequestRecords(asUser(userID), connect.NewRequest(&pb.ListPullRequestRecordsRequest{
		Repository: "acme/widgets", PageSize: size, PageToken: token,
	}))
	if err != nil {
		t.Fatalf("list as %s: %v", userID, err)
	}
	var nums []int32
	for _, r := range resp.Msg.GetRecords() {
		nums = append(nums, r.GetNumber())
	}
	return nums, resp.Msg.GetNextPageToken()
}

// TestRecordsReadOnlyWithTheCallersOwnAccess: a record is read only through
// the caller's own fresh grant for the repository; another user's grant, a
// stale grant or a revoked one reads as not found.
func TestRecordsReadOnlyWithTheCallersOwnAccess(t *testing.T) {
	svc, env := newService(t)
	seed(t, env, "42", 7, "review:changes")
	grant(t, env, alice, now.Add(-time.Minute), "42")

	if err := get(svc, alice, 7); err != nil {
		t.Fatalf("alice (granted): %v", err)
	}
	if nums, _ := list(t, svc, alice, 0, ""); len(nums) != 1 {
		t.Fatalf("alice lists %v", nums)
	}
	// Bob holds no grant: alice's does not reach him.
	if err := get(svc, bob, 7); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob: err = %v, want NotFound", err)
	}
	if nums, tok := list(t, svc, bob, 0, ""); len(nums) != 0 || tok != "" {
		t.Fatalf("bob lists %v (token %q)", nums, tok)
	}
	// A grant for another repository of the installation does not reach it.
	grant(t, env, bob, now.Add(-time.Minute), "43")
	if err := get(svc, bob, 7); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob with another repository: err = %v", err)
	}
	// A grant no refresh has confirmed within the freshness window is stale.
	grant(t, env, bob, now.Add(-core.IntegrationAccessFreshness-time.Minute), "42")
	if err := get(svc, bob, 7); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob with a stale grant: err = %v", err)
	}
	// A revocation (the repository removed from the installation) ends it.
	if _, err := env.Repo.RevokeIntegrationAccess(context.Background(), "github",
		core.IntegrationAccessRevocation{AccountKey: "inst", ResourceKey: "42"}); err != nil {
		t.Fatal(err)
	}
	if err := get(svc, alice, 7); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("alice after revocation: err = %v", err)
	}
	if nums, _ := list(t, svc, alice, 0, ""); len(nums) != 0 {
		t.Fatalf("alice after revocation lists %v", nums)
	}

	if _, err := svc.GetPullRequestRecord(context.Background(), connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "acme/widgets", Number: 7})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("signed out: err = %v", err)
	}
	if err := get(svc, alice, 0); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("number 0: err = %v", err)
	}
	if _, err := svc.GetPullRequestRecord(asUser(alice), connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "widgets", Number: 7})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad repository: err = %v", err)
	}
}

// TestRecordsListAcrossARecreatedRepository: a name that named a deleted
// repository lists only the identity the caller can see, and pages through
// numbers both identities share without skipping or stopping early.
func TestRecordsListAcrossARecreatedRepository(t *testing.T) {
	svc, env := newService(t)
	// The deleted repository (id 41) had 1-4; the re-created one (id 42) has 3-6.
	for n := 1; n <= 4; n++ {
		seed(t, env, "41", n)
	}
	for n := 3; n <= 6; n++ {
		seed(t, env, "42", n)
	}
	grant(t, env, alice, now.Add(-time.Minute), "42")
	var got []int32
	token := ""
	for page := 0; page < 10; page++ {
		nums, next := list(t, svc, alice, 1, token)
		got = append(got, nums...)
		if next == "" {
			break
		}
		token = next
	}
	if fmt.Sprint(got) != "[3 4 5 6]" {
		t.Fatalf("alice pages through %v, want [3 4 5 6]", got)
	}

	// Granted both, every record of both, in (number, repository) order, a
	// shared number split across a page boundary.
	grant(t, env, bob, now.Add(-time.Minute), "41", "42")
	got, token = nil, ""
	for page := 0; page < 20; page++ {
		nums, next := list(t, svc, bob, 3, token)
		got = append(got, nums...)
		if next == "" {
			break
		}
		token = next
	}
	if fmt.Sprint(got) != "[1 2 3 3 4 4 5 6]" {
		t.Fatalf("bob pages through %v", got)
	}
	if _, err := svc.ListPullRequestRecords(asUser(alice), connect.NewRequest(&pb.ListPullRequestRecordsRequest{Repository: "acme/widgets", PageToken: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
}

// TestRecordsGetAcrossARecreatedRepository: of a deleted and a re-created
// repository sharing the name and a number, Get returns the one the caller
// may read, even when the other was observed more recently (a late
// delivery for the deleted repository).
func TestRecordsGetAcrossARecreatedRepository(t *testing.T) {
	svc, env := newService(t)
	seed(t, env, "42", 3)
	seed(t, env, "41", 3)
	// A stale snapshot of the deleted repository's #3, delivered late: it
	// changes nothing but marks that record observed last.
	late := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	if _, err := env.Repo.ApplyPRRecordChange(context.Background(), core.PRRecordChange{
		Delivery: core.PRRecordDelivery{Provider: "github", DeliveryKey: "late-41-3", EventType: "pull_request.labeled", AppliedAt: late},
		Snapshot: &core.PRRecordSnapshot{
			ID: "late-41-3", Provider: "github", AccountKey: "inst", RepositoryID: "41", Repository: "acme/widgets",
			Number: 3, State: core.PRRecordOpen, HeadSHA: head, SnapshotAt: late.Add(-2 * time.Hour), ObservedAt: late, Full: true,
		},
		HeadID: "late-41-3-h",
	}); err != nil {
		t.Fatal(err)
	}
	repoOf := func(userID string) (string, error) {
		resp, err := svc.GetPullRequestRecord(asUser(userID), connect.NewRequest(&pb.GetPullRequestRecordRequest{Repository: "acme/widgets", Number: 3}))
		if err != nil {
			return "", err
		}
		return resp.Msg.GetRecord().GetRepositoryId(), nil
	}

	grant(t, env, alice, now.Add(-time.Minute), "42")
	if id, err := repoOf(alice); err != nil || id != "42" {
		t.Fatalf("alice (granted the re-created repository): repository %q, err %v; want 42", id, err)
	}
	grant(t, env, bob, now.Add(-time.Minute), "41")
	if id, err := repoOf(bob); err != nil || id != "41" {
		t.Fatalf("bob (granted the deleted repository): repository %q, err %v; want 41", id, err)
	}
	if _, err := repoOf("user-carol"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("carol (no grant): err = %v, want NotFound", err)
	}
}

func TestRecordsReadComputesTheVerdict(t *testing.T) {
	svc, env := newService(t)
	seed(t, env, "42", 2)
	grant(t, env, alice, now.Add(-time.Minute), "42")
	ctx := asUser(alice)
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
	listed, err := svc.ListPullRequestRecords(ctx, connect.NewRequest(&pb.ListPullRequestRecordsRequest{Repository: "acme/widgets", Policy: policy}))
	if err != nil || len(listed.Msg.GetRecords()) != 1 || listed.Msg.GetRecords()[0].GetVerdict().GetKind() != core.PRReviewChanges {
		t.Fatalf("listed = %v, %v", listed, err)
	}
}
