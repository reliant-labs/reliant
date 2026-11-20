// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

// fixturePrincipal names every call's caller as the fixture's user.
type fixturePrincipal struct{ userID string }

func (p fixturePrincipal) Actor(context.Context) activity.Actor { return activity.User(p.userID, "") }
func (p fixturePrincipal) Scope(context.Context) string         { return p.userID }

// activityClient serves the fixture's service behind forge's real activity
// interceptor, as the fixture's user, and returns a client for it plus the
// events the interceptor wrote. The declarations under test are the ones
// worktree.proto carries.
func (f *discoverFixture) activityClient(t *testing.T) (reliantv1connect.WorktreeServiceClient, func() []activity.Event) {
	t.Helper()
	var (
		mu     sync.Mutex
		events []activity.Event
	)
	ic, err := activity.InterceptorV2(activity.Deps{
		Principal: fixturePrincipal{userID: f.userID},
		Writer: activity.WriterFunc(func(_ context.Context, ev activity.Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return nil
		}),
	})
	require.NoError(t, err)
	authed := connect.ServerInterceptor(func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(context.WithValue(ctx, auth.UserIDContextKey, f.userID), spec, stream)
		}
	})
	mux := http.NewServeMux()
	server := connect.NewServer(svcerr.InterceptorV2(), authed, ic)
	reliantv1connect.RegisterWorktreeServiceHandler(server, f.svc)
	mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reliantv1connect.NewWorktreeServiceClient(rpcclient.New(srv.Client(), srv.URL, nil)), func() []activity.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]activity.Event(nil), events...)
	}
}

// A prune that removed git records is a project event; one that found nothing
// stale changed nothing, so the handler skips the declared event.
func TestPruneWorktrees_RecordsAnEventOnlyWhenSomethingWasPruned(t *testing.T) {
	testkit.Slow(t, "runs the handler against a real database")
	t.Parallel()

	t.Run("pruned", func(t *testing.T) {
		f := newDiscoverTestFixture(t, "a")
		f.router.pruned = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/gone", "prunable": true}}
		client, events := f.activityClient(t)

		_, err := client.PruneWorktrees(context.Background(), &reliantv1.PruneWorktreesRequest{ProjectId: f.projectID})
		require.NoError(t, err)

		got := events()
		require.Len(t, got, 1)
		assert.Equal(t, "prune_worktrees", got[0].Verb)
		assert.Equal(t, activity.Target{Kind: "project", ID: f.projectID, Label: f.projectID}, got[0].Target)
		assert.Equal(t, f.userID, got[0].Actor.ID)
		assert.Equal(t, reliantv1connect.WorktreeServicePruneWorktreesProcedure, got[0].Procedure)
	})

	t.Run("nothing stale", func(t *testing.T) {
		f := newDiscoverTestFixture(t, "a")
		client, events := f.activityClient(t)

		resp, err := client.PruneWorktrees(context.Background(), &reliantv1.PruneWorktreesRequest{ProjectId: f.projectID})
		require.NoError(t, err)
		require.Empty(t, resp.GetPruned())
		assert.Empty(t, events(), "a prune that removed nothing must not be recorded as one")
	})
}
