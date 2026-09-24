// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One test per gap in specs/template-validation-gaps.md "Recommended first
// batch", each using the spec's own repro. Every "flags" case is a workflow
// that validated clean before these checks and fails at run time; every
// "allows" case is the guarded form that must stay clean.

func gapFindings(t *testing.T, src string) (errs, warns []*Error) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	result := NewResult()
	ValidateCELWithCompilation(wf, result, nil)
	return result.Errors(), result.Warnings()
}

func findingContaining(list []*Error, substr string) *Error {
	for _, e := range list {
		if strings.Contains(e.Message, substr) {
			return e
		}
	}
	return nil
}

func messages(list []*Error) []string {
	var out []string
	for _, e := range list {
		out = append(out, strings.Join(e.Path, ".")+": "+e.Message)
	}
	return out
}

// ---------------------------------------------------------------------------
// G1: unguarded refs to maybe-skipped nodes are errors
// ---------------------------------------------------------------------------

func TestG1_ConditionSkippedNodeFieldReadInDeclaredOutput(t *testing.T) {
	t.Parallel()
	// The spec's repro: b is skipped, its output is {skipped: true}, and
	// `response_text + '!'` is not a bare path the typed-zero rescue covers.
	errs, _ := gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: b, type: call_llm, condition: "false", args: {model: claude-4-sonnet}}
edges: [{from: a, to: b}]
outputs:
  t: "{{nodes.b.response_text + '!'}}"
`)
	e := findingContaining(errs, "a skipped node's output has no 'response_text'")
	require.NotNil(t, e, "expected an error, got %v", messages(errs))
	assert.Equal(t, CategoryConditionalAccess, e.Category)
}

func TestG1_RouterSkippedNodeReadInInjectIsError(t *testing.T) {
	t.Parallel()
	// The F1 shape (pitch-deck drafts): a router can dispatch past scrape.
	errs, _ := gapFindings(t, `
name: probe
entry: [classify]
nodes:
  - id: classify
    type: router
    model: {tags: [fast]}
    nodes:
      - {id: scrape, description: a}
      - {id: plan, description: b}
  - {id: scrape, type: call_llm, args: {model: {tags: [fast]}}}
  - id: plan
    type: call_llm
    args:
      model: {tags: [fast]}
      system_prompt: "{{nodes.scrape.response_text}}"
edges: [{from: scrape, to: plan}]
`)
	e := findingContaining(errs, "node 'scrape' is not guaranteed to have executed")
	require.NotNil(t, e, "expected an ordering error, got %v", messages(errs))
	assert.Equal(t, CategoryNodeOrdering, e.Category)
}

func TestG1_RouterSkippedNodeReadInDeclaredOutputIsError(t *testing.T) {
	t.Parallel()
	// The F2 shape (one-ring plan_summary): declared outputs evaluate at
	// completion, and the node may never have run.
	errs, _ := gapFindings(t, `
name: probe
entry: [classify]
nodes:
  - id: classify
    type: router
    model: {tags: [fast]}
    nodes:
      - {id: planning, description: a}
      - {id: impl, description: b}
  - {id: planning, type: call_llm, args: {model: {tags: [fast]}}}
  - {id: impl, type: call_llm, args: {model: {tags: [fast]}}}
edges: [{from: planning, to: impl}]
outputs:
  plan_summary: "{{nodes.planning.response_text}}"
`)
	require.NotNil(t, findingContaining(errs, "when the workflow completes"), "got %v", messages(errs))
}

func TestG1_GuardedFormsAreClean(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: b, type: call_llm, condition: "false", args: {model: claude-4-sonnet}}
edges: [{from: a, to: b}]
outputs:
  t1: "{{has(nodes.b.response_text) ? nodes.b.response_text + '!' : ''}}"
  t2: "{{nodes.b.?response_text.orValue('') + '!'}}"
`)
	assert.Empty(t, errs, "%v", messages(errs))
}

// The runtime's substituteTypedZero rescues a bare `nodes.<id>.<field>`
// declared output whose field is a list or map: exempt exactly that shape.
func TestG1_TypedZeroRescuedDeclaredOutputIsExempt(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: b, type: call_llm, condition: "false", args: {model: claude-4-sonnet}}
edges: [{from: a, to: b}]
outputs:
  calls: "{{nodes.b.tool_calls}}"
`)
	assert.Empty(t, errs, "a bare list-field output is rescued by substituteTypedZero: %v", messages(errs))

	// A scalar is NOT rescued (a substituted 0/"" would be a false value).
	errs, _ = gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: b, type: call_llm, condition: "false", args: {model: claude-4-sonnet}}
edges: [{from: a, to: b}]
outputs:
  text: "{{nodes.b.response_text}}"
`)
	assert.NotEmpty(t, errs, "a scalar field is not rescued")

	// Only a BARE path qualifies: an operator makes it a normal expression.
	errs, _ = gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: b, type: call_llm, condition: "false", args: {model: claude-4-sonnet}}
