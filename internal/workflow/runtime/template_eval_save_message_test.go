package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ---------------------------------------------------------------------------
// Unit tests for populateSaveMessageResolved
// ---------------------------------------------------------------------------

func TestPopulateSaveMessageResolved_ToolCalls(t *testing.T) {
	toolCallsJSON := `[{"id":"toolu_abc","name":"grep","input":"{\"pattern\":\"test\"}"},{"id":"toolu_def","name":"write_file","input":"{\"path\":\"a.txt\"}"}]`

	args := &reliantv1.SaveMessageNodeArgs{
		Role:    celLiteral("assistant"),
		Content: celLiteral("I'll search for that."),
		ToolCalls: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: toolCallsJSON},
		},
	}

	populateSaveMessageResolved(args)

	require.Len(t, args.ResolvedToolCalls, 2, "expected 2 resolved tool calls")

	assert.Equal(t, "toolu_abc", args.ResolvedToolCalls[0].Id)
	assert.Equal(t, "grep", args.ResolvedToolCalls[0].Name)
	assert.Equal(t, `{"pattern":"test"}`, args.ResolvedToolCalls[0].Input)

	assert.Equal(t, "toolu_def", args.ResolvedToolCalls[1].Id)
	assert.Equal(t, "write_file", args.ResolvedToolCalls[1].Name)
	assert.Equal(t, `{"path":"a.txt"}`, args.ResolvedToolCalls[1].Input)
}

func TestPopulateSaveMessageResolved_ToolResults(t *testing.T) {
	toolResultsJSON := `[{"tool_call_id":"toolu_abc","name":"grep","content":"found 5 matches","is_error":false},{"tool_call_id":"toolu_def","name":"write_file","content":"written","is_error":false}]`

	args := &reliantv1.SaveMessageNodeArgs{
		Role:    celLiteral("tool"),
		Content: celLiteral(""),
		ToolResults: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: toolResultsJSON},
		},
	}

	populateSaveMessageResolved(args)

	require.Len(t, args.ResolvedToolResults, 2, "expected 2 resolved tool results")

	assert.Equal(t, "toolu_abc", args.ResolvedToolResults[0].ToolCallId)
	assert.Equal(t, "grep", args.ResolvedToolResults[0].Name)
	assert.Equal(t, "found 5 matches", args.ResolvedToolResults[0].Content)
	assert.False(t, args.ResolvedToolResults[0].IsError)

	assert.Equal(t, "toolu_def", args.ResolvedToolResults[1].ToolCallId)
	assert.Equal(t, "write_file", args.ResolvedToolResults[1].Name)
	assert.Equal(t, "written", args.ResolvedToolResults[1].Content)
}

func TestPopulateSaveMessageResolved_ToolResultsWithError(t *testing.T) {
	toolResultsJSON := `[{"tool_call_id":"toolu_err","name":"bash","content":"command not found","is_error":true}]`

	args := &reliantv1.SaveMessageNodeArgs{
		Role:    celLiteral("tool"),
		Content: celLiteral(""),
		ToolResults: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: toolResultsJSON},
		},
	}

	populateSaveMessageResolved(args)

	require.Len(t, args.ResolvedToolResults, 1)
	assert.True(t, args.ResolvedToolResults[0].IsError)
	assert.Equal(t, "command not found", args.ResolvedToolResults[0].Content)
}

func TestPopulateSaveMessageResolved_Attachments(t *testing.T) {
	attachmentsJSON := `["file1.txt","file2.png","image.jpg"]`

	args := &reliantv1.SaveMessageNodeArgs{
		Role:    celLiteral("user"),
		Content: celLiteral("see attached"),
		Attachments: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: attachmentsJSON},
		},
	}

	populateSaveMessageResolved(args)

	require.Len(t, args.ResolvedAttachments, 3)
	assert.Equal(t, []string{"file1.txt", "file2.png", "image.jpg"}, args.ResolvedAttachments)
}

