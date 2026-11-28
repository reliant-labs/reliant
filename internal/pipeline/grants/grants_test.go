// Copyright (c) 2025 Reliant Labs
package grants_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/grants/grantstest"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

const testOrg = "org-1"

type harness struct {
	t   *testing.T
	ctx context.Context
	env *storetest.Env
	svc *grants.Service
	f   grantstest.Fixture
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := storetest.New(t)
	h := &harness{t: t, ctx: context.Background(), env: env, now: time.Now().UTC().Truncate(time.Second)}
	svc, err := grants.New(grants.Deps{Store: store.NewPipelineGrantExtStore(env.ORM), Cards: env.Repo, Now: func() time.Time { return h.now }})
	require.NoError(t, err)
	h.svc = svc
	h.f = grantstest.Seed(t, env.Repo, storetest.SeedUserID, testOrg, storetest.SeedProjectID)
	return h
}

// card is a capability-grant card in the fixture's chat, asking for caps.
func (h *harness) card(repoID string, caps ...core.PipelineCapability) *core.Approval {
	h.t.Helper()
	meta, err := json.Marshal(core.CapabilityGrantRequest{Kind: core.ApprovalKindPipelineCapabilityGrant,
		TriggerID: h.f.TriggerID, MachinePoolID: h.f.PoolID, RepoID: repoID, Capabilities: caps})
	require.NoError(h.t, err)
	m := string(meta)
	a := &core.Approval{ID: uuid.NewString(), ChatID: h.f.ChatID, ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL),
		EntityID: "pipeline_capability_grant:" + uuid.NewString(), Status: int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title: "grant", Metadata: &m, CreatedAt: h.now}
	require.NoError(h.t, h.env.Repo.CreateApproval(h.ctx, a))
	return a
}

func (h *harness) grant(repoID string, caps ...core.PipelineCapability) []*core.PipelineCapabilityGrant {
	h.t.Helper()
	gs, err := h.svc.GrantApproved(h.ctx, h.f.UserID, h.card(repoID, caps...))
	require.NoError(h.t, err)
	return gs
}

func (h *harness) run() grants.Run {
	return grants.Run{RunID: "run-1", ChatID: h.f.ChatID, TriggerID: h.f.TriggerID, DaemonID: h.f.DaemonID, UserID: h.f.UserID}
}

func (h *harness) repo(name string) string {
	h.t.Helper()
	r, err := h.env.Repo.UpsertPipelineRepo(h.ctx, core.PipelineRepoEnrolment{
		OrgID: testOrg, ProjectID: storetest.SeedProjectID, Provider: "github", AccountKey: "inst-1",
		RepositoryID: "rid-" + name, Repository: "acme/" + name, CreatedByUserID: storetest.SeedUserID,
	})
	require.NoError(h.t, err)
	return r.ID
}

func refusedFor(t *testing.T, err error, reason grants.Reason) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, grants.ErrNotAuthorized)
	var e *grants.NotAuthorizedError
	require.True(t, errors.As(err, &e), "a refusal is a *NotAuthorizedError: %v", err)
	assert.Equal(t, reason, e.Reason, "%v", err)
}

