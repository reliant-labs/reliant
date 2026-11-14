// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// A tool call that spends a provider (an image or a video generation)
// resolves in the scope of its chat's run, read from the chat row as
// call_llm's own calls are (llmCallSpec.scope): the chat's org, and whether a
// person is behind the run (org-provider-accounts.md §4.1, §11.1).
func TestExecuteToolsRunScopeIsTheChatsRun(t *testing.T) {
	a := NewExecuteToolsActivity(nil, nil)
	ctx := context.Background()

	assert.Equal(t, drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true},
		a.runScope(ctx, chatOf("c", string(core.TriggerEventKindChatStart), nil))(),
		"a person's chat in an org: its org, attended")

	assert.Equal(t, drivers.Scope{UserID: "u1", OrgID: "o1"},
		a.runScope(ctx, chatOf("c", string(core.TriggerEventKindSchedule), strPtr("t1")))(),
		"a trigger's run whose attendance cannot be read: org-funded sources only, never the person's own")

	assert.Equal(t, drivers.PersonalScope("u1"),
		a.runScope(ctx, &db.Chat{ID: "c", UserID: "u1"})(),
		"a chat that predates orgs: the person's own accounts")
}

// The scope a tool call resolves in is bound to the person its request
// executes as: an org chat's scope never reaches a call made as another
// person, whoever the tool then asks for (drivers.runScopeFor).
func TestExecuteToolsRunScopeIsBoundToTheActingUser(t *testing.T) {
	tec := &toolExecutionContext{
		chat:     chatOf("c", string(core.TriggerEventKindChatStart), nil),
		project:  &db.Project{ID: "p", UserID: "u1"},
		worktree: &rctx.WorktreeInfo{},
	}
	assert.Equal(t, tec.buildToolRequest().UserID, tec.actingUserID(), "the request and the run scope name the same person")
}
