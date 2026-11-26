package task

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// Tenant isolation for TaskService, on a real database. A task is reached
// through its plan, the plan's thread and that thread's chat: the owner may do
// everything; a viewer of the shared chat may only list a plan's tasks
// (ListTasks, ReadChatAccess); another tenant gets nothing. Codes and row
// effects only, never message text. Every refusal of a task the caller may not
// write is NotFound: a task's existence in another tenant, or in a chat only
// shared with them, is never revealed.
//
// TODO(forge#2461): the scoping is hand-written in handlers_crud.go (a join
// through the chat); switch to a forge:owner declaration once forge can follow it.
func TestTaskTenantIsolation(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestTask(t, WithTaskDeps(RealDeps(repo)))

	// A task of its own per case, in the fixture's shared plan, so a write
	// case never changes what a later case reads.
	seed := func(t *testing.T, f *viewertest.Fixture) string {
		t.Helper()
		now := time.Now().UTC().Truncate(time.Second)
		id := "task-" + uuid.NewString()[:8]
		require.NoError(t, f.Repo.CreateTask(context.Background(), &db.Task{
			ID: id, PlanID: f.PlanID, Title: "Iso task", CreatedAt: now, UpdatedAt: now,
		}))
		return id
	}
	exists := func(t *testing.T, f *viewertest.Fixture, id string) bool {
		t.Helper()
		_, err := f.Repo.GetTask(context.Background(), id)
		return err == nil
	}
	title := func(t *testing.T, f *viewertest.Fixture, id string) string {
		t.Helper()
		task, err := f.Repo.GetTask(context.Background(), id)
		require.NoError(t, err)
		return task.Title
	}

	var cases []viewertest.IsolationCase
	add := func(name, rpc string, as viewertest.Who, want []connect.Code, build func(f *viewertest.Fixture, id string) proto.Message, after func(t *testing.T, f *viewertest.Fixture, id string, err error)) {
		var id string
		c := viewertest.IsolationCase{
			Name: rpc + "/" + name, Rpc: rpc, As: as, Want: want,
			Request: func(t *testing.T, f *viewertest.Fixture) proto.Message {
				id = seed(t, f)
				return build(f, id)
			},
		}
		if after != nil {
			c.After = func(t *testing.T, f *viewertest.Fixture, err error) { after(t, f, id, err) }
		}
		cases = append(cases, c)
	}
	ok := viewertest.Code(viewertest.OK)
	viewerRefused := viewertest.Code(connect.CodeNotFound)
	tenantRefused := viewertest.Code(connect.CodeNotFound)
	getTask := func(_ *viewertest.Fixture, id string) proto.Message { return &pb.GetTaskRequest{Id: id} }
	updateTask := func(_ *viewertest.Fixture, id string) proto.Message {
		return &pb.UpdateTaskRequest{Task: &pb.Task{Id: id, Title: "renamed"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}}
	}
	deleteTask := func(_ *viewertest.Fixture, id string) proto.Message { return &pb.DeleteTaskRequest{Id: id} }
	unchanged := func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.Error(t, err)
		require.True(t, exists(t, f, id), "a refused call must leave the task")
		require.Equal(t, "Iso task", title(t, f, id), "a refused call must not change the task")
	}

	add("owner", "GetTask", viewertest.AsOwner, ok, getTask, nil)
	add("viewer", "GetTask", viewertest.AsViewer, viewerRefused, getTask, nil)
	add("other tenant", "GetTask", viewertest.AsOtherTenant, tenantRefused, getTask, nil)
	add("owner", "UpdateTask", viewertest.AsOwner, ok, updateTask, func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.NoError(t, err)
		require.Equal(t, "renamed", title(t, f, id))
	})
	add("viewer", "UpdateTask", viewertest.AsViewer, viewerRefused, updateTask, unchanged)
	add("other tenant", "UpdateTask", viewertest.AsOtherTenant, tenantRefused, updateTask, unchanged)
	add("owner", "DeleteTask", viewertest.AsOwner, ok, deleteTask, func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.NoError(t, err)
		require.False(t, exists(t, f, id), "the owner's delete removes the task")
	})
	add("viewer", "DeleteTask", viewertest.AsViewer, viewerRefused, deleteTask, unchanged)
	add("other tenant", "DeleteTask", viewertest.AsOtherTenant, tenantRefused, deleteTask, unchanged)

	// Absent rows and invalid requests answer the same for the owner and for
	// another tenant: no row is invented, an empty id is the caller's mistake.
	for _, rpc := range []struct {
		name  string
		empty func() proto.Message
		gone  func() proto.Message
	}{
		{"GetTask", func() proto.Message { return &pb.GetTaskRequest{} }, func() proto.Message { return &pb.GetTaskRequest{Id: "absent"} }},
		{"UpdateTask", func() proto.Message { return &pb.UpdateTaskRequest{} }, func() proto.Message {
			return &pb.UpdateTaskRequest{Task: &pb.Task{Id: "absent", Title: "x"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}}
		}},
		{"DeleteTask", func() proto.Message { return &pb.DeleteTaskRequest{} }, func() proto.Message { return &pb.DeleteTaskRequest{Id: "absent"} }},
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

	// By plan: the viewer of a shared chat lists the plan's tasks and cannot
	// add one; another tenant does neither.
	listReq := func(_ *testing.T, f *viewertest.Fixture) proto.Message {
		return &pb.ListTasksRequest{PlanId: proto.String(f.PlanID)}
	}
	createReq := func(_ *testing.T, f *viewertest.Fixture) proto.Message {
		return &pb.CreateTaskRequest{PlanId: f.PlanID, Title: "Created in test"}
	}
	cases = append(cases,
		viewertest.IsolationCase{Name: "ListTasks/owner", Rpc: "ListTasks", As: viewertest.AsOwner, Want: ok, Request: listReq},
		viewertest.IsolationCase{Name: "ListTasks/viewer", Rpc: "ListTasks", As: viewertest.AsViewer, Want: ok, Request: listReq},
		viewertest.IsolationCase{Name: "ListTasks/other tenant", Rpc: "ListTasks", As: viewertest.AsOtherTenant, Want: tenantRefused, Request: listReq},
		viewertest.IsolationCase{Name: "ListTasks/absent plan", Rpc: "ListTasks", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeNotFound),
			Request: func(*testing.T, *viewertest.Fixture) proto.Message {
				return &pb.ListTasksRequest{PlanId: proto.String("absent")}
			}},
		viewertest.IsolationCase{Name: "ListTasks/invalid", Rpc: "ListTasks", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeInvalidArgument),
			Request: func(*testing.T, *viewertest.Fixture) proto.Message { return &pb.ListTasksRequest{} }},
		viewertest.IsolationCase{Name: "CreateTask/owner", Rpc: "CreateTask", As: viewertest.AsOwner, Want: ok, Request: createReq},
		viewertest.IsolationCase{Name: "CreateTask/viewer", Rpc: "CreateTask", As: viewertest.AsViewer, Want: viewerRefused, Request: createReq},
		viewertest.IsolationCase{Name: "CreateTask/other tenant", Rpc: "CreateTask", As: viewertest.AsOtherTenant, Want: tenantRefused, Request: createReq},
		viewertest.IsolationCase{Name: "CreateTask/absent plan", Rpc: "CreateTask", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeNotFound),
			Request: func(*testing.T, *viewertest.Fixture) proto.Message {
				return &pb.CreateTaskRequest{PlanId: "absent", Title: "x"}
			}},
		viewertest.IsolationCase{Name: "CreateTask/invalid", Rpc: "CreateTask", As: viewertest.AsOwner, Want: viewertest.Code(connect.CodeInvalidArgument),
			Request: func(_ *testing.T, f *viewertest.Fixture) proto.Message {
				return &pb.CreateTaskRequest{PlanId: f.PlanID}
			}},
	)

	viewertest.RunIsolation(t, f, svc, cases)
}
