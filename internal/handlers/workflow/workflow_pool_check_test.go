package workflow

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const poolCheckWorkflowLevel = `
name: pool-check
entry: [ask]
daemon: {pool: %s}
nodes:
  - id: ask
    type: call_llm
    args: {model: flagship}
`

const poolCheckNested = `
name: pool-check
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    daemon: {pool: gate}
    args: {model: flagship}
  - id: repeat
    type: loop
    while: "iter.iteration < 3"
    inline:
      entry: [inner]
      nodes:
        - id: inner
          type: call_llm
          daemon: {pool: gpu}
          args: {model: flagship}
edges:
  - {from: ask, default: [repeat]}
`

const poolCheckNone = `
name: pool-check
entry: [ask]
daemon: {name: build-box}
nodes:
  - id: ask
    type: call_llm
    daemon: {labels: {role: gate}}
    args: {model: flagship}
`

const poolCheckCEL = `
name: pool-check
entry: [ask]
inputs:
  machine: {type: string, default: build-box}
daemon: "{{ {'name': inputs.machine} }}"
nodes:
  - id: ask
    type: call_llm
    args: {model: flagship}
`

// noRefsLoader resolves no refs: the definitions here have none.
func noRefsLoader(*reliantv1.Workflow) v2.WorkflowLoader { return nil }

// checkMachinePools turns every literal `daemon: {pool: …}` the owner has no
// pool for into an error on its site, and nothing else.
func TestCheckMachinePools(t *testing.T) {
	owned := map[string]bool{"gate": true}
	tests := []struct {
		name       string
		yaml       string
		lookupErr  error
		wantErrors []string // "path|message substring"
		wantWarn   int
		wantLooked []string
	}{
		{name: "an unknown workflow-level pool is an error",
			yaml:       strings.Replace(poolCheckWorkflowLevel, "%s", "nope", 1),
			wantErrors: []string{`pool-check|no machine pool named "nope" — create it in Settings → Machines or fix the workflow's daemon: {pool: nope}`},
			wantLooked: []string{"nope"}},
		{name: "a known pool is valid",
			yaml:       strings.Replace(poolCheckWorkflowLevel, "%s", "gate", 1),
			wantLooked: []string{"gate"}},
		{name: "a node's unknown pool, in a loop body, names its path",
			yaml:       poolCheckNested,
			wantErrors: []string{`pool-check.nodes.repeat.inline.nodes.inner|no machine pool named "gpu"`},
			wantLooked: []string{"gate", "gpu"}},
		{name: "no pool checks nothing", yaml: poolCheckNone},
		{name: "a CEL selector is not checked", yaml: poolCheckCEL},
		{name: "a failed read is a warning, not an error",
			yaml:      strings.Replace(poolCheckWorkflowLevel, "%s", "gate", 1),
			lookupErr: errors.New("connection reset"), wantWarn: 1, wantLooked: []string{"gate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := validateWorkflowDefinitionWith([]byte(tt.yaml), noRefsLoader)
			require.NoError(t, check.parseErr)
			before := len(check.result.Errors())
			var looked []string
			checkMachinePools(check, func(name string) error {
				looked = append(looked, name)
				if tt.lookupErr != nil {
					return tt.lookupErr
				}
				if !owned[name] {
					return core.ErrMachinePoolNotFound
				}
				return nil
			})
			assert.Equal(t, tt.wantLooked, looked)
			var got []string
			for _, e := range check.result.Errors()[before:] {
				got = append(got, strings.Join(e.Path, ".")+"|"+e.Message)
			}
			require.Len(t, got, len(tt.wantErrors), "errors: %v", got)
			for i, want := range tt.wantErrors {
				path, msg, _ := strings.Cut(want, "|")
				gotPath, gotMsg, _ := strings.Cut(got[i], "|")
				assert.Equal(t, path, gotPath)
				assert.Contains(t, gotMsg, msg)
			}
			warns := 0
			for _, w := range check.result.Warnings() {
				if w.Field == "daemon.pool" {
					warns++
				}
			}
			assert.Equal(t, tt.wantWarn, warns)
		})
	}
}

// ValidateWorkflow, with the caller as the owner: an unknown pool is an
// error located on its node, a pool the caller has is valid, and a workflow
// with no pool is judged as before.
func TestValidateWorkflow_ChecksMachinePools(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	_, err := repo.PutMachinePool(ctx, &db.MachinePool{UserID: "test-user", Name: "gate", Selector: map[string]string{"pool": "gate"}})
	require.NoError(t, err)

	tests := []struct {
		name      string
		yaml      string
		wantValid bool
		wantNode  string
		wantMsg   string
	}{
		{name: "unknown workflow-level pool", yaml: strings.Replace(poolCheckWorkflowLevel, "%s", "nope", 1),
			wantMsg: `no machine pool named "nope"`},
		{name: "unknown node pool", yaml: poolCheckNested, wantNode: "repeat", wantMsg: `no machine pool named "gpu"`},
		{name: "known pool", yaml: strings.Replace(poolCheckWorkflowLevel, "%s", "gate", 1), wantValid: true},
		{name: "no pool", yaml: poolCheckNone, wantValid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := svc.ValidateWorkflow(ctx, connect.NewRequest(&reliantv1.ValidateWorkflowRequest{
				ProjectId: projectID,
				Workflow:  mustParse(t, tt.yaml),
			}))
			require.NoError(t, err)
			if tt.wantValid {
				assert.True(t, resp.Msg.Valid, "errors: %v", resp.Msg.Errors)
				return
			}
			assert.False(t, resp.Msg.Valid)
			var found *reliantv1.ValidationError
			for _, e := range resp.Msg.Errors {
				if strings.Contains(e.Message, tt.wantMsg) {
					found = e
				}
			}
			require.NotNil(t, found, "no %q among %v", tt.wantMsg, resp.Msg.Errors)
			assert.Equal(t, tt.wantNode, found.NodeId)
		})
	}
}
