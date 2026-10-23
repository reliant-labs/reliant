// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestAskUserTool_Name(t *testing.T) {
	t.Parallel()

	factory := &tools.ToolsFactory{}
	tool := factory.AskUser()

	assert.Equal(t, tools.ToolAskUser, tool.Name())
	assert.Equal(t, "ask_user", tool.Name())
}

func TestAskUserTool_HasDescription(t *testing.T) {
	t.Parallel()

	factory := &tools.ToolsFactory{}
	tool := factory.AskUser()

	assert.NotEmpty(t, tool.Description())
	assert.Contains(t, tool.Description(), "Ask the user one or more questions")
}

func TestAskUserTool_SchemaProperties(t *testing.T) {
	t.Parallel()

	factory := &tools.ToolsFactory{}
	tool := factory.AskUser()

	schemaOnly, ok := tool.(*tools.SchemaOnlyTool)
	require.True(t, ok, "ask_user tool should be a SchemaOnlyTool")

	schemaMap := schemaOnly.ParamSchemaMap()
	require.NotNil(t, schemaMap)

	// Top-level type is object
	assert.Equal(t, "object", schemaMap["type"])

	// Has required fields
	required, ok := schemaMap["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "questions")

	// Has properties
	props, ok := schemaMap["properties"].(map[string]interface{})
	require.True(t, ok)

	// Check for "questions" array property at top level
	questionsRaw, hasQuestions := props["questions"]
	assert.True(t, hasQuestions, "Schema should have 'questions' property")
	questionsMap, ok := questionsRaw.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "array", questionsMap["type"])

	// Async is the default (research/ASYNC_ASK_USER.md): blocking is the
	// opt-in, optional, default false, and the old async option is gone.
	blockingRaw, hasBlocking := props["blocking"]
	require.True(t, hasBlocking, "Schema should have the 'blocking' option")
	blockingMap, ok := blockingRaw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "boolean", blockingMap["type"])
	assert.Equal(t, false, blockingMap["default"])
	assert.NotContains(t, required, "blocking")
	assert.NotContains(t, props, "async", "async is the default, not an option")
	assert.Contains(t, tool.Description(), "blocking: true")
	assert.Contains(t, tool.Description(), "withdraw_question")
}