// A run of the granted activation on a member of the granted pool holds the
// capability; it holds nothing else, and nobody else holds it.
func TestAuthorizeAllowsOnlyTheGrantedActivationOnAPoolMember(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	refusedFor(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve), grants.ReasonNoGrant)

	h.grant("", grants.Approve)
	require.NoError(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve))
	refusedFor(t, h.svc.Authorize(h.ctx, h.run(), grants.Land), grants.ReasonNoGrant)

	t.Run("an unknown capability", func(t *testing.T) {
		refusedFor(t, h.svc.Authorize(h.ctx, h.run(), "merge"), grants.ReasonUnknownCapability)
	})
	t.Run("a run no activation launched", func(t *testing.T) {
		r := h.run()
		r.TriggerID = ""
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNoActivation)
	})
	t.Run("a run on no machine", func(t *testing.T) {
		r := h.run()
		r.DaemonID = ""
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNoMachine)
	})
	t.Run("another activation", func(t *testing.T) {
		other := grantstest.Seed(t, h.env.Repo, h.f.UserID, testOrg, storetest.SeedProjectID)
		r := h.run()
		r.TriggerID = other.TriggerID
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNoGrant)
	})
	t.Run("a machine its owner did not label into the pool", func(t *testing.T) {
		r := h.run()
		r.DaemonID = grantstest.Machine(t, h.env.Repo, h.f.UserID, map[string]string{"pool": "other"})
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNotPoolMember)
		r.DaemonID = grantstest.Machine(t, h.env.Repo, h.f.UserID, nil)
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNotPoolMember)
		r.DaemonID = "no-such-machine"
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNotPoolMember)
	})
	t.Run("another user's machine with the pool's labels", func(t *testing.T) {
		r := h.run()
		r.DaemonID = grantstest.Machine(t, h.env.Repo, "mallory", h.f.PoolSelector)
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNotPoolMember)
	})
	t.Run("a run for another user", func(t *testing.T) {
		r := h.run()
		r.UserID = "mallory"
		refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonOwnerMismatch)
	})
}

// Labels a machine reports when it connects never make it a member: only
// the labels its owner set (daemons.labels) do.
func TestAuthorizeJudgesMembershipByOwnerSetLabels(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.grant("", grants.Approve)
	require.NoError(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve))
	// The owner relabels the machine out of the pool: it is no longer a member.
	_, err := store.SetDaemonLabels(h.ctx, h.env.ORM, h.f.UserID, h.f.DaemonID, map[string]string{"pool": "elsewhere"})
	require.NoError(t, err)
	refusedFor(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve), grants.ReasonNotPoolMember)
}

// The owners are read again on every use: a pool or an activation that no
// longer has the grant's owner stops the grant.
func TestAuthorizeRefusesWhenTheOwnersNoLongerMatch(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"machine_pools", "triggers"} {
		t.Run(table, func(t *testing.T) {
			h := newHarness(t)
			h.grant("", grants.Approve, grants.Land)
			require.NoError(t, h.svc.Authorize(h.ctx, h.run(), grants.Land))
			id := h.f.PoolID
			if table == "triggers" {
				id = h.f.TriggerID
			}
			grantstest.Exec(t, h.env.Repo, `UPDATE `+table+` SET user_id = 'mallory' WHERE id = ?`, id)
			r := h.run()
			r.UserID = ""
			refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Land), grants.ReasonOwnerMismatch)
		})
	}
}

// A grant for one repository is not a grant for another; a grant for every
// repository covers each.
func TestAuthorizeHonoursTheRepositoryScope(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	app, web := h.repo("app"), h.repo("web")
	h.grant(app, grants.Approve)
	r := h.run()
	r.RepoID = app
	require.NoError(t, h.svc.Authorize(h.ctx, r, grants.Approve))
	r.RepoID = web
	refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNoGrant)
	r.RepoID = ""
	refusedFor(t, h.svc.Authorize(h.ctx, r, grants.Approve), grants.ReasonNoGrant)

	h.grant("", grants.Approve)
	r.RepoID = web
	require.NoError(t, h.svc.Authorize(h.ctx, r, grants.Approve))
}

