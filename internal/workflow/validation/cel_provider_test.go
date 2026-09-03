package validation

import (
	"reflect"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowTypeProvider_StrictUnknownNestedNodeOutputField(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv()
	require.NoError(t, err)

	provider := newWorkflowTypeProvider(env.CELTypeProvider(), &WorkflowTypeContext{
		NodeOutputs: map[string]map[string]*FieldInfo{
			"llm": {
				"message": {
					Name: "message",
					Properties: map[string]*FieldInfo{
						"text": {Name: "text", Kind: reflect.String},
					},
				},
			},
		},
	})

	fieldType, ok := provider.FindStructFieldType("node_output.llm.message", "non_existent")
	assert.False(t, ok, "expected unknown nested field lookup to fail for strict typed outputs")
	assert.Nil(t, fieldType)
}

func TestWorkflowTypeProvider_DynamicNestedNodeOutputFieldException(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv()
	require.NoError(t, err)

	provider := newWorkflowTypeProvider(env.CELTypeProvider(), &WorkflowTypeContext{
		NodeOutputs: map[string]map[string]*FieldInfo{
			"llm": {
				"message": {
					Name:      "message",
					Kind:      reflect.Interface,
					IsDynamic: true,
				},
			},
		},
	})

	fieldType, ok := provider.FindStructFieldType("node_output.llm.message", "non_existent")
	require.True(t, ok, "expected dynamic nested outputs to remain permissive")
	require.NotNil(t, fieldType)
	assert.Equal(t, types.DynType, fieldType.Type)
}

func TestWorkflowTypeProvider_AdditionalPropertiesNestedNodeOutputFieldException(t *testing.T) {
	t.Parallel()
	env, err := cel.NewEnv()
	require.NoError(t, err)

	provider := newWorkflowTypeProvider(env.CELTypeProvider(), &WorkflowTypeContext{
		NodeOutputs: map[string]map[string]*FieldInfo{
			"llm": {
				"message": {
					Name: "message",
					Properties: map[string]*FieldInfo{
						"text": {Name: "text", Kind: reflect.String},
					},
					AdditionalPropertiesAllowed: true,
				},
			},
		},
	})

	fieldType, ok := provider.FindStructFieldType("node_output.llm.message", "non_existent")
	require.True(t, ok, "expected additionalProperties=true to allow unknown nested fields")
	require.NotNil(t, fieldType)
	assert.Equal(t, types.DynType, fieldType.Type)
}

func TestGetAvailableToolNames_DeterministicOrder(t *testing.T) {
	t.Parallel()
	names := getAvailableToolNames(map[string]*ResponseToolSchema{
		"zeta":  {},
		"alpha": {},
		"beta":  {},
	})

	assert.Equal(t, []string{"alpha", "beta", "zeta"}, names)
}
