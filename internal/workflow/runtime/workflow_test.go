// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func TestGetBoolInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		inputs      map[string]interface{}
		key         string
		defaultVal  bool
		expectedVal bool
		description string
	}{
		{
			name:        "valid bool true",
			inputs:      map[string]interface{}{"planning_mode": true},
			key:         "planning_mode",
			defaultVal:  false,
			expectedVal: true,
			description: "should return true when key exists and value is bool true",
		},
		{
			name:        "valid bool false",
			inputs:      map[string]interface{}{"planning_mode": false},
			key:         "planning_mode",
			defaultVal:  true,
			expectedVal: false,
			description: "should return false when key exists and value is bool false",
		},
		{
			name:        "missing key returns default",
			inputs:      map[string]interface{}{"other_key": true},
			key:         "planning_mode",
			defaultVal:  true,
			expectedVal: true,
			description: "should return default value when key does not exist",
		},
		{
			name:        "wrong type returns default",
			inputs:      map[string]interface{}{"planning_mode": "true"},
			key:         "planning_mode",
			defaultVal:  false,
			expectedVal: false,
			description: "should return default value when value is not a bool",
		},
		{
			name:        "wrong type int returns default",
			inputs:      map[string]interface{}{"planning_mode": 1},
			key:         "planning_mode",
			defaultVal:  false,
			expectedVal: false,
			description: "should return default value when value is int instead of bool",
		},
		{
			name:        "nil map returns default",
			inputs:      nil,
			key:         "planning_mode",
			defaultVal:  true,
			expectedVal: true,
			description: "should return default value when inputs map is nil",
		},
		{
			name:        "empty map returns default",
			inputs:      map[string]interface{}{},
			key:         "planning_mode",
			defaultVal:  false,
			expectedVal: false,
			description: "should return default value when inputs map is empty",
		},
		{
			name:        "nil value returns default",
			inputs:      map[string]interface{}{"planning_mode": nil},
			key:         "planning_mode",
			defaultVal:  true,
			expectedVal: true,
			description: "should return default value when value is nil",
		},
		{
			name:        "empty string key",
			inputs:      map[string]interface{}{"": true},
			key:         "",
			defaultVal:  false,
			expectedVal: true,
			description: "should handle empty string as valid key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBoolInput(tt.inputs, tt.key, tt.defaultVal)
			if result != tt.expectedVal {
				t.Errorf("getBoolInput() = %v, want %v - %s", result, tt.expectedVal, tt.description)
			}
		})
	}
}

func TestGetStringInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		inputs      map[string]interface{}
		key         string
		defaultVal  string
		expectedVal string
		description string
	}{
		{
			name:        "valid string value",
			inputs:      map[string]interface{}{"agent_name": "coding"},
			key:         "agent_name",
			defaultVal:  "",
			expectedVal: "coding",
			description: "should return string when key exists and value is string",
		},
		{
			name:        "empty string value",
			inputs:      map[string]interface{}{"agent_name": ""},
			key:         "agent_name",
			defaultVal:  "default",
			expectedVal: "",
			description: "should return empty string when key exists and value is empty string",
		},
		{
			name:        "missing key returns default",
			inputs:      map[string]interface{}{"other_key": "value"},
			key:         "agent_name",
			defaultVal:  "planning",
			expectedVal: "planning",
			description: "should return default value when key does not exist",
		},
		{
			name:        "wrong type returns default",
			inputs:      map[string]interface{}{"agent_name": 123},
			key:         "agent_name",
			defaultVal:  "default",
			expectedVal: "default",
			description: "should return default value when value is not a string",
		},
		{
			name:        "wrong type bool returns default",
			inputs:      map[string]interface{}{"agent_name": true},
			key:         "agent_name",
			defaultVal:  "default",
			expectedVal: "default",
			description: "should return default value when value is bool instead of string",
		},
		{
			name:        "nil map returns default",
			inputs:      nil,
			key:         "agent_name",
			defaultVal:  "planning",
			expectedVal: "planning",
			description: "should return default value when inputs map is nil",
		},
		{
			name:        "empty map returns default",
			inputs:      map[string]interface{}{},
			key:         "agent_name",
			defaultVal:  "coding",
			expectedVal: "coding",
			description: "should return default value when inputs map is empty",
		},
		{
			name:        "nil value returns default",
			inputs:      map[string]interface{}{"agent_name": nil},
			key:         "agent_name",
			defaultVal:  "default",
			expectedVal: "default",
			description: "should return default value when value is nil",
		},
		{
			name:        "empty string key",
			inputs:      map[string]interface{}{"": "value"},
			key:         "",
			defaultVal:  "default",
			expectedVal: "value",
			description: "should handle empty string as valid key",
		},
		{
			name:        "string with special characters",
			inputs:      map[string]interface{}{"agent_name": "agent-123_test!@#"},
			key:         "agent_name",
			defaultVal:  "",
			expectedVal: "agent-123_test!@#",
			description: "should handle strings with special characters",
		},
		{
			name:        "unicode string",
			inputs:      map[string]interface{}{"agent_name": "エージェント"},
			key:         "agent_name",
			defaultVal:  "",
			expectedVal: "エージェント",
			description: "should handle unicode strings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStringInput(tt.inputs, tt.key, tt.defaultVal)
			if result != tt.expectedVal {
				t.Errorf("getStringInput() = %v, want %v - %s", result, tt.expectedVal, tt.description)
			}
		})
	}
}

func TestModeFromInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		inputs       map[string]interface{}
		expectedMode string
	}{
		{
			name: "mode auto",
			inputs: map[string]interface{}{
				"mode": "auto",
			},
			expectedMode: "auto",
		},
		{
			name: "mode plan",
			inputs: map[string]interface{}{
				"mode": "plan",
			},
			expectedMode: "plan",
		},
		{
			name: "mode manual",
			inputs: map[string]interface{}{
				"mode": "manual",
			},
			expectedMode: "manual",
		},
		{
			name:         "nil inputs - default to manual",
			inputs:       nil,
			expectedMode: "manual",
		},
		{
			name:         "empty inputs - default to manual",
			inputs:       map[string]interface{}{},
			expectedMode: "manual",
		},
		{
			name: "no mode field - default to manual",
			inputs: map[string]interface{}{
				"other_field": "value",
			},
			expectedMode: "manual",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode := getModeFromInputs(tt.inputs)

			if mode != tt.expectedMode {
				t.Errorf("getModeFromInputs() = %v, want %v", mode, tt.expectedMode)
			}
		})
	}
}

func TestWorkflowInputData_ToMap(t *testing.T) {
	t.Parallel()
	// Test basic fields using builder pattern
	t.Run("basic fields", func(t *testing.T) {
		data := NewWorkflowInputs().
			Set("prompt", "test prompt").
			Set("mode", "auto")
		result := data.ToMap()

		if result["prompt"] != "test prompt" {
			t.Errorf("prompt = %v, want %v", result["prompt"], "test prompt")
		}
		if result["mode"] != "auto" {
			t.Errorf("mode = %v, want auto", result["mode"])
		}
	})

	// Test with chat context
	t.Run("with chat context", func(t *testing.T) {
		data := NewWorkflowInputs().
			Set("chat", NewChatContext("chat-123"))
		result := data.ToMap()

		chat, ok := result["chat"].(*ChatContext)
		if !ok {
			t.Fatalf("chat should be *ChatContext, got %T", result["chat"])
		}
		if chat.ID != "chat-123" {
			t.Errorf("chat.id = %v, want chat-123", chat.ID)
		}
	})

	// Test multiple fields
	t.Run("multiple fields", func(t *testing.T) {
		data := NewWorkflowInputs().
			Set("prompt", "child prompt").
			Set("parent_field", "inherited").
			Set("custom", 123)
		result := data.ToMap()

		if result["prompt"] != "child prompt" {
			t.Errorf("prompt = %v, want child prompt", result["prompt"])
		}
		if result["parent_field"] != "inherited" {
			t.Errorf("parent_field = %v, want inherited", result["parent_field"])
		}
		if result["custom"] != 123 {
			t.Errorf("custom = %v, want 123", result["custom"])
		}
	})

	// Test SetIfNotEmpty
	t.Run("SetIfNotEmpty", func(t *testing.T) {
		data := NewWorkflowInputs().
			SetIfNotEmpty("prompt", "hello").
			SetIfNotEmpty("empty", "")
		result := data.ToMap()

		if result["prompt"] != "hello" {
			t.Errorf("prompt = %v, want hello", result["prompt"])
		}
		if _, exists := result["empty"]; exists {
			t.Errorf("empty field should not exist, got %v", result["empty"])
		}
	})

	// Test empty inputs
	t.Run("empty inputs", func(t *testing.T) {
		data := NewWorkflowInputs()
		result := data.ToMap()

		if len(result) != 0 {
			t.Errorf("expected empty map, got %v", result)
		}
	})

	// Test nil values are skipped
	t.Run("nil values skipped", func(t *testing.T) {
		data := NewWorkflowInputs().
			Set("valid", "value").
			Set("nil_field", nil)
		result := data.ToMap()

		if result["valid"] != "value" {
			t.Errorf("valid = %v, want value", result["valid"])
		}
		if _, exists := result["nil_field"]; exists {
			t.Errorf("nil_field should not exist")
		}
	})
}