// Approving a card writes its grants with the approver and the card; doing
// it again is a no-op. Only the owner of the activation and the pool grants.
func TestGrantApprovedWritesTheOwnersGrants(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	card := h.card("", grants.Land, grants.Approve, grants.Land)

	_, err := h.svc.GrantApproved(h.ctx, "mallory", card)
	assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%v", err)
	live, err := h.svc.List(h.ctx, h.f.UserID, "", true)
	require.NoError(t, err)
	assert.Empty(t, live, "a refused grant writes nothing")

	gs, err := h.svc.GrantApproved(h.ctx, h.f.UserID, card)
	require.NoError(t, err)
	require.Len(t, gs, 2, "each capability once")
	assert.Equal(t, grants.Approve, gs[0].Capability)
	assert.Equal(t, grants.Land, gs[1].Capability)
	for _, g := range gs {
		assert.Equal(t, h.f.UserID, g.UserID)
		assert.Equal(t, testOrg, g.OrgID)
		assert.Equal(t, card.ID, g.ApprovalID)
		assert.Equal(t, h.f.TriggerID, g.TriggerID)
		assert.Equal(t, h.f.PoolID, g.MachinePoolID)
		assert.True(t, g.Live())
	}
	again, err := h.svc.GrantApproved(h.ctx, h.f.UserID, h.card("", grants.Approve))
	require.NoError(t, err)
	assert.Equal(t, gs[0].ID, again[0].ID, "granting what is granted returns the live grant")

	notACard := &core.Approval{ID: "x"}
	_, err = h.svc.GrantApproved(h.ctx, h.f.UserID, notACard)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// Revoking takes effect at once, needs no card, and touches only the
// caller's own grants.
func TestRevokeStopsTheGrant(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	g := h.grant("", grants.Approve)[0]
	require.NoError(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve))

	_, err := h.svc.Revoke(h.ctx, "mallory", g.ID)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "another user's grant is not found")
	_, err = h.svc.Revoke(h.ctx, h.f.UserID, "no-such-grant")
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	h.now = h.now.Add(time.Minute)
	revoked, err := h.svc.Revoke(h.ctx, h.f.UserID, g.ID)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	assert.Equal(t, h.f.UserID, revoked.RevokedByUserID)
	refusedFor(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve), grants.ReasonNoGrant)

	twice, err := h.svc.Revoke(h.ctx, h.f.UserID, g.ID)
	require.NoError(t, err)
	assert.Equal(t, revoked.RevokedAt.Unix(), twice.RevokedAt.Unix(), "revoking again changes nothing")

	live, err := h.svc.List(h.ctx, h.f.UserID, "", false)
	require.NoError(t, err)
	assert.Empty(t, live)
	all, err := h.svc.List(h.ctx, h.f.UserID, h.f.TriggerID, true)
	require.NoError(t, err)
	require.Len(t, all, 1, "a revoked grant stays as the audit trail")

	// It may be granted again after a revoke.
	again := h.grant("", grants.Approve)[0]
	assert.NotEqual(t, g.ID, again.ID)
	require.NoError(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve))
}

func (h *harness) request(key string, principal serveraction.Principal, params map[string]any) (*serveraction.Result, error) {
	return h.svc.RequestGrant(h.ctx, &serveraction.Request{
		Name: serveraction.PipelineRequestGrant, Params: params, Principal: principal,
		Invocation: serveraction.Invocation{RunID: "run-1", ChatID: h.f.ChatID, NodeID: "ask", ExecutionKey: key},
	})
}

func (h *harness) principal() serveraction.Principal {
	return serveraction.Principal{UserID: h.f.UserID, OrgID: testOrg, ProjectID: storetest.SeedProjectID, TriggerID: h.f.TriggerID, DaemonID: h.f.DaemonID}
}

