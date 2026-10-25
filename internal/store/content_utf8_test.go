// Copyright (c) 2025 Reliant Labs
package store

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestNormalizeContentBlock_ReplacesInvalidUTF8(t *testing.T) {
	invalid := "before\xe2..after"
	block := &core.MessageContentBlock{
		Content:          &invalid,
		ToolName:         &invalid,
		ToolInput:        &invalid,
		ToolCallID:       &invalid,
		ThoughtSignature: &invalid,
		Phase:            &invalid,
		ActivityID:       &invalid,
		WorkflowRunID:    &invalid,
	}

	NormalizeContentBlock(block)

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
	block := &core.MessageContentBlock{Content: &value}

	NormalizeContentBlock(block)

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

// PostgreSQL text refuses NUL even though it is valid UTF-8 ("invalid byte
// sequence for encoding \"UTF8\": 0x00"); a NUL in a tool result ended a prod
// sub-agent when save_message exhausted its retries on it (2026-10-19).
func TestValidUTF8ReplacesNUL(t *testing.T) {
	got := validUTF8("before\x00after\xe2..")
	if strings.IndexByte(got, 0) >= 0 || !utf8.ValidString(got) {
		t.Fatalf("validUTF8 returned %q, want no NUL and valid UTF-8", got)
	}
	if got != "before\uFFFDafter\uFFFD.." {
		t.Fatalf("validUTF8 = %q", got)
	}
	nul := "a\x00b"
	if p := validUTF8Ptr(&nul); p == &nul || *p != "a\uFFFDb" {
		t.Fatalf("validUTF8Ptr kept the NUL: %q", *p)
	}
}
