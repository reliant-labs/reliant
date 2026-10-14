// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// One declaration, many activations: a filter, an inputs mapping and a prompt
// read the ACTIVATION's own params, so each instance carries its repository
// list instead of having it pasted into the workflow.
const repoScopedWorkflow = `name: triage
inputs:
  repos:
    type: array
    default: []
  team:
    type: string
    default: platform
  issue_number:
    type: integer
    default: 0
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.attributes.repository in trigger.activation.params.repos"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "{{ trigger.activation.params.team }} triages #{{ trigger.payload.data.issue.number }} in {{ trigger.payload.attributes.repository }}"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func repoActivation(id string, params map[string]any) *core.Trigger {
	name := "new-issue"
	return &core.Trigger{
		ID: id, UserID: "user-1", ProjectID: "project-1", Name: id,
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "triage", WorkflowTrigger: &name,
		DaemonID: testDaemonID, Params: params,
		Config: json.RawMessage(`{"integration":"github","events":["issues.opened"]}`),
	}
}

func issueIn(repo string, number int) map[string]any {
	return map[string]any{
		"attributes": map[string]any{"repository": repo},
		"data":       map[string]any{"issue": map[string]any{"number": number}},
	}
}

func TestOneWorkflowActivatedTwiceFiltersByEachActivationsOwnRepos(t *testing.T) {
	repo := newFakeRepo()
	frontend := repoActivation("act-frontend", map[string]any{"repos": []any{"acme/web", "acme/docs"}, "team": "frontend"})
	backend := repoActivation("act-backend", map[string]any{"repos": []any{"acme/api"}})
	unscoped := repoActivation("act-unscoped", nil)
	for _, a := range []*core.Trigger{frontend, backend, unscoped} {
		repo.triggers[a.ID] = a
	}
	workflows := fakeWorkflows{yaml: map[string]string{"triage": repoScopedWorkflow}}
	intake := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(workflows)

	deliver := func(a *core.Trigger, key, repoName string) *AcceptResult {
		t.Helper()
		res, err := intake.Accept(context.Background(), a, InboundEvent{
			Kind: core.TriggerEventKindIntegration, DedupeKey: a.ID + ":" + key,
			OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
			Payload:    issueIn(repoName, 7), Sender: octocat,
		}, AcceptOptions{})
		require.NoError(t, err)
		return res
	}

	// The same events go to every activation; each admits only its repos.
	for _, tc := range []struct {
		repo string
		want map[*core.Trigger]core.TriggerEventOutcome
	}{
		{"acme/web", map[*core.Trigger]core.TriggerEventOutcome{frontend: core.TriggerEventPending, backend: core.TriggerEventSkipped, unscoped: core.TriggerEventSkipped}},
		{"acme/docs", map[*core.Trigger]core.TriggerEventOutcome{frontend: core.TriggerEventPending, backend: core.TriggerEventSkipped, unscoped: core.TriggerEventSkipped}},
		{"acme/api", map[*core.Trigger]core.TriggerEventOutcome{frontend: core.TriggerEventSkipped, backend: core.TriggerEventPending, unscoped: core.TriggerEventSkipped}},
		{"acme/other", map[*core.Trigger]core.TriggerEventOutcome{frontend: core.TriggerEventSkipped, backend: core.TriggerEventSkipped, unscoped: core.TriggerEventSkipped}},
	} {
		for a, want := range tc.want {
			res := deliver(a, tc.repo, tc.repo)
			assert.Equal(t, want, res.Outcome, "%s for an issue in %s: %s", a.ID, tc.repo, res.Detail)
		}
	}

	// A miss says why, so "why did my pack ignore this repo?" has an answer.
	res := deliver(backend, "again", "acme/web")
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Contains(t, res.Detail, "trigger.activation.params.repos")

	// The launched run reads the same params: inputs and the prompt template.
	launcher := &fakeLauncher{}
	for _, tc := range []struct {
		a    *core.Trigger
		repo string
	}{
		{frontend, "acme/web"},
		{backend, "acme/api"},
	} {
		out, err := NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), EventFireInput{
			TriggerID: tc.a.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: tc.a.ID + ":" + tc.repo,
		})
		require.NoError(t, err)
		assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	}
	calls := launcher.snapshot()
	require.Len(t, calls, 2)
	byTrigger := map[string]int{}
	for i, c := range calls {
		byTrigger[c.Event.TriggerID] = i
	}
	fe, be := calls[byTrigger[frontend.ID]], calls[byTrigger[backend.ID]]
	assert.Contains(t, fe.Spec.Messages[0].Content, "frontend triages #7 in acme/web\n\n<trigger_event")
	assert.Contains(t, be.Spec.Messages[0].Content, "platform triages #7 in acme/api\n\n<trigger_event", "an unset param reads the workflow's default")
	assert.Equal(t, float64(7), fe.Spec.Params["issue_number"].GetNumberValue())
	assert.Equal(t, "frontend", fe.Spec.Params["team"].GetStringValue(), "the stored param still reaches the run")
}

// trigger.activation.id and trigger.activation.owner are readable, and the params the filter
// reads are the ones the run will get: the workflow's defaults applied, and
// an input the trigger maps from the event left out (the event sets it).
func TestActivationScopeReadsDefaultsAndIdentity(t *testing.T) {
	name := "new-issue"
	trig := &core.Trigger{ID: "act-1", UserID: "user-9", WorkflowTrigger: &name, Params: map[string]any{
		"team": "web", "issue_number": 99,
	}}
	wf, err := wfyaml.ParseWorkflow([]byte(repoScopedWorkflow))
	require.NoError(t, err)
	decl, err := DeclarationIn(wf, trig)
	require.NoError(t, err)

	scope := ActivationScope(context.Background(), nil, trig, decl, map[string]any{"kind": "integration"})
	act := scope.Activation
	assert.Equal(t, "act-1", act["id"])
	assert.Equal(t, map[string]any{"user_id": "user-9", "github_id": ""}, act["owner"])
	params := act["params"].(map[string]any)
	assert.Equal(t, "web", params["team"])
	assert.Equal(t, []any{}, params["repos"], "an unset list reads as empty, so a filter on it matches nothing instead of failing")
	assert.NotContains(t, params, "issue_number", "the trigger maps issue_number from the event, so the event sets it")

	f, err := triggerspec.CompileFilter("trigger.activation.owner.user_id == 'user-9' && trigger.activation.id == 'act-1' && size(trigger.activation.params.repos) == 0")
	require.NoError(t, err)
	hit, err := f.MatchScope(scope)
	require.NoError(t, err)
	assert.True(t, hit)
}

// An ad hoc trigger has no declaration, so its params are as stored.
func TestActivationScopeOfAnAdHocTrigger(t *testing.T) {
	trig := &core.Trigger{ID: "adhoc", UserID: "u", Params: map[string]any{"repos": []any{"acme/a"}}}
	scope := ActivationScope(context.Background(), nil, trig, nil, map[string]any{})
	assert.Equal(t, map[string]any{"repos": []any{"acme/a"}}, scope.Activation["params"])

	empty := ActivationScope(context.Background(), nil, &core.Trigger{ID: "x"}, nil, nil)
	assert.Equal(t, map[string]any{}, empty.Activation["params"], "params is always a map")
}

// "Only me, or these people": trust is plain CEL over data the trigger already
// has. trigger.sender.id (a GitHub receiver writes the numeric user id as a
// string) is compared with trigger.activation.owner.github_id (the sender_id of
// the connection the activation listens through, also a string) and with an
// array of strings the activation sets, so no id is hardcoded in the workflow
// and no table of trusted people stands behind it.
const trustedWorkflow = `name: triage
inputs:
  trusted_ids:
    type: array
    default: []
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.sender.verified && (trigger.sender.id == trigger.activation.owner.github_id || trigger.sender.id in trigger.activation.params.trusted_ids)"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func TestFilterTrustsTheOwnersGitHubIdentityAndTheActivationsTrustedIDs(t *testing.T) {
	const (
		me       = "583231" // the connection's sender_id, a GitHub user id
		friend   = "7001"
		stranger = "9999"
	)
	github := func(id string, verified bool) *core.TriggerSender {
		return &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: id, DisplayName: "u" + id, Verified: verified}
	}
	repo := newFakeRepo()
	own := "conn-1"
	repo.connections[own] = &core.Connection{ID: own, UserID: "user-1", IntegrationID: "github", SenderID: strPtr(me)}
	repo.connections["conn-slack"] = &core.Connection{ID: "conn-slack", UserID: "user-1", IntegrationID: "slack", SenderID: strPtr(me)}
	repo.connections["conn-theirs"] = &core.Connection{ID: "conn-theirs", UserID: "someone-else", IntegrationID: "github", SenderID: strPtr(me)}

	name := "new-issue"
	activation := func(id string, conn *string, params map[string]any) *core.Trigger {
		a := repoActivation(id, params)
		a.WorkflowTrigger = &name
		a.ConnectionID = conn
		repo.triggers[a.ID] = a
		return a
	}
	slack, theirs := "conn-slack", "conn-theirs"
	onlyMe := activation("only-me", &own, nil)
	withFriends := activation("with-friends", &own, map[string]any{"trusted_ids": []any{friend}})
	noConnection := activation("no-connection", nil, map[string]any{"trusted_ids": []any{friend}})
	slackConn := activation("slack-conn", &slack, nil)
	foreignConn := activation("foreign-conn", &theirs, nil)

	intake := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(fakeWorkflows{yaml: map[string]string{"triage": trustedWorkflow}})
	n := 0
	accepts := func(a *core.Trigger, s *core.TriggerSender) bool {
		t.Helper()
		n++
		res, err := intake.Accept(context.Background(), a, InboundEvent{
			Kind: core.TriggerEventKindIntegration, DedupeKey: fmt.Sprintf("%s:%d", a.ID, n),
			OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC), Payload: issueIn("acme/web", 1), Sender: s,
		}, AcceptOptions{})
		require.NoError(t, err)
		require.NotEqual(t, core.TriggerEventFailed, res.Outcome, "the filter must evaluate: %s", res.Detail)
		return res.Outcome == core.TriggerEventPending
	}

	assert.True(t, accepts(onlyMe, github(me, true)), "the owner, by the id on their GitHub connection")
	assert.False(t, accepts(onlyMe, github(friend, true)), "nobody else, with no trusted_ids")
	assert.False(t, accepts(onlyMe, github(me, false)), "the owner's id is not enough when the source could not vouch for it")

	assert.True(t, accepts(withFriends, github(me, true)))
	assert.True(t, accepts(withFriends, github(friend, true)), "a trusted id, a string compared with a string")
	assert.False(t, accepts(withFriends, github(stranger, true)))

	// An activation with no GitHub connection has no owner id: "" never equals
	// a verified sender's id, so only trusted_ids admit anyone.
	assert.False(t, accepts(noConnection, github(me, true)))
	assert.True(t, accepts(noConnection, github(friend, true)))
	// A connection that is not GitHub's, or is not the owner's, gives no id
	// either: it must never widen who a filter admits.
	assert.False(t, accepts(slackConn, github(me, true)))
	assert.False(t, accepts(foreignConn, github(me, true)))
}

