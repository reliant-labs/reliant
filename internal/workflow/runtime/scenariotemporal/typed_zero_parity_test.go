// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/stretchr/testify/assert"
)

// TestTypedZeroRescueParity pins validation's exemption for declared outputs
// (validation.TypedZeroRescuable) to what the runtime actually does
// (runtime.EvaluateDeclaredOutputs → substituteTypedZero), for every output
// field of every activity node type.
//
// Validation skips the "node may not have run" check for exactly the outputs
// the runtime rescues with a typed zero. If the two disagree, validation
// either rejects a workflow that runs, or passes one that fails with
// "no such key" at completion. The runtime cannot import validation's rule
// (validation is imported by the runtime), so the rule is restated there and
// this test is what keeps the two statements identical.
//
// It lives here because this package imports the activity registrations
// the runtime's zero values come from.
func TestTypedZeroRescueParity(t *testing.T) {
	registry := wfcel.NewTypeRegistry()
	checked := 0
	for _, nodeType := range registry.NodeTypes() {
		if !model.IsActivityNode(nodeType) && nodeType != model.NodeTypeRun {
			continue
		}
		for _, field := range registry.OutputFieldsForNodeType(nodeType) {
			wf := &reliantv1.Workflow{
				Name:  "parity",
				Nodes: []*reliantv1.Node{{Id: "n", Type: nodeType}},
			}
			expr := "{{nodes.n." + field.Name + "}}"
			// The node never ran: nodes.n is absent.
			_, err := runtime.EvaluateDeclaredOutputs(
				map[string]string{"out": expr}, map[string]interface{}{},
				map[string]interface{}{}, wf, nil)
			runtimeRescues := err == nil

			assert.Equal(t, runtimeRescues, validation.TypedZeroRescuable(nodeType, field.Name),
				"%s.%s: runtime rescues=%v, validation exempts=%v", nodeType, field.Name,
				runtimeRescues, validation.TypedZeroRescuable(nodeType, field.Name))
			checked++
		}
	}
	assert.Greater(t, checked, 20, "the parity sweep checked too few fields to mean anything")
}
