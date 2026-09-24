// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func joinErrors(t *testing.T, src string) []*Error {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	result := NewResult()
	ValidateCELWithCompilation(wf, result, nil)
	var out []*Error
	for _, e := range result.Errors() {
		if e.Category == CategoryStructure {
			out = append(out, e)
		}
	}
	return out
}

// The migrate.yaml shape that hung every run: a branch that ends at exactly
// one of two nodes, both wired into one all-join with an unrelated source.
const exclusiveAllJoin = `
name: t
entry: [start]
nodes:
  - {id: start, type: save_message, args: {role: user, content: hi}}
  - {id: other, type: save_message, args: {role: user, content: hi}}
  - {id: analyze, type: save_message, args: {role: user, content: hi}}
  - {id: build, type: save_message, args: {role: user, content: hi}}
  - {id: none, type: save_message, args: {role: user, content: hi}}
  - {id: done, type: join, condition: %s}
  - {id: after, type: save_message, args: {role: user, content: hi}}
edges:
  - {from: start, default: [other]}
  - {from: start, default: [analyze]}
  - from: analyze
    cases:
      - {to: [build], condition: "inputs.x"}
    default: [none]
  - {from: other, default: [done]}
  - {from: build, default: [done]}
  - {from: none, default: [done]}
  - {from: done, default: [after]}
inputs:
  x: {type: boolean, default: false}
`

func TestAllJoinOverExclusiveBranches_IsError(t *testing.T) {
	t.Parallel()
	errs := joinErrors(t, sprintf(exclusiveAllJoin, "all"))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "all-join 'done' can never be satisfied")
	assert.Contains(t, errs[0].Message, "edge from 'analyze'")
}

func TestAnyJoinOverExclusiveBranches_IsFine(t *testing.T) {
	t.Parallel()
	assert.Empty(t, joinErrors(t, sprintf(exclusiveAllJoin, "any")))
}

// Parallel fan-out into an all-join (the normal use) must not be flagged,
// including when one parallel branch itself contains a choice that
// re-converges before the join.
func TestAllJoinOverParallelBranches_IsFine(t *testing.T) {
	t.Parallel()
	errs := joinErrors(t, `
name: t
entry: [start]
nodes:
  - {id: start, type: save_message, args: {role: user, content: hi}}
  - {id: a, type: save_message, args: {role: user, content: hi}}
  - {id: b, type: save_message, args: {role: user, content: hi}}
  - {id: b1, type: save_message, args: {role: user, content: hi}}
  - {id: b2, type: save_message, args: {role: user, content: hi}}
  - {id: bdone, type: join, condition: any}
  - {id: done, type: join}
edges:
  - {from: start, default: [a]}
  - {from: start, default: [b]}
  - from: b
    cases:
      - {to: [b1], condition: "inputs.x"}
    default: [b2]
  - {from: b1, default: [bdone]}
  - {from: b2, default: [bdone]}
  - {from: a, default: [done]}
  - {from: bdone, default: [done]}
inputs:
  x: {type: boolean, default: false}
`)
	assert.Empty(t, errs)
}

// A condition-SKIPPED source still publishes a completion, so a skippable
// source never makes an all-join unsatisfiable.
func TestAllJoinWithConditionalSource_IsFine(t *testing.T) {
	t.Parallel()
	errs := joinErrors(t, `
name: t
entry: [start]
nodes:
  - {id: start, type: save_message, args: {role: user, content: hi}}
  - {id: a, type: save_message, condition: "inputs.x", args: {role: user, content: hi}}
  - {id: b, type: save_message, args: {role: user, content: hi}}
  - {id: done, type: join}
edges:
  - {from: start, default: [a]}
  - {from: start, default: [b]}
  - {from: a, default: [done]}
  - {from: b, default: [done]}
inputs:
  x: {type: boolean, default: false}
`)
	assert.Empty(t, errs)
}

var sprintf = fmt.Sprintf
