// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// fakeConnections is a per-user connection store: each user sees only their
// own rows, as the real store's user_id predicate guarantees.
type fakeConnections struct {
	byUser map[string][]*core.Connection
	err    error
}

func (f *fakeConnections) ListConnections(_ context.Context, userID, integrationID string) ([]*core.Connection, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []*core.Connection
	for _, c := range f.byUser[userID] {
		if integrationID == "" || c.IntegrationID == integrationID {
			out = append(out, c)
		}
	}
	return out, nil
}

func conn(integration, kind, status string) *core.Connection {
	return &core.Connection{IntegrationID: integration, AuthKind: kind, Status: status}
}

func TestServiceConnectedIsPerCaller(t *testing.T) {
	conns := &fakeConnections{byUser: map[string][]*core.Connection{
		"alice": {conn("slack", core.ConnectionAuthAPIKey, core.ConnectionStatusActive)},
		"bob":   {conn("github", core.ConnectionAuthAPIKey, core.ConnectionStatusActive)},
	}}
	svc := NewService(fixtureIndex(t), conns, nil)
	ctx := context.Background()

	alice, err := svc.Search(ctx, "alice", Query{ConnectedOnly: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"http/request@1", "slack/issue.report@1", "slack/message.post@1"}, refs(alice))

	bob, err := svc.Search(ctx, "bob", Query{ConnectedOnly: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1", "github/pr.get@1", "http/request@1"}, refs(bob))

	carol, err := svc.Search(ctx, "carol", Query{ConnectedOnly: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"http/request@1"}, refs(carol), "no connections: only what needs none")
}

func TestServiceIgnoresUnusableConnections(t *testing.T) {
	conns := &fakeConnections{byUser: map[string][]*core.Connection{
		"u": {
			conn("slack", core.ConnectionAuthAPIKey, core.ConnectionStatusNeedsReauth),
			// github's fixture declares delegated and api_key only.
			conn("github", core.ConnectionAuthBasic, core.ConnectionStatusActive),
			conn("unknown", core.ConnectionAuthAPIKey, core.ConnectionStatusActive),
		},
	}}
	svc := NewService(fixtureIndex(t), conns, nil)
	usable, err := svc.Usable(context.Background(), "u")
	require.NoError(t, err)
	assert.Empty(t, usable, "needs_reauth, an undeclared kind and an unknown integration are all unusable")
}

func TestServiceDelegatedBrokerMakesIntegrationUsableWithoutAConnection(t *testing.T) {
	served := func(broker string) (bool, string) { return broker == "controlplane-github", "" }
	svc := NewService(fixtureIndex(t), &fakeConnections{}, served)
	r, err := svc.Search(context.Background(), "anyone", Query{Text: "issue", ConnectedOnly: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1", "http/request@1"}, refs(r))

	notServed := func(string) (bool, string) { return false, "no control plane" }
	svc = NewService(fixtureIndex(t), &fakeConnections{}, notServed)
	r, err = svc.Search(context.Background(), "anyone", Query{Text: "issue", ConnectedOnly: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"http/request@1"}, refs(r))
}

func TestServiceGet(t *testing.T) {
	conns := &fakeConnections{byUser: map[string][]*core.Connection{
		"u": {conn("slack", core.ConnectionAuthAPIKey, core.ConnectionStatusActive)},
	}}
	svc := NewService(fixtureIndex(t), conns, nil)
	ctx := context.Background()

	e, connected, err := svc.Get(ctx, "u", "slack/message.post@1")
	require.NoError(t, err)
	assert.Equal(t, "Post message", e.DisplayName)
	assert.True(t, connected)

	_, connected, err = svc.Get(ctx, "u", "github/issue.create@1")
	require.NoError(t, err)
	assert.False(t, connected)

	_, connected, err = svc.Get(ctx, "", "http/request@1")
	require.NoError(t, err)
	assert.True(t, connected, "an entry that needs no connection is connected for anyone")

	for _, ref := range []string{"github/issue.create@2", "github/nope@1", "nope", ""} {
		_, _, err = svc.Get(ctx, "u", ref)
		assert.ErrorIs(t, err, ErrNotFound, ref)
	}
}

func TestServiceSurfacesStoreErrors(t *testing.T) {
	svc := NewService(fixtureIndex(t), &fakeConnections{err: errors.New("db down")}, nil)
	_, err := svc.Search(context.Background(), "u", Query{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db down")
}

func TestServiceWithoutAStoreTreatsNobodyAsConnected(t *testing.T) {
	svc := NewService(fixtureIndex(t), nil, nil)
	r, err := svc.Search(context.Background(), "u", Query{ConnectedOnly: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"http/request@1"}, refs(r))
}

func TestEntrySchemasAndToolName(t *testing.T) {
	idx := fixtureIndex(t)
	e, _ := idx.Get("github/issue.create@1")
	params, output := e.Schemas()
	assert.Equal(t, "object", params["type"])
	assert.Contains(t, params["properties"], "title")
	assert.Contains(t, output["properties"], "number")
	assert.Empty(t, e.ToolName(), "not exposed as a tool in the fixture")

	b, err := Builtin()
	require.NoError(t, err)
	h, ok := b.Get("http/request@1")
	require.True(t, ok)
	assert.Equal(t, "http__request", h.ToolName())
}
