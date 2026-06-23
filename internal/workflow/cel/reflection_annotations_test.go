// Copyright (c) 2025 Reliant Labs
package wfcel

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func TestExtractFieldInfo_CallLLMArgs_Annotations(t *testing.T) {
	md := (&reliantv1.CallLLMArgs{}).ProtoReflect().Descriptor()
	fields := ExtractFieldInfo(md)

	if len(fields) == 0 {
		t.Fatal("Expected fields from CallLLMArgs descriptor, got 0")
	}

	byName := make(map[string]FieldInfo)
	for _, f := range fields {
		byName[f.Name] = f
	}

	t.Run("temperature", func(t *testing.T) {
		temp, ok := byName["temperature"]
		if !ok {
			t.Fatal("Expected 'temperature' field")
		}
		if temp.Description == "" {
			t.Error("should have a description")
		}
		if temp.Type != "double" {
			t.Errorf("type: want %q, got %q", "double", temp.Type)
		}
		if temp.DefaultValue != "0.7" {
			t.Errorf("default_value: want %q, got %q", "0.7", temp.DefaultValue)
		}
		if temp.MinValue == nil || *temp.MinValue != 0 {
			t.Errorf("min_value: want 0, got %v", temp.MinValue)
		}
		if temp.MaxValue == nil || *temp.MaxValue != 2 {
			t.Errorf("max_value: want 2, got %v", temp.MaxValue)
		}
		if temp.Label != "Temperature" {
			t.Errorf("label: want %q, got %q", "Temperature", temp.Label)
		}
		if len(temp.VisibilityContexts) != 1 || temp.VisibilityContexts[0] != "advanced" {
			t.Errorf("visibility_contexts: want [advanced], got %v", temp.VisibilityContexts)
		}
		if temp.CleanupSemantics == nil || *temp.CleanupSemantics != "trim" {
			t.Errorf("cleanup_semantics: want %q, got %v", "trim", temp.CleanupSemantics)
		}
		if temp.IsCEL != true {
			t.Error("should be IsCEL (CelDouble)")
		}
	})

	t.Run("thinking_level", func(t *testing.T) {
		f, ok := byName["thinking_level"]
		if !ok {
			t.Fatal("Expected 'thinking_level' field")
		}
		if len(f.EnumValues) != 5 {
			t.Errorf("enum_values: want 5, got %d: %v", len(f.EnumValues), f.EnumValues)
		}
		if f.DefaultValue != "none" {
			t.Errorf("default_value: want %q, got %q", "none", f.DefaultValue)
		}
	})

	t.Run("max_tokens", func(t *testing.T) {
		f, ok := byName["max_tokens"]
		if !ok {
			t.Fatal("Expected 'max_tokens' field")
		}
		if f.MinValue == nil || *f.MinValue != 1 {
			t.Errorf("min_value: want 1, got %v", f.MinValue)
		}
		if f.MaxValue == nil || *f.MaxValue != 128000 {
			t.Errorf("max_value: want 128000, got %v", f.MaxValue)
		}
		if f.DefaultValue != "" {
			t.Errorf("should have no default, got %q", f.DefaultValue)
		}
	})

	t.Run("system_prompt", func(t *testing.T) {
		f, ok := byName["system_prompt"]
		if !ok {
			t.Fatal("Expected 'system_prompt' field")
		}
		if f.UIHint != "textarea" {
			t.Errorf("ui_hint: want %q, got %q", "textarea", f.UIHint)
		}
		if f.Label != "System prompt" {
			t.Errorf("label: want %q, got %q", "System prompt", f.Label)
		}
		if f.Placeholder == nil || *f.Placeholder != "Optional instructions for model behavior" {
			t.Errorf("placeholder mismatch: got %v", f.Placeholder)
		}
		if len(f.VisibilityContexts) != 1 || f.VisibilityContexts[0] != "advanced" {
			t.Errorf("visibility_contexts: want [advanced], got %v", f.VisibilityContexts)
		}
		if f.CleanupSemantics == nil || *f.CleanupSemantics != "trim" {
			t.Errorf("cleanup_semantics: want %q, got %v", "trim", f.CleanupSemantics)
		}
	})

	t.Run("model", func(t *testing.T) {
		f, ok := byName["model"]
		if !ok {
			t.Fatal("Expected 'model' field")
		}
		if f.Description == "" {
			t.Error("should have a description")
		}
		if f.Type != "model_selector" {
			t.Errorf("type: want %q, got %q", "model_selector", f.Type)
		}
		if f.Label != "Model" {
			t.Errorf("label: want %q, got %q", "Model", f.Label)
		}
		if f.Placeholder == nil || *f.Placeholder != "e.g. flagship, fast, cheap, or explicit model ID" {
			t.Errorf("placeholder mismatch: got %v", f.Placeholder)
		}
		if len(f.VisibilityContexts) != 1 || f.VisibilityContexts[0] != "basic" {
			t.Errorf("visibility_contexts: want [basic], got %v", f.VisibilityContexts)
		}
		if f.IsCEL != true {
			t.Error("should be IsCEL (CelModelSelector)")
		}
	})
}

func TestExtractFieldInfo_SaveMessageNodeArgs_HiddenFields(t *testing.T) {
	md := (&reliantv1.SaveMessageNodeArgs{}).ProtoReflect().Descriptor()
	fields := ExtractFieldInfo(md)

	byName := make(map[string]FieldInfo)
	for _, f := range fields {
		byName[f.Name] = f
	}

	// Verify hidden fields have Hidden=true
	hiddenFields := []string{
		"resolved_role", "resolved_content", "resolved_tool_calls",
		"resolved_tool_results", "resolved_attachments", "resolved_thinking",
		"resolved_display_style", "token_count", "attachments",
		"tool_results",
	}
	for _, name := range hiddenFields {
		f, ok := byName[name]
		if !ok {
			t.Errorf("Field %q not found (expected it with Hidden=true)", name)
			continue
		}
		if !f.Hidden {
			t.Errorf("Field %q should have Hidden=true", name)
		}
	}

	// Verify visible fields are not hidden
	visibleFields := []string{"role", "content", "tool_calls", "display_style"}
	for _, name := range visibleFields {
		f, ok := byName[name]
		if !ok {
			t.Errorf("Field %q should exist", name)
			continue
		}
		if f.Hidden {
			t.Errorf("Field %q should not be hidden", name)
		}
	}
}

func TestExtractFieldInfoMap_CallLLMArgs(t *testing.T) {
	md := (&reliantv1.CallLLMArgs{}).ProtoReflect().Descriptor()
	m := ExtractFieldInfoMap(md)

	if len(m) == 0 {
		t.Fatal("Expected non-empty field info map")
	}

	temp, ok := m["temperature"]
	if !ok {
		t.Fatal("Expected 'temperature' in map")
	}
	if temp.DefaultValue != "0.7" {
		t.Errorf("temperature default: want %q, got %q", "0.7", temp.DefaultValue)
	}
}
