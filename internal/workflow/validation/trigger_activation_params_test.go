// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A trigger's filter, inputs and prompt may read `activation`: its id, owner
// and params. The params are typed from the workflow's own inputs, so a
// misspelt or mistyped read fails when the workflow is saved rather than
// matching nothing on every event.

func paramsWorkflow(t *testing.T, triggers string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(`name: triage
inputs:
  repos:
    type: array
    default: []
  team:
    type: string
    default: platform
  max_age_days:
    type: integer
    default: 30
  dry_run:
    type: boolean
    default: false
  issue_number:
    type: integer
    default: 0
  label:
    type: string
    default: ""
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
triggers:
` + triggers))
	require.NoError(t, err)
	return wf
}

func TestTriggerExpressionsMayReadTheActivationsParams(t *testing.T) {
	cases := map[string]string{
		"a list membership filter": "    filter: \"trigger.payload.attributes.repository in trigger.activation.params.repos\"\n",
		"typed scalar comparisons": "    filter: \"trigger.activation.params.team == 'platform' && trigger.activation.params.max_age_days > 7 && !trigger.activation.params.dry_run\"\n",
		"identity":                 "    filter: \"trigger.activation.owner.user_id != '' && trigger.activation.owner.github_id == '' && trigger.activation.id != ''\"\n",
		"a list helper":            "    filter: \"size(trigger.activation.params.repos) > 0 && trigger.activation.params.repos.exists(r, r.startsWith('acme/'))\"\n",
		"an inputs mapping":        "    inputs:\n      label: \"{{ trigger.activation.params.team }}\"\n",
		"a prompt":                 "    prompt: \"{{ trigger.activation.params.team }} triages #{{ trigger.payload.data.issue.number }}\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			wf := paramsWorkflow(t, githubTrigger("issues.opened", body))
			assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
		})
	}
}

func TestTriggerExpressionsReadingAParamsTheWorkflowDoesNotDeclareAreErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		want []string
	}{
		"a misspelt param": {
			"    filter: \"trigger.payload.attributes.repository in trigger.activation.params.repo\"\n",
			[]string{"filter", "trigger.activation.params has no field 'repo'", "repos"},
		},
		"a field of a string param": {
			"    filter: \"trigger.activation.params.team.name == 'x'\"\n",
			[]string{"filter", "trigger.activation.params.team", "string", "field 'name'"},
		},
		"a field of a boolean param": {
			"    filter: \"trigger.activation.params.dry_run.enabled\"\n",
			[]string{"filter", "trigger.activation.params.dry_run", "field 'enabled'"},
		},
		"a misspelt root field": {
			"    filter: \"trigger.activation.parms.repos == []\"\n",
			[]string{"filter", "trigger.activation has no field 'parms'"},
		},
		"in an inputs mapping": {
			"    inputs:\n      label: \"{{ trigger.activation.params.nope }}\"\n",
			[]string{"inputs.label", "trigger.activation.params has no field 'nope'"},
		},
		"in a prompt": {
			"    prompt: \"{{ trigger.activation.params.nope }}\"\n",
			[]string{"prompt", "trigger.activation.params has no field 'nope'"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wf := paramsWorkflow(t, githubTrigger("issues.opened", tc.body))
			requireTriggerFinding(t, StaticAnalysisWithOptions(wf, nil), SeverityError, tc.want...)
		})
	}
}

// An input the trigger maps from the event is not the activation's to set
// (the event's value always wins), so it is not an activation param.
func TestAnInputTheTriggerMapsFromTheEventIsNotAnActivationParam(t *testing.T) {
	wf := paramsWorkflow(t, githubTrigger("issues.opened",
		"    filter: \"trigger.activation.params.issue_number > 0\"\n    inputs:\n      issue_number: \"{{ trigger.payload.data.issue.number }}\"\n"))
	requireTriggerFinding(t, StaticAnalysisWithOptions(wf, nil), SeverityError, "filter", "trigger.activation.params has no field 'issue_number'")
}

// Every kind of source has activations, so every kind can read the params,
// not only the ones whose events declare a payload.
func TestAScheduleOrWebhookTriggerMayReadActivationParams(t *testing.T) {
	wf := paramsWorkflow(t, `
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "Digest for {{ trigger.activation.params.team }}"
  - name: hook
    webhook: {}
    filter: "trigger.activation.params.team == 'platform'"
`)
	assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))

	bad := paramsWorkflow(t, `
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "Digest for {{ trigger.activation.params.teem }}"
`)
	requireTriggerFinding(t, StaticAnalysisWithOptions(bad, nil), SeverityError, "prompt", "trigger.activation.params has no field 'teem'")
}

func TestValidateParams(t *testing.T) {
	wf := paramsWorkflow(t, "")
	declared := wf.GetInputs()

	assert.Empty(t, ValidateParams(declared, map[string]any{
		"repos": []any{"acme/web"}, "team": "web", "max_age_days": float64(14), "dry_run": true,
	}))
	assert.Empty(t, ValidateParams(declared, nil), "params nobody set are the workflow's defaults")

	problems := ValidateParams(declared, map[string]any{"repo": []any{"acme/web"}})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], `"repo" is not an input of this workflow`)
	assert.Contains(t, problems[0], "did you mean 'repos'?")

	problems = ValidateParams(declared, map[string]any{"repos": "acme/web", "max_age_days": "soon"})
	require.Len(t, problems, 2)
	assert.Contains(t, problems[0], "max_age_days")
	assert.Contains(t, problems[1], "input 'repos' expects array")
}

// The documented trust filter validates: both ids are strings, and trusted_ids
// is an array the activation sets. A number where an id belongs is an error.
func TestTheTrustFilterValidatesAndNumericIDsDoNot(t *testing.T) {
	const trust = "trigger.sender.verified && (trigger.sender.id == trigger.activation.owner.github_id || trigger.sender.id in trigger.activation.params.trusted_ids)"
	wf := func(filter string) *reliantv1.Workflow {
		w, err := wfyaml.ParseWorkflow([]byte(`name: triage
inputs:
  trusted_ids: {type: array, default: []}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
triggers:
` + githubTrigger("issues.opened", "    filter: \""+filter+"\"\n")))
		require.NoError(t, err)
		return w
	}
	assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf(trust), nil), SeverityError))

	requireTriggerFinding(t, StaticAnalysisWithOptions(wf("trigger.sender.id == 583231"), nil), SeverityError, "filter")
	requireTriggerFinding(t, StaticAnalysisWithOptions(wf("trigger.sender.id == trigger.activation.owner.github_idd"), nil), SeverityError,
		"filter", "trigger.activation.owner has no field 'github_idd'")
}
