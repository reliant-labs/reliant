// Copyright (c) 2025 Reliant Labs
package triggertools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/triggertools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

type manualAPI struct {
	user, id string
	limit    int32
	token    string
	err      error
}

func (a *manualAPI) ListTriggers(context.Context, *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	panic("not used")
}
func (a *manualAPI) FireTrigger(ctx context.Context, req *connect.Request[reliantv1.FireTriggerRequest]) (*connect.Response[reliantv1.FireTriggerResponse], error) {
	a.user, a.id = auth.MustGetUserID(ctx), req.Msg.Id
	return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: "fire-1"}), a.err
}
func (a *manualAPI) ListTriggerEvents(ctx context.Context, req *connect.Request[reliantv1.ListTriggerEventsRequest]) (*connect.Response[reliantv1.ListTriggerEventsResponse], error) {
	a.user, a.id, a.limit, a.token = auth.MustGetUserID(ctx), req.Msg.TriggerId, req.Msg.Limit, req.Msg.GetPageToken()
	chat := "chat-1"
	return connect.NewResponse(&reliantv1.ListTriggerEventsResponse{Events: []*reliantv1.TriggerEvent{{Id: "event-1", FireWorkflowId: "fire-1", ChatId: &chat, OccurredAt: "2026-10-23T00:00:00Z", Outcome: reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED, Payload: &structpb.Struct{Fields: map[string]*structpb.Value{"secret": structpb.NewStringValue("must-not-appear")}}}}, NextPageToken: "next"}), a.err
}
func TestManualAdapterActsAsOwnerAndSanitizesErrors(t *testing.T) {
	a := &manualAPI{}
	adapter := triggertools.New(a)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "wrong-context-user")
	id, err := adapter.FireTrigger(ctx, "owner", "t1")
	require.NoError(t, err)
	assert.Equal(t, "fire-1", id)
	assert.Equal(t, "owner", a.user)
	assert.Equal(t, "t1", a.id)
	a.err = errors.New("private backend diagnostic")
	_, err = adapter.FireTrigger(ctx, "owner", "t1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private backend diagnostic")
	a.err = svcerr.NotFound("trigger")
	_, err = adapter.FireTrigger(ctx, "owner", "other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trigger")
	_, err = adapter.ListTriggerEvents(ctx, "owner", "other", 10, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trigger")
}
func TestEventAdapterBoundsPagesAndOmitsPayload(t *testing.T) {
	a := &manualAPI{}
	adapter := triggertools.New(a)
	for _, tc := range []struct {
		input int
		want  int32
	}{{0, 50}, {-1, 50}, {2, 2}, {501, 500}} {
		page, err := adapter.ListTriggerEvents(context.Background(), "owner", "t1", tc.input, "cursor")
		require.NoError(t, err)
		assert.Equal(t, "owner", a.user)
		assert.Equal(t, "t1", a.id)
		assert.Equal(t, tc.want, a.limit)
		assert.Equal(t, "cursor", a.token)
		require.Len(t, page.Events, 1)
		assert.Equal(t, "chat-1", page.Events[0].ChatID)
		assert.Equal(t, "fire-1", page.Events[0].FireWorkflowID)
		assert.Equal(t, "LAUNCHED", page.Events[0].Outcome)
		assert.Equal(t, "next", page.NextPageToken)
		raw, err := json.Marshal(page)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "must-not-appear")
		assert.NotContains(t, string(raw), "payload")
	}
}
func TestManualAdaptersUnavailable(t *testing.T) {
	a := triggertools.New(nil)
	_, err := a.FireTrigger(context.Background(), "owner", "t1")
	require.Error(t, err)
	_, err = a.ListTriggerEvents(context.Background(), "owner", "t1", 1, "")
	require.Error(t, err)
}
func TestManualToolUsesServiceOwnershipWithoutEnabling(t *testing.T) {
	e := setup(t, "https://api.example.com")
	id := e.activate(t, "nightly")
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, e.userID)
	_, err := e.svc.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
	require.NoError(t, err)
	tool := tools.NewFireTriggerTool(e.repo, triggertools.New(e.svc))
	before, err := e.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	resp := e.call(t, tool, tools.FireTriggerParams{TriggerID: id})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "fire_workflow_id: fire")
	after, err := e.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, before, after, "manual firing cannot change activation configuration")
	// Change the trigger's owner, not the chat: no caller-controlled owner exists.
	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, "other-owner")
	adapter := triggertools.New(e.svc)
	_, missing := adapter.FireTrigger(otherCtx, "other-owner", "missing")
	_, foreign := adapter.FireTrigger(otherCtx, "other-owner", id)
	require.Error(t, missing)
	require.Error(t, foreign)
	assert.Equal(t, missing.Error(), foreign.Error())
	_, missing = adapter.ListTriggerEvents(otherCtx, "other-owner", "missing", 1, "")
	_, foreign = adapter.ListTriggerEvents(otherCtx, "other-owner", id, 1, "")
	require.Error(t, missing)
	require.Error(t, foreign)
	assert.Equal(t, missing.Error(), foreign.Error())
}
