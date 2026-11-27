// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/reliant-labs/reliant/internal/store"
)

// fakeAccess stands in for the GitHub access refresher: it records whose
// access was refreshed and answers with a configured error.
type fakeAccess struct {
	mu        sync.Mutex
	refreshed []string
	err       error
	permanent bool
}

func (f *fakeAccess) Refresh(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, userID)
	return f.err
}

func (f *fakeAccess) IsPermanent(error) bool { return f.permanent }

func (f *fakeAccess) users() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refreshed...)
}

func setupAccessTriggerTest(t *testing.T) (*triggerTestEnv, *fakeAccess) {
	t.Helper()
	env, _ := setupInboundTriggerTest(t)
	access := &fakeAccess{}
	env.configure(t, func(d *Deps) {
		d.Integrations = &configv1.IntegrationsConfig{PublicUrl: "https://api.example.com/"}
		d.Sealer = fakeSealer{}
		d.Catalog = fakeCatalog{"test": true, "gh": true}
		d.Intake = &fakeIntake{}
		d.Access = map[string]IntegrationAccess{"gh": access}
	})
	return env, access
}

func ghDefinition(env *triggerTestEnv, mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
	return env.definition(func(d *reliantv1.TriggerDefinition) {
		d.Name = "on issue " + uuid.NewString()[:6]
		d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
			Integration: "gh", Events: []string{"issues.opened"}, Match: map[string]string{"repository": "acme/app"},
		}}
		if mutate != nil {
			mutate(d)
		}
	})
}

// An access-gated integration (GitHub, hosted) has no connection row: its
// token lives at control-plane. Creating a trigger needs no connection, and
// refreshes the owner's access so the first event routes.
func TestAccessGatedTriggerNeedsNoConnectionAndRefreshesAccess(t *testing.T) {
	t.Parallel()

	env, access := setupAccessTriggerTest(t)
	created := env.create(t, ghDefinition(env, nil))
	assert.Empty(t, created.GetConnectionId(), "delegated: no connection row")
	assert.Equal(t, []string{env.userID}, access.users(), "the OWNER's access, refreshed at activation")

	stored, err := store.GetTrigger(context.Background(), env.repo.ORM(), created.GetId())
	require.NoError(t, err)
	assert.Nil(t, stored.ConnectionID)
}

// With a saved connection the trigger still binds it (self-hosted), and an
// explicitly named one must be the caller's.
func TestAccessGatedTriggerBindsASavedConnectionWhenThereIsOne(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _ := setupAccessTriggerTest(t)
	mine := env.createConnection(t, env.userID, "gh", "11", core.ConnectionStatusActive)
	created := env.create(t, ghDefinition(env, nil))
	assert.Equal(t, mine.ID, created.GetConnectionId())

	theirs := env.createConnection(t, uuid.NewString(), "gh", "22", core.ConnectionStatusActive)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: ghDefinition(env, func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &theirs.ID }),
	}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}

// A permanent refresh failure (GitHub not connected, or a token that cannot
// list installations) refuses the write with the reason; nothing is stored.
// A transient one is Unavailable.
func TestAccessGatedTriggerRefusesWhenAccessCannotBeRead(t *testing.T) {
	t.Parallel()

	env, access := setupAccessTriggerTest(t)
	access.err, access.permanent = fmt.Errorf("GitHub is not connected: connect GitHub in Settings"), true
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: ghDefinition(env, nil)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, err.Error(), "connect GitHub")

	access.err, access.permanent = errors.New("control-plane unreachable"), false
	_, err = env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: ghDefinition(env, nil)}))
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))

	list, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetTriggers(), "a refused write stores nothing")
}

// Re-enabling and editing refresh too: either can be the first activation
// after access changed.
func TestAccessGatedTriggerRefreshesOnUpdateAndEnable(t *testing.T) {
	t.Parallel()

	env, access := setupAccessTriggerTest(t)
	created := env.create(t, ghDefinition(env, nil))
	_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: false}))
	require.NoError(t, err)
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: true}))
	require.NoError(t, err)
	_, err = env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: ghDefinition(env, nil),
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{env.userID, env.userID, env.userID}, access.users(),
		"create, enable and update refresh; disabling does not")

	access.err, access.permanent = errors.New("not connected"), true
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: true}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
}

// txProbeAccess records, per refresh, whether it ran inside a transaction.
type txProbeAccess struct {
	mu   sync.Mutex
	inTx []bool
}

