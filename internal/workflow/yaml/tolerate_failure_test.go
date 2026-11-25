// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const tolerateFailureYAML = `
name: optional-reader
entry: [reader]
nodes:
  - id: reader
    type: compute
    tolerate_failure: %s
    outputs:
      n: "{{ 1 }}"
`

// tolerate_failure is a node-level boolean (reliant#2752): it parses, survives
// a round trip, and refuses anything that is not true or false.
func TestTolerateFailure_ParsesAndRoundTrips(t *testing.T) {
	t.Parallel()

	wf, err := ParseWorkflow([]byte(strings.Replace(tolerateFailureYAML, "%s", "true", 1)))
	require.NoError(t, err)
	require.Len(t, wf.GetNodes(), 1)
	assert.True(t, wf.GetNodes()[0].GetTolerateFailure())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "tolerate_failure: true")
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.True(t, again.GetNodes()[0].GetTolerateFailure())

	off, err := ParseWorkflow([]byte(strings.Replace(tolerateFailureYAML, "%s", "false", 1)))
	require.NoError(t, err)
	assert.False(t, off.GetNodes()[0].GetTolerateFailure())
	out, err = MarshalWorkflow(off)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "tolerate_failure", "false is the default and is not written")

	for _, bad := range []string{`"yes"`, "maybe", "[true]"} {
		_, err := ParseWorkflow([]byte(strings.Replace(tolerateFailureYAML, "%s", bad, 1)))
		assert.Error(t, err, "tolerate_failure: %s must be refused", bad)
	}
}

// tolerate_failure is a base field of EVERY node kind, and the runtime honours
// it on activity steps too (runtime/tolerate_failure.go). The activity-kind
// allow-list once missed it, so action, call_llm and execute_tools nodes
// refused it as an unknown key. This walks every kind the parser binds.
func TestTolerateFailure_EveryNodeKindParsesAndRoundTrips(t *testing.T) {
	t.Parallel()

	kinds := make([]string, 0, len(generatedNodeBindingsByType))
	for kind := range generatedNodeBindingsByType {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for kind := range model.DiscoverNodeMetas() {
		assert.Contains(t, kinds, kind, "node kind %q has no YAML binding to test", kind)
	}

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("name: tolerate-%s\nentry: [n]\nnodes:\n  - id: n\n    type: %s\n    tolerate_failure: true\n", kind, kind)
			wf, err := ParseWorkflow([]byte(src))
			require.NoError(t, err, "tolerate_failure must parse on a %s node", kind)
			require.Len(t, wf.GetNodes(), 1)
			assert.True(t, wf.GetNodes()[0].GetTolerateFailure(), "tolerate_failure on %s", kind)

			out, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			again, err := ParseWorkflow(out)
			require.NoError(t, err, "re-parse of:\n%s", out)
			assert.True(t, proto.Equal(wf, again), "round trip of a %s node changed it:\n%s", kind, out)
		})
	}
}
