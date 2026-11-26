// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/store"
)

// scopedSources stands in for the scoped loader: the org has no source of
// its own, and every scope asked is recorded.
type scopedSources struct {
	mu     sync.Mutex
	scopes []drivers.Scope
}

func (s *scopedSources) lookup(_ context.Context, scope drivers.Scope) (models.AvailableDrivers, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes = append(s.scopes, scope)
	return models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{}}, nil
}

func orgOnlySchedule(projectID string) (Event, Spec) {
	chatID := uuid.NewString()
	return Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "fire-" + chatID},
		Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, OrgID: "acme", NewChatID: chatID,
			Workflow: "builtin://agent", Messages: userSeed("scheduled"), OrgAccountsOnly: true,
		}
}

// An org_only automation whose model none of its org's own sources serves
// is refused before anything is written (§11.4, I5): no chat, no event row,
// no run. The model was resolved in the unattended scope, the one the run's
// own calls use: the chat's org, nobody attending, not "any source".
func TestLaunch_OrgAccountsOnlyRefusesAModelNoOrgSourceServes(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	sources := &scopedSources{}
	launcher.sources = sources.lookup

	ev, spec := orgOnlySchedule(projectID)
	spec.Params = mockModelParams(t)
	_, err := launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrNoOrgAccount)
	var refused *NoOrgAccountError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "mock", refused.Model)
	assert.True(t, svcerr.IsFailedPrecondition(err), "a verdict, not a fault: %v", err)

	require.Len(t, sources.scopes, 1)
	scope := sources.scopes[0]
	assert.Equal(t, drivers.Scope{UserID: launchTestUserID, OrgID: "acme"}, scope)
	assert.True(t, scope.OrgFundedOnly())

	_, err = store.GetChat(ctx, repo.ORM(), spec.NewChatID)
	require.ErrorIs(t, err, core.ErrChatNotFound, "nothing is written")
	_, err = repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
	require.ErrorIs(t, err, core.ErrTriggerEventNotFound)
	assert.Empty(t, starter.startedIDs(), "no run starts")
}

// The run's model is resolved over the org's sources alone: one the org's
// accounts serve launches, one only another provider serves is refused and
// named, and a local model (no account pays for it) is the run's to decide.
func TestCheckOrgAccountsOnly_ResolvesTheModelOverTheOrgsSources(t *testing.T) {
	orgID := "acme"
	chat := &db.Chat{UserID: launchTestUserID, OrgID: &orgID}
	anthropicOnly := func(context.Context, drivers.Scope) (models.AvailableDrivers, error) {
		return models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
			"anthropic": {DriverID: "anthropic", APIKey: "sk-ant-api03-test", Enabled: true},
		}}, nil
	}
	l := &Launcher{sources: anthropicOnly}

	assert.NoError(t, l.refuseUnservedModels(context.Background(), chat, nil,
		map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"flagship"}}}))

	err := l.refuseUnservedModels(context.Background(), chat, nil,
		map[string]interface{}{"model": map[string]interface{}{"id": "gpt-5.6-sol@codex"}})
	var refused *NoOrgAccountError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "gpt-5.6-sol", refused.Model)

	asked := false
	l.sources = func(ctx context.Context, scope drivers.Scope) (models.AvailableDrivers, error) {
		asked = true
		return anthropicOnly(ctx, scope)
	}
	assert.NoError(t, l.refuseUnservedModels(context.Background(), chat, nil,
		map[string]interface{}{"model": map[string]interface{}{"id": "qwen3@local"}}))
	assert.NoError(t, l.refuseUnservedModels(context.Background(), chat, nil, map[string]interface{}{}))
	assert.False(t, asked, "a local model, or none, is never refused here")
}