// reliant/pipeline.request_grant@1 raises one card in the run's chat, which
// grants nothing until the owner approves it; asking again finds it.
func TestRequestGrantRaisesOneCard(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	params := map[string]any{"capabilities": []any{"approve", "land"}, "reason": "the pr workflow reviews and lands"}
	res, err := h.request("exec-1", h.principal(), params)
	require.NoError(t, err)
	assert.Equal(t, grants.RequestRequested, res.Data["status"])
	id, _ := res.Data["approval_id"].(string)
	require.NotEmpty(t, id)

	card, err := h.env.Repo.GetApproval(h.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, h.f.ChatID, card.ChatID)
	assert.Equal(t, int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL), card.ApprovalType)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING), card.Status)
	assert.Empty(t, card.TemporalWorkflowID, "no run waits on the card")
	require.NotNil(t, card.MinRole)
	assert.Equal(t, "owner", *card.MinRole)
	assert.Contains(t, card.Title, "\n\nTechnical details\n", "laid out as a card that grants standing power")
	assert.Contains(t, card.Title, "Operation: pipeline_capability_grant")
	req := card.CapabilityGrantRequest()
	require.NotNil(t, req)
	assert.Equal(t, h.f.TriggerID, req.TriggerID)
	assert.Equal(t, h.f.PoolID, req.MachinePoolID)
	assert.Equal(t, []core.PipelineCapability{grants.Approve, grants.Land}, req.Capabilities)
	refusedFor(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve), grants.ReasonNoGrant)

	// A retry of the execution, and another run asking for part of it,
	// find the card waiting.
	for _, key := range []string{"exec-1", "exec-2"} {
		again, err := h.request(key, h.principal(), map[string]any{"capabilities": []any{"approve"}})
		require.NoError(t, err)
		assert.Equal(t, grants.RequestPending, again.Data["status"], key)
		assert.Equal(t, id, again.Data["approval_id"], key)
	}

	_, err = h.svc.GrantApproved(h.ctx, h.f.UserID, card)
	require.NoError(t, err)
	granted, err := h.request("exec-3", h.principal(), map[string]any{"capabilities": []any{"land"}})
	require.NoError(t, err)
	assert.Equal(t, grants.RequestGranted, granted.Data["status"])
	require.NoError(t, h.svc.Authorize(h.ctx, h.run(), grants.Approve))
}

// A card waits on no run, so the stale-approval sweep leaves it pending
// after the run that asked has finished.
func TestRequestGrantCardOutlivesItsRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res, err := h.request("exec-1", h.principal(), map[string]any{"capabilities": []any{"approve"}})
	require.NoError(t, err)
	id := res.Data["approval_id"].(string)
	runID := "run-" + uuid.NewString()
	grantstest.Exec(t, h.env.Repo, `INSERT INTO workflows (id, chat_id, workflow_name, thread, state, stop_reason, created_at)
		VALUES (?, ?, 'pr', 'thread-1', 3, 1, now())`, runID, h.f.ChatID)
	grantstest.Exec(t, h.env.Repo, `UPDATE chats SET workflow_id = ? WHERE id = ?`, runID, h.f.ChatID)
	stale, err := store.ListStaleApprovals(h.ctx, h.env.ORM, 100)
	require.NoError(t, err)
	for _, s := range stale {
		assert.NotEqual(t, id, s.ID, "a capability-grant card is not closed when its run ends")
	}
}

func TestRequestGrantRefusals(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	caps := map[string]any{"capabilities": []any{"approve"}}
	code := func(err error) string {
		e, ok := serveraction.AsError(err)
		require.True(t, ok, "%v", err)
		return e.Code
	}

	p := h.principal()
	p.TriggerID = ""
	_, err := h.request("e1", p, caps)
	assert.Equal(t, serveraction.CodePermissionDenied, code(err), "a run no activation launched")

	p = h.principal()
	p.UserID = "mallory"
	_, err = h.request("e2", p, caps)
	assert.Equal(t, serveraction.CodePermissionDenied, code(err), "a run of another owner")

	_, err = h.request("e3", h.principal(), map[string]any{"capabilities": []any{"merge"}})
	assert.Equal(t, serveraction.CodeInvalidArgument, code(err))

	_, err = h.request("e4", h.principal(), map[string]any{"capabilities": []any{"approve"}, "repo_id": "no-such-repo"})
	assert.Equal(t, serveraction.CodeNotFound, code(err))

	single := grantstest.Seed(t, h.env.Repo, h.f.UserID, testOrg, storetest.SeedProjectID)
	grantstest.Exec(t, h.env.Repo, `UPDATE triggers SET machine_pool_id = NULL, no_machine = true WHERE id = ?`, single.TriggerID)
	p = h.principal()
	p.TriggerID = single.TriggerID
	_, err = h.request("e5", p, caps)
	assert.Equal(t, serveraction.CodeFailedPrecondition, code(err), "an activation on no pool")
}
