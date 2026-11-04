// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
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
