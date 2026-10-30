// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// Exercise the creation boundary without a database: the source is already
// resolved here for both inline sources and workflow-trigger declarations.
func TestActingIdentityFor_SourceDefaultsAndStoredChoices(t *testing.T) {
	github := &triggers.Source{Kind: core.TriggerKindIntegration, Integration: "github"}
	for _, tc := range []struct {
		name     string
		source   *triggers.Source
		chosen   reliantv1.ActingIdentity
		existing *core.Trigger
		hosted   bool
		want     core.ActingIdentity
		refused  bool
	}{
		{name: "new GitHub defaults app", source: github, hosted: true, want: core.ActingIdentityApp},
		{name: "explicit app", source: github, chosen: reliantv1.ActingIdentity_ACTING_IDENTITY_APP, hosted: true, want: core.ActingIdentityApp},
		{name: "explicit owner", source: github, chosen: reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, want: core.ActingIdentityOwner},
		{name: "stored owner unchanged", source: github, existing: &core.Trigger{ActingIdentity: core.ActingIdentityOwner}, hosted: true, want: core.ActingIdentityOwner},
		{name: "legacy stored owner unchanged", source: github, existing: &core.Trigger{}, want: core.ActingIdentityOwner},
		{name: "stored app unchanged", source: github, existing: &core.Trigger{ActingIdentity: core.ActingIdentityApp}, hosted: true, want: core.ActingIdentityApp},
		// Every new source defaults to the bot where the deployment has one:
		// a scheduled or Slack-triggered workflow's GitHub writes are the bot's too.
		{name: "new other integration defaults app", source: &triggers.Source{Kind: core.TriggerKindIntegration, Integration: "slack"}, hosted: true, want: core.ActingIdentityApp},
		{name: "new schedule defaults app", source: &triggers.Source{Kind: core.TriggerKindSchedule}, hosted: true, want: core.ActingIdentityApp},
		{name: "new webhook defaults app", source: &triggers.Source{Kind: core.TriggerKindWebhook}, hosted: true, want: core.ActingIdentityApp},
		{name: "new workflow event defaults app", source: &triggers.Source{Kind: core.TriggerKindWorkflowEvent}, hosted: true, want: core.ActingIdentityApp},
		{name: "explicit owner on a schedule", source: &triggers.Source{Kind: core.TriggerKindSchedule}, chosen: reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, hosted: true, want: core.ActingIdentityOwner},
		{name: "stored owner schedule unchanged", source: &triggers.Source{Kind: core.TriggerKindSchedule}, existing: &core.Trigger{ActingIdentity: core.ActingIdentityOwner}, hosted: true, want: core.ActingIdentityOwner},
		// A deployment with no control plane has no bot to default to.
		{name: "no control plane: other integration owner", source: &triggers.Source{Kind: core.TriggerKindIntegration, Integration: "slack"}, want: core.ActingIdentityOwner},
		{name: "no control plane: schedule owner", source: &triggers.Source{Kind: core.TriggerKindSchedule}, want: core.ActingIdentityOwner},
		{name: "no control plane: webhook owner", source: &triggers.Source{Kind: core.TriggerKindWebhook}, want: core.ActingIdentityOwner},
		{name: "no control plane: workflow event owner", source: &triggers.Source{Kind: core.TriggerKindWorkflowEvent}, want: core.ActingIdentityOwner},
		{name: "missing bot infrastructure fails closed", source: github, refused: true},
		{name: "explicit app without control plane fails closed", source: &triggers.Source{Kind: core.TriggerKindSchedule}, chosen: reliantv1.ActingIdentity_ACTING_IDENTITY_APP, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{}
			if tc.hosted {
				svc.deps.ControlPlane = &fakeControlPlaneClient{}
			}
			got, err := svc.actingIdentityFor(&reliantv1.TriggerDefinition{ActingIdentity: tc.chosen}, tc.existing, tc.source)
			if tc.refused {
				require.Error(t, err)
				assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
				assert.Empty(t, got, "missing bot configuration must not resolve to owner")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCreateTrigger_GitHubIdentityDefaultAndExplicitOwner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chosen reliantv1.ActingIdentity
		want   reliantv1.ActingIdentity
		hosted bool
	}{
		{"omitted", reliantv1.ActingIdentity_ACTING_IDENTITY_UNSPECIFIED, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, true},
		{"explicit app", reliantv1.ActingIdentity_ACTING_IDENTITY_APP, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, true},
		{"explicit owner without App", reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := setupInboundTriggerTest(t)
			if tc.hosted {
				env.hosted(t)
			}
			env.configure(t, func(d *Deps) { d.Catalog = fakeCatalog{"github": true} })
			conn := env.createConnection(t, env.userID, "github", "acct-1", core.ConnectionStatusActive)
			got := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
				d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github", Events: []string{"issues.opened"}}}
				d.ConnectionId = &conn.ID
				d.ActingIdentity = tc.chosen
			}))
			assert.Equal(t, tc.want, got.GetActingIdentity())
		})
	}
}