func TestNewChatContext(t *testing.T) {
	t.Parallel()
	ctx := NewChatContext("chat-456")

	if ctx.ID != "chat-456" {
		t.Errorf("ID = %q, want %q", ctx.ID, "chat-456")
	}
	// NOTE: auto_approve was removed from ChatContext - now use inputs.mode
}

// TestBuildWorkflowContext_ThreadAccessibility tests that thread is accessible via inputs.thread
// NOTE: workflow.thread is DEPRECATED and no longer exposed - use inputs.thread instead
func TestBuildWorkflowContext_ThreadAccessibility(t *testing.T) {
	t.Parallel()
	t.Run("thread is accessible via inputs.thread", func(t *testing.T) {
		inputs := map[string]interface{}{
			"thread": "test-thread-path",
			"prompt": "test prompt",
			"mode":   "auto",
		}

		ctx := buildWorkflowContext("wf-id", "test-workflow", "chat-id", inputs)

		// Check inputs are accessible
		inputsMap, ok := ctx["inputs"].(map[string]interface{})
		if !ok {
			t.Fatalf("inputs should be map[string]interface{}, got %T", ctx["inputs"])
		}

		// Verify thread is in inputs
		if inputsMap["thread"] != "test-thread-path" {
			t.Errorf("inputs.thread = %v, want 'test-thread-path'", inputsMap["thread"])
		}

		// Verify workflow.thread is NOT set (deprecated)
		if _, hasThread := ctx["thread"]; hasThread {
			t.Errorf("workflow.thread should NOT be set (deprecated, use inputs.thread)")
		}
	})

	t.Run("nil inputs creates empty map", func(t *testing.T) {
		ctx := buildWorkflowContext("wf-id", "test-workflow", "chat-id", nil)

		// Verify inputs exists as empty map
		inputsMap, ok := ctx["inputs"].(map[string]interface{})
		if !ok {
			t.Fatalf("inputs should be map[string]interface{}, got %T", ctx["inputs"])
		}

		// inputs should be empty when nothing provided
		if len(inputsMap) != 0 {
			t.Errorf("inputs should be empty, got %v", inputsMap)
		}

		// workflow.thread should NOT be set
		if _, hasThread := ctx["thread"]; hasThread {
			t.Errorf("workflow.thread should not be set")
		}
	})
}

