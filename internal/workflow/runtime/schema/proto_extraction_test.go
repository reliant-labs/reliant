// Copyright (c) 2025 Reliant Labs
package schema

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

func TestExtractFieldInfo_CallLLMArgs(t *testing.T) {
	md := (&reliantv1.CallLLMArgs{}).ProtoReflect().Descriptor()
	fields := wfcel.ExtractFieldInfo(md)

	if len(fields) == 0 {
		t.Fatal("Expected fields from CallLLMArgs descriptor, got 0")
	}

	// Build a map for easier assertions
	byName := make(map[string]wfcel.FieldInfo)
	for _, f := range fields {
		byName[f.Name] = f
	}

	// Verify temperature field has min/max/default from proto annotations
	temp, ok := byName["temperature"]
	if !ok {
		t.Fatal("Expected 'temperature' field in CallLLMArgs")
	}
	if temp.Description == "" {
		t.Error("temperature should have a description")
	}
	if temp.DefaultValue != "0.7" {
		t.Errorf("temperature default_value: want %q, got %q", "0.7", temp.DefaultValue)
	}
	if temp.MinValue == nil || *temp.MinValue != 0 {
		t.Errorf("temperature min_value: want 0, got %v", temp.MinValue)
	}
	if temp.MaxValue == nil || *temp.MaxValue != 2 {
		t.Errorf("temperature max_value: want 2, got %v", temp.MaxValue)
	}

	// Verify thinking_level has enum_values and default
	thinking, ok := byName["thinking_level"]
	if !ok {
		t.Fatal("Expected 'thinking_level' field in CallLLMArgs")
	}
	if len(thinking.EnumValues) != 5 {
		t.Errorf("thinking_level enum_values: want 5 values, got %d: %v", len(thinking.EnumValues), thinking.EnumValues)
	}
	if thinking.DefaultValue != "none" {
		t.Errorf("thinking_level default_value: want %q, got %q", "none", thinking.DefaultValue)
	}

	// Verify max_tokens has min/max but no default
	maxTokens, ok := byName["max_tokens"]
	if !ok {
		t.Fatal("Expected 'max_tokens' field in CallLLMArgs")
	}
	if maxTokens.MinValue == nil || *maxTokens.MinValue != 1 {
		t.Errorf("max_tokens min_value: want 1, got %v", maxTokens.MinValue)
	}
	if maxTokens.MaxValue == nil || *maxTokens.MaxValue != 128000 {
		t.Errorf("max_tokens max_value: want 128000, got %v", maxTokens.MaxValue)
	}
	if maxTokens.DefaultValue != "" {
		t.Errorf("max_tokens should have no default, got %q", maxTokens.DefaultValue)
	}

	// tools_config should exist as a message field
	if _, ok := byName["tools_config"]; !ok {
		t.Error("Expected 'tools_config' field")
	}

	t.Logf("CallLLMArgs has %d fields, all annotations verified", len(fields))
}

func TestExtractInputFieldsFromProto_CallLLMArgs(t *testing.T) {
	md := (&reliantv1.CallLLMArgs{}).ProtoReflect().Descriptor()
	fields := extractInputFieldsFromProto(md)

	if len(fields) == 0 {
		t.Fatal("Expected fields from CallLLMArgs, got 0")
	}

	// Build map
	byName := make(map[string]InputFieldInfo)
	for _, f := range fields {
		byName[f.Name] = f
	}

	// Verify temperature carries through
	temp, ok := byName["temperature"]
	if !ok {
		t.Fatal("Expected 'temperature' field")
	}
	if temp.Type != "number" {
		t.Errorf("temperature type: want %q, got %q", "number", temp.Type)
	}
	if temp.Default != "0.7" {
		t.Errorf("temperature default: want %q, got %v", "0.7", temp.Default)
	}
	if temp.Min == nil || *temp.Min != 0 {
		t.Errorf("temperature min: want 0, got %v", temp.Min)
	}
	if temp.Max == nil || *temp.Max != 2 {
		t.Errorf("temperature max: want 2, got %v", temp.Max)
	}
	if temp.Label != "Temperature" {
		t.Errorf("temperature label: want %q, got %q", "Temperature", temp.Label)
	}
	if len(temp.VisibilityContexts) != 1 || temp.VisibilityContexts[0] != "advanced" {
		t.Errorf("temperature visibility_contexts: want [advanced], got %v", temp.VisibilityContexts)
	}
	if temp.CleanupSemantics == nil || *temp.CleanupSemantics != "trim" {
		t.Errorf("temperature cleanup_semantics: want %q, got %v", "trim", temp.CleanupSemantics)
	}

	modelField, ok := byName["model"]
	if !ok {
		t.Fatal("Expected 'model' field")
	}
	if modelField.Label != "Model" {
		t.Errorf("model label: want %q, got %q", "Model", modelField.Label)
	}
	if modelField.Placeholder == nil || *modelField.Placeholder != "e.g. flagship, fast, cheap, or explicit model ID" {
		t.Errorf("model placeholder mismatch: got %v", modelField.Placeholder)
	}
	if len(modelField.VisibilityContexts) != 1 || modelField.VisibilityContexts[0] != "basic" {
		t.Errorf("model visibility_contexts: want [basic], got %v", modelField.VisibilityContexts)
	}

	systemPrompt, ok := byName["system_prompt"]
	if !ok {
		t.Fatal("Expected 'system_prompt' field")
	}
	if systemPrompt.Placeholder == nil || *systemPrompt.Placeholder != "Optional instructions for model behavior" {
		t.Errorf("system_prompt placeholder mismatch: got %v", systemPrompt.Placeholder)
	}

	// Verify hidden fields are excluded from extractInputFieldsFromProto
	for _, f := range fields {
		t.Logf("  field: %s (type=%s, desc=%q)", f.Name, f.Type, f.Description)
	}
	// tools is hidden, should not appear in extractInputFieldsFromProto output
	if _, ok := byName["tools"]; ok {
		t.Error("Field 'tools' should be hidden and excluded from extractInputFieldsFromProto")
	}
}

func TestExtractInputFieldsFromProto_SaveMessageNodeArgs(t *testing.T) {
	md := (&reliantv1.SaveMessageNodeArgs{}).ProtoReflect().Descriptor()
	fields := extractInputFieldsFromProto(md)

	// SaveMessageNodeArgs has many hidden fields (resolved_*, token_count)
	// Verify they are excluded
	byName := make(map[string]InputFieldInfo)
	for _, f := range fields {
		byName[f.Name] = f
	}

	// These should be hidden
	hiddenFields := []string{
		"resolved_role", "resolved_content", "resolved_tool_calls",
		"resolved_tool_results", "resolved_attachments", "resolved_thinking",
		"resolved_display_style", "token_count", "attachments",
		"tool_results",
	}
	for _, name := range hiddenFields {
		if _, ok := byName[name]; ok {
			t.Errorf("Field %q should be hidden but was returned", name)
		}
	}

	// These should be visible
	visibleFields := []string{"role", "content", "tool_calls", "display_style"}
	for _, name := range visibleFields {
		if _, ok := byName[name]; !ok {
			t.Errorf("Field %q should be visible but was not returned", name)
		}
	}

	t.Logf("SaveMessageNodeArgs: %d visible fields", len(fields))
}