func TestActivationOwnerIsReadOnlyDataForCEL(t *testing.T) {
	repo := newFakeRepo()
	repo.connections["c"] = &core.Connection{ID: "c", UserID: "user-9", IntegrationID: "github", SenderID: strPtr("583231")}
	conn := "c"
	trig := &core.Trigger{ID: "a", UserID: "user-9", ConnectionID: &conn}

	scope := ActivationScope(context.Background(), repo, trig, nil, map[string]any{"kind": "integration"})
	assert.Equal(t, map[string]any{"user_id": "user-9", "github_id": "583231"}, scope.Activation["owner"])

	// It is reachable at trigger.activation, and typed: both ids are strings.
	f, err := triggerspec.CompileFilter(`trigger.activation.owner.user_id == 'user-9' && trigger.activation.owner.github_id == '583231'`)
	require.NoError(t, err)
	hit, err := f.MatchScope(scope)
	require.NoError(t, err)
	assert.True(t, hit)

	// A number is not the string: comparing the sender's id with an int is a
	// type error caught at save time, not a filter that silently never matches.
	shape, err := triggerspec.NewShape(nil)
	require.NoError(t, err)
	errs := shape.CheckFilter(`trigger.sender.id == 583231`)
	require.NotEmpty(t, errs, "a string id compared with a number")
	assert.Empty(t, shape.CheckFilter(`trigger.sender.id == trigger.activation.owner.github_id`))
	assert.NotEmpty(t, shape.CheckFilter(`trigger.activation.owner.github_idd == ''`), "a misspelt owner field")
}

func strPtr(s string) *string { return &s }
