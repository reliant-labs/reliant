// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
)

// fakeSealer seals by prefixing, so a test can see what was sealed and for
// whom without a vault.
type fakeSealer struct{}

func (fakeSealer) Seal(_ context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error) {
	return []byte("sealed:" + tenant.ID + ":" + string(aad) + ":" + string(plaintext)), nil
}

type fakeCatalog map[string]bool

func (c fakeCatalog) HasInboundSource(integration string) bool { return c[integration] }

// fakeIntake records manual fires of inbound triggers.
type fakeIntake struct {
	mu       sync.Mutex
	accepted []triggers.InboundEvent
}

func (f *fakeIntake) Accept(_ context.Context, _ *core.Trigger, ev triggers.InboundEvent, opts triggers.AcceptOptions) (*triggers.AcceptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !opts.Manual {
		panic("the handler only ever accepts manual fires")
	}
	f.accepted = append(f.accepted, ev)
	return &triggers.AcceptResult{EventID: "ev-" + ev.DedupeKey, Outcome: core.TriggerEventPending}, nil
}

func setupInboundTriggerTest(t *testing.T) (*triggerTestEnv, *fakeIntake) {
	t.Helper()
	env := setupTriggerTest(t)
	intake := &fakeIntake{}
	env.svc.WithInbound(InboundOptions{
		PublicURL: "https://api.example.com/",
		Sealer:    fakeSealer{},
		Catalog:   fakeCatalog{"test": true},
		Intake:    intake,
	})
	return env, intake
}

func webhookDefinition(e *triggerTestEnv, mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
	return e.definition(func(d *reliantv1.TriggerDefinition) {
		d.Name = "deploy hook"
		d.Source = &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{}}
		if mutate != nil {
			mutate(d)
		}
	})
}

func (e *triggerTestEnv) createConnection(t *testing.T, userID, integration, account, status string) *core.Connection {
	t.Helper()
	conn := &core.Connection{
		ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerUser, UserID: userID,
		IntegrationID: integration, AuthKind: "none", Name: integration + "-" + uuid.NewString()[:6],
		ExternalAccountID: &account, Status: status,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, e.repo.Connections().CreateConnection(context.Background(), conn, nil,
		core.ConnectionEvent{ConnectionID: conn.ID, UserID: userID, Kind: core.ConnectionEventCreated, Actor: "user:" + userID}))
	return conn
}

func TestCreateWebhookTriggerReturnsItsTokenOnceAndStoresOnlyAHash(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)

	resp, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: webhookDefinition(env, nil)}))
	require.NoError(t, err)
	trig, cred := resp.Msg.GetTrigger(), resp.Msg.GetWebhook()

	require.NotNil(t, cred, "the token is returned at create")
	require.GreaterOrEqual(t, len(cred.GetToken()), 40, "a token carries at least 256 bits")
	assert.Equal(t, "https://api.example.com/hooks/"+trig.GetId(), trig.GetWebhookUrl())
	assert.Equal(t, trig.GetWebhookUrl()+"/"+cred.GetToken(), cred.GetUrl())
	assert.NotNil(t, trig.GetWebhook(), "the source arm round-trips")
	assert.Equal(t, 0, env.backend.syncCount(), "an inbound trigger has no schedule to converge")

	stored, err := env.repo.GetTriggerWebhookCredentials(context.Background(), trig.GetId())
	require.NoError(t, err)
	want := sha256.Sum256([]byte(cred.GetToken()))
	assert.Equal(t, want[:], stored.TokenHash, "only the hash is stored")

	got, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: trig.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, trig.GetWebhookUrl(), got.Msg.GetTrigger().GetWebhookUrl())
	assert.NotContains(t, got.Msg.GetTrigger().String(), cred.GetToken(), "the token is never rendered again")
}

func TestRotateWebhookTokenInvalidatesTheOldOne(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	resp, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: webhookDefinition(env, nil)}))
	require.NoError(t, err)
	id, old := resp.Msg.GetTrigger().GetId(), resp.Msg.GetWebhook().GetToken()

	rotated, err := env.svc.RotateWebhookToken(env.ctx, connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: id}))
	require.NoError(t, err)
	fresh := rotated.Msg.GetWebhook().GetToken()
	require.NotEmpty(t, fresh)
	assert.NotEqual(t, old, fresh)

	stored, err := env.repo.GetTriggerWebhookCredentials(context.Background(), id)
	require.NoError(t, err)
	want := sha256.Sum256([]byte(fresh))
	assert.Equal(t, want[:], stored.TokenHash)

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err = env.svc.RotateWebhookToken(otherCtx, connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: id}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "another user's trigger is not found")
}

func TestRotateWebhookTokenRejectsANonWebhookTrigger(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	created := env.create(t, env.definition(nil))
	_, err := env.svc.RotateWebhookToken(env.ctx, connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: created.GetId()}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestWebhookHmacSecretIsSealedForTheOwnerAndRequired(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	hmacSource := &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{
		Hmac: &reliantv1.WebhookHmac{Header: "X-Hub-Signature-256", Prefix: "sha256="},
	}}

	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: webhookDefinition(env, func(d *reliantv1.TriggerDefinition) { d.Source = hmacSource }),
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "hmac without a secret can never verify")

	secret := "s3cret"
	resp, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: webhookDefinition(env, func(d *reliantv1.TriggerDefinition) {
			d.Source = hmacSource
			d.WebhookHmacSecret = &secret
		}),
	}))
	require.NoError(t, err)
	id := resp.Msg.GetTrigger().GetId()
	stored, err := env.repo.GetTriggerWebhookCredentials(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "sealed:"+env.userID+":"+string(triggers.WebhookSecretAAD(id))+":s3cret", string(stored.SecretSealed))
	assert.NotContains(t, resp.Msg.GetTrigger().String(), "s3cret")

	// An update that leaves the secret unset keeps it.
	_, err = env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: id, Trigger: webhookDefinition(env, func(d *reliantv1.TriggerDefinition) { d.Source = hmacSource }),
	}))
	require.NoError(t, err)
	kept, err := env.repo.GetTriggerWebhookCredentials(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, stored.SecretSealed, kept.SecretSealed)
}

