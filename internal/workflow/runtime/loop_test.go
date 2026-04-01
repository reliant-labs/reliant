package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestLoopOutputProtoBackedSemantics verifies proto-backed loop output preserves CEL map shape.
func TestLoopOutputProtoBackedSemantics(t *testing.T) {
	protoOutputs, err := structpb.NewStruct(map[string]interface{}{
		"exit_code": 0,
		"message":   "success",
	})
	require.NoError(t, err)

	loopOutput := &reliantv1.LoopOutput{
		Iterations: 2,
		Outputs:    protoOutputs,
	}

	outputMap := model.LoopOutputToMap(int(loopOutput.GetIterations()), loopOutput.GetOutputs().AsMap())

	assert.Equal(t, float64(0), outputMap["exit_code"])
	assert.Equal(t, "success", outputMap["message"])
	assert.Equal(t, 2, outputMap["_iterations"])
}