// Every model input the workflow declares is checked, whatever its name, and
// in one read of the org's sources: autopilot-pr-review's review_model was
// once unchecked, so its org_only runs launched and waited at their first
// call while an issue worker (whose input is "model") was refused at launch.
func TestCheckOrgAccountsOnly_ChecksEveryDeclaredModelInput(t *testing.T) {
	orgID := "acme"
	chat := &db.Chat{UserID: launchTestUserID, OrgID: &orgID}
	reads := 0
	l := &Launcher{sources: func(context.Context, drivers.Scope) (models.AvailableDrivers, error) {
		reads++
		return models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
			"anthropic": {DriverID: "anthropic", APIKey: "sk-ant-api03-test", Enabled: true},
		}}, nil
	}}
	schema := map[string]*reliantv1.Input{
		"review_model": {Type: "model"},
		"agent": {Type: "group", Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
			Inputs: map[string]*reliantv1.Input{"model": {Type: "model"}},
		}}},
		"repo": {Type: "string"},
	}
	served := map[string]interface{}{"tags": []interface{}{"flagship"}}
	codex := map[string]interface{}{"id": "gpt-5.6-sol@codex"}

	err := l.refuseUnservedModels(context.Background(), chat, schema,
		map[string]interface{}{"review_model": codex, "repo": "acme/web"})
	var refused *NoOrgAccountError
	require.ErrorAs(t, err, &refused, "a model input not named \"model\" is checked")
	assert.Equal(t, "gpt-5.6-sol", refused.Model)

	err = l.refuseUnservedModels(context.Background(), chat, schema, map[string]interface{}{
		"review_model": served, "agent": map[string]interface{}{"model": codex},
	})
	require.ErrorAs(t, err, &refused, "a model input inside a group is checked")

	reads = 0
	assert.NoError(t, l.refuseUnservedModels(context.Background(), chat, schema, map[string]interface{}{
		"review_model": served, "agent": map[string]interface{}{"model": served},
	}))
	assert.Equal(t, 1, reads, "the org's sources are read once for every input")
}

// A schedule of a workflow whose only model input is not named "model" is
// refused at launch like any other org_only automation: nothing is written.
func TestLaunch_OrgAccountsOnlyRefusesAnyNamedModelInput(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "review-bot", reviewModelWorkflowYAML)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	sources := &scopedSources{}
	launcher.sources = sources.lookup

	ev, spec := orgOnlySchedule(projectID)
	spec.Workflow = "review-bot"
	_, err := launcher.Launch(ctx, ev, spec)
	var refused *NoOrgAccountError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "mock", refused.Model)
	require.Len(t, sources.scopes, 1)

	_, err = store.GetChat(ctx, repo.ORM(), spec.NewChatID)
	require.ErrorIs(t, err, core.ErrChatNotFound, "nothing is written")
	assert.Empty(t, starter.startedIDs(), "no run starts")
}

const reviewModelWorkflowYAML = `
name: review-bot
apiVersion: v2
inputs:
  review_model:
    type: model
    default:
      id: mock
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.review_model}}"
`

// The check is the org_only stamp's alone: an "any" (or pre-stamp)
// automation, a person's own start, and a chat with no org never consult
// the org's sources, so they launch exactly as before.
func TestLaunch_OrgAccountsOnlyIsCheckedOnlyWhereItApplies(t *testing.T) {
	for name, mutate := range map[string]func(*Event, *Spec){
		"an automation that may use any source": func(_ *Event, s *Spec) { s.OrgAccountsOnly = false },
		"a person's own start": func(e *Event, s *Spec) {
			e.Kind, e.DedupeKey = core.TriggerEventKindChatStart, ""
		},
		"a chat with no org": func(_ *Event, s *Spec) { s.OrgID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			repo, ctx, projectID, _ := launchFixture(t)
			launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
			sources := &scopedSources{}
			launcher.sources = sources.lookup

			ev, spec := orgOnlySchedule(projectID)
			spec.Params = mockModelParams(t)
			mutate(&ev, &spec)
			_, err := launcher.Launch(ctx, ev, spec)
			require.NoError(t, err)
			assert.Empty(t, sources.scopes)
		})
	}
}

// A failed read of the org's sources is retryable, never a verdict: a store
// outage must not record a no_org_account failure that counts toward a pause.
func TestLaunch_OrgAccountsOnlyReadFailureIsInternal(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
	launcher.sources = func(context.Context, drivers.Scope) (models.AvailableDrivers, error) {
		return models.AvailableDrivers{}, drivers.ErrDriverLookupFailed
	}

	ev, spec := orgOnlySchedule(projectID)
	spec.Params = mockModelParams(t)
	_, err := launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrInternal)
	assert.NotErrorIs(t, err, ErrNoOrgAccount)
}

func TestSelectorLabel(t *testing.T) {
	assert.Equal(t, "claude-opus-4-6", selectorLabel(map[string]any{"id": "claude-opus-4-6@claude"}))
	assert.Equal(t, "the flagship, fast model", selectorLabel(map[string]any{"tags": []string{"flagship", "fast"}}))
	assert.Equal(t, "this automation's model", selectorLabel(map[string]any{}))
}