func TestTriggerFilterIsValidatedOnWrite(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)

	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: webhookDefinition(env, func(d *reliantv1.TriggerDefinition) { d.Filter = "trigger.payload.body ==" }),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "filter")

	_, err = env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.Filter = "true" }),
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "a schedule has no event to filter")

	created := env.create(t, webhookDefinition(env, func(d *reliantv1.TriggerDefinition) {
		d.Filter = "trigger.payload.body.action == 'deploy'"
	}))
	assert.Equal(t, "trigger.payload.body.action == 'deploy'", created.GetFilter())
}

func TestCreateIntegrationTriggerBindsTheCallersConnection(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	mine := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	theirs := env.createConnection(t, uuid.NewString(), "test", "acct-1", core.ConnectionStatusActive)
	source := &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
		Integration: "test", Events: []string{"thing.created"}, Match: map[string]string{"repo": "a/b"},
	}}
	def := func(connID *string) *reliantv1.TriggerDefinition {
		return env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Name = "on thing " + uuid.NewString()[:6]
			d.Source = source
			d.ConnectionId = connID
		})
	}

	// Unset: the caller's default connection for the integration.
	created := env.create(t, def(nil))
	assert.Equal(t, mine.ID, created.GetConnectionId())
	assert.Equal(t, []string{"thing.created"}, created.GetIntegration().GetEvents())
	// An integration trigger is converged (a polled one needs a poll
	// schedule; the syncer decides, and a pushed one converges to none).
	assert.Equal(t, 1, env.backend.syncCount())

	// Another user's connection is indistinguishable from a missing one.
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def(&theirs.ID)}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// A connection for a different integration is refused.
	other := env.createConnection(t, env.userID, "other", "acct-2", core.ConnectionStatusActive)
	_, err = env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def(&other.ID)}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCreateIntegrationTriggerRequiresAnInboundIntegration(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
				Integration: "nope", Events: []string{"x"},
			}}
		}),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.True(t, strings.Contains(err.Error(), "nope"))
}

func TestTriggerKindIsNotUpdatable(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	created := env.create(t, env.definition(nil))
	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: webhookDefinition(env, nil),
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// A reference to a trigger the workflow does not declare is NotFound, and
// says what the workflow does declare (builtin://agent declares none).
// Activating real declarations is trigger_activation_test.go.
func TestWorkflowTriggerReferenceToAnUndeclaredTrigger(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: "on-push"}
		}),
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), `does not declare a trigger named "on-push"`)
}

func TestFireInboundTriggerGoesThroughIntake(t *testing.T) {
	env, intake := setupInboundTriggerTest(t)
	created := env.create(t, webhookDefinition(env, nil))

	resp, err := env.svc.FireTrigger(env.ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: created.GetId()}))
	require.NoError(t, err)
	intake.mu.Lock()
	defer intake.mu.Unlock()
	require.Len(t, intake.accepted, 1)
	ev := intake.accepted[0]
	assert.Equal(t, core.TriggerEventKindWebhook, ev.Kind)
	assert.True(t, strings.HasPrefix(ev.DedupeKey, created.GetId()+":manual-"))
	assert.Equal(t, true, ev.Payload["manual"])
	assert.Equal(t, triggers.EventFireWorkflowID("ev-"+ev.DedupeKey), resp.Msg.GetFireWorkflowId())
	env.backend.mu.Lock()
	assert.Empty(t, env.backend.fires, "an inbound trigger's run-now never goes to the schedule backend")
	env.backend.mu.Unlock()
}

// texterCatalog is a catalog whose "texter" integration's webhook URL
// is set by each user on their own resources (Twilio's per-number webhook).
type texterCatalog struct{ fakeCatalog }

func (texterCatalog) UserConfiguredURL(integration string) bool { return integration == "texter" }

// A trigger on a provider whose webhook the user configures themselves shows
// them the URL to paste into the provider's console; one whose app-level
// webhook the operator registers does not.
func TestIntegrationTriggerShowsTheWebhookURLTheUserMustConfigure(t *testing.T) {
	env := setupTriggerTest(t)
	env.svc.WithInbound(InboundOptions{
		PublicURL: "https://api.example.com/",
		Sealer:    fakeSealer{},
		Catalog:   texterCatalog{fakeCatalog{"texter": true, "test": true}},
		Intake:    &fakeIntake{},
	})
	env.createConnection(t, env.userID, "texter", "AC0001", core.ConnectionStatusActive)
	env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	integration := func(id string) *reliantv1.TriggerDefinition {
		return env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Name = "on " + id
			d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
				Integration: id, Events: []string{"message.received"},
			}}
		})
	}

	texter := env.create(t, integration("texter"))
	assert.Equal(t, "https://api.example.com/integrations/texter/events", texter.GetWebhookUrl())
	got, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: texter.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, texter.GetWebhookUrl(), got.Msg.GetTrigger().GetWebhookUrl(), "every read shows it, not only the create")

	appLevel := env.create(t, integration("test"))
	assert.Nil(t, appLevel.WebhookUrl, "an operator-registered webhook is not the user's to configure")
}