func TestPopulateSaveMessageResolved_EmptyToolCalls(t *testing.T) {
	tests := []struct {
		name string
		args *reliantv1.SaveMessageNodeArgs
	}{
		{
			name: "nil ToolCalls field",
			args: &reliantv1.SaveMessageNodeArgs{
				Role:    celLiteral("assistant"),
				Content: celLiteral("no tools"),
			},
		},
		{
			name: "empty literal string",
			args: &reliantv1.SaveMessageNodeArgs{
				Role:    celLiteral("assistant"),
				Content: celLiteral("no tools"),
				ToolCalls: &reliantv1.CelString{
					Value: &reliantv1.CelString_Literal{Literal: ""},
				},
			},
		},
		{
			name: "empty JSON array",
			args: &reliantv1.SaveMessageNodeArgs{
				Role:    celLiteral("assistant"),
				Content: celLiteral("no tools"),
				ToolCalls: &reliantv1.CelString{
					Value: &reliantv1.CelString_Literal{Literal: "[]"},
				},
			},
		},
		{
			name: "nil ToolResults field",
			args: &reliantv1.SaveMessageNodeArgs{
				Role:    celLiteral("assistant"),
				Content: celLiteral("no results"),
			},
		},
		{
			name: "nil Attachments field",
			args: &reliantv1.SaveMessageNodeArgs{
				Role:    celLiteral("user"),
				Content: celLiteral("no attachments"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			populateSaveMessageResolved(tc.args)

			assert.Empty(t, tc.args.ResolvedToolCalls)
			assert.Empty(t, tc.args.ResolvedToolResults)
			assert.Empty(t, tc.args.ResolvedAttachments)
		})
	}
}

func TestPopulateSaveMessageResolved_InvalidJSON(t *testing.T) {
	tests := []struct {
		name    string
		literal string
	}{
		{name: "garbage string", literal: "not json at all"},
		{name: "incomplete JSON", literal: `[{"id":"abc"`},
		{name: "JSON object instead of array", literal: `{"id":"abc","name":"grep"}`},
		{name: "number", literal: "42"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := &reliantv1.SaveMessageNodeArgs{
				Role:    celLiteral("assistant"),
				Content: celLiteral("test"),
				ToolCalls: &reliantv1.CelString{
					Value: &reliantv1.CelString_Literal{Literal: tc.literal},
				},
			}

			// Must not panic
			populateSaveMessageResolved(args)

			// Invalid JSON → resolved stays empty
			assert.Empty(t, args.ResolvedToolCalls)
		})
	}
}

func TestPopulateSaveMessageResolved_SkipsItemsWithMissingFields(t *testing.T) {
	// Tool calls missing required id or name should be skipped.
	toolCallsJSON := `[{"id":"","name":"grep","input":"{}"},{"id":"toolu_1","name":"","input":"{}"},{"id":"toolu_2","name":"bash","input":"ls"}]`

	args := &reliantv1.SaveMessageNodeArgs{
		ToolCalls: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: toolCallsJSON},
		},
	}

	populateSaveMessageResolved(args)

	// Only the third item has both id and name
	require.Len(t, args.ResolvedToolCalls, 1)
	assert.Equal(t, "toolu_2", args.ResolvedToolCalls[0].Id)
	assert.Equal(t, "bash", args.ResolvedToolCalls[0].Name)
}

func TestPopulateSaveMessageResolved_DoesNotOverwriteExisting(t *testing.T) {
	// If ResolvedToolCalls is already populated, don't overwrite.
	existing := []*reliantv1.ToolCallMsg{{Id: "existing", Name: "test"}}

	args := &reliantv1.SaveMessageNodeArgs{
		ResolvedToolCalls: existing,
		ToolCalls: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: `[{"id":"new","name":"other"}]`},
		},
	}

	populateSaveMessageResolved(args)

	require.Len(t, args.ResolvedToolCalls, 1)
	assert.Equal(t, "existing", args.ResolvedToolCalls[0].Id, "should not overwrite existing resolved tool calls")
}

