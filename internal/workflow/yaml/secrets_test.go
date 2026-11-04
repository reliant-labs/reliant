package wfyaml

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretsYAML = `
name: secrets
inputs:
  env: {type: string, default: "trader/staging"}
secrets:
  TRADER_KEY:
    environment: "{{ inputs.env }}"
  DEPLOY_TOKEN:
    environment: ops/prod
entry: [call, ship, each]
nodes:
  - id: call
    type: action
    uses: http/request@1
    with:
      url: https://example.com
      headers:
        X-Key: "{{ secrets.TRADER_KEY }}"
  - id: plain
    type: action
    uses: http/request@1
    with:
      url: https://example.com
  - id: ship
    type: run
    command: deploy
  - id: gh
    type: action
    uses: github/user.get@1
    credential: {secret: DEPLOY_TOKEN}
  - id: each
    type: loop
    items: "{{ [1] }}"
    inline:
      entry: [inner]
      nodes:
        - id: inner
          type: action
          uses: http/request@1
          with:
            url: https://example.com
            headers: {Authorization: "Bearer {{ secrets.DEPLOY_TOKEN }}"}
            body: {note: "{{ secrets.TRADER_KEY }}"}
`

func TestSecretsBlockParsesAndBindsRefsToTheNodesThatNeedThem(t *testing.T) {
	wf, err := ParseWorkflow([]byte(secretsYAML))
	require.NoError(t, err)

	require.Len(t, wf.GetSecrets(), 2)
	assert.Equal(t, "ops/prod", wf.GetSecrets()["DEPLOY_TOKEN"].GetEnvironment().GetLiteral())
	assert.Equal(t, "{{ inputs.env }}", wf.GetSecrets()["TRADER_KEY"].GetEnvironment().GetExpr())

	byID := map[string][]string{}
	for _, n := range wf.GetNodes() {
		for _, r := range n.GetSecretRefs() {
			byID[n.GetId()] = append(byID[n.GetId()], r.GetName())
		}
	}
	assert.Equal(t, []string{"TRADER_KEY"}, byID["call"], "an action gets the secrets its header slots name")
	assert.Empty(t, byID["plain"])
	assert.Empty(t, byID["ship"], "a run step gets no secret: its command could print it")
	assert.Equal(t, []string{"DEPLOY_TOKEN"}, byID["gh"], "an action gets its credential's secret")
	assert.Equal(t, "DEPLOY_TOKEN", wf.GetNodes()[3].GetAction().GetCredential().GetSecret())

	inner := wf.GetNodes()[4].GetLoop().GetInline().GetNodes()[0]
	require.Len(t, inner.GetSecretRefs(), 1, "inline bodies use the root's declarations; a body hole binds nothing")
	assert.Equal(t, "DEPLOY_TOKEN", inner.GetSecretRefs()[0].GetName())
}

func TestSecretsBlockRoundTrips(t *testing.T) {
	wf, err := ParseWorkflow([]byte(secretsYAML))
	require.NoError(t, err)
	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "TRADER_KEY")
	assert.Contains(t, string(out), "ops/prod")
	assert.NotContains(t, string(out), "secret_refs", "bound refs are derived, not authored")

	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.Len(t, again.GetSecrets(), 2)
	assert.Empty(t, again.GetNodes()[2].GetSecretRefs())
	assert.Equal(t, "DEPLOY_TOKEN", again.GetNodes()[3].GetAction().GetCredential().GetSecret(), "credential round-trips")
	assert.Len(t, again.GetNodes()[3].GetSecretRefs(), 1)
}

func TestSecretsBlockRejectsMalformedDeclarations(t *testing.T) {
	for name, block := range map[string]string{
		"scalar":        "secrets: nope",
		"list":          "secrets: [A]",
		"entry scalar":  "secrets:\n  A: ops/prod",
		"unknown field": "secrets:\n  A:\n    environment: ops/prod\n    value: hunter2",
		"duplicate":     "secrets:\n  A: {environment: x}\n  A: {environment: y}",
	} {
		t.Run(name, func(t *testing.T) {
			src := "name: s\nentry: [a]\n" + block + "\nnodes:\n  - id: a\n    type: run\n    command: ls\n"
			_, err := ParseWorkflow([]byte(src))
			require.Error(t, err)
		})
	}
}
