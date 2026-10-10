// Copyright (c) 2025 Reliant Labs
package db

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeContentBlock_ReplacesInvalidUTF8(t *testing.T) {
	invalid := "before\xe2..after"
	block := &MessageContentBlock{
		Content:          &invalid,
		ToolName:         &invalid,
		ToolInput:        &invalid,
		ToolCallID:       &invalid,
		ThoughtSignature: &invalid,
		Phase:            &invalid,
		ActivityID:       &invalid,
		WorkflowRunID:    &invalid,
	}

	normalizeContentBlock(block)

	for name, text := range map[string]*string{
		"content":           block.Content,
		"tool name":         block.ToolName,
		"tool input":        block.ToolInput,
		"tool call ID":      block.ToolCallID,
		"thought signature": block.ThoughtSignature,
		"phase":             block.Phase,
		"activity ID":       block.ActivityID,
		"workflow run ID":   block.WorkflowRunID,
	} {
		if text == nil {
			t.Errorf("%s is nil, want valid UTF-8", name)
			continue
		}
		if !utf8.ValidString(*text) {
			t.Errorf("%s = %q, want valid UTF-8", name, *text)
		}
		if !strings.Contains(*text, "�") {
			t.Errorf("%s = %q, want malformed bytes replaced", name, *text)
		}
	}
}

func TestNormalizeContentBlock_LeavesValidPointersUntouched(t *testing.T) {
	value := "valid"
	block := &MessageContentBlock{Content: &value}

	normalizeContentBlock(block)

	if block.Content != &value {
		t.Fatal("valid content pointer changed")
	}
}

func TestValidUTF8(t *testing.T) {
	if got := validUTF8("before\xe2..after"); !utf8.ValidString(got) || !strings.Contains(got, "�") {
		t.Fatalf("validUTF8 returned %q", got)
	}
	if got := validUTF8("valid"); got != "valid" {
		t.Fatalf("validUTF8 changed valid content to %q", got)
	}
}