func (p *txProbeAccess) Refresh(ctx context.Context, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inTx = append(p.inTx, orm.InTx(ctx))
	return nil
}

func (p *txProbeAccess) IsPermanent(error) bool { return false }

// Credential access must join ManageActivation's transaction, which already
// holds connection locks. Only snapshot persistence may run detached.
func TestRefreshTriggerAccessRetainsTheCallersTransaction(t *testing.T) {
	t.Parallel()

	env, _ := setupAccessTriggerTest(t)
	probe := &txProbeAccess{}
	s := &Service{deps: Deps{Access: map[string]IntegrationAccess{"gh": probe}}}
	err := env.repo.ORM().RunTx(env.ctx, orm.Serializable, func(txCtx context.Context) error {
		require.True(t, orm.InTx(txCtx))
		return s.refreshTriggerAccess(txCtx, memoTestTrigger(t, env.userID))
	})
	require.NoError(t, err)
	assert.Equal(t, []bool{true}, probe.inTx, "credential renewal must use the caller's connection locks")
}

// renewingAccessToken exercises the same connection lock as self-hosted
// OAuth renewal, without making a real OAuth request.
type renewingAccessToken struct {
	env *triggerTestEnv
	id  string
}

func (r renewingAccessToken) Token(ctx context.Context, userID string) (string, error) {
	err := store.WithConnectionSecretsLock(ctx, r.env.repo.ORM(), userID, r.id,
		func(txCtx context.Context, _ *core.Connection, _ map[string]core.ConnectionSecret) error {
			expires := time.Now().Add(time.Hour)
			return store.ReplaceConnectionSecrets(txCtx, r.env.repo.ORM(), r.id, nil, &expires)
		})
	return "renewed", err
}

type snapshotTxProbe struct{ inTx []bool }

func (p *snapshotTxProbe) ReplaceIntegrationAccess(ctx context.Context, _, _, _ string, _ time.Time, _ []core.IntegrationAccessGrant) error {
	p.inTx = append(p.inTx, orm.InTx(ctx))
	return nil
}

func (*snapshotTxProbe) PruneIntegrationAccess(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// Reproduce ManageActivation's dependency lock before refreshing access. A
// detached credential renewal waits on this transaction's own SHARE lock.
func TestAccessRefreshRenewsUnderActivationConnectionLock(t *testing.T) {
	testkit.Slow(t, "DB-backed connection locking and access refresh")
	t.Parallel()
	env, _ := setupAccessTriggerTest(t)
	conn := env.createConnection(t, env.userID, "github", "11", core.ConnectionStatusActive)
	expired := time.Now().Add(-time.Hour)
	require.NoError(t, store.ReplaceConnectionSecrets(env.ctx, env.repo.ORM(), conn.ID, nil, &expired))
	server := netguardtest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"id":11}`))
			return
		}
		_, _ = w.Write([]byte(`{"installations":[]}`))
	}))
	grants := &snapshotTxProbe{}
	refresher := ghaccess.New(grants, nil, renewingAccessToken{env: env, id: conn.ID}, ghaccess.Options{
		APIBaseURL: server.URL, HTTPClient: server.Client(),
	})
	s := &Service{deps: Deps{Access: map[string]IntegrationAccess{"gh": refresherAccess{refresher}}}}
	ctx, cancel := context.WithTimeout(env.ctx, 5*time.Second)
	defer cancel()
	err := env.repo.ORM().RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		var id string
		if err := env.repo.ORM().Bun().NewRaw("SELECT id FROM connections WHERE id = ? FOR SHARE", conn.ID).Scan(txCtx, &id); err != nil {
			return err
		}
		return s.refreshTriggerAccess(txCtx, memoTestTrigger(t, env.userID))
	})
	require.NoError(t, err)
	fresh, err := env.repo.GetConnection(env.ctx, env.userID, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.AccessExpiresAt)
	assert.True(t, fresh.AccessExpiresAt.After(time.Now()), "renewal committed without waiting on its own activation lock")
	assert.Equal(t, []bool{false}, grants.inTx, "only snapshot persistence is detached")
}

type refresherAccess struct{ *ghaccess.Refresher }

func (refresherAccess) IsPermanent(err error) bool { return ghaccess.IsPermanent(err) }

// Integrations that are not access-gated keep requiring a connection.
func TestNonAccessGatedIntegrationStillRequiresAConnection(t *testing.T) {
	t.Parallel()

	env, access := setupAccessTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
				Integration: "test", Events: []string{"thing.created"},
			}}
		}),
	}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Empty(t, access.users())
}