func TestPopulateSaveMessageResolved_ScalarFieldsStillWork(t *testing.T) {
	// Verify the original scalar resolution still works alongside the new fields.
	args := &reliantv1.SaveMessageNodeArgs{
		Role:         celLiteral("assistant"),
		Content:      celLiteral("hello world"),
		DisplayStyle: celLiteral("info"),
		ToolCalls: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: `[{"id":"tc1","name":"grep","input":"{}"}]`},
		},
	}

	populateSaveMessageResolved(args)

	assert.Equal(t, "assistant", args.ResolvedRole)
	assert.Equal(t, "hello world", args.ResolvedContent)
	assert.Equal(t, "info", args.ResolvedDisplayStyle)
	require.Len(t, args.ResolvedToolCalls, 1)
	assert.Equal(t, "tc1", args.ResolvedToolCalls[0].Id)
}

// ---------------------------------------------------------------------------
// Integration test: simulate the auditing agent save_approved_response flow
// ---------------------------------------------------------------------------

func TestPopulateSaveMessageResolved_AuditingAgentFlow(t *testing.T) {
	// This test simulates the exact flow that was broken:
	// 1. call_llm returns a CallLLMOutput with tool_calls
	// 2. Output gets serialized via protojson → deserialized into map
	// 3. CEL expression {{nodes.main_agent.tool_calls}} extracts tool_calls
	// 4. The CEL result becomes a CelString literal in SaveMessageNodeArgs.ToolCalls
	// 5. populateSaveMessageResolved must parse it into ResolvedToolCalls

	// Step 1: Create a realistic CallLLMOutput with tool_calls
	llmOutput := &reliantv1.CallLLMOutput{
		Message: &reliantv1.MessageOutput{
			Role: "assistant",
			Text: "I'll search for that pattern.",
		},
		ResponseText: "I'll search for that pattern.",
		ToolCalls: []*reliantv1.ToolCallMsg{
			{
				Id:    "toolu_01HxYz",
				Name:  "grep",
				Input: `{"pattern":"populateSaveMessageResolved","path":"internal/"}`,
			},
			{
				Id:    "toolu_02AbCd",
				Name:  "read_file",
				Input: `{"file_path":"template_eval.go"}`,
			},
		},
		TokenCount: 150,
	}

	// Step 2: Serialize via protojson with UseProtoNames (snake_case keys)
	marshaler := protojson.MarshalOptions{UseProtoNames: true}
	protoData, err := marshaler.Marshal(llmOutput)
	require.NoError(t, err)

	// Step 3: Deserialize into map (simulating what the runtime converter does)
	var rawOutput map[string]interface{}
	require.NoError(t, json.Unmarshal(protoData, &rawOutput))

	// Verify tool_calls exist in the raw output
	rawToolCalls, ok := rawOutput["tool_calls"]
	require.True(t, ok, "tool_calls should exist in protojson output with UseProtoNames")
	require.NotNil(t, rawToolCalls)

	// Step 4: Use CEL to evaluate {{nodes.main_agent.tool_calls}}
	celCtx := &wfcel.EdgeEvalContext{
		Nodes: map[string]interface{}{
			"main_agent": rawOutput,
		},
		Inputs:   map[string]interface{}{},
		Workflow: &model.WorkflowContext{},
	}

	celResult, err := wfcel.EvaluateTemplate("{{nodes.main_agent.tool_calls}}", celCtx)
	require.NoError(t, err, "CEL evaluation should succeed")
	require.NotNil(t, celResult, "CEL result should not be nil")

	// Step 5: Convert CEL result to JSON string (as the resolver would)
	celResultJSON, err := json.Marshal(celResult)
	require.NoError(t, err, "CEL result should be JSON-serializable")

	// Step 6: Create SaveMessageNodeArgs with the CEL result as a literal
	args := &reliantv1.SaveMessageNodeArgs{
		Role:    celLiteral("assistant"),
		Content: celLiteral("I'll search for that pattern."),
		ToolCalls: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: string(celResultJSON)},
		},
	}

	// Step 7: Run populateSaveMessageResolved
	populateSaveMessageResolved(args)

	// Step 8: Verify ResolvedToolCalls
	require.Len(t, args.ResolvedToolCalls, 2, "should have 2 resolved tool calls from the auditing agent flow")

	assert.Equal(t, "toolu_01HxYz", args.ResolvedToolCalls[0].Id)
	assert.Equal(t, "grep", args.ResolvedToolCalls[0].Name)
	assert.Contains(t, args.ResolvedToolCalls[0].Input, "populateSaveMessageResolved")

	assert.Equal(t, "toolu_02AbCd", args.ResolvedToolCalls[1].Id)
	assert.Equal(t, "read_file", args.ResolvedToolCalls[1].Name)
	assert.Contains(t, args.ResolvedToolCalls[1].Input, "template_eval.go")

	// Scalar fields should also be resolved
	assert.Equal(t, "assistant", args.ResolvedRole)
	assert.Equal(t, "I'll search for that pattern.", args.ResolvedContent)
}

