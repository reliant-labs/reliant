// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// fakeSenders stands in for the GitHub people lookup.
type fakeSenders struct {
	asked     []string // userID, then each handle and id
	found     []ResolvedSender
	err       error
	permanent bool
	invalid   bool
}

func (f *fakeSenders) Resolve(_ context.Context, userID string, handles, ids []string) ([]ResolvedSender, error) {
	f.asked = append(append(append(f.asked, userID), handles...), ids...)
	return f.found, f.err
}

func (f *fakeSenders) IsPermanent(error) bool { return f.permanent }
func (f *fakeSenders) IsInvalid(error) bool   { return f.invalid }

func resolveSenders(env *triggerTestEnv, integration string, handles, ids []string) (*reliantv1.ResolveTriggerSendersResponse, error) {
	resp, err := env.svc.ResolveTriggerSenders(env.ctx, connect.NewRequest(&reliantv1.ResolveTriggerSendersRequest{
		Integration: integration, Handles: handles, SenderIds: ids,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// "Only from" resolves a login to the id it stores, and an id to the login it
// shows, asking as the caller.
func TestResolveTriggerSendersAsksAsTheCaller(t *testing.T) {
	env := setupTriggerTest(t)
	dir := &fakeSenders{found: []ResolvedSender{
		{Query: "@octocat", ID: "583231", DisplayName: "octocat"},
		{Query: "7", ID: "7", DisplayName: "hubot"},
	}}
	env.svc.WithSenderDirectories(map[string]SenderDirectory{"github": dir})

	got, err := resolveSenders(env, "github", []string{"@octocat"}, []string{"7"})
	require.NoError(t, err)
	assert.Equal(t, []string{env.userID, "@octocat", "7"}, dir.asked)
	require.Len(t, got.GetSenders(), 2)
	assert.Equal(t, "@octocat", got.GetSenders()[0].GetQuery())
	assert.Equal(t, "583231", got.GetSenders()[0].GetSenderId())
	assert.Equal(t, "octocat", got.GetSenders()[0].GetDisplayName())
	assert.Equal(t, "hubot", got.GetSenders()[1].GetDisplayName())
}

func TestResolveTriggerSendersErrors(t *testing.T) {
	env := setupTriggerTest(t)

	_, err := resolveSenders(env, "slack", []string{"U123"}, nil)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "Slack ids are already what a person pastes")

	_, err = resolveSenders(env, "github", []string{"octocat"}, nil)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "no GitHub on this server")

	cases := map[string]struct {
		dir  *fakeSenders
		code connect.Code
	}{
		"not connected": {&fakeSenders{err: errors.New("not connected"), permanent: true}, connect.CodeFailedPrecondition},
		"too many":      {&fakeSenders{err: errors.New("too many"), invalid: true}, connect.CodeInvalidArgument},
		"GitHub down":   {&fakeSenders{err: errors.New("502")}, connect.CodeUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env.svc.WithSenderDirectories(map[string]SenderDirectory{"github": tc.dir})
			_, err := resolveSenders(env, "github", []string{"octocat"}, nil)
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err))
		})
	}
}

// A firing carries trigger.sender, so a list of them can name who sent each.
func TestListTriggerEventsCarriesTheSender(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))
	sender := &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: "583231", DisplayName: "octocat", Verified: true}
	for i, s := range []*core.TriggerSender{sender, nil} {
		ok, err := env.repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
			ID: uuid.NewString(), TriggerID: &created.Id, UserID: env.userID, Kind: core.TriggerEventKindIntegration,
			DedupeKey: uuid.NewString(), OccurredAt: time.Date(2026, 1, 2, 9+i, 0, 0, 0, time.UTC),
			Payload: map[string]any{}, Sender: s, Outcome: core.TriggerEventSkipped,
		})
		require.NoError(t, err)
		require.True(t, ok)
	}

	resp, err := env.svc.ListTriggerEvents(env.ctx, connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: created.GetId()}))
	require.NoError(t, err)
	events := resp.Msg.GetEvents()
	require.Len(t, events, 2)
	assert.Nil(t, events[0].GetSender(), "none recorded: none rendered")
	got := events[1].GetSender()
	require.NotNil(t, got)
	assert.Equal(t, "github", got.GetKind())
	assert.Equal(t, "583231", got.GetId())
	assert.Equal(t, "octocat", got.GetDisplayName())
	assert.True(t, got.GetVerified())
}
