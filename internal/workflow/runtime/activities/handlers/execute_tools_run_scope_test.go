// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// A tool call that spends a provider (an image or a video generation)
// resolves in the scope of its chat's run, read from the chat row as
// call_llm's own calls are (llmCallSpec.scope): the chat's org, and whether a
// person is behind the run (org-provider-accounts.md §4.1, §11.1).
func TestExecuteToolsRunScopeIsTheChatsRun(t *testing.T) {
	a := NewExecuteToolsActivity(nil, nil)
	ctx := context.Background()
	scopeOf := func(chat *db.Chat) drivers.Scope { s, _ := a.runScope(ctx, chat)(); return s }

	assert.Equal(t, drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true},
		scopeOf(chatOf("c", string(core.TriggerEventKindChatStart), nil)),
		"a person's chat in an org: its org, attended")

	assert.Equal(t, drivers.Scope{UserID: "u1", OrgID: "o1"},
		scopeOf(chatOf("c", string(core.TriggerEventKindSchedule), strPtr("t1"))),
		"a trigger's run whose attendance cannot be read: org-funded sources only, never the person's own")

	assert.Equal(t, drivers.PersonalScope("u1"),
		scopeOf(&db.Chat{ID: "c", UserID: "u1"}),
		"a chat that predates orgs: the person's own accounts")

	_, funding := a.runScope(ctx, chatOf("c", string(core.TriggerEventKindChatStart), nil))()
	assert.Equal(t, drivers.RunFunding{OrgOnly: true}, funding, "with no store to read consent from, the person's own accounts are never spent")
}

// fundingStore answers the person's per-org setting and the chat's order.
type fundingStore struct {
	setting  string
	order    string
	orderErr error
}

func (f fundingStore) GetSettingValue(_ context.Context, _, key string) (string, bool) {
	return f.setting, key == orgfunding.SettingKey && f.setting != ""
}

func (f fundingStore) ChatFundingOrder(context.Context, string) (string, error) {
	return f.order, f.orderErr
}

// Who may pay for a tool call's generation, read as the run's model calls
// read it (fundingPlanFor): the person's per-org setting, outranked by the
// chat's own order, tightened (never loosened) by the run's.
func TestRunFunding(t *testing.T) {
	chat := chatOf("c", string(core.TriggerEventKindChatStart), nil)
	attended := attendance{attended: true}
	setting := func(order, limit string) string {
		return `{"o1":{"order":"` + order + `","on_org_limit":"` + limit + `"}}`
	}
	for _, tc := range []struct {
		name     string
		store    fundingStore
		who      attendance
		runOrder string
		want     drivers.RunFunding
	}{
		{"the defaults: the org's first, ask", fundingStore{}, attended, "", drivers.RunFunding{}},
		{"use_personal when the org's cannot", fundingStore{setting: setting("org_first", "use_personal")}, attended, "", drivers.RunFunding{PersonalWhenOrgCannot: true}},
		{"their own first", fundingStore{setting: setting("personal_first", "ask")}, attended, "", drivers.RunFunding{PersonalFirst: true}},
		{"their org_only", fundingStore{setting: setting("org_only", "use_personal")}, attended, "", drivers.RunFunding{OrgOnly: true}},
		{"the chat's order outranks the setting", fundingStore{setting: setting("personal_first", "ask"), order: "org_only"}, attended, "", drivers.RunFunding{OrgOnly: true}},
		{"an unreadable chat order fails closed", fundingStore{setting: setting("personal_first", "ask"), orderErr: errors.New("db down")}, attended, "", drivers.RunFunding{OrgOnly: true}},
		{"the run's org_only tightens", fundingStore{setting: setting("personal_first", "use_personal")}, attended, "org_only", drivers.RunFunding{OrgOnly: true}},
		{"the run's org_first undoes personal_first", fundingStore{setting: setting("personal_first", "ask")}, attended, "org_first", drivers.RunFunding{}},
		{"the run's personal_first is no consent", fundingStore{}, attended, "personal_first", drivers.RunFunding{}},
		{"an automation stamped any: its stamp is the consent", fundingStore{}, attendance{anySource: true}, "", drivers.RunFunding{PersonalWhenOrgCannot: true}},
		{"another org's setting is not this org's", fundingStore{setting: `{"o2":{"order":"personal_first"}}`}, attended, "", drivers.RunFunding{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, runFunding(context.Background(), tc.store, chat, tc.who, tc.runOrder))
		})
	}
	assert.Equal(t, drivers.RunFunding{OrgOnly: true}, runFunding(context.Background(), nil, chat, attended, ""), "no store: fail closed")
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
