// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

func observeExecutor(defaults *reliantv1.NodeObserve, inputs map[string]interface{}) *StepExecutor {
	return &StepExecutor{
		workflowID:     "wf-1",
		workflowName:   "rc-train",
		chatID:         "chat-1",
		workflowInputs: inputs,
		nodePathPrefix: "outer",
		workflow:       &reliantv1.Workflow{Name: "rc-train", Observe: defaults},
		logger:         log.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	}
}

// The request carries the node's effective block (the workflow's defaults
// merged with its own), only the inputs it reads, and the node's identity.
func TestObserveRequest_MergesDefaultsAndShipsOnlyWhatItReads(t *testing.T) {
	t.Parallel()
	defaults := &reliantv1.NodeObserve{
		Attributes: map[string]string{"tier": "{{ inputs.tier }}", "kind": "default"},
		Metrics:    []*reliantv1.ObserveMetric{{Name: "node.duration", Kind: model.ObserveKindHistogram, Value: "{{ node.duration_ms }}"}},
	}
	e := observeExecutor(defaults, map[string]interface{}{
		"tier":                "HIGH",
		"big":                 strings.Repeat("x", 10_000),
		wfcel.TriggerInputKey: map[string]interface{}{"kind": "schedule"},
	})
	node := &reliantv1.Node{Id: "plan", Type: model.NodeTypeRun, Observe: &reliantv1.NodeObserve{
		Attributes: map[string]string{"kind": "{{ trigger.kind }}"},
		Events:     []*reliantv1.ObserveEvent{{Name: "decision"}},
	}}

	req := e.observeRequest(node)
	require.NotNil(t, req)
	assert.Equal(t, map[string]string{"tier": "{{ inputs.tier }}", "kind": "{{ trigger.kind }}"}, req.Block.GetAttributes(), "the node's key wins")
	assert.Len(t, req.Block.GetEvents(), 1)
	assert.Len(t, req.Block.GetMetrics(), 1)
	assert.Equal(t, map[string]interface{}{
		"tier":                "HIGH",
		wfcel.TriggerInputKey: map[string]interface{}{"kind": "schedule"},
	}, req.Inputs, "only the inputs the block reads ride in history")
	assert.Equal(t, "plan", req.NodeID)
	assert.Equal(t, model.NodeTypeRun, req.NodeType)
	assert.Equal(t, "outer.plan", req.NodePath)
	assert.Equal(t, "wf-1", req.Workflow.ID)
	assert.Nil(t, req.Iter)

	// It survives the trip through history.
	b, err := json.Marshal(req)
	require.NoError(t, err)
	var back types.ObserveRequest
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, req.Block.GetAttributes(), back.Block.GetAttributes())
	assert.Equal(t, "outer.plan", back.NodePath)
}

func TestObserveRequest_NoneWithoutABlock(t *testing.T) {
	t.Parallel()
	e := observeExecutor(nil, nil)
	assert.Nil(t, e.observeRequest(&reliantv1.Node{Id: "n", Type: model.NodeTypeRun}))
	assert.Nil(t, e.observeRequest(&reliantv1.Node{Id: "n", Type: model.NodeTypeRun, Observe: &reliantv1.NodeObserve{}}))
}

// A block over the request cap rides in no history: the node runs unobserved
// (validation warned about it when the definition loaded).
func TestObserveRequest_OverTheSizeCapIsNotSent(t *testing.T) {
	t.Parallel()
	attrs := map[string]string{}
	for i := 0; i < 30; i++ {
		attrs[strings.Repeat("k", 10)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "{{ '" + strings.Repeat("v", 200) + "' }}"
	}
	e := observeExecutor(nil, nil)
	assert.Nil(t, e.observeRequest(&reliantv1.Node{Id: "n", Type: model.NodeTypeRun, Observe: &reliantv1.NodeObserve{Attributes: attrs}}))
}

// In a loop the iteration ships, and its item only when the block reads iter.
func TestObserveRequest_InALoopShipsTheItemOnlyWhenRead(t *testing.T) {
	t.Parallel()
	inputs := map[string]interface{}{"iter": map[string]interface{}{"iteration": 3, "index": 3, "item": map[string]interface{}{"pr": 7}}}
	e := observeExecutor(nil, inputs)
	e.loopNodeID, e.loopIteration = "sweep", 3

	unread := e.observeRequest(&reliantv1.Node{Id: "n", Type: model.NodeTypeRun, Observe: &reliantv1.NodeObserve{Attributes: map[string]string{"x": "{{ output.exit_code }}"}}})
	require.NotNil(t, unread)
	require.NotNil(t, unread.Iter)
	assert.Nil(t, unread.Iter.Item)

	read := e.observeRequest(&reliantv1.Node{Id: "n", Type: model.NodeTypeRun, Observe: &reliantv1.NodeObserve{Attributes: map[string]string{"pr": "{{ iter.item.pr }}"}}})
	require.NotNil(t, read)
	require.NotNil(t, read.Iter)
	assert.NotNil(t, read.Iter.Item)
}
