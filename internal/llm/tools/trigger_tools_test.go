// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type triggerCallerStore struct {
	chat *db.Chat
	err  error
}

func (s triggerCallerStore) GetChat(context.Context, string) (*db.Chat, error) { return s.chat, s.err }

type triggerToolBackend struct {
	user, id string
	limit    int
	token    string
	err      error
}

func (b *triggerToolBackend) FireTrigger(_ context.Context, user, id string) (string, error) {
	b.user, b.id = user, id
	return "fire-1", b.err
}
func (b *triggerToolBackend) ListTriggerEvents(_ context.Context, user, id string, limit int, token string) (TriggerEventPage, error) {
	b.user, b.id, b.limit, b.token = user, id, limit, token
	return TriggerEventPage{Events: []TriggerEventSummary{{ID: "event-1", ChatID: "run-1", Outcome: "LAUNCHED", RunState: "RUNNING"}}, NextPageToken: "next"}, b.err
}
func TestFireTriggerTool(t *testing.T) {
	ctx := rctx.NewToolContext(context.Background(), "chat", "chat", nil, nil)
	repo := triggerCallerStore{chat: &db.Chat{UserID: "chat-owner"}}
	b := &triggerToolBackend{}
	tool := NewFireTriggerTool(repo, b)
	resp, err := tool.Run(ctx, ToolCall{ID: "injected", Name: tool.Name(), Input: `{"trigger_id":"t1","user_id":"attacker"}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "owner injection must be rejected by the schema")
	assert.Empty(t, b.id)
	resp, err = tool.Run(ctx, ToolCall{ID: "call", Name: tool.Name(), Input: `{"trigger_id":" t1 "}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, "chat-owner", b.user)
	assert.Equal(t, "t1", b.id)
	var meta FireTriggerResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	assert.Equal(t, "fire-1", meta.FireWorkflowID)
	assert.Contains(t, resp.Content, "list_trigger_events")
	assert.Contains(t, resp.Content, "asynchronous")
	b.err = errors.New("trigger not found")
	resp, err = tool.Run(ctx, ToolCall{Input: `{"trigger_id":"t1"}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "trigger not found")
}
func TestTriggerToolsFailClosed(t *testing.T) {
	ctx := rctx.NewToolContext(context.Background(), "chat", "chat", nil, nil)
	repo := triggerCallerStore{chat: &db.Chat{UserID: "owner"}}
	b := &triggerToolBackend{}
	for _, tool := range []Tool{NewFireTriggerTool(nil, b), NewFireTriggerTool(triggerCallerStore{}, b), NewFireTriggerTool(repo, nil), NewListTriggerEventsTool(nil, b), NewListTriggerEventsTool(repo, nil)} {
		resp, err := tool.Run(ctx, ToolCall{Input: `{"trigger_id":"t1"}`})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
	}
	for _, tool := range []Tool{NewFireTriggerTool(repo, b), NewListTriggerEventsTool(repo, b)} {
		resp, err := tool.Run(ctx, ToolCall{Input: `{"trigger_id":" "}`})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
	}
	assert.Empty(t, b.id, "no failed precondition may reach the backend")
}
func TestListTriggerEventsTool(t *testing.T) {
	ctx := rctx.NewToolContext(context.Background(), "chat", "chat", nil, nil)
	b := &triggerToolBackend{}
	tool := NewListTriggerEventsTool(triggerCallerStore{chat: &db.Chat{UserID: "owner"}}, b)
	resp, err := tool.Run(ctx, ToolCall{Input: `{"trigger_id":"t1","limit":2,"page_token":"cursor"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, "owner", b.user)
	assert.Equal(t, 2, b.limit)
	assert.Equal(t, "cursor", b.token)
	assert.Contains(t, resp.Content, "chat_id run-1")
	assert.Contains(t, resp.Content, "next_page_token: next")
	b.err = errors.New("not found")
	resp, err = tool.Run(ctx, ToolCall{Input: `{"trigger_id":"t1"}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
}
func TestTriggerToolRegistrationAndPermissions(t *testing.T) {
	assert.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolFireTrigger))
	assert.False(t, PermissionAtLeast(PermissionMutating, MinimumPermissionForTool(ToolFireTrigger)))
	assert.Equal(t, PermissionMutating, MinimumPermissionForTool(ToolListTriggerEvents))
	assert.NotEmpty(t, UnattendedWithholding(ToolFireTrigger))
	assert.Empty(t, UnattendedWithholding(ToolListTriggerEvents))
	found := map[string]bool{}
	for _, def := range GetToolRegistry() {
		if def.Name == ToolFireTrigger || def.Name == ToolListTriggerEvents {
			found[def.Name] = true
			assert.Equal(t, PlacementServer, def.Placement)
		}
	}
	assert.Len(t, found, 2)
	base := ResolveCapabilities(CapabilityInputs{Access: ResolveToolAccess(nil, []string{"*"}, nil), Permission: PermissionMutating})
	assert.False(t, base.CanLoad(ToolFireTrigger))
	assert.True(t, base.CanLoad(ToolListTriggerEvents))
	orchestrator := ResolveCapabilities(CapabilityInputs{Access: ResolveToolAccess(nil, []string{"*"}, nil), Permission: PermissionOrchestrator})
	assert.True(t, orchestrator.CanLoad(ToolFireTrigger))
	unattended := unattendedCaps(PermissionOrchestrator, []string{"tag:workflow"}, []string{"*"}, true)
	assert.False(t, unattended.CanLoad(ToolFireTrigger))
	optedIn := unattendedCaps(PermissionOrchestrator, []string{ToolFireTrigger}, nil, true)
	assert.True(t, optedIn.Offers(ToolFireTrigger))
}

type brokenTriggerLister struct{ list []TriggerSummary }

func (l brokenTriggerLister) ListTriggers(context.Context, string, TriggerListFilter) ([]TriggerSummary, error) {
	return l.list, nil
}

// An enabled BROKEN activation reads as live unless the listing says its
// firings are refused (prod "Merge steward · queued" after its workflow was
// deleted).
func TestListTriggersSaysABrokenEnabledActivationLaunchesNothing(t *testing.T) {
	ctx := rctx.NewToolContext(context.Background(), "chat", "chat", nil, nil)
	repo := triggerCallerStore{chat: &db.Chat{UserID: "owner"}}
	lister := brokenTriggerLister{list: []TriggerSummary{
		{ID: "t1", Name: "Merge steward · queued", Enabled: true, Health: "BROKEN", HealthDetail: "the workflow does not resolve", NoMachine: true},
		{ID: "t2", Name: "Paused broken", Enabled: false, Health: "BROKEN", HealthDetail: "gone", NoMachine: true},
	}}
	tool := NewListTriggersTool(repo, lister)
	resp, err := tool.Run(ctx, ToolCall{Input: `{}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, 1, strings.Count(resp.Content, "every firing is refused"), resp.Content)
}

// list_triggers without limit or page_token lists every trigger, as it did
// before paging existed: no caller is silently cut short, and a long list
// says it can be paged.
func TestListTriggersWithoutPagingListsEveryTrigger(t *testing.T) {
	ctx := rctx.NewToolContext(context.Background(), "chat", "chat", nil, nil)
	repo := triggerCallerStore{chat: &db.Chat{UserID: "owner"}}
	var list []TriggerSummary
	for i := 0; i < 25; i++ {
		list = append(list, TriggerSummary{ID: fmt.Sprintf("t%02d", i), Name: fmt.Sprintf("trigger %02d", i), Health: "HEALTHY", NoMachine: true})
	}
	tool := NewListTriggersTool(repo, brokenTriggerLister{list: list})
	for _, input := range []string{`{}`, `{"health":"HEALTHY"}`} {
		resp, err := tool.Run(ctx, ToolCall{Input: input})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		var got []TriggerSummary
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &got))
		require.Len(t, got, 25, input)
		assert.Equal(t, "t00", got[0].ID)
		assert.Equal(t, "t24", got[24].ID)
		assert.Equal(t, 25, strings.Count(resp.Content, "\n- trigger "), resp.Content)
		assert.Contains(t, resp.Content, "25 trigger(s)")
		assert.NotContains(t, resp.Content, "showing")
		assert.NotContains(t, resp.Content, "next_page_token")
		assert.Contains(t, resp.Content, "All 25 triggers are listed")
	}
	bad, err := tool.Run(ctx, ToolCall{Input: `{"limit":-1}`})
	require.NoError(t, err)
	assert.True(t, bad.IsError)
}

// list_triggers shows unhealthy triggers first; an explicit limit pages it,
// with a next_page_token that reads the rest; health narrows it.
func TestListTriggersPagesUnhealthyFirstAndFiltersByHealth(t *testing.T) {
	ctx := rctx.NewToolContext(context.Background(), "chat", "chat", nil, nil)
	repo := triggerCallerStore{chat: &db.Chat{UserID: "owner"}}
	var list []TriggerSummary
	for i := 0; i < 25; i++ {
		list = append(list, TriggerSummary{ID: fmt.Sprintf("h%02d", i), Name: fmt.Sprintf("a healthy %02d", i), Health: "HEALTHY", NoMachine: true})
	}
	list = append(list,
		TriggerSummary{ID: "f1", Name: "z failing", Health: "FAILING", NoMachine: true},
		TriggerSummary{ID: "b1", Name: "z broken", Health: "BROKEN", NoMachine: true},
		TriggerSummary{ID: "u1", Name: "m unknown", Health: "UNKNOWN", NoMachine: true},
	)
	tool := NewListTriggersTool(repo, brokenTriggerLister{list: list})
	run := func(input string) ToolResponse {
		t.Helper()
		resp, err := tool.Run(ctx, ToolCall{Input: input})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		return resp
	}
	ids := func(resp ToolResponse) []string {
		t.Helper()
		var page []TriggerSummary
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &page))
		out := make([]string, len(page))
		for i, s := range page {
			out[i] = s.ID
		}
		return out
	}

	first := run(`{"limit":20}`)
	got := ids(first)
	require.Len(t, got, 20)
	assert.Equal(t, []string{"b1", "f1", "u1", "h00"}, got[:4], "unhealthy first, then unknown, then healthy by name")
	assert.Contains(t, first.Content, "28 trigger(s), showing 1-20 (1 BROKEN, 1 FAILING, 1 UNKNOWN, 25 HEALTHY)")
	assert.Contains(t, first.Content, "next_page_token: 20")

	second := run(`{"page_token":"20"}`)
	assert.Equal(t, []string{"h17", "h18", "h19", "h20", "h21", "h22", "h23", "h24"}, ids(second))
	assert.NotContains(t, second.Content, "next_page_token")

	// A smaller limit pages too, and its token continues where it stopped.
	small := run(`{"limit":3}`)
	assert.Equal(t, []string{"b1", "f1", "u1"}, ids(small))
	assert.Contains(t, small.Content, "next_page_token: 3")
	assert.Equal(t, []string{"h00", "h01", "h02"}, ids(run(`{"limit":3,"page_token":"3"}`)))

	unhealthy := run(`{"health":"UNHEALTHY"}`)
	assert.Equal(t, []string{"b1", "f1"}, ids(unhealthy))
	assert.NotContains(t, unhealthy.Content, "next_page_token")
	assert.Equal(t, []string{"u1"}, ids(run(`{"health":"UNKNOWN"}`)))

	bad, err := tool.Run(ctx, ToolCall{Input: `{"page_token":"nope"}`})
	require.NoError(t, err)
	assert.True(t, bad.IsError)
	none := run(`{"health":"DEGRADED"}`)
	assert.Contains(t, none.Content, "None of the 28 trigger(s) is DEGRADED")
}
