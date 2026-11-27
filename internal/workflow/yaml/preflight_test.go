// Copyright (c) 2026 Reliant Labs
package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

const preflightYAML = `
name: pf
entry: [n]
nodes:
  - id: n
    type: run
    command: make
    preflight: %s
`

func parsePreflight(t *testing.T, value string) (*reliantv1.Node, error) {
	t.Helper()
	wf, err := ParseWorkflow([]byte(strings.Replace(preflightYAML, "%s", value, 1)))
	if err != nil {
		return nil, err
	}
	return wf.GetNodes()[0], nil
}

// preflight is true, false or a mapping of overrides; each survives a round
// trip, and false (the default) is not written.
func TestPreflight_ParsesAndRoundTrips(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]*reliantv1.NodePreflight{
		"true":  {},
		"false": nil,
		"{tools: [go, task], min_free_disk: 10GiB, max_load: 2.5, allow_root: true, max_machines: 2}": {
			Tools: []string{"go", "task"}, MinFreeDisk: "10GiB", MaxLoad: 2.5, AllowRoot: true, MaxMachines: 2,
		},
	} {
		t.Run(value, func(t *testing.T) {
			node, err := parsePreflight(t, value)
			require.NoError(t, err)
			assert.True(t, proto.Equal(want, node.GetPreflight()), "got %v, want %v", node.GetPreflight(), want)

			out, err := MarshalWorkflow(&reliantv1.Workflow{Name: "pf", Entry: []string{"n"}, Nodes: []*reliantv1.Node{node}})
			require.NoError(t, err)
			if want == nil {
				assert.NotContains(t, string(out), "preflight")
				return
			}
			if len(want.GetTools()) == 0 {
				assert.Contains(t, string(out), "preflight: true")
			}
			again, err := ParseWorkflow(out)
			require.NoError(t, err)
			assert.True(t, proto.Equal(want, again.GetNodes()[0].GetPreflight()), "round trip: %s", out)
		})
	}
}

func TestPreflight_Refusals(t *testing.T) {
	t.Parallel()
	for value, msg := range map[string]string{
		`"yes"`:                    "preflight must be true, false or a mapping",
		"[git]":                    "preflight must be true, false or a mapping",
		"{tools: go}":              "preflight.tools must be a list",
		"{min_free_disk: lots}":    "preflight.min_free_disk",
		"{max_load: high}":         "preflight.max_load must be a number",
		"{allow_root: sometimes}":  "preflight.allow_root must be true or false",
		"{max_machines: 0.5}":      "preflight.max_machines must be a whole number",
		"{max_machines: 50}":       "max_machines must be between 1 and 20",
		"{timeout: 5m}":            `unknown preflight field: "timeout"`,
		"{tools: [go], tools: []}": "preflight.tools is set twice",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := parsePreflight(t, value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), msg)
		})
	}
}
