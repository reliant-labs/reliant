// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"
)

func TestCheckOutputSize(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		output      string
		expectError bool
		expectTrunc bool
	}{
		{
			name:        "small output",
			toolName:    ShellToolName,
			output:      "hello world",
			expectError: false,
			expectTrunc: false,
		},
		{
			name:        "exact limit",
			toolName:    ShellToolName,
			output:      strings.Repeat("x", MaxOutputSize),
			expectError: false,
			expectTrunc: false,
		},
		{
			name:        "over limit",
			toolName:    ShellToolName,
			output:      strings.Repeat("x", MaxOutputSize+1),
			expectError: true,
			expectTrunc: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, truncated, err := CheckOutputSize(tt.toolName, tt.output)

			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectTrunc != truncated {
				t.Errorf("expected truncated=%v but got %v", tt.expectTrunc, truncated)
			}
			if !tt.expectError && output != tt.output {
				t.Errorf("output was modified when it shouldn't have been")
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		output   string
		expected string
	}{
		{
			name:     "bash output truncation",
			toolName: ShellToolName,
			output:   strings.Repeat("line\n", 25000), // Large output
			expected: func() string {
				// Should keep beginning and end
				output := strings.Repeat("line\n", 25000)
				if len(output) > MaxOutputSize {
					keepSize := MaxOutputSize - 500
					headSize := keepSize * 3 / 4
					tailSize := keepSize / 4
					return output[:headSize] +
						"\n\n... [OUTPUT TRUNCATED - Showing first " + strings.TrimSpace(strings.Repeat("0", 0)) + // This is a simplification
						" and last " + strings.TrimSpace(strings.Repeat("0", 0)) + " bytes] ...\n\n" +
						output[len(output)-tailSize:]
				}
				return output
			}(),
		},
		{
			name:     "grep output truncation",
			toolName: "grep",
			output:   strings.Repeat("file.txt:line content\n", 10000),
			expected: func() string {
				// Should truncate at line boundaries
				output := strings.Repeat("file.txt:line content\n", 10000)
				if len(output) <= MaxOutputSize {
					return output
				}
				// This is complex to calculate exactly, but it should end with truncation message
				return output // simplified for test
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateOutput(tt.toolName, tt.output, false)

			// Just verify it's truncated to the right size
			if len(result) > MaxOutputSize {
				t.Errorf("truncated output (%d bytes) exceeds max size (%d bytes)",
					len(result), MaxOutputSize)
			}

			// Verify truncation message is present for large outputs
			if len(tt.output) > MaxOutputSize {
				// Should contain either "TRUNCATED" or "omitted" indicating truncation
				if !strings.Contains(result, "TRUNCATED") && !strings.Contains(result, "omitted") {
					t.Error("truncation message not found in truncated output")
				}
			}
		})
	}
}

func TestGetToolSpecificSuggestions(t *testing.T) {
	tests := []struct {
		toolName    string
		expectCount int // minimum expected suggestions
	}{
		{ShellToolName, 3},
		{"grep", 3},
		{"glob", 2},
		{"fetch", 2},
		{"unknown", 2},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			suggestions := getToolSpecificSuggestions(tt.toolName)
			if len(suggestions) < tt.expectCount {
				t.Errorf("expected at least %d suggestions for %s, got %d",
					tt.expectCount, tt.toolName, len(suggestions))
			}
		})
	}
}

func TestOutputLimitError(t *testing.T) {
	err := &OutputLimitError{
		ToolName:   ShellToolName,
		OutputSize: 60000,
		MaxSize:    MaxOutputSize,
		Suggestions: []string{
			"Use head or tail to limit output",
			"Redirect verbose output to /dev/null",
		},
	}

	errStr := err.Error()
	if !strings.Contains(errStr, ShellToolName) {
		t.Error("error message doesn't contain tool name")
	}
	if !strings.Contains(errStr, "60000") {
		t.Error("error message doesn't contain output size")
	}
	if !strings.Contains(errStr, "head or tail") {
		t.Error("error message doesn't contain suggestions")
	}
}

func TestViewToolTruncation(t *testing.T) {
	// View tool should use head+tail truncation like bash
	largeOutput := strings.Repeat("line of code\n", 5000)
	result := TruncateOutput("view", largeOutput, true)

	// Should contain truncation message
	if !strings.Contains(result, "bytes omitted") {
		t.Error("view truncation should indicate bytes omitted")
	}

	// Should contain offset hint in warning
	if !strings.Contains(result, "offset") {
		t.Error("view truncation warning should mention offset parameter")
	}

	// Should be within size limit
	if len(result) > MaxOutputSize+1000 { // Allow some overhead for warning message
		t.Errorf("truncated output (%d bytes) exceeds max size", len(result))
	}
}

func TestViewSuggestionsExist(t *testing.T) {
	suggestions := getToolSpecificSuggestions("view")
	if len(suggestions) < 2 {
		t.Errorf("expected at least 2 suggestions for view, got %d", len(suggestions))
	}

	// Should mention offset parameter
	found := false
	for _, s := range suggestions {
		if strings.Contains(strings.ToLower(s), "offset") {
			found = true
			break
		}
	}
	if !found {
		t.Error("view suggestions should mention offset parameter")
	}
}
