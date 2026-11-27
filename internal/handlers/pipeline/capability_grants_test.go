package pipeline_test

import (
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/pipeline"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/grants/grantstest"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// An owner lists the grants they made and revokes one with a plain rpc: no
// card, any of their credentials, effective at once. Another user sees and
// revokes none of them.
func TestCapabilityGrantsListAndRevoke(t *testing.T) {
	e := newEnv(t, true)
	g, err := grants.New(grants.Deps{Store: store.NewPipelineGrantExtStore(e.repo.ORM()), Cards: e.repo})
	require.NoError(t, err)
	svc := pipeline.NewTestPipeline(t, pipeline.WithPipelineDeps(pipeline.Deps{
		Logger: testkit.DiscardLogger(), Config: &config.Config{}, Pipeline: &configv1.PipelineConfig{Enabled: true},
		Store: e.repo, Grants: g,
	}))
	f := grantstest.Seed(t, e.repo, adminID, org, "p-ada")
	meta, _ := json.Marshal(core.CapabilityGrantRequest{Kind: core.ApprovalKindPipelineCapabilityGrant,
		TriggerID: f.TriggerID, MachinePoolID: f.PoolID, Capabilities: []core.PipelineCapability{grants.Approve, grants.Land}})
	m := string(meta)
	card := &core.Approval{ID: uuid.NewString(), ChatID: f.ChatID, EntityID: uuid.NewString(), Status: 1, Title: "grant", Metadata: &m, CreatedAt: time.Now()}
	require.NoError(t, e.repo.CreateApproval(e.person(adminID), card))
	_, err = g.GrantApproved(e.person(adminID), adminID, card)
	require.NoError(t, err)

	list, err := svc.ListCapabilityGrants(e.person(adminID), connect.NewRequest(&pb.ListCapabilityGrantsRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetGrants(), 2)
	first := list.Msg.GetGrants()[0]
	assert.Equal(t, f.TriggerID, first.GetTriggerId())
	assert.Equal(t, f.PoolID, first.GetMachinePoolId())
	assert.Equal(t, adminID, first.GetGrantedByUserId())
	assert.Equal(t, card.ID, first.GetApprovalId())
	assert.Nil(t, first.GetRevokedAt())

	other, err := svc.ListCapabilityGrants(e.person(memberID), connect.NewRequest(&pb.ListCapabilityGrantsRequest{}))
	require.NoError(t, err)
	assert.Empty(t, other.Msg.GetGrants(), "another user lists none of them")
	_, err = svc.RevokeCapabilityGrant(e.person(memberID), connect.NewRequest(&pb.RevokeCapabilityGrantRequest{GrantId: first.GetId()}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "nor revokes one: %v", err)

	// The owner's machine-bound token may revoke: revoking only takes power
	// away.
	revoked, err := svc.RevokeCapabilityGrant(e.as(adminID, auth.CredentialAccessToken),
		connect.NewRequest(&pb.RevokeCapabilityGrantRequest{GrantId: first.GetId()}))
	require.NoError(t, err)
	assert.NotNil(t, revoked.Msg.GetGrant().GetRevokedAt())
	assert.Equal(t, adminID, revoked.Msg.GetGrant().GetRevokedByUserId())
	require.ErrorIs(t, g.Authorize(e.person(adminID), grants.Run{TriggerID: f.TriggerID, DaemonID: f.DaemonID},
		grants.Capability(first.GetCapability())), grants.ErrNotAuthorized, "revoked at once")

	live, err := svc.ListCapabilityGrants(e.person(adminID), connect.NewRequest(&pb.ListCapabilityGrantsRequest{TriggerId: &f.TriggerID}))
	require.NoError(t, err)
	assert.Len(t, live.Msg.GetGrants(), 1, "the revoked grant is no longer live")
	includeRevoked := true
	all, err := svc.ListCapabilityGrants(e.person(adminID), connect.NewRequest(&pb.ListCapabilityGrantsRequest{IncludeRevoked: &includeRevoked}))
	require.NoError(t, err)
	assert.Len(t, all.Msg.GetGrants(), 2, "and stays in the audit trail")
}

// Without the grants service the rpcs are unavailable, never a silent
// empty answer.
func TestCapabilityGrantsWithoutTheServiceAreUnavailable(t *testing.T) {
	e := newEnv(t, true)
	_, err := e.svc.ListCapabilityGrants(e.person(adminID), connect.NewRequest(&pb.ListCapabilityGrantsRequest{}))
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	_, err = e.svc.RevokeCapabilityGrant(e.person(adminID), connect.NewRequest(&pb.RevokeCapabilityGrantRequest{GrantId: "g"}))
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
}