edges: [{from: a, to: b}]
outputs:
  n: "{{size(nodes.b.tool_calls)}}"
`)
	assert.NotEmpty(t, errs, "size(...) around the path is not the rescued shape")
}

// A field the skip output DOES supply (run nodes zero-fill exit_code) cannot
// fail, but a skip reads as "exit 0": a warning, not an error.
func TestG1_SkipSuppliedFieldIsWarning(t *testing.T) {
	t.Parallel()
	errs, warns := gapFindings(t, `
name: probe
entry: [a]
inputs:
  lint: {type: boolean, default: false}
nodes:
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: lint, type: run, condition: "inputs.lint", command: "true"}
  - {id: r, type: save_message, args: {role: user, content: "{{nodes.lint.exit_code}}"}}
edges: [{from: a, to: lint}, {from: lint, to: r}]
`)
	assert.Empty(t, errs, "%v", messages(errs))
	assert.NotNil(t, findingContaining(warns, "zero value from the skip output"), "%v", messages(warns))
}

// ---------------------------------------------------------------------------
// G2: per-site environments from the runtime contexts
// ---------------------------------------------------------------------------

func TestG2_NodeConfigCannotReadOutputOrOutputs(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: "{{outputs.foo}} {{output.bar}}"}}
`)
	assert.NotNil(t, findingContaining(errs, "undeclared reference to 'outputs'"), "%v", messages(errs))
	assert.NotNil(t, findingContaining(errs, "undeclared reference to 'output'"), "%v", messages(errs))
}

func TestG2_WhileIsCompiled(t *testing.T) {
	t.Parallel()
	// `while` used to get only heuristics; a typo in it is now a compile error.
	errs, _ := gapFindings(t, `
name: probe
entry: [l]
nodes:
  - id: l
    type: loop
    while: "outputs.doen != true && iter.iteraton < 3"
    inline:
      entry: [x]
      outputs:
        done: "{{nodes.x.response_text == 'done'}}"
      nodes:
        - {id: x, type: call_llm, args: {model: {tags: [fast]}}}
`)
	assert.NotNil(t, findingContaining(errs, "undefined field 'doen'"), "%v", messages(errs))
	assert.NotNil(t, findingContaining(errs, "undefined field 'iteraton'"), "%v", messages(errs))
}

func TestG2_WhileCannotReadBodyNodes(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [l]
nodes:
  - id: l
    type: loop
    while: "nodes.x.response_text != 'done' && iter.iteration < 3"
    inline:
      entry: [x]
      outputs:
        text: "{{nodes.x.response_text}}"
      nodes:
        - {id: x, type: call_llm, args: {model: {tags: [fast]}}}
`)
	assert.NotNil(t, findingContaining(errs, "a node of the loop body"), "%v", messages(errs))
}

func TestG2_LoopOutputsInBodyNeedAFirstIterationGuard(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [l]
nodes:
  - id: l
    type: loop
    while: "iter.iteration < 3"
    inline:
      entry: [x]
      outputs:
        feedback: "{{nodes.x.response_text}}"
      nodes:
        - id: x
          type: call_llm
          args:
            model: {tags: [fast]}
            system_prompt: "Last time: {{outputs.feedback}}"
`)
	assert.NotNil(t, findingContaining(errs, "on the first iteration `outputs` is empty"), "%v", messages(errs))

	for _, guarded := range []string{
		"{{has(outputs.feedback) ? outputs.feedback : ''}}",
		"{{iter.iteration > 0 ? outputs.feedback : ''}}",
		"{{iter.iteration == 0 ? '' : outputs.feedback}}",
	} {
		errs, _ = gapFindings(t, `
name: probe
entry: [l]
nodes:
  - id: l
    type: loop
    while: "iter.iteration < 3"
    inline:
      entry: [x]
      outputs:
        feedback: "{{nodes.x.response_text}}"
      nodes:
        - id: x
          type: call_llm
          args:
            model: {tags: [fast]}
            system_prompt: "`+guarded+`"
`)
		assert.Empty(t, errs, "%s: %v", guarded, messages(errs))
	}
}

// ---------------------------------------------------------------------------
// G3 + G4: response-tool fields in a node's own save_message
// ---------------------------------------------------------------------------

const g3Workflow = `
name: probe
entry: [a]
nodes:
  - id: a
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: audit
        schema: {type: object, properties: {approved: {type: boolean}, guidance: {type: string}}, required: [approved]}
  - id: b
    type: execute_tools
    args: {tool_calls: "{{nodes.a.tool_calls}}"}
    save_message: {role: user, content: "FEEDBACK: %s"}
edges: [{from: a, to: b}]
`

