// Copyright (c) 2025 Reliant Labs
package schema

import (
	"slices"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func outputFieldsByName(fields []InputFieldInfo) map[string]InputFieldInfo {
	byName := make(map[string]InputFieldInfo, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	return byName
}

// The builder's Outputs tab lists what downstream steps read. For call_llm
// that is above all message, tool_calls (what Execute Tools consumes) and
// response_data (the structured output) — all message-typed, and all
// dropped when output fields were extracted with the INPUT rules.
func TestOutputFields_CallLLMIncludesMessageTypedFields(t *testing.T) {
	md := (&reliantv1.CallLLMOutput{}).ProtoReflect().Descriptor()
	byName := outputFieldsByName(extractOutputFieldsFromProto(md, 0))

	toolCalls, ok := byName["tool_calls"]
	require.True(t, ok, "tool_calls must be listed")
	assert.Equal(t, "array", toolCalls.Type)
	assert.NotEmpty(t, toolCalls.Children, "tool_calls lists its items' fields")
	assert.Contains(t, outputFieldsByName(toolCalls.Children), "name")

	message, ok := byName["message"]
	require.True(t, ok, "message must be listed")
	assert.Equal(t, "object", message.Type)
	assert.Contains(t, outputFieldsByName(message.Children), "text")

	responseData, ok := byName["response_data"]
	require.True(t, ok, "response_data must be listed")
	assert.Equal(t, "object", responseData.Type)
	assert.Empty(t, responseData.Children, "a Struct's keys are only known at run time")

	assert.Equal(t, "string", byName["response_text"].Type)
	assert.Equal(t, "integer", byName["token_count"].Type)

	_, ok = byName["thinking"]
	assert.False(t, ok, "a message-only field never reaches the workflow")
}

// Debug correlation ids and loop plumbing stay available, but behind
// "advanced", so the list leads with what an author needs.
func TestOutputFields_CallLLMDebugFieldsAreAdvanced(t *testing.T) {
	md := (&reliantv1.CallLLMOutput{}).ProtoReflect().Descriptor()
	byName := outputFieldsByName(extractOutputFieldsFromProto(md, 0))

	for _, name := range []string{"upstream_proxyman_id", "upstream_request_id", "last_stream_seq", "compaction_threshold", "pending_inbox", "message_id"} {
		field, ok := byName[name]
		require.True(t, ok, "%s is still listed", name)
		assert.True(t, slices.Contains(field.VisibilityContexts, "advanced"), "%s should be advanced", name)
	}
	for _, name := range []string{"response_text", "tool_calls", "response_data", "message", "stop_reason"} {
		assert.False(t, slices.Contains(byName[name].VisibilityContexts, "advanced"), "%s is a primary output", name)
	}
}

func TestOutputFields_NestingIsBounded(t *testing.T) {
	var deepest func(fields []InputFieldInfo) int
	deepest = func(fields []InputFieldInfo) int {
		max := 0
		for _, f := range fields {
			if d := 1 + deepest(f.Children); d > max {
				max = d
			}
		}
		return max
	}
	md := (&reliantv1.CallLLMOutput{}).ProtoReflect().Descriptor()
	assert.LessOrEqual(t, deepest(extractOutputFieldsFromProto(md, 0)), maxOutputFieldDepth+1)
}
