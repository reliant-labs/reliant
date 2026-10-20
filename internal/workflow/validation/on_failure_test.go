// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const onFailureBase = `
name: guarded
inputs:
  who: {type: string, default: "x"}
entry: [work]
nodes:
  - id: work
    type: compute
    outputs:
      n: "{{ 1 }}"
  - id: later
    type: compute
    outputs:
      label: "{{ 'l' }}"
edges:
  - from: work
    default: later
on_failure:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "%s"
`

func onFailureSrc(content string) string { return strings.Replace(onFailureBase, "%s", content, 1) }

func TestOnFailure_FailureNamespaceCompilesInTheHandler(t *testing.T) {
	errs := computeFindings(t, onFailureSrc("failed at {{ failure.node_id }} ({{ failure.error_code }}): {{ failure.error }} #{{ failure.attempt + 1 }} {{ failure.node_path }} {{ inputs.who }}"))
	assert.Empty(t, errs, "%v", messages(errs))
}

func TestOnFailure_UnknownFailureFieldIsCaught(t *testing.T) {
	// failure is a map like trigger; only the namespace is typed. A typo in the
	// namespace itself must still fail.
	errs := computeFindings(t, onFailureSrc("{{ faliure.node_id }}"))
	assert.True(t, hasFinding(errs, "faliure"), "%v", messages(errs))
}

func TestOnFailure_FailureIsUndeclaredInTheMainGraph(t *testing.T) {
	src := strings.Replace(onFailureSrc("x"), `label: "{{ 'l' }}"`, `label: "{{ failure.node_id }}"`, 1)
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "failure"), "failure must not compile outside the handler: %v", messages(errs))
}

func TestOnFailure_UnguardedReadOfAMainNodeIsAnError(t *testing.T) {
	errs := computeFindings(t, onFailureSrc("{{ nodes.later.label }}"))
	assert.True(t, hasFinding(errs, "not guaranteed to have executed"), "an unguarded read of a node that may not have run: %v", messages(errs))
}

func TestOnFailure_GuardedReadOfAMainNodeIsClean(t *testing.T) {
	errs := computeFindings(t, onFailureSrc("{{ has(nodes.later) ? nodes.later.label : 'none' }} {{ nodes.?work.n.orValue(0) }}"))
	assert.Empty(t, errs, "%v", messages(errs))
}

func TestOnFailure_NodeIDsMustNotCollideWithTheMainGraph(t *testing.T) {
	src := strings.Replace(onFailureSrc("x"), "- id: note", "- id: later", 1)
	src = strings.Replace(src, "entry: [note]", "entry: [later]", 1)
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "also a node of the main graph"), "%v", messages(errs))
}

func TestOnFailure_HandlerCannotWaitForAPerson(t *testing.T) {
	src := `
name: guarded
entry: [work]
nodes:
  - id: work
    type: compute
    outputs: {n: "{{ 1 }}"}
on_failure:
  entry: [ask]
  nodes:
    - id: ask
      type: approval
      args:
        title: "ok?"
`
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "nobody is answering"), "%v", messages(errs))
}

func TestOnFailure_NoNestedHandlerNoOutputsNoInputs(t *testing.T) {
	src := `
name: guarded
entry: [work]
nodes:
  - id: work
    type: compute
    outputs: {n: "{{ 1 }}"}
on_failure:
  entry: [note]
  inputs:
    x: {type: string, default: ""}
  outputs:
    y: "{{ 1 }}"
  nodes:
    - id: note
      type: compute
      outputs: {z: "{{ 1 }}"}
  on_failure:
    entry: [n2]
    nodes:
      - id: n2
        type: compute
        outputs: {z: "{{ 1 }}"}
`
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "cannot declare its own on_failure"), "%v", messages(errs))
	assert.True(t, hasFinding(errs, "no outputs"), "%v", messages(errs))
	assert.True(t, hasFinding(errs, "shares the workflow's inputs"), "%v", messages(errs))
}

func TestOnFailure_SyntaxErrorInTheHandlerIsReported(t *testing.T) {
	errs := computeFindings(t, onFailureSrc("{{ failure.node_id "))
	assert.NotEmpty(t, errs)
}

func TestOnFailure_ReservedNodeID(t *testing.T) {
	src := strings.Replace(onFailureSrc("x"), "- id: later", "- id: on_failure", 1)
	src = strings.Replace(src, "default: later", "default: on_failure", 1)
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "reserved"), "%v", messages(errs))
}
