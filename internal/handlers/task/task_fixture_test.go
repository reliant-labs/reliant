package task_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
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

// newTaskRepo returns a StubRepo answering the fixture's chat, thread, plan
// and task lookups and accepting refetch notifications. Rows program the
// writes they reach.
func newTaskRepo() *task.MockRepository {
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
	return &task.MockRepository{
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			if c, ok := chats[id]; ok {
				return &db.ChatRow{ID: c.ID, UserID: c.UserID, ProjectID: c.ProjectID, WorkflowID: c.WorkflowID}, nil
			}
			return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
		},
		GetThreadFunc: func(_ context.Context, id string) (*db.Thread, error) {
			if th, ok := threads[id]; ok {
				return th, nil
			}
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("thread"), id)
		},
		GetPlanFunc: func(_ context.Context, id string) (*db.Plan, error) {
			if p, ok := plans[id]; ok {
				return p, nil
			}
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("plan"), id)
		},
		GetTaskFunc: func(_ context.Context, id string) (*db.Task, error) {
			if tk, ok := tasks[id]; ok {
				cp := *tk
				return &cp, nil
			}
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("task"), id)
		},
		EmitChatRefetchFunc: func(context.Context, string, db.RefetchType) error { return nil },
	}
}
