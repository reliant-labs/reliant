package plan

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// Tenant isolation for PlanService, on a real database. A plan is reached
// through its thread's chat: the owner may do everything; a viewer of the
// shared chat may only read it by chat (GetPlanByChatId); another tenant gets
// nothing. Codes and row effects only, never message text, so the conversion
// to forge CRUD (which changes both) leaves this file alone except where a
// TODO says a code tightens.
//
// TODO facade wave 1c (plan CRUD conversion): another tenant's cases accept
// PermissionDenied today and NotFound after; tighten each RefusedToday to
// Code(NotFound) in that PR (owner decision D-B, breaking changes allowed).
func TestPlanTenantIsolation(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestPlan(t, WithPlanDeps(Deps{Repo: repo}))

	// A plan of its own per case, so a write case never changes what a later
	// case reads.
	seed := func(t *testing.T, f *viewertest.Fixture) string {
		t.Helper()
		now := time.Now().UTC().Truncate(time.Second)
		id := "plan-" + uuid.NewString()[:8]
		require.NoError(t, f.Repo.CreatePlan(context.Background(), &db.Plan{
			ID: id, ThreadID: f.Transcript.ThreadID, Title: "Iso plan", CreatedAt: now, UpdatedAt: now,
		}))
		return id
	}
	exists := func(t *testing.T, f *viewertest.Fixture, id string) bool {
		t.Helper()
		_, err := f.Repo.GetPlan(context.Background(), id)
		return err == nil
	}
	title := func(t *testing.T, f *viewertest.Fixture, id string) string {
		t.Helper()
		p, err := f.Repo.GetPlan(context.Background(), id)
		require.NoError(t, err)
		return p.Title
	}

	// Each seeded id is remembered by the case's closure, for After.
	var cases []viewertest.IsolationCase
	add := func(name, rpc string, as viewertest.Who, want []connect.Code, build func(t *testing.T, f *viewertest.Fixture, id string) proto.Message, after func(t *testing.T, f *viewertest.Fixture, id string, err error)) {
		var id string
		c := viewertest.IsolationCase{
			Name: rpc + "/" + name, Rpc: rpc, As: as, Want: want,
			Request: func(t *testing.T, f *viewertest.Fixture) proto.Message {
				id = seed(t, f)
				return build(t, f, id)
			},
		}
		if as == viewertest.AsOtherTenant {
			c.TODO = "facade wave 1c: another tenant's row answers NotFound"
		}
		if after != nil {
			c.After = func(t *testing.T, f *viewertest.Fixture, err error) { after(t, f, id, err) }
		}
		cases = append(cases, c)
	}
	viewerRefused := viewertest.Code(connect.CodePermissionDenied)
	tenantRefused := viewertest.RefusedToday(connect.CodePermissionDenied)
	getPlan := func(_ *testing.T, _ *viewertest.Fixture, id string) proto.Message {
		return &pb.GetPlanRequest{PlanId: id}
	}
	updatePlan := func(_ *testing.T, _ *viewertest.Fixture, id string) proto.Message {
		return &pb.UpdatePlanRequest{PlanId: id, Title: proto.String("renamed")}
	}
	deletePlan := func(_ *testing.T, _ *viewertest.Fixture, id string) proto.Message {
		return &pb.DeletePlanRequest{PlanId: id}
	}
	listTasks := func(_ *testing.T, _ *viewertest.Fixture, id string) proto.Message {
		return &pb.ListPlanTasksRequest{PlanId: id}
	}
	unchanged := func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.Error(t, err)
		require.True(t, exists(t, f, id), "a refused call must leave the plan")
		require.Equal(t, "Iso plan", title(t, f, id), "a refused call must not change the plan")
	}

	// Reads by plan id: the owner only (they take write access).
	add("owner", "GetPlan", viewertest.AsOwner, viewertest.Code(viewertest.OK), getPlan, nil)
	add("viewer", "GetPlan", viewertest.AsViewer, viewerRefused, getPlan, nil)
	add("other tenant", "GetPlan", viewertest.AsOtherTenant, tenantRefused, getPlan, nil)
	add("owner", "ListPlanTasks", viewertest.AsOwner, viewertest.Code(viewertest.OK), listTasks, nil)
	add("viewer", "ListPlanTasks", viewertest.AsViewer, viewerRefused, listTasks, nil)
	add("other tenant", "ListPlanTasks", viewertest.AsOtherTenant, tenantRefused, listTasks, nil)

	// Writes: refused callers change nothing.
	add("owner", "UpdatePlan", viewertest.AsOwner, viewertest.Code(viewertest.OK), updatePlan, func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.NoError(t, err)
		require.Equal(t, "renamed", title(t, f, id))
	})
	add("viewer", "UpdatePlan", viewertest.AsViewer, viewerRefused, updatePlan, unchanged)
	add("other tenant", "UpdatePlan", viewertest.AsOtherTenant, tenantRefused, updatePlan, unchanged)
	add("owner", "DeletePlan", viewertest.AsOwner, viewertest.Code(viewertest.OK), deletePlan, func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.NoError(t, err)
		require.False(t, exists(t, f, id), "the owner's delete removes the plan")
	})
	add("viewer", "DeletePlan", viewertest.AsViewer, viewerRefused, deletePlan, unchanged)
	add("other tenant", "DeletePlan", viewertest.AsOtherTenant, tenantRefused, deletePlan, unchanged)

	// An absent row and an invalid request answer the same for everyone who
	// could ask: no row is invented, an empty id is the caller's mistake.
	for _, rpc := range []struct {
		name  string
		empty func() proto.Message
		gone  func() proto.Message
	}{
		{"GetPlan", func() proto.Message { return &pb.GetPlanRequest{} }, func() proto.Message { return &pb.GetPlanRequest{PlanId: "absent"} }},
		{"UpdatePlan", func() proto.Message { return &pb.UpdatePlanRequest{} }, func() proto.Message { return &pb.UpdatePlanRequest{PlanId: "absent"} }},
		{"DeletePlan", func() proto.Message { return &pb.DeletePlanRequest{} }, func() proto.Message { return &pb.DeletePlanRequest{PlanId: "absent"} }},
		{"ListPlanTasks", func() proto.Message { return &pb.ListPlanTasksRequest{} }, func() proto.Message { return &pb.ListPlanTasksRequest{PlanId: "absent"} }},
	} {
		rpc := rpc
		for _, who := range []viewertest.Who{viewertest.AsOwner, viewertest.AsOtherTenant} {
			cases = append(cases,
				viewertest.IsolationCase{Name: rpc.name + "/absent/" + string(who), Rpc: rpc.name, As: who, Want: viewertest.Code(connect.CodeNotFound),
					Request: func(*testing.T, *viewertest.Fixture) proto.Message { return rpc.gone() }},
				viewertest.IsolationCase{Name: rpc.name + "/invalid/" + string(who), Rpc: rpc.name, As: who, Want: viewertest.Code(connect.CodeInvalidArgument),
					Request: func(*testing.T, *viewertest.Fixture) proto.Message { return rpc.empty() }},
			)
		}
	}

	// By chat: the viewer of a shared chat reads its plan; another tenant and
	// a viewer-write are refused.
	chatReq := func(_ *testing.T, f *viewertest.Fixture) proto.Message {
		return &pb.GetPlanByChatIdRequest{ChatId: f.ChatID}
	}
	createReq := func(_ *testing.T, f *viewertest.Fixture) proto.Message {
		return &pb.CreatePlanRequest{ChatId: f.ChatID, Title: "Created in test"}
	}
	cases = append(cases,
		viewertest.IsolationCase{Name: "GetPlanByChatId/owner", Rpc: "GetPlanByChatId", As: viewertest.AsOwner, Want: viewertest.Code(viewertest.OK), Request: chatReq},
		viewertest.IsolationCase{Name: "GetPlanByChatId/viewer", Rpc: "GetPlanByChatId", As: viewertest.AsViewer, Want: viewertest.Code(viewertest.OK), Request: chatReq},
		viewertest.IsolationCase{Name: "GetPlanByChatId/other tenant", Rpc: "GetPlanByChatId", As: viewertest.AsOtherTenant, Want: tenantRefused, Request: chatReq,
			TODO: "facade wave 1c: another tenant's chat answers NotFound"},
		viewertest.IsolationCase{Name: "GetPlanByChatId/absent", Rpc: "GetPlanByChatId", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeNotFound),
			Request: func(*testing.T, *viewertest.Fixture) proto.Message {
				return &pb.GetPlanByChatIdRequest{ChatId: "absent"}
			}},
		viewertest.IsolationCase{Name: "GetPlanByChatId/invalid", Rpc: "GetPlanByChatId", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeInvalidArgument),
			Request: func(*testing.T, *viewertest.Fixture) proto.Message { return &pb.GetPlanByChatIdRequest{} }},
		viewertest.IsolationCase{Name: "CreatePlan/owner", Rpc: "CreatePlan", As: viewertest.AsOwner, Want: viewertest.Code(viewertest.OK), Request: createReq},
		viewertest.IsolationCase{Name: "CreatePlan/viewer", Rpc: "CreatePlan", As: viewertest.AsViewer, Want: viewerRefused, Request: createReq},
		viewertest.IsolationCase{Name: "CreatePlan/other tenant", Rpc: "CreatePlan", As: viewertest.AsOtherTenant, Want: tenantRefused, Request: createReq,
			TODO: "facade wave 1c: another tenant's chat answers NotFound"},
		viewertest.IsolationCase{Name: "CreatePlan/absent chat", Rpc: "CreatePlan", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeNotFound),
			Request: func(*testing.T, *viewertest.Fixture) proto.Message {
				return &pb.CreatePlanRequest{ChatId: "absent", Title: "x"}
			}},
		viewertest.IsolationCase{Name: "CreatePlan/invalid", Rpc: "CreatePlan", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeInvalidArgument),
			Request: func(_ *testing.T, f *viewertest.Fixture) proto.Message {
				return &pb.CreatePlanRequest{ChatId: f.ChatID}
			}},
	)

	viewertest.RunIsolation(t, f, svc, cases)
}
