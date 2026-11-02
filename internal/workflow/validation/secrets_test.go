package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func parseForTest(src string) (*reliantv1.Workflow, error) { return wfyaml.ParseWorkflow([]byte(src)) }

func secretWorkflow(nodes string) string {
	return `
name: secret-wf
entry: [call]
inputs:
  base: {type: string, default: "https://api.example.com"}
  env: {type: string, default: "trader/staging"}
secrets:
  TRADER_KEY:
    environment: "{{ inputs.env }}"
nodes:
` + nodes + "\n"
}

func secretErrors(t *testing.T, source string) string {
	t.Helper()
	return errorsText(staticAnalysisOf(t, source))
}

func TestSecretsAreAllowedInNonModelSteps(t *testing.T) {
	allowed := map[string]string{
		"action url, header and body": `  - id: call
    type: action
    uses: http/request@1
    with:
      url: "{{ inputs.base }}/x?k={{ secrets.TRADER_KEY }}"
      method: POST
      headers: {X-API-Key: "{{ secrets.TRADER_KEY }}", Authorization: "Bearer {{ secrets.TRADER_KEY }}"}
      body: {key: "{{ secrets.TRADER_KEY }}", nested: [{k: "{{ secrets.TRADER_KEY }}"}]}
`,
		"action connection-less integration input": `  - id: call
    type: action
    uses: http/request@1
    with:
      url: "https://x.example/"
      query: {token: "{{ secrets.TRADER_KEY }}"}
`,
		"run command": `  - id: call
    type: run
    command: 'curl -H "X-API-Key: {{ secrets.TRADER_KEY }}" https://x.example/'
`,
		"run command by environment variable": `  - id: call
    type: run
    command: 'curl -H "X-API-Key: $TRADER_KEY" https://x.example/'
`,
	}
	for name, node := range allowed {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, "", secretErrors(t, secretWorkflow(node)))
		})
	}
}

func TestSecretsAreRefusedWhereAModelOrTheGraphReadsThem(t *testing.T) {
	const header = "secret-wf.nodes"
	refused := map[string]string{
		"call_llm system prompt": `  - id: call
    type: call_llm
    args:
      model: {id: m}
      system_prompt: "key is {{ secrets.TRADER_KEY }}"
`,
		"call_llm message": `  - id: call
    type: call_llm
    args:
      model: {id: m}
      messages: [{role: user, content: "{{ secrets.TRADER_KEY }}"}]
`,
		"router prompt": `  - id: call
    type: router
    args:
      system_prompt: "{{ secrets.TRADER_KEY }}"
      workflows: [{ref: builtin://agent}]
`,
		"save_message node": `  - id: call
    type: save_message
    args:
      role: user
      content: "{{ secrets.TRADER_KEY }}"
`,
		"save_message on a step": `  - id: call
    type: compute
    outputs: {a: "1"}
    save_message:
      role: user
      content: "{{ secrets.TRADER_KEY }}"
`,
		"tools_config": `  - id: call
    type: call_llm
    args:
      model: {id: m}
      tools_config:
        tools:
          shell: {timeout: "{{ secrets.TRADER_KEY }}"}
`,
		"response tool description": `  - id: call
    type: call_llm
    args:
      model: {id: m}
      response_tool: {name: r, description: "{{ secrets.TRADER_KEY }}", schema: {type: object}}
`,
		"compute": `  - id: call
    type: compute
    outputs: {leaked: "{{ secrets.TRADER_KEY }}"}
`,
		"node condition": `  - id: call
    type: compute
    condition: "secrets.TRADER_KEY != ''"
    outputs: {a: "1"}
`,
		"invoke_tool params": `  - id: call
    type: invoke_tool
    tool: get_run
    params: {id: "{{ secrets.TRADER_KEY }}"}
`,
		"ask_question": `  - id: call
    type: ask_question
    args:
      metadata: "{{ secrets.TRADER_KEY }}"
`,
		"workflow node args": `  - id: call
    type: workflow
    ref: builtin://agent
    args: {prompt: "{{ secrets.TRADER_KEY }}"}
`,
		"git_publish message": `  - id: call
    type: git_publish
    worktree: /tmp/x
    repo: o/r
    branch: b
    message: "{{ secrets.TRADER_KEY }}"
`,
	}
	for name, node := range refused {
		t.Run(name, func(t *testing.T) {
			got := secretErrors(t, secretWorkflow(node))
			require.Contains(t, got, "secrets.NAME may not be used here", "%s accepted a secret:\n%s", header, got)
		})
	}
}

