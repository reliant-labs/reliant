package plan_test

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/reliant/internal/db"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

// The plan handlers authorize a plan through its thread and that thread's
// chat. The fixture holds one plan the caller ("test-user", AuthedContext's
// default principal) owns and one another user owns; any other ID is
// missing.
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
func newPlanRepo() *plan.StubRepo {
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
	return &plan.StubRepo{
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
				cp := *p
				return &cp, nil
			}
			return nil, sql.ErrNoRows
		},
		EmitChatRefetchFunc: func(context.Context, string, db.RefetchType) error { return nil },
	}
}
