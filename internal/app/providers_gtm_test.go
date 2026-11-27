// Copyright (c) 2026 Reliant Labs
package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/featuregate"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

const (
	gtmTestOrg   = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	gtmTestUser  = "user-1"
	gtmOtherUser = "user-2"
)

// gtmMembers makes gtmTestUser a member of gtmTestOrg, and nobody else of
// anything.
type gtmMembers struct{}

func (gtmMembers) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: userID == gtmTestUser && orgID == gtmTestOrg}, nil
}

func gtmConfig(orgIDs ...string) *configv1.GtmConfig {
	return &configv1.GtmConfig{AllowedOrgIds: orgIDs}
}

// The gate is GTM_ALLOWED_ORG_IDS over the org resolver, and fails closed:
// no setting, an empty list and a deployment with no orgs all allow nothing.
func TestNewFeatureGate(t *testing.T) {
	ctx := context.Background()
	resolver := orgs.New(gtmMembers{})
	for name, tc := range map[string]struct {
		cfg        *configv1.GtmConfig
		membership *orgs.Resolver
		want       bool
	}{
		"listed org, live member": {cfg: gtmConfig(gtmTestOrg), membership: resolver, want: true},
		"empty gtm config":        {cfg: &configv1.GtmConfig{}, membership: resolver},
		"no gtm config":           {membership: resolver},
		"empty list":              {cfg: gtmConfig(), membership: resolver},
		"blank ids only":          {cfg: gtmConfig(" ", ""), membership: resolver},
		"another org listed":      {cfg: gtmConfig("org-other"), membership: resolver},
		"no orgs (self-hosted)":   {cfg: gtmConfig(gtmTestOrg), membership: nil},
	} {
		t.Run(name, func(t *testing.T) {
			gate, err := NewFeatureGate(FeatureGateDeps{Gtm: tc.cfg, Membership: tc.membership})
			require.NoError(t, err)
			require.NotNil(t, gate)
			ok, err := gate.Allowed(ctx, featuregate.GTM, gtmTestUser, gtmTestOrg)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
			ok, err = gate.Allowed(ctx, featuregate.GTM, gtmOtherUser, gtmTestOrg)
			require.NoError(t, err)
			assert.False(t, ok, "a non-member is never allowed")
		})
	}
}

// Each root's domain is gated by the gate it was built with.
func TestGtmDomainsAreGated(t *testing.T) {
	env := storetest.New(t)
	gate, err := NewFeatureGate(FeatureGateDeps{Gtm: gtmConfig(gtmTestOrg), Membership: orgs.New(gtmMembers{})})
	require.NoError(t, err)

	api, err := NewGtmDomain(GtmDomainDeps{DB: env.Repo, Gate: gate, ConnectionVault: &connectionvault.MockService{}})
	require.NoError(t, err)
	worker, err := NewWorkerGtmDomain(WorkerGtmDomainDeps{DB: env.Repo, Gate: gate, ConnectionVault: &connectionvault.MockService{}, Gtm: gtmConfig(gtmTestOrg)})
	require.NoError(t, err)
	require.NotNil(t, worker.Email, "send_gtm_email always has its approver")

	for name, svc := range map[string]gtmsvc.Service{"api": api.Service, "worker": worker.Service} {
		in, err := svc.GetAccess(context.Background(), gtmsvc.Actor{Kind: gtmsvc.ActorPerson, UserID: gtmTestUser, OrgID: gtmTestOrg})
		require.NoError(t, err, name)
		assert.True(t, in.Enabled, name)
		out, err := svc.GetAccess(context.Background(), gtmsvc.Actor{Kind: gtmsvc.ActorPerson, UserID: gtmOtherUser, OrgID: gtmTestOrg})
		require.NoError(t, err, name)
		assert.False(t, out.Enabled, name)
	}
}

