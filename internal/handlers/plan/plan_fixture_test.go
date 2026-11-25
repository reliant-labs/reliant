package plan_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
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

// planMocks is the mock of every dependency the plan handlers take.
type planMocks struct {
	Plans   *plan.MockPlanStore
	Tasks   *plan.MockTaskStore
	Threads *plan.MockThreadStore
	Chats   *plan.MockChatStore
	Refetch *plan.MockChatRefetcher
	DB      orm.Context
}

// Deps is the Deps the mocks stand in for.
func (m *planMocks) Deps() plan.Deps {
	return plan.Deps{Plans: m.Plans, Tasks: m.Tasks, Threads: m.Threads, Chats: m.Chats, Refetch: m.Refetch, DB: m.DB}
}

// newPlanMocks answers the fixture's chat, thread and plan lookups, runs a
// transaction's body in place and accepts refetch notifications. Tests
// program the writes they reach.
func newPlanMocks(t *testing.T) *planMocks {
	chats := map[string]*core.ChatRow{
		ownChatID:   {ID: ownChatID, UserID: "test-user"},
		otherChatID: {ID: otherChatID, UserID: "someone-else"},
	}
	threads := map[string]*core.Thread{
		ownThreadID:   {ID: ownThreadID, ChatID: ownChatID},
		otherThreadID: {ID: otherThreadID, ChatID: otherChatID},
	}
	plans := map[string]*core.Plan{
		ownPlanID:   {ID: ownPlanID, ThreadID: ownThreadID, Title: "Own plan"},
		otherPlanID: {ID: otherPlanID, ThreadID: otherThreadID, Title: "Other plan"},
	}
	return &planMocks{
		Plans: &plan.MockPlanStore{
			GetPlanFunc: func(_ context.Context, id string) (*core.Plan, error) {
				if p, ok := plans[id]; ok {
					cp := *p
					return &cp, nil
				}
				return nil, fmt.Errorf("%w: %s", svcerr.NotFound("plan"), id)
			},
		},
		Tasks: &plan.MockTaskStore{},
		Threads: &plan.MockThreadStore{
			GetThreadFunc: func(_ context.Context, id string) (*core.Thread, error) {
				if th, ok := threads[id]; ok {
					return th, nil
				}
				return nil, fmt.Errorf("%w: %s", svcerr.NotFound("thread"), id)
			},
		},
		Chats: &plan.MockChatStore{
			GetChatRowFunc: func(_ context.Context, id string) (*core.ChatRow, error) {
				if c, ok := chats[id]; ok {
					cp := *c
					return &cp, nil
				}
				return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
			},
		},
		Refetch: &plan.MockChatRefetcher{
			EmitChatRefetchFunc: func(context.Context, string, core.RefetchType) error { return nil },
		},
		// The CRUD rpcs run on a real database; the rpcs these mocks serve never touch it.
		DB: plan.NewMigratedTestDB(t),
	}
}