func TestSecretsAreRefusedInWorkflowOutputsEdgesAndLoopControls(t *testing.T) {
	outputs := `
name: secret-wf
entry: [call]
secrets:
  TRADER_KEY: {environment: trader/staging}
outputs:
  leaked: "{{ secrets.TRADER_KEY }}"
nodes:
  - id: call
    type: compute
    outputs: {a: "1"}
`
	require.Contains(t, secretErrors(t, outputs), "may not be used here")

	edge := `
name: secret-wf
entry: [a]
secrets:
  TRADER_KEY: {environment: trader/staging}
nodes:
  - id: a
    type: compute
    outputs: {a: "1"}
  - id: b
    type: compute
    outputs: {b: "1"}
edges:
  - from: a
    cases:
      - condition: "secrets.TRADER_KEY == 'x'"
        to: b
`
	require.Contains(t, secretErrors(t, edge), "may not be used here")

	loopItems := secretWorkflow(`  - id: call
    type: loop
    parallel: true
    items: "{{ [secrets.TRADER_KEY] }}"
    inline:
      entry: [x]
      nodes:
        - id: x
          type: compute
          outputs: {a: "1"}
`)
	require.Contains(t, secretErrors(t, loopItems), "secrets")
}

func TestSecretsInLoopBodiesAndSubWorkflows(t *testing.T) {
	loop := strings.Replace(secretWorkflow(`  - id: each
    type: loop
    parallel: true
    items: "{{ [1, 2] }}"
    inline:
      entry: [call]
      nodes:
        - id: call
          type: action
          uses: http/request@1
          with:
            url: "https://x.example/"
            headers: {X-API-Key: "{{ secrets.TRADER_KEY }}"}
`), "entry: [call]", "entry: [each]", 1)
	require.Equal(t, "", secretErrors(t, loop), "a loop body is part of the workflow: it uses the root's declarations")

	leaky := strings.Replace(loop, `headers: {X-API-Key: "{{ secrets.TRADER_KEY }}"}`, `headers: {X-API-Key: "{{ secrets.TRADER_KEY }}"}
            body: "{{ secrets.TRADER_KEY }}"`, 1)
	require.Equal(t, "", secretErrors(t, leaky), "a request body is an allowed place")

	// A model call inside the same loop body is not.
	withModel := strings.Replace(loop, "      nodes:\n", `      nodes:
        - id: ask
          type: call_llm
          args: {model: {id: m}, system_prompt: "{{ secrets.TRADER_KEY }}"}
`, 1)
	require.Contains(t, secretErrors(t, withModel), "may not be used here")

	// A declaration inside an inline body is parsed (it is a workflow) and refused.
	declaredInBody := strings.Replace(loop, "    inline:\n      entry: [call]\n", "    inline:\n      secrets:\n        OTHER: {environment: a/b}\n      entry: [call]\n", 1)
	got := secretErrors(t, declaredInBody)
	require.True(t, strings.Contains(got, "declared once") || strings.Contains(got, "unknown"), "an inline body's own declaration was accepted:\n%s", got)
}

func TestUndeclaredSecretAndIrregularReads(t *testing.T) {
	undeclared := `  - id: call
    type: action
    uses: http/request@1
    with:
      url: "https://x.example/"
      headers: {X-Key: "{{ secrets.OTHER_KEY }}"}
`
	require.Contains(t, secretErrors(t, secretWorkflow(undeclared)), "secrets.OTHER_KEY is not declared")

	for name, hole := range map[string]string{
		"indexed":      `{{ secrets['TRADER_KEY'] }}`,
		"function":     `{{ secrets.TRADER_KEY.upperAscii() }}`,
		"concatenated": `{{ 'a' + secrets.TRADER_KEY }}`,
		"whole map":    `{{ secrets }}`,
	} {
		t.Run(name, func(t *testing.T) {
			node := `  - id: call
    type: action
    uses: http/request@1
    with:
      url: "https://x.example/"
      headers: {X-Key: "` + hole + `"}
`
			got := secretErrors(t, secretWorkflow(node))
			require.NotEmpty(t, got, "an irregular read of secrets was accepted")
		})
	}
}

func TestSecretDeclarationRules(t *testing.T) {
	for name, tc := range map[string]struct{ decl, want string }{
		"no environment": {"  TRADER_KEY: {}", "required"},
		"bad name":       {`  "BAD-NAME": {environment: a/b}`, "letters, digits and underscores"},
		"reserved name":  {"  PATH: {environment: a/b}", "cannot be a secret's name"},
		"reliant prefix": {"  RELIANT_TOKEN: {environment: a/b}", "cannot be a secret's name"},
		"node-derived":   {`  TRADER_KEY: {environment: "{{ nodes.x.env }}"}`, "inputs only"},
		"iter-derived":   {`  TRADER_KEY: {environment: "{{ iter.item }}"}`, "inputs only"},
	} {
		t.Run(name, func(t *testing.T) {
			src := "name: s\nentry: [call]\nsecrets:\n" + tc.decl + "\nnodes:\n  - id: call\n    type: compute\n    outputs: {a: \"1\"}\n"
			require.Contains(t, secretErrors(t, src), tc.want)
		})
	}
}

func TestSecretRefsAreParserOutputNotAuthored(t *testing.T) {
	src := secretWorkflow(`  - id: call
    type: run
    command: echo hi
    secret_refs: [{name: TRADER_KEY}]
`)
	_, err := parseForTest(src)
	require.Error(t, err, "secret_refs is bound from the declarations; it cannot be written")
}
