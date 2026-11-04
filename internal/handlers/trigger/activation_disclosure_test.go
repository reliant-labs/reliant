package trigger

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func TestAgentActivationCardContainsActionAndScheduleDisclosure(t *testing.T) {
	testkit.Slow(t, "DB-backed complete approval card disclosure")
	e, p := securityActivation(t)
	e.saveWorkflow(t, "secure-child", `name: secure-child
entry: [act]
nodes:
 - id: act
   type: action
   args:
     uses: github/issue.create@1
     with: {owner: acme, repo: releases, title: private-title, body: sk-private-body}
`)
	card, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	require.Contains(t, card.Title, "github/issue.create@1 [MUTATES external state]")
	require.Contains(t, card.Title, "repo=releases")
	require.Contains(t, card.Title, "cron=[0 9 * * *]")
	require.Contains(t, card.Title, e.daemonID)
	require.NotContains(t, card.Title, "sk-private-body")
	require.NotContains(t, card.Title, "private-title")
}

func TestActivationActionDisclosureDistinguishesReadAndMutation(t *testing.T) {
	parse := func(uses string) *pb.Node {
		w, err := wfyaml.ParseWorkflow([]byte(`name: action
entry: [act]
nodes:
 - id: act
   type: action
   args:
     uses: ` + uses + `
     connection: conn-reviewed
     with:
       owner: reliant-labs
       repo: application
       environment: prod
       body: sk-private-body
       headers: {Authorization: "Bearer very-private-token"}
`))
		require.NoError(t, err)
		return w.Nodes[0]
	}
	read := strings.Join(nodePrivilegeDisclosure(parse("github/issue.get@1"), nil, "workflow", nil), "\n")
	write := strings.Join(nodePrivilegeDisclosure(parse("github/issue.create@1"), nil, "workflow", nil), "\n")
	require.Contains(t, read, "github/issue.get@1 [READ]")
	require.Contains(t, write, "github/issue.create@1 [MUTATES external state]")
	require.NotEqual(t, read, write)
	for _, card := range []string{read, write} {
		require.Contains(t, card, "repo=application")
		require.Contains(t, card, "owner=reliant-labs")
		require.Contains(t, card, "environment=prod")
		require.Contains(t, card, "conn-reviewed")
		require.NotContains(t, card, "sk-private-body")
		require.NotContains(t, card, "very-private-token")
	}
}

func TestActivationDynamicTargetsAreExplicitAndSecretsWithheld(t *testing.T) {
	w, err := wfyaml.ParseWorkflow([]byte(`name: dynamic-action
entry: [act]
nodes:
 - id: act
   type: action
   args:
     uses: github/issue.create@1
     with:
       repo: "{{nodes.event.repo}}"
       channel: "{{inputs.channel}}"
       environment: "{{inputs.environment}}"
       body: "{{inputs.secret}}"
`))
	require.NoError(t, err)
	card := strings.Join(nodePrivilegeDisclosure(w.Nodes[0], map[string]any{"channel": "C012345", "environment": "password-do-not-disclose", "secret": "opaque-sensitive-literal"}, "workflow", nil), "\n")
	require.Contains(t, card, "github/issue.create@1")
	require.Contains(t, card, "repo=dynamic/unbounded")
	require.Contains(t, card, "channel=C012345")
	require.Contains(t, card, "environment=dynamic/unbounded")
	require.NotContains(t, card, "password-do-not-disclose")
	require.NotContains(t, card, "opaque-sensitive-literal")
	require.NotContains(t, card, "nodes.event")
}
func TestActivationInvokeToolAndURLDisclosure(t *testing.T) {
	w, err := wfyaml.ParseWorkflow([]byte(`name: tool
entry: [invoke]
nodes:
 - id: invoke
   type: invoke_tool
   args:
     tool: generate_image
     params: {prompt: sk-private-prompt, environment: staging}
`))
	require.NoError(t, err)
	card := strings.Join(nodePrivilegeDisclosure(w.Nodes[0], nil, "workflow", nil), "\n")
	require.Contains(t, card, "Invoked tool: generate_image")
	require.Contains(t, card, "environment=staging")
	require.NotContains(t, card, "sk-private-prompt")
	require.Equal(t, "https://api.example.com/repos/acme/app", discloseScope("https://api.example.com/repos/acme/app?key=opaque-credential#private"))
	require.Equal(t, unknownScope, discloseScope("https://user:pass@example.com/private"))
}

func TestActivationSourceScopeDisclosureChangesVisibly(t *testing.T) {
	row := &core.Trigger{Kind: core.TriggerKindIntegration, Filter: "secret-filter-literal"}
	summary := func(repo string) string {
		raw, err := json.Marshal(core.IntegrationConfig{Integration: "github", Events: []string{"issues.opened"}, Match: map[string]string{"repository": repo}, MatchAny: map[string][]string{"channel": {"C012345"}}})
		require.NoError(t, err)
		row.Config = raw
		return sourceDisclosure(row)
	}
	a, b := summary("acme/api"), summary("acme/releases")
	require.NotEqual(t, a, b)
	require.Contains(t, a, "acme/api")
	require.Contains(t, b, "acme/releases")
	require.Contains(t, a, "issues.opened")
	require.Contains(t, a, "C012345")
	require.NotContains(t, a, "secret-filter-literal")
	require.Contains(t, a, "predicate scope dynamic/unknown")
	row.Config = []byte(`{"integration":"github","events":["issues.opened"]}`)
	require.Contains(t, sourceDisclosure(row), "dynamic/unbounded")
	row.Kind = core.TriggerKindSchedule
	row.Config = []byte(`{"cron":["0 9 * * 1-5"],"timezone":"Europe/London"}`)
	require.Contains(t, sourceDisclosure(row), "0 9 * * 1-5")
	require.Contains(t, sourceDisclosure(row), "Europe/London")
}
