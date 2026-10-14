package task_test

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/reliant/internal/db"
	task "github.com/reliant-labs/reliant/internal/handlers/task"
)

// The task handlers authorize a task through its plan, the plan's thread and
// that thread's chat. The fixture holds one task the caller ("test-user",
// AuthedContext's default principal) owns and one another user owns; any
// other ID is missing.
const (
	ownChatID   = "chat-own"
	ownPlanID   = "plan-own"
	ownTaskID   = "task-own"
	otherPlanID = "plan-other"
	otherTaskID = "task-other"
	missingID   = "missing"
)

// newTaskRepo returns a StubRepo answering the fixture's chat, thread, plan
// and task lookups and accepting refetch notifications. Rows program the
// writes they reach.
func newTaskRepo() *task.StubRepo {
	chats := map[string]*db.Chat{
		ownChatID:    {ID: ownChatID, UserID: "test-user"},
		"chat-other": {ID: "chat-other", UserID: "someone-else"},
	}
	threads := map[string]*db.Thread{
		"thread-own":   {ID: "thread-own", ChatID: ownChatID},
		"thread-other": {ID: "thread-other", ChatID: "chat-other"},
	}
	plans := map[string]*db.Plan{
		ownPlanID:   {ID: ownPlanID, ThreadID: "thread-own"},
		otherPlanID: {ID: otherPlanID, ThreadID: "thread-other"},
	}
	tasks := map[string]*db.Task{
		ownTaskID:   {ID: ownTaskID, PlanID: ownPlanID, Title: "Own task", Position: 2},
		otherTaskID: {ID: otherTaskID, PlanID: otherPlanID, Title: "Other task"},
	}
	return &task.StubRepo{
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			if c, ok := chats[id]; ok {
				return c, nil
			}
			return nil, sql.ErrNoRows
		},
		GetThreadFunc: func(_ context.Context, id string) (*db.Thread, error) {
			if th, ok := threads[id]; ok {
				return th, nil
			}
			return nil, sql.ErrNoRows
		},
		GetPlanFunc: func(_ context.Context, id string) (*db.Plan, error) {
			if p, ok := plans[id]; ok {
				return p, nil
			}
			return nil, sql.ErrNoRows
		},
		GetTaskFunc: func(_ context.Context, id string) (*db.Task, error) {
			if tk, ok := tasks[id]; ok {
				cp := *tk
				return &cp, nil
			}
			return nil, sql.ErrNoRows
		},
		EmitChatRefetchFunc: func(context.Context, string, db.RefetchType) error { return nil },
	}
}