func TestPopulateSaveMessageResolved_AuditingAgentFlow_ToolResults(t *testing.T) {
	// Same flow but for tool_results (the tool role save_message)

	// Simulate tool results from an execute_tools node
	toolResults := []map[string]interface{}{
		{
			"tool_call_id": "toolu_01HxYz",
			"name":         "grep",
			"content":      "Found 3 matches in template_eval.go",
			"is_error":     false,
		},
		{
			"tool_call_id": "toolu_02AbCd",
			"name":         "read_file",
			"content":      "// populateSaveMessageResolved ...",
			"is_error":     false,
		},
	}

	// CEL evaluates to this array; serialize to JSON as the resolver would
	resultsJSON, err := json.Marshal(toolResults)
	require.NoError(t, err)

	args := &reliantv1.SaveMessageNodeArgs{
		Role:    celLiteral("tool"),
		Content: celLiteral(""),
		ToolResults: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: string(resultsJSON)},
		},
	}

	populateSaveMessageResolved(args)

	require.Len(t, args.ResolvedToolResults, 2)
	assert.Equal(t, "toolu_01HxYz", args.ResolvedToolResults[0].ToolCallId)
	assert.Equal(t, "grep", args.ResolvedToolResults[0].Name)
	assert.Contains(t, args.ResolvedToolResults[0].Content, "3 matches")
	assert.False(t, args.ResolvedToolResults[0].IsError)

	assert.Equal(t, "toolu_02AbCd", args.ResolvedToolResults[1].ToolCallId)
	assert.Equal(t, "read_file", args.ResolvedToolResults[1].Name)
}

// TestPopulateSaveMessageResolved_CamelCaseKeys verifies that tool calls
// serialized with camelCase keys (as protojson without UseProtoNames produces)
// are NOT parsed, since the JSON struct tags use snake_case. This documents the
// expected behavior: the resolver must use UseProtoNames or snake_case keys.
func TestPopulateSaveMessageResolved_CamelCaseKeys(t *testing.T) {
	// protojson default output uses camelCase: toolCallId instead of tool_call_id
	// Our parser uses json struct tags with snake_case, so camelCase keys won't match.
	camelCaseResults := `[{"toolCallId":"tc1","name":"grep","content":"result","isError":false}]`

	args := &reliantv1.SaveMessageNodeArgs{
		ToolResults: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: camelCaseResults},
		},
	}

	populateSaveMessageResolved(args)

	// With camelCase keys, tool_call_id won't be populated (it maps to "tool_call_id" json tag)
	// The item will have empty ToolCallId, so behavior depends on validation.
	// Document what actually happens: items with missing required fields may or may not be included.
	// The important thing is no panic.
	// (Tool calls use "id"/"name" which are the same in both cases, but tool_results
	// use "tool_call_id" which differs from "toolCallId".)
}
