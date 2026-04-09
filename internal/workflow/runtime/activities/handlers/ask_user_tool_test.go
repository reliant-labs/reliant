// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestGetAskUserTool_Name(t *testing.T) {
	tool := getAskUserTool()

	assert.Equal(t, tools.ToolAskUser, tool.Name())
	assert.Equal(t, "ask_user", tool.Name())
}

func TestGetAskUserTool_HasDescription(t *testing.T) {
	tool := getAskUserTool()

	assert.NotEmpty(t, tool.Description())
	assert.Contains(t, tool.Description(), "Ask the user one or more questions")
}

func TestGetAskUserTool_SchemaProperties(t *testing.T) {
	tool := getAskUserTool()

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
}
