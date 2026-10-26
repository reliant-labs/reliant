// Copyright (c) 2025 Reliant Labs
package preset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
)

// testPrincipal names every call's caller as the user the request carries.
type testPrincipal struct{}

func (testPrincipal) Actor(ctx context.Context) activity.Actor {
	id, _ := auth.GetUserIDFromContext(ctx)
	return activity.User(id, "")
}

func (testPrincipal) Scope(ctx context.Context) string {
	id, _ := auth.GetUserIDFromContext(ctx)
	return id
}

// presetActivityClient serves svc as userID behind forge's real activity
// interceptor and returns a client plus the events the interceptor wrote. The
// declarations under test are the ones preset.proto carries.
func presetActivityClient(t *testing.T, svc reliantv1connect.PresetServiceHandler, userID string) (reliantv1connect.PresetServiceClient, func() []activity.Event) {
	t.Helper()
	var (
		mu     sync.Mutex
		events []activity.Event
	)
	ic, err := activity.Interceptor(activity.Deps{
		Principal: testPrincipal{},
		Writer: activity.WriterFunc(func(_ context.Context, ev activity.Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return nil
		}),
	})
	require.NoError(t, err)
	authed := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithUser(ctx, userID, "user", ""), req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewPresetServiceHandler(svc, connect.WithInterceptors(authed, ic)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reliantv1connect.NewPresetServiceClient(srv.Client(), srv.URL), func() []activity.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]activity.Event(nil), events...)
	}
}

// A preset write records its event only when it changed something: a
// refused create or delete (success false) records nothing, and a delete's
// event names the preset id the response now carries.
func TestPresetWrites_RecordActivityOnlyWhenTheyChangeSomething(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)
	client, events := presetActivityClient(t, service, userID)
	ctx := context.Background()

	created, err := client.CreatePreset(ctx, connect.NewRequest(&reliantv1.CreatePresetRequest{
		ProjectId: projectID, Name: "careful", Tag: "agent",
	}))
	require.NoError(t, err)
	require.True(t, created.Msg.GetSuccess(), created.Msg.GetError())
	presetID := created.Msg.GetPreset().GetId()
	require.NotEmpty(t, presetID)

	clash, err := client.CreatePreset(ctx, connect.NewRequest(&reliantv1.CreatePresetRequest{
		ProjectId: projectID, Name: "careful", Tag: "agent",
	}))
	require.NoError(t, err)
	require.False(t, clash.Msg.GetSuccess(), "a second preset with the same slug is refused")

	deleted, err := client.DeletePreset(ctx, connect.NewRequest(&reliantv1.DeletePresetRequest{
		ProjectId: projectID, Name: "careful",
	}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.GetSuccess(), deleted.Msg.GetError())
	assert.Equal(t, presetID, deleted.Msg.GetId())

	missing, err := client.DeletePreset(ctx, connect.NewRequest(&reliantv1.DeletePresetRequest{
		ProjectId: projectID, Name: "careful",
	}))
	require.NoError(t, err)
	require.False(t, missing.Msg.GetSuccess(), "deleting a preset that is gone is refused")

	got := events()
	require.Len(t, got, 2, "only the create and the delete that changed something record an event")
	assert.Equal(t, "create", got[0].Verb)
	assert.Equal(t, activity.Target{Kind: "preset", ID: presetID, Label: "careful"}, got[0].Target)
	assert.Equal(t, "delete", got[1].Verb)
	assert.Equal(t, activity.Target{Kind: "preset", ID: presetID, Label: "careful"}, got[1].Target)
	for _, ev := range got {
		assert.Equal(t, userID, ev.Actor.ID)
		assert.Equal(t, userID, ev.Scope)
	}
}
