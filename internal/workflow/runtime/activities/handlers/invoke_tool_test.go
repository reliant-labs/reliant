// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	structpb "google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// TestInvokeToolRefusesUnexposedToolAsStepConfigFailure pins how a node naming
// a tool that is not exposed fails: once, as the definition's error. A plain
// error was retried, paused the run, and an unattended run self-resumed into
// it for 33 minutes before giving up (prod 23295ad3). StepConfigFailed ends
// the run instead, and Benign keeps a user's workflow out of the ERROR log.
func TestInvokeToolRefusesUnexposedToolAsStepConfigFailure(t *testing.T) {
	t.Parallel()

	activity := &InvokeToolActivity{}
	_, err := activity.Execute(context.Background(), ActivityInput{
		Runtime: activitytypes.RuntimeContext{StepID: "look"},
		Node: &reliantv1.Node{Id: "look", Type: "invoke_tool", Args: &reliantv1.Node_InvokeTool{InvokeTool: &reliantv1.InvokeToolArgs{
			Tool: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "shell_output"}},
		}}},
	})
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "want a temporal application error, got %T", err)
	assert.Equal(t, activitytypes.StepConfigFailedErrType, appErr.Type())
	assert.True(t, appErr.NonRetryable(), "no retry can expose the tool")
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, appErr.Category())
	assert.Contains(t, appErr.Message(), `tool "shell_output" is not exposed as a node`)
}

// TestStructuredToolDataParsesTypedOutput is the behavioral core of
// tool-as-node: a tool's typed output is JSON-encoded into the Metadata string
// by WithResponseMetadata, and this is the read that turns it back into
// something a graph edge can walk as nodes.<id>.data.<field>.
func TestStructuredToolDataParsesTypedOutput(t *testing.T) {
	t.Parallel()

	// Exactly what generate_image publishes through WithResponseMetadata.
	metadata := `{"attachment_id":"att_abc123","filename":"hero.png","mime_type":"image/png","size":20481,"model":"gpt-image-1","saved_to":"/w/assets/hero.png","revised_prompt":"a wide landscape"}`

	data := structuredToolData(metadata)
	require.NotNil(t, data, "structured output must be recoverable from the metadata string")

	fields := data.GetFields()
	assert.Equal(t, "att_abc123", fields["attachment_id"].GetStringValue())
	assert.Equal(t, "hero.png", fields["filename"].GetStringValue())
	assert.Equal(t, "image/png", fields["mime_type"].GetStringValue())
	assert.Equal(t, float64(20481), fields["size"].GetNumberValue())
	assert.Equal(t, "/w/assets/hero.png", fields["saved_to"].GetStringValue())
}

// TestStructuredToolDataDegradesGracefully pins the failure modes. A tool that
// never calls WithResponseMetadata, or whose metadata is not a JSON object,
// contributes no structured data — and that is NOT an error, because content
// is still returned and the node still succeeds.
func TestStructuredToolDataDegradesGracefully(t *testing.T) {
	t.Parallel()

	for name, metadata := range map[string]string{
		"empty":            "",
		"whitespace":       "   ",
		"not_json":         "Output truncated from 900000 bytes to 30000 bytes",
		"json_but_scalar":  `"a bare string"`,
		"json_but_array":   `[1,2,3]`,
		"trailing_garbage": `{"attachment_id":"x"}; truncated`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, structuredToolData(metadata))
		})
	}
}

// TestToolInputFromParams verifies that a node's params map is rendered as the
// same JSON object encoding a model-emitted tool call arrives in. Going
// through that encoding is what makes an invoked tool indistinguishable from a
// called one everywhere downstream.
func TestToolInputFromParams(t *testing.T) {
	t.Parallel()

	t.Run("empty params produce an empty object, not null", func(t *testing.T) {
		input, err := toolInputFromParams(nil)
		require.NoError(t, err)
		// A tool must be fully functional with zero configuration; "null"
		// would fail to unmarshal into the tool's param struct.
		assert.Equal(t, "{}", input)
	})

	t.Run("mixed-type params round-trip", func(t *testing.T) {
		params := map[string]*structpb.Value{
			"prompt":  structpb.NewStringValue("a red bicycle"),
			"size":    structpb.NewStringValue("1536x1024"),
			"save_to": structpb.NewStringValue("assets/bike.png"),
		}
		input, err := toolInputFromParams(params)
		require.NoError(t, err)

		assert.JSONEq(t, `{"prompt":"a red bicycle","size":"1536x1024","save_to":"assets/bike.png"}`, input)
	})
}

// TestInvokeToolCallIDIsPositional pins the idempotency key. The tool_call_id
// IS the key checkPriorTerminalResult refuses to re-run under, so it must be
// derived from the node's position in the run rather than minted fresh — a
// random id per attempt would let an interrupted-then-resumed workflow
// generate the same image twice.
func TestInvokeToolCallIDIsPositional(t *testing.T) {
	t.Parallel()

	first := invokeToolCallID("wf-1", "hero", "", 0)
	second := invokeToolCallID("wf-1", "hero", "", 0)
	assert.Equal(t, first, second, "the same node in the same run must produce the same tool_call_id")

	assert.NotEqual(t, first, invokeToolCallID("wf-1", "thumbnail", "", 0),
		"different nodes must not share an idempotency key")
	assert.NotEqual(t, first, invokeToolCallID("wf-2", "hero", "", 0),
		"different runs must not share an idempotency key")

	// Loop iterations are genuinely separate calls: iteration 2 must be
	// allowed to run even though iteration 1 already reached terminal status.
	assert.NotEqual(t,
		invokeToolCallID("wf-1", "hero", "each_item", 1),
		invokeToolCallID("wf-1", "hero", "each_item", 2),
		"loop iterations must each get their own idempotency key")
}