// TestGetExecContext tests the GetExecContext helper on WorkflowInput.
func TestGetExecContext(t *testing.T) {
	t.Parallel()
	t.Run("returns ExecContext when set", func(t *testing.T) {
		execCtx := &ExecutionContext{
			WorkflowID:   "wf-123",
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
			Thread:       "thread-789",
			ThreadMode:   model.ThreadModeInherit,
		}

		input := WorkflowInput{
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
			ExecContext:  execCtx,
		}

		result := input.GetExecContext()

		// Should return the exact same pointer
		if result != execCtx {
			t.Error("expected GetExecContext to return the exact ExecContext when set")
		}
	})

	t.Run("panics when ExecContext is nil", func(t *testing.T) {
		input := WorkflowInput{
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
		}

		defer func() {
			if r := recover(); r == nil {
				t.Error("expected GetExecContext to panic when ExecContext is nil")
			}
		}()

		_ = input.GetExecContext()
	})

	t.Run("ExecContext with full data", func(t *testing.T) {
		execCtx := &ExecutionContext{
			WorkflowID:   "wf-123",
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
			Thread:       "thread-abc",
			ThreadMode:   model.ThreadModeFork,
			ForkedFrom:   "parent-thread",
			Parent: &ParentContext{
				WorkflowID: "parent-wf",
				StepPath:   "step-1",
			},
		}

		input := WorkflowInput{
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
			ExecContext:  execCtx,
		}

		result := input.GetExecContext()

		if result.WorkflowID != "wf-123" {
			t.Errorf("WorkflowID = %q, want %q", result.WorkflowID, "wf-123")
		}
		if result.ChatID != "chat-456" {
			t.Errorf("ChatID = %q, want %q", result.ChatID, "chat-456")
		}
		if result.Thread != "thread-abc" {
			t.Errorf("Thread = %q, want %q", result.Thread, "thread-abc")
		}
		if result.ThreadMode != model.ThreadModeFork {
			t.Errorf("ThreadMode = %q, want %q", result.ThreadMode, model.ThreadModeFork)
		}
		if result.ForkedFrom != "parent-thread" {
			t.Errorf("ForkedFrom = %q, want %q", result.ForkedFrom, "parent-thread")
		}

		// Check parent context
		if result.Parent == nil {
			t.Fatal("Parent should not be nil")
		}
		if result.Parent.WorkflowID != "parent-wf" {
			t.Errorf("Parent.WorkflowID = %q, want %q", result.Parent.WorkflowID, "parent-wf")
		}
		if result.Parent.StepPath != "step-1" {
			t.Errorf("Parent.StepPath = %q, want %q", result.Parent.StepPath, "step-1")
		}
	})

	t.Run("ExecContext with loop context", func(t *testing.T) {
		execCtx := &ExecutionContext{
			WorkflowID:   "wf-123",
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
			Thread:       "loop-thread",
			ThreadMode:   model.ThreadModeNew,
			Loop: &ExecLoopContext{
				NodeID:    "loop-node",
				Iteration: 3,
			},
		}

		input := WorkflowInput{
			ChatID:       "chat-456",
			WorkflowName: "test-workflow",
			ExecContext:  execCtx,
		}

		result := input.GetExecContext()

		if result.Loop == nil {
			t.Fatal("Loop should not be nil")
		}
		if result.Loop.Iteration != 3 {
			t.Errorf("Loop.Iteration = %d, want %d", result.Loop.Iteration, 3)
		}
	})
}

func TestHumanizeRetryError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		err          error
		expectedHint string
	}{
		{
			name:         "DNS error",
			err:          errors.New(`dial tcp: lookup api.anthropic.com: no such host`),
			expectedHint: "DNS resolution failed",
		},
		{
			name:         "rate limit 429",
			err:          errors.New(`POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests`),
			expectedHint: "Rate limited by API provider",
		},
		{
			name:         "timeout",
			err:          errors.New("context deadline exceeded"),
			expectedHint: "Request timed out",
		},
		{
			name:         "connection refused",
			err:          errors.New("dial tcp 1.2.3.4:443: connect: connection refused"),
			expectedHint: "Connection failed",
		},
		{
			name:         "auth error 401",
			err:          errors.New(`401 Unauthorized`),
			expectedHint: "Authentication failed",
		},
		{
			name:         "unknown error passes through",
			err:          errors.New("something completely unexpected"),
			expectedHint: "something completely unexpected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := humanizeRetryError("test-step", tt.err)
			if !strings.Contains(result, tt.expectedHint) {
				t.Errorf("expected hint %q in result %q", tt.expectedHint, result)
			}
			if !strings.Contains(result, "test-step") {
				t.Errorf("expected step ID in result %q", result)
			}
			if !strings.Contains(result, "Workflow paused") {
				t.Errorf("expected 'Workflow paused' in result %q", result)
			}
		})
	}
}