func TestEnableWorkflow_IdentityDefaultsToTheBotAndPreservesStoredOwner(t *testing.T) {
	t.Parallel()
	env, _ := setupInboundTriggerTest(t)
	env.hosted(t).configure(t, func(d *Deps) { d.Catalog = fakeCatalog{"github": true, "slack": true} })
	yaml := strings.Replace(enableWorkflowYAML, "integration: test", "integration: github", 1)
	yaml = strings.Replace(yaml, "entry: [a]", "  - name: slack-event\n    integration: {integration: slack, events: [message.channels]}\n    prompt: Handle it.\nentry: [a]", 1)
	env.saveWorkflow(t, "triage", yaml)
	github := env.createConnection(t, env.userID, "github", "acct-1", core.ConnectionStatusActive)
	slack := env.createConnection(t, env.userID, "slack", "acct-2", core.ConnectionStatusActive)
	req := env.enableRequest(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"github": github.ID, "slack": slack.ID}
	})
	resp := env.enable(t, req)
	require.True(t, resp.Enabled, "%v", resp.Results)
	require.Len(t, resp.Results, 4)
	for _, result := range resp.Results {
		// GitHub, schedule, webhook and Slack sources alike: the bot by default.
		assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, result.Activation.GetActingIdentity(), result.Trigger)
	}
	// Existing activations deliberately set to owner must stay owner when the
	// workflow is enabled again without a shared identity choice.
	for _, row := range env.storedTriggers(t) {
		if row.WorkflowTrigger != nil && (*row.WorkflowTrigger == "new-issue" || *row.WorkflowTrigger == "nightly") {
			row.ActingIdentity = core.ActingIdentityOwner
			require.NoError(t, env.repo.UpdateTrigger(env.ctx, row))
		}
	}
	resp = env.enable(t, req)
	require.True(t, resp.Enabled, "%v", resp.Results)
	for _, result := range resp.Results {
		want := reliantv1.ActingIdentity_ACTING_IDENTITY_APP
		if result.Trigger == "new-issue" || result.Trigger == "nightly" {
			want = reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER
		}
		assert.Equal(t, want, result.Activation.GetActingIdentity(), result.Trigger)
	}
}

// hosted is a deployment with a control plane to mint the bot's tokens from.
func (e *triggerTestEnv) hosted(t *testing.T) *triggerTestEnv {
	t.Helper()
	e.configure(t, func(d *Deps) { d.ControlPlane = &fakeControlPlaneClient{} })
	return e
}

func (e *triggerTestEnv) storedIdentity(t *testing.T, id string) core.ActingIdentity {
	t.Helper()
	stored, err := e.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	return stored.ActingIdentity
}

func asApp(d *reliantv1.TriggerDefinition) {
	d.ActingIdentity = reliantv1.ActingIdentity_ACTING_IDENTITY_APP
}

// Nothing chose an identity on a deployment with the GitHub App: the
// automation's GitHub writes are the bot's, whatever its source.
func TestCreateTrigger_ActingIdentityDefaultsToTheBot(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t).hosted(t)
	got := env.create(t, env.definition(nil))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, got.GetActingIdentity(), "never UNSPECIFIED on a stored trigger")
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, got.GetId()))
}

// No control plane, so no bot: a non-GitHub source defaults to the owner.
func TestCreateTrigger_ActingIdentityDefaultsToTheOwnerWithoutAControlPlane(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t)
	got := env.create(t, env.definition(nil))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, got.GetActingIdentity(), "never UNSPECIFIED on a stored trigger")
	assert.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, got.GetId()))
}

func TestCreateTrigger_ActingAsTheAppIsStoredAndReturned(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t).hosted(t)
	got := env.create(t, env.definition(asApp))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, got.GetActingIdentity())
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, got.GetId()))

	read, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: got.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, read.Msg.GetTrigger().GetActingIdentity())
}

// Self-hosted: no control plane, so no App key to mint from. The activation is
// refused when it is written rather than held as one that could never act.
func TestCreateTrigger_AppIdentityIsRefusedWithoutAControlPlane(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t) // no control plane
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.definition(asApp)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, err.Error(), "control plane")
	assert.Zero(t, env.backend.syncCount(), "a refused activation must not touch the schedule backend")
}

// The owner identity is always available, hosted or not.
func TestCreateTrigger_ExplicitOwnerWorksEverywhere(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env := setupTriggerTest(t)
	got := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
		d.ActingIdentity = reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER
	}))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, got.GetActingIdentity())
}

// An update that does not mention the identity leaves it alone, so a client that
// predates the field cannot flip an activation back to the owner by omission.
func TestUpdateTrigger_OmittedIdentityIsUnchanged(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t).hosted(t)
	created := env.create(t, env.definition(asApp))

	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.Message = "A new prompt." }),
	}))
	require.NoError(t, err)
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, created.GetId()), "an omitted identity must not reset to the owner")
}

func TestUpdateTrigger_IdentityCanBeChangedBothWays(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t).hosted(t)
	created := env.create(t, env.definition(nil))
	require.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, created.GetId()))

	update := func(identity reliantv1.ActingIdentity) {
		t.Helper()
		_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
			Id: created.GetId(), Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.ActingIdentity = identity }),
		}))
		require.NoError(t, err)
	}
	update(reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER)
	assert.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, created.GetId()))
	update(reliantv1.ActingIdentity_ACTING_IDENTITY_APP)
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, created.GetId()))
}

// Choosing the bot on an existing activation is refused where it could never
// work, as at create.
func TestUpdateTrigger_AppIdentityIsRefusedWithoutAControlPlane(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))
	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: env.definition(asApp),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, created.GetId()))
}

func TestListTriggers_CarriesTheIdentity(t *testing.T) {
	t.Parallel()

	env := setupTriggerTest(t).hosted(t)
	owner := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
		d.Name = "mine"
		d.ActingIdentity = reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER
	}))
	bot := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "the bot's"; asApp(d) }))

	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	got := map[string]reliantv1.ActingIdentity{}
	for _, tr := range resp.Msg.GetTriggers() {
		got[tr.GetId()] = tr.GetActingIdentity()
	}
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, got[owner.GetId()])
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, got[bot.GetId()])
}
