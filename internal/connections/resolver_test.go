// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestResolver_OwnerComesFromDBNotInput(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.connect("alice", "a")
	e.gh.accountID, e.gh.login = 9, "bobby"
	bob := e.connect("bob", "b")

	// The run belongs to bob. CallSite has no user field at all, so there is
	// nothing for a forged activity input to set; the explicit id names alice's.
	run := e.newRun("bob")
	_, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: alice.ID})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)

	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: bob.ID})
	require.NoError(t, err)
	require.Equal(t, bob.ID, got.ConnectionID)

	owner, err := e.resolver.OwnerOf(ctx, run)
	require.NoError(t, err)
	require.Equal(t, "bob", owner)
}

func TestResolver_OwnerFallsBackToChatWhenRunHasNone(t *testing.T) {
	e := newEnv(t)
	run := e.newRun("alice")
	_, err := e.raw.Exec(`UPDATE workflows SET owner_user_id = NULL WHERE id = $1`, run)
	require.NoError(t, err)
	owner, err := e.resolver.OwnerOf(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, "alice", owner)
}

func TestResolver_DefaultResolutionAndApply(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	require.True(t, conn.IsDefault, "first connection for an integration becomes the default")
	run := e.newRun("alice")

	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, NodeID: "n1", ToolCallID: "tc1", Placement: connections.PlacementServer},
		connections.Ref{IntegrationID: "github"})
	require.NoError(t, err)
	require.Equal(t, conn.ID, got.ConnectionID)

	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, got.Apply(req))
	require.Regexp(t, `^Bearer ghu_access_\d+$`, req.Header.Get("Authorization"))

	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used' AND run_id=$2 AND node_id='n1' AND tool_call_id='tc1' AND actor='worker'`, conn.ID, run))
}

func TestResolver_SecondConnectionIsNotDefaultUntilSet(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.connect("alice", "a")
	e.gh.accountID, e.gh.login = 11, "second"
	second := e.connect("alice", "b")
	require.False(t, second.IsDefault)

	run := e.newRun("alice")
	site := connections.CallSite{RunID: run, Placement: connections.PlacementServer}
	got, err := e.resolver.ForCall(ctx, site, connections.Ref{IntegrationID: "github"})
	require.NoError(t, err)
	require.Equal(t, first.ID, got.ConnectionID)

	_, err = e.svc.SetDefault(ctx, "alice", second.ID)
	require.NoError(t, err)
	got, err = e.resolver.ForCall(ctx, site, connections.Ref{IntegrationID: "github"})
	require.NoError(t, err)
	require.Equal(t, second.ID, got.ConnectionID)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections WHERE user_id='alice' AND integration_id='github' AND is_default`))
}

func TestResolver_MissingConnectionIsFailedPrecondition(t *testing.T) {
	e := newEnv(t)
	run := e.newRun("alice")
	_, err := e.resolver.ForCall(context.Background(), connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		connections.Ref{IntegrationID: "github"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err))
	require.Contains(t, err.Error(), "no github connection")

	_, err = e.resolver.ForCall(context.Background(), connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

func TestResolver_RefusesDaemonPlacedAndUnspecified(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	run := e.newRun("alice")
	for name, p := range map[string]connections.Placement{
		"daemon": connections.PlacementDaemon, "unspecified": connections.PlacementUnspecified,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.resolver.ForCall(context.Background(), connections.CallSite{RunID: run, Placement: p},
				connections.Ref{ConnectionID: conn.ID})
			require.ErrorIs(t, err, connections.ErrDaemonPlacement)
			require.ErrorIs(t, err, connections.ErrFailedPrecondition)
		})
	}
	require.Zero(t, e.count(`SELECT count(*) FROM connection_events WHERE kind='used'`))
}

func TestResolver_RejectsOrgOwnedConnection(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	_, err := e.raw.Exec(`UPDATE connections SET owner_kind='org', org_id='org1' WHERE id=$1`, conn.ID)
	require.NoError(t, err)
	run := e.newRun("alice")
	_, err = e.resolver.ForCall(context.Background(), connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: conn.ID})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

func TestResolver_ActiveStatusRequired(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	run := e.newRun("alice")
	site := connections.CallSite{RunID: run, Placement: connections.PlacementServer}

	_, err := e.raw.Exec(`UPDATE connections SET status='needs_reauth', status_reason='invalid_grant' WHERE id=$1`, conn.ID)
	require.NoError(t, err)
	_, err = e.resolver.ForCall(ctx, site, connections.Ref{ConnectionID: conn.ID})
	require.ErrorIs(t, err, connections.ErrNeedsReauth)

	_, err = e.raw.Exec(`UPDATE connections SET status='revoked' WHERE id=$1`, conn.ID)
	require.NoError(t, err)
	_, err = e.resolver.ForCall(ctx, site, connections.Ref{ConnectionID: conn.ID})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

func TestResolver_IntegrationMustMatch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	linear, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "svc", Name: "lin", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "lin_api_key_value", "header": "bearer"},
	})
	require.NoError(t, err)
	run := e.newRun("alice")
	_, err = e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: linear.ID, IntegrationID: "github"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

func TestResolver_DeletedConnectionIsGone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	require.NoError(t, e.svc.Delete(ctx, "alice", conn.ID))
	run := e.newRun("alice")
	_, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Zero(t, e.count(`SELECT count(*) FROM connection_secrets WHERE connection_id=$1`, conn.ID), "ciphertext destroyed on delete")
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections WHERE id=$1 AND deleted_at IS NOT NULL`, conn.ID), "row kept for audit")
	_ = core.ConnectionStatusActive
}

func TestResolver_UnknownRun(t *testing.T) {
	e := newEnv(t)
	_, err := e.resolver.ForCall(context.Background(), connections.CallSite{RunID: "nope", Placement: connections.PlacementServer}, connections.Ref{IntegrationID: "github"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

func TestResolver_TriggerFireIsAttributed(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	run := e.newRun("alice")
	_, err := e.resolver.ForCall(context.Background(), connections.CallSite{RunID: run, Placement: connections.PlacementServer, TriggerID: "trg1"},
		connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used' AND actor='trigger:trg1'`, conn.ID))
}

// An integration call identifies its run by its THREAD. A nested
// `type: workflow` node with `thread: {mode: new}` runs inline under its
// parent's workflow row on a thread of its own, so that thread id names no
// workflow. The owner must still resolve — through the thread's workflow, then
// its chat — or every integration tool of a nested agent is withheld
// ("run not found") and the agent is told it has none.
func TestResolver_OwnerOfANestedWorkflowThread(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	root := e.newRun("alice")

	nested := uuid.NewString()
	rootWorkflow := root
	_, err := e.repo.CreateThread(ctx, &db.Thread{
		ID: nested, ChatID: root, WorkflowID: &rootWorkflow, Origin: db.ThreadOriginNode, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	owner, err := e.resolver.OwnerOf(ctx, nested)
	require.NoError(t, err, "a thread with no workflow row of its own resolves through its parent run")
	require.Equal(t, "alice", owner)

	got, err := e.resolver.Usable(ctx, connections.CallSite{RunID: nested, Placement: connections.PlacementServer},
		[]connections.Ref{{IntegrationID: "github"}})
	require.NoError(t, err)
	require.NoError(t, got[connections.Ref{IntegrationID: "github"}], "the owner's connection %s is usable from the nested thread", conn.ID)

	// An id that is neither a run nor a thread still reads like no run.
	_, err = e.resolver.OwnerOf(ctx, uuid.NewString())
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}
