package task_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db/core"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

// The task handlers authorize a task through its plan, the plan's thread and
// that thread's chat. The fixture holds one task the caller ("test-user",
// AuthedContext's default principal) owns and one another user owns; any
// other ID is missing, answered with the not-found the store reports.
const (
	ownChatID   = "chat-own"
	ownPlanID   = "plan-own"
	ownTaskID   = "task-own"
	otherPlanID = "plan-other"
	otherTaskID = "task-other"
	missingID   = "missing"
)

// taskMocks is the mock of every dependency the task handlers take.
type taskMocks struct {
	Tasks   *task.MockTaskStore
	Plans   *task.MockPlanStore
	Threads *task.MockThreadStore
	Chats   *task.MockChatStore
	Refetch *task.MockChatRefetcher
	Tx      *task.MockTxRunner
}

// Deps is the Deps the mocks stand in for.
func (m *taskMocks) Deps() task.Deps {
	return task.Deps{Tasks: m.Tasks, Plans: m.Plans, Threads: m.Threads, Chats: m.Chats, Refetch: m.Refetch, Tx: m.Tx}
}

// newTaskMocks answers the fixture's chat, thread, plan and task lookups,
// runs a transaction's body in place and accepts refetch notifications.
// Tests program the writes they reach.
func newTaskMocks() *taskMocks {
	chats := map[string]*core.ChatRow{
		ownChatID:    {ID: ownChatID, UserID: "test-user"},
		"chat-other": {ID: "chat-other", UserID: "someone-else"},
	}
	threads := map[string]*core.Thread{
		"thread-own":   {ID: "thread-own", ChatID: ownChatID},
		"thread-other": {ID: "thread-other", ChatID: "chat-other"},
	}
	plans := map[string]*core.Plan{
		ownPlanID:   {ID: ownPlanID, ThreadID: "thread-own"},
		otherPlanID: {ID: otherPlanID, ThreadID: "thread-other"},
	}
	tasks := map[string]*core.Task{
		ownTaskID:   {ID: ownTaskID, PlanID: ownPlanID, Title: "Own task", Position: 2},
		otherTaskID: {ID: otherTaskID, PlanID: otherPlanID, Title: "Other task"},
	}
	return &taskMocks{
		Tasks: &task.MockTaskStore{
			GetTaskFunc: func(_ context.Context, id string) (*core.Task, error) {
				if tk, ok := tasks[id]; ok {
					cp := *tk
					return &cp, nil
				}
				return nil, fmt.Errorf("%w: %s", svcerr.NotFound("task"), id)
			},
		},
		Plans: &task.MockPlanStore{
			GetPlanFunc: func(_ context.Context, id string) (*core.Plan, error) {
				if p, ok := plans[id]; ok {
					return p, nil
				}
				return nil, fmt.Errorf("%w: %s", svcerr.NotFound("plan"), id)
			},
		},
		Threads: &task.MockThreadStore{
			GetThreadFunc: func(_ context.Context, id string) (*core.Thread, error) {
				if th, ok := threads[id]; ok {
					return th, nil
				}
				return nil, fmt.Errorf("%w: %s", svcerr.NotFound("thread"), id)
			},
		},
		Chats: &task.MockChatStore{
			GetChatRowFunc: func(_ context.Context, id string) (*core.ChatRow, error) {
				if c, ok := chats[id]; ok {
					cp := *c
					return &cp, nil
				}
				return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
			},
		},
		Refetch: &task.MockChatRefetcher{
			EmitChatRefetchFunc: func(context.Context, string, core.RefetchType) error { return nil },
		},
		Tx: &task.MockTxRunner{
			RunTxFunc: func(ctx context.Context, _ orm.TxPolicy, fn func(context.Context) error) error { return fn(ctx) },
		},
	}
}