// Without a control plane the worker's analytics is off (the tool says it is
// unavailable); without run records, so is email.
func TestWorkerGtmDomain_OptionalPartsAreOff(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	assert.Nil(t, gtmAnalytics(nil, gtmConfig(gtmTestOrg), discard))
	assert.Nil(t, gtmAnalytics(&configv1.ControlPlaneConfig{Url: "http://cp.example.com"}, gtmConfig(gtmTestOrg), discard),
		"a plain-http control plane off loopback is refused: the token would travel in the clear")
	assert.NotNil(t, gtmAnalytics(&configv1.ControlPlaneConfig{Url: "https://cp.example.com"}, gtmConfig(gtmTestOrg), discard))
	assert.Nil(t, gtmEmail(nil, nil, discard))
	assert.Nil(t, gtmEmail(&WorkerIntegrationCredentials{}, nil, discard))
}

// Email is sent only through the write ledger, which records each send as
// the run's write: without a ledger there is no sender (email unavailable),
// never an unrecorded one.
func TestGtmEmail_NeedsTheWriteLedger(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := &WorkerIntegrationCredentials{Source: noCredentials{}, Runs: connections.NewResolver(connections.ResolverDeps{})}

	assert.Nil(t, gtmEmail(creds, nil, discard), "no ledger, no sender")

	wrapped := 0
	ledger := &writeledger.MockService{WrapFunc: func(inner writeledger.Runner) writeledger.Runner {
		wrapped++
		return inner
	}}
	assert.NotNil(t, gtmEmail(creds, ledger, discard))
	assert.Equal(t, 1, wrapped, "the sender's runner is the ledger's")
}

// noCredentials resolves no credential.
type noCredentials struct{}

func (noCredentials) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return nil, errors.New("none")
}

