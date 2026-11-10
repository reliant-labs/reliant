package run_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	run "github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// orgSource: test-user is a live member of acme and home; home is the default.
type orgSource struct{}

func (orgSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

// One session's or one parent's runs are its chat's org's: listing them with
// an org verifies the org, and a chat outside it lists nothing.
func TestListRuns_SessionAndParentHonourTheOrg(t *testing.T) {
	acme := "acme"
	chat := &db.Chat{ID: "c-1", UserID: "test-user", OrgID: &acme}
	runs := []*db.Workflow{{ID: "w-1", ChatID: "c-1"}}
	repo := &run.MockRepository{
		GetChatRowFunc: func(context.Context, string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: "c-1", UserID: "test-user", OrgID: &acme}, nil
		},
		GetChatFunc:            func(context.Context, string) (*db.Chat, error) { return chat, nil },
		GetWorkflowFunc:        func(context.Context, string) (*db.Workflow, error) { return runs[0], nil },
		ListRootWorkflowsFunc:  func(context.Context, string) ([]*db.Workflow, error) { return runs, nil },
		ListChildWorkflowsFunc: func(context.Context, string) ([]*db.Workflow, error) { return runs, nil },
	}
	resolver := orgs.New(orgSource{})
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Orgs: resolver}))
	// A request through a listener carries the membership authz decides with.
	ctx := authz.WithMembership(run.AuthedContext(t), resolver)
	str := func(s string) *string { return &s }

	for name, req := range map[string]func(org *string) *pb.ListRunsRequest{
		"session": func(org *string) *pb.ListRunsRequest { return &pb.ListRunsRequest{SessionId: str("c-1"), OrgId: org} },
		"parent":  func(org *string) *pb.ListRunsRequest { return &pb.ListRunsRequest{ParentId: str("w-1"), OrgId: org} },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.ListRuns(ctx, connect.NewRequest(req(str("acme"))))
			if err != nil || len(got.Msg.GetRuns()) != 1 {
				t.Fatalf("its own org: %v runs, %v; want the run", len(got.Msg.GetRuns()), err)
			}
			got, err = svc.ListRuns(ctx, connect.NewRequest(req(str("home"))))
			if err != nil || len(got.Msg.GetRuns()) != 0 {
				t.Fatalf("another org: %d runs, %v; want none", len(got.Msg.GetRuns()), err)
			}
			if _, err := svc.ListRuns(ctx, connect.NewRequest(req(str("globex")))); svcerr.Code(err) != connect.CodePermissionDenied {
				t.Fatalf("an org the caller is not in: %v, want PermissionDenied", err)
			}
			got, err = svc.ListRuns(ctx, connect.NewRequest(req(nil)))
			if err != nil || len(got.Msg.GetRuns()) != 1 {
				t.Fatalf("no org named: %v, %v; want the run, as before orgs", got, err)
			}
		})
	}
}

// The run log shows what the single reads would: a run (its chat) or a firing
// (its trigger) of an org the caller has left is not listed, even when the
// request names no org, while their live org's rows and rows with no org are.
// A page whose rows are all denied still continues the log.
func TestListRuns_LeavesOutARemovedMembersOrgRows(t *testing.T) {
	acme, globex := "acme", "globex"
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	repo := &run.MockRepository{
		ListRunsFunc: func(_ context.Context, f db.RunListFilters) ([]*db.RunListItem, bool, error) {
			if f.After != nil {
				return []*db.RunListItem{{ChatID: "c-acme-older", CreatedAt: at.Add(-time.Hour), OrgID: &acme}}, false, nil
			}
			return []*db.RunListItem{
				{ChatID: "c-acme", CreatedAt: at.Add(-1 * time.Minute), OrgID: &acme},
				{ChatID: "c-globex", CreatedAt: at.Add(-2 * time.Minute), OrgID: &globex},
				{ChatID: "c-legacy", CreatedAt: at.Add(-3 * time.Minute)},
			}, false, nil
		},
		ListUnlaunchedTriggerEventsFunc: func(_ context.Context, f db.RunListFilters) ([]*db.UnlaunchedTriggerEvent, bool, error) {
			if f.After != nil {
				return nil, false, nil
			}
			return []*db.UnlaunchedTriggerEvent{
				{ID: "e-globex", OccurredAt: at, OrgID: &globex},
				{ID: "e-acme", OccurredAt: at.Add(-90 * time.Second), OrgID: &acme},
			}, false, nil
		},
	}
	resolver := orgs.New(orgSource{}) // test-user is in acme and home, not globex
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Orgs: resolver}))
	ctx := authz.WithMembership(run.AuthedContext(t), resolver)

	got, err := svc.ListRuns(ctx, connect.NewRequest(&pb.ListRunsRequest{IncludeUnlaunchedEvents: ptrBool(true)}))
	require.NoError(t, err)
	var runs, events []string
	for _, r := range got.Msg.GetRuns() {
		runs = append(runs, r.GetSessionId())
	}
	for _, e := range got.Msg.GetUnlaunchedEvents() {
		events = append(events, e.GetId())
	}
	require.Equal(t, []string{"c-acme", "c-legacy"}, runs)
	require.Equal(t, []string{"e-acme"}, events)

	// Limit 1: the newest row is a denied firing. The page is empty, but its
	// cursor continues the log rather than ending it.
	got, err = svc.ListRuns(ctx, connect.NewRequest(&pb.ListRunsRequest{IncludeUnlaunchedEvents: ptrBool(true), Limit: 1}))
	require.NoError(t, err)
	require.Empty(t, got.Msg.GetRuns())
	require.Empty(t, got.Msg.GetUnlaunchedEvents())
	require.NotEmpty(t, got.Msg.GetNextPageToken(), "a page of denied rows is not the end of the log")

	// A credential bound to globex, whose acting user is not (or no longer) a
	// member of it, lists nothing of it.
	machine := auth.WithMachineToken(ctx, &fat.Principal{OrgID: globex, ActingUserID: "test-user"})
	_, err = svc.ListRuns(machine, connect.NewRequest(&pb.ListRunsRequest{}))
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("a credential of an org its user left: %v, want PermissionDenied", err)
	}
}

func ptrBool(b bool) *bool { return &b }
