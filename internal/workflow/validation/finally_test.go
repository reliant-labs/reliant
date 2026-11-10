// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// finally is held to on_failure's promises, and reads `run` as well as
// `failure`.
func finallySrc(content string) string {
	return strings.Replace(strings.Replace(onFailureBase, "on_failure:", "finally:", 1), "%s", content, 1)
}

func TestFinally_RunAndFailureCompileInTheHandler(t *testing.T) {
	errs := computeFindings(t, finallySrc("{{ run.status == 'cancelled' ? 'stopped' : run.status }} {{ failure.node_id }} {{ failure.error_code }} {{ inputs.who }}"))
	assert.Empty(t, errs, "%v", messages(errs))
}

func TestFinally_RunIsUndeclaredOutsideTheHandler(t *testing.T) {
	// Not in the main graph...
	src := strings.Replace(finallySrc("x"), `label: "{{ 'l' }}"`, `label: "{{ run.status }}"`, 1)
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "run"), "run must not compile in the main graph: %v", messages(errs))

	// ...and not in on_failure, which never sees a cancel or a success.
	errs = computeFindings(t, onFailureSrc("{{ run.status }}"))
	assert.True(t, hasFinding(errs, "run"), "run must not compile in on_failure: %v", messages(errs))
}

func TestFinally_UnguardedReadOfAMainNodeIsAnError(t *testing.T) {
	errs := computeFindings(t, finallySrc("{{ nodes.later.label }}"))
	assert.True(t, hasFinding(errs, "not guaranteed to have executed"), "a cancel can stop the graph anywhere: %v", messages(errs))

	errs = computeFindings(t, finallySrc("{{ nodes.?later.label.orValue('-') }}"))
	assert.Empty(t, errs, "%v", messages(errs))
}

func TestFinally_StructureRules(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"id collides with the main graph": {
			strings.Replace(finallySrc("x"), "- id: note", "- id: later", 1), "inside finally, nodes.later would be ambiguous"},
		"reserved id": {
			strings.Replace(finallySrc("x"), "- id: later", "- id: finally", 1), "node id 'finally' is reserved"},
		"waits for a person": {`
name: guarded
entry: [work]
nodes:
  - id: work
    type: compute
    outputs: {n: "{{ 1 }}"}
finally:
  entry: [ask]
  nodes:
    - id: ask
      type: approval
      args:
        title: "ok?"
`, "cannot be part of finally: the run has ended and nobody is answering"},
		"has outputs": {
			finallySrc("x") + "  outputs:\n    a: \"{{ 1 }}\"\n", "a finally handler has no outputs"},
		"nested finally": {
			finallySrc("x") + "  finally:\n    entry: [z]\n    nodes:\n      - id: z\n        type: compute\n        outputs:\n          q: \"{{ 1 }}\"\n",
			"a finally handler cannot declare its own finally"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := computeFindings(t, tc.src)
			assert.True(t, hasFinding(errs, tc.want), "%v", messages(errs))
		})
	}
}

// Both handlers together are fine, and their node ids live in separate
// namespaces (on_failure.<id>, finally.<id>).
func TestFinally_BesideOnFailure(t *testing.T) {
	src := onFailureSrc("{{ failure.node_id }}") + `finally:
  entry: [note]
  nodes:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "{{ run.status }}"
`
	errs := computeFindings(t, src)
	assert.Empty(t, errs, "%v", messages(errs))
}