// connection_state is the actor's live connection of the integration: their
// default one, or the named one (theirs, else their org's).
func TestGtmConnections_State(t *testing.T) {
	ctx := context.Background()
	actor := gtmsvc.Actor{Kind: gtmsvc.ActorPerson, UserID: gtmTestUser, OrgID: gtmTestOrg}
	gmail := func(status string) *core.Connection {
		return &core.Connection{ID: "conn-1", UserID: gtmTestUser, IntegrationID: "gmail", Status: status}
	}
	notFound := func(context.Context, string, string) (*core.Connection, error) {
		return nil, core.ErrConnectionNotFound
	}

	for name, tc := range map[string]struct {
		store        *connectionvault.MockService
		connectionID string
		want         gtmsvc.ConnectionState
		wantErr      bool
	}{
		"default, active": {store: &connectionvault.MockService{DefaultConnectionFunc: func(_ context.Context, user, integration string) (*core.Connection, error) {
			if user != gtmTestUser || integration != "gmail" {
				return nil, core.ErrConnectionNotFound
			}
			return gmail(core.ConnectionStatusActive), nil
		}}, want: gtmsvc.ConnectionConnected},
		"default, needs reauth": {store: &connectionvault.MockService{DefaultConnectionFunc: func(context.Context, string, string) (*core.Connection, error) {
			return gmail(core.ConnectionStatusNeedsReauth), nil
		}}, want: gtmsvc.ConnectionNotConnected},
		"no default":   {store: &connectionvault.MockService{DefaultConnectionFunc: notFound}, want: gtmsvc.ConnectionNotConnected},
		"store outage": {store: &connectionvault.MockService{DefaultConnectionFunc: func(context.Context, string, string) (*core.Connection, error) { return nil, errors.New("down") }}, want: gtmsvc.ConnectionUnknown, wantErr: true},
		"named, theirs": {store: &connectionvault.MockService{GetConnectionFunc: func(context.Context, string, string) (*core.Connection, error) {
			return gmail(core.ConnectionStatusActive), nil
		}}, connectionID: "conn-1", want: gtmsvc.ConnectionConnected},
		"named, the org's": {store: &connectionvault.MockService{
			GetConnectionFunc: notFound,
			GetOwnedConnectionFunc: func(_ context.Context, owner core.ConnectionOwner, _ string) (*core.Connection, error) {
				if owner != core.OrgConnectionOwner(gtmTestOrg) {
					return nil, core.ErrConnectionNotFound
				}
				return gmail(core.ConnectionStatusActive), nil
			},
		}, connectionID: "conn-1", want: gtmsvc.ConnectionConnected},
		"named, another integration": {store: &connectionvault.MockService{GetConnectionFunc: func(context.Context, string, string) (*core.Connection, error) {
			return &core.Connection{ID: "conn-1", IntegrationID: "slack", Status: core.ConnectionStatusActive}, nil
		}}, connectionID: "conn-1", want: gtmsvc.ConnectionNotConnected},
		"named, nowhere": {store: &connectionvault.MockService{
			GetConnectionFunc: notFound,
			GetOwnedConnectionFunc: func(context.Context, core.ConnectionOwner, string) (*core.Connection, error) {
				return nil, core.ErrConnectionNotFound
			},
		}, connectionID: "conn-1", want: gtmsvc.ConnectionNotConnected},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := gtmConnections{store: tc.store}.State(ctx, actor, "gmail", tc.connectionID)
			assert.Equal(t, tc.wantErr, err != nil, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// No bucket is assets off, and so no sweep; a bucket without its endpoint or
// credentials fails boot; a whole one gives both domains the store and the
// api its sweep.
func TestGtmAssets(t *testing.T) {
	off, err := NewGtmAssets(GtmAssetsDeps{})
	require.NoError(t, err)
	assert.Nil(t, off.Store)
	sweep, err := NewGtmAssetSweep(GtmAssetSweepDeps{Assets: off})
	require.NoError(t, err)
	assert.Nil(t, sweep, "no sweep without a bucket")

	_, err = NewGtmAssets(GtmAssetsDeps{Assets: &configv1.GtmAssetsConfig{Bucket: "gtm-assets"}})
	require.Error(t, err)

	on, err := NewGtmAssets(GtmAssetsDeps{Assets: &configv1.GtmAssetsConfig{
		Bucket: "gtm-assets", Endpoint: "https://s3.example.com", Region: "us-east-1", AccessKeyId: "id", SecretAccessKey: "secret",
	}})
	require.NoError(t, err)
	require.NotNil(t, on.Store)

	env := storetest.New(t)
	sweep, err = NewGtmAssetSweep(GtmAssetSweepDeps{DB: env.Repo, Assets: on})
	require.NoError(t, err)
	require.NotNil(t, sweep)
}

// A pass sweeps only on the replica that holds the sweep's lock.
func TestGtmAssetSweep_OnlyTheLockHolderSweeps(t *testing.T) {
	env := storetest.New(t)
	on, err := NewGtmAssets(GtmAssetsDeps{Assets: &configv1.GtmAssetsConfig{
		Bucket: "gtm-assets", Endpoint: "https://s3.example.com", Region: "us-east-1", AccessKeyId: "id", SecretAccessKey: "secret",
	}})
	require.NoError(t, err)
	sweep, err := NewGtmAssetSweep(GtmAssetSweepDeps{DB: env.Repo, Assets: on})
	require.NoError(t, err)

	var asked []dblock.Key
	released := 0
	for _, held := range []bool{false, true} {
		sweep.locker = &dblock.MockService{TryLockFunc: func(_ context.Context, key dblock.Key) (func(), bool, error) {
			asked = append(asked, key)
			if !held {
				return func() {}, false, nil
			}
			return func() { released++ }, true, nil
		}}
		sweep.pass(context.Background())
	}
	assert.Equal(t, []dblock.Key{dblock.GtmAssetSweep, dblock.GtmAssetSweep}, asked)
	assert.Equal(t, 1, released, "the holder sweeps (no stale rows here) and releases the lock")
}

// attach_gtm_asset reads chat attachments only with
// GTM_ATTACH_FROM_CHAT_ENABLED, and then only the actor's own attachment
// stamped with the actor's org: another org's, and one stamped with none,
// are the bare ErrNotFound a missing one is.
func TestGtmChatAttachments(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	const otherOrg = "6b1f2b2e-0f43-4a7e-9a64-0c0f3d1d7c11"
	put := func(id string, org *string) {
		t.Helper()
		now := time.Now().UTC()
		require.NoError(t, store.InsertAttachment(ctx, env.ORM, &core.Attachment{
			ID: id, UserID: gtmTestUser, Filename: id + ".png", Size: 3, MimeType: "image/png",
			AttachmentType: "image", Content: []byte("png"), OrgID: org, CreatedAt: now, UpdatedAt: now,
		}))
	}
	org, other := gtmTestOrg, otherOrg
	put("att-same-org", &org)
	put("att-other-org", &other)
	put("att-no-org", nil)
	actor := gtmsvc.Actor{Kind: gtmsvc.ActorAgent, UserID: gtmTestUser, OrgID: gtmTestOrg, ChatID: "chat-1"}

	t.Run("flag off", func(t *testing.T) {
		assert.Nil(t, gtmChatAttachments(&configv1.GtmConfig{AllowedOrgIds: []string{gtmTestOrg}}, env.Repo, discard))
		assert.Nil(t, gtmChatAttachments(nil, env.Repo, discard))
		// The worker's domain, flag off, past the gate: attach_gtm_asset
		// reports chat attachments unavailable.
		gate, err := NewFeatureGate(FeatureGateDeps{Gtm: gtmConfig(gtmTestOrg), Membership: orgs.New(gtmMembers{})})
		require.NoError(t, err)
		// A bucket is configured, so the asset store is not what refuses.
		assets, err := NewGtmAssets(GtmAssetsDeps{Assets: &configv1.GtmAssetsConfig{
			Bucket: "gtm-assets", Endpoint: "https://s3.example.com", Region: "us-east-1", AccessKeyId: "id", SecretAccessKey: "secret",
		}})
		require.NoError(t, err)
		worker, err := NewWorkerGtmDomain(WorkerGtmDomainDeps{DB: env.Repo, Gate: gate, ConnectionVault: &connectionvault.MockService{}, Gtm: gtmConfig(gtmTestOrg), Assets: assets})
		require.NoError(t, err)
		_, err = worker.AttachFromAttachment(ctx, actor, gtmsvc.AttachFromAttachmentInput{AttachmentID: "att-same-org", ItemID: "item-1"})
		assert.ErrorIs(t, err, gtmsvc.ErrAttachmentsUnavailable)
	})

	on := gtmChatAttachments(&configv1.GtmConfig{AttachFromChatEnabled: true}, env.Repo, discard)
	require.NotNil(t, on)

	t.Run("flag on, same org", func(t *testing.T) {
		file, body, err := on.Open(ctx, actor, "att-same-org")
		require.NoError(t, err)
		defer body.Close()
		got, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, []byte("png"), got)
		assert.Equal(t, gtmsvc.AttachmentFile{Filename: "att-same-org.png", ContentType: "image/png", Size: 3}, file)
	})
	t.Run("flag on, another org", func(t *testing.T) {
		_, body, err := on.Open(ctx, actor, "att-other-org")
		assert.ErrorIs(t, err, gtmsvc.ErrNotFound)
		assert.Nil(t, body)
		// ... and the same attachment from the other org's chat is no
		// one else's to take either: another user, the right org.
		_, _, err = on.Open(ctx, gtmsvc.Actor{Kind: gtmsvc.ActorAgent, UserID: gtmOtherUser, OrgID: gtmTestOrg}, "att-same-org")
		assert.ErrorIs(t, err, gtmsvc.ErrNotFound)
	})
	t.Run("flag on, NULL org", func(t *testing.T) {
		_, body, err := on.Open(ctx, actor, "att-no-org")
		assert.ErrorIs(t, err, gtmsvc.ErrNotFound)
		assert.Nil(t, body)
	})
	t.Run("flag on, missing", func(t *testing.T) {
		_, _, err := on.Open(ctx, actor, "att-nope")
		assert.ErrorIs(t, err, gtmsvc.ErrNotFound)
	})
}
