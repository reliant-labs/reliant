package plan_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

// The plan handlers authorize a plan through its thread and that thread's
// chat. The fixture holds one plan the caller ("test-user", AuthedContext's
// default principal) owns and one another user owns; any other ID is
// missing, answered with the not-found the store reports.
const (
	ownChatID     = "chat-own"
	ownThreadID   = "thread-own"
	ownPlanID     = "plan-own"
	otherChatID   = "chat-other"
	otherThreadID = "thread-other"
	otherPlanID   = "plan-other"
	missingID     = "missing"
)

// newPlanRepo returns a StubRepo answering the fixture's chat, thread and
// plan lookups and accepting refetch notifications. Rows program the writes
// they reach.
func newPlanRepo() *plan.MockRepository {
	chats := map[string]*db.Chat{
		ownChatID:   {ID: ownChatID, UserID: "test-user"},
		otherChatID: {ID: otherChatID, UserID: "someone-else"},
	}
	threads := map[string]*db.Thread{
		ownThreadID:   {ID: ownThreadID, ChatID: ownChatID},
		otherThreadID: {ID: otherThreadID, ChatID: otherChatID},
	}
	plans := map[string]*db.Plan{
		ownPlanID:   {ID: ownPlanID, ThreadID: ownThreadID, Title: "Own plan"},
		otherPlanID: {ID: otherPlanID, ThreadID: otherThreadID, Title: "Other plan"},
	}
	return &plan.MockRepository{
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
				cp := *p
				return &cp, nil
			}
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("plan"), id)
		},
		EmitChatRefetchFunc: func(context.Context, string, db.RefetchType) error { return nil },
	}
}
