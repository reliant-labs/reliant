package builtin_test

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	// Import activities to trigger init() which registers activity schemas.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// TestOneRingCopyValidation verifies that copying a builtin workflow
// preserves all inputs through the YAML→proto→proto.Clone→proto→YAML roundtrip
// and that validation passes on the copy.
func TestOneRingCopyValidation(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("one-ring.yaml")
	require.NoError(t, err)

	t.Run("builtin_yaml_validates", func(t *testing.T) {
		result, err := v2.ValidateYAMLResult(data, nil)
		require.NoError(t, err)
		require.False(t, result.HasErrors(), "builtin one-ring.yaml should validate cleanly")
	})

	t.Run("copy_roundtrip_validates", func(t *testing.T) {
		proto1, err := wfyaml.ParseWorkflow(data)
		require.NoError(t, err)

		copyWf := proto.Clone(proto1).(*reliantv1.Workflow)
		copyWf.Name = "one-ring-copy-abc123"

		yamlBytes, err := wfyaml.MarshalWorkflow(copyWf)
		require.NoError(t, err)

		result, err := v2.ValidateYAMLResult(yamlBytes, nil)
		require.NoError(t, err)

		if result.HasErrors() {
			for _, e := range result.Errors() {
				t.Errorf("Copy validation error: %v - %s", e.Path, e.Message)
			}
		}
	})

	t.Run("proto_validates_directly", func(t *testing.T) {
		proto1, err := wfyaml.ParseWorkflow(data)
		require.NoError(t, err)

		copyWf := proto.Clone(proto1).(*reliantv1.Workflow)
		copyWf.Name = "one-ring-copy-direct"

		result := validation.StaticAnalysisWithOptions(copyWf, nil)
		if result.HasErrors() {
			for _, e := range result.Errors() {
				t.Errorf("Proto validation error: %v - %s", e.Path, e.Message)
			}
		}
	})

	t.Run("inputs_survive_roundtrip", func(t *testing.T) {
		proto1, err := wfyaml.ParseWorkflow(data)
		require.NoError(t, err)

		copyWf := proto.Clone(proto1).(*reliantv1.Workflow)
		copyWf.Name = "one-ring-copy-roundtrip"

		yamlBytes, err := wfyaml.MarshalWorkflow(copyWf)
		require.NoError(t, err)

		proto2, err := wfyaml.ParseWorkflow(yamlBytes)
		require.NoError(t, err)

		// All inputs must survive the roundtrip
		for name := range proto1.GetInputs() {
			_, ok := proto2.GetInputs()[name]
			require.True(t, ok, "input %q was lost in roundtrip", name)
		}
		require.Equal(t, len(proto1.GetInputs()), len(proto2.GetInputs()), "input count mismatch")
		require.Equal(t, len(proto1.GetNodes()), len(proto2.GetNodes()), "node count mismatch")
	})
}