func TestG4_OutputResponseDataTypoInSaveMessage(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, strings.Replace(g3Workflow, "%s", "{{output.response_data.audit.guidanc}}", 1))
	assert.NotNil(t, findingContaining(errs, "has no field 'guidanc'"), "%v", messages(errs))
}

func TestG3_OptionalResponseToolFieldMustBeGuarded(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, strings.Replace(g3Workflow, "%s", "{{output.response_data.audit.guidance}}", 1))
	assert.NotNil(t, findingContaining(errs, "response-tool field 'guidance' is optional"), "%v", messages(errs))
	assert.NotNil(t, findingContaining(errs, "did not call the 'audit' response tool"), "%v", messages(errs))

	// Required field, tool guarded: clean.
	errs, _ = gapFindings(t, strings.Replace(g3Workflow, "%s",
		"{{output.response_data.audit != null ? string(output.response_data.audit.approved) : ''}}", 1))
	assert.Empty(t, errs, "%v", messages(errs))

	// Optional field guarded with has() (which also proves the tool non-null).
	errs, _ = gapFindings(t, strings.Replace(g3Workflow, "%s",
		"{{has(output.response_data.audit.guidance) ? output.response_data.audit.guidance : ''}}", 1))
	assert.Empty(t, errs, "%v", messages(errs))
}

// The save_message condition gates the content: a has() there counts.
func TestG3_SaveMessageConditionGuardsContent(t *testing.T) {
	t.Parallel()
	src := strings.Replace(g3Workflow, `    save_message: {role: user, content: "FEEDBACK: %s"}`, `    save_message:
      condition: "has(output.response_data.audit.guidance) && output.response_data.audit.guidance != ''"
      role: user
      content: "FEEDBACK: {{output.response_data.audit.guidance}}"`, 1)
	errs, _ := gapFindings(t, src)
	assert.Empty(t, errs, "%v", messages(errs))
}

// A schema with no `required` array at all is usually loose rather than
// deliberately all-optional: warn instead of error.
func TestG3_SchemaWithoutRequiredIsWarning(t *testing.T) {
	t.Parallel()
	src := strings.Replace(strings.Replace(g3Workflow, ", required: [approved]", "", 1),
		"%s", "{{output.response_data.audit != null ? output.response_data.audit.guidance : ''}}", 1)
	errs, warns := gapFindings(t, src)
	assert.Empty(t, errs, "%v", messages(errs))
	assert.NotNil(t, findingContaining(warns, "declares no `required` fields"), "%v", messages(warns))
}

// The same rules apply through nodes.<id>.response_data in another node.
func TestG3_NodesFormIsCheckedToo(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [a]
nodes:
  - id: a
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: audit
        schema: {type: object, properties: {approved: {type: boolean}, guidance: {type: string}}, required: [approved]}
  - id: b
    type: execute_tools
    args: {tool_calls: "{{nodes.a.tool_calls}}"}
  - id: c
    type: save_message
    args: {role: user, content: "{{nodes.b.response_data.audit.guidance}}"}
edges: [{from: a, to: b}, {from: b, to: c}]
`)
	assert.NotNil(t, findingContaining(errs, "response-tool field 'guidance' is optional"), "%v", messages(errs))
}

// ---------------------------------------------------------------------------
// G5: iter outside any loop
// ---------------------------------------------------------------------------

func TestG5_IterItemOutsideALoop(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: "{{iter.item.name}}"}}
`)
	assert.NotNil(t, findingContaining(errs, "undefined field 'item'"), "%v", messages(errs))

	// iter.iteration is always bound (0 outside a loop).
	errs, _ = gapFindings(t, `
name: probe
entry: [a]
nodes:
  - {id: a, type: save_message, args: {role: user, content: "{{iter.iteration}}"}}
`)
	assert.Empty(t, errs, "%v", messages(errs))
}

func TestG5_IterItemInsideAnItemsLoopIsFine(t *testing.T) {
	t.Parallel()
	errs, _ := gapFindings(t, `
name: probe
entry: [l]
inputs:
  things: {type: array, default: []}
nodes:
  - id: l
    type: loop
    parallel: true
    items: "{{inputs.things}}"
    inline:
      entry: [x]
      nodes:
        - id: x
          type: call_llm
          args:
            model: {tags: [fast]}
            system_prompt: "{{iter.item}} / {{iter.key}}"
`)
	assert.Empty(t, errs, "%v", messages(errs))

	// A counter loop (no items) has no item.
	errs, _ = gapFindings(t, `
name: probe
entry: [l]
nodes:
  - id: l
    type: loop
    while: "iter.iteration < 3"
    inline:
      entry: [x]
      nodes:
        - id: x
          type: call_llm
          args:
            model: {tags: [fast]}
            system_prompt: "{{iter.item}}"
`)
	assert.NotNil(t, findingContaining(errs, "undefined field 'item'"), "%v", messages(errs))
}
