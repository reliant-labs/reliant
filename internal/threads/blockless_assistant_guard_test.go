// Copyright (c) 2025 Reliant Labs
package threads

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// PREVENTION half. An assistant turn that produced nothing must be REJECTED at
// write time, not warned about and stored.
//
// The stored row has zero content blocks, becomes the tail of the thread, and
// makes CallLLM's end-of-history guard fail every subsequent turn — forever,
// because the retry ladder keeps re-reading the same row. This used to warn and
// continue; warning was wrong, because the row it allowed is unrecoverable
// without editing the database, while rejecting costs only a turn that had no
// content to lose.
func TestValidateSaveMessageOpts_AssistantContent(t *testing.T) {
	const assistant = int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	tests := []struct {
		name    string
		opts    SaveMessageOpts
		wantErr bool
	}{
		{
			name:    "blockless assistant is rejected",
			opts:    SaveMessageOpts{ChatID: "c", Thread: "t", Role: assistant},
			wantErr: true,
		},
		{
			name:    "text is enough",
			opts:    SaveMessageOpts{ChatID: "c", Thread: "t", Role: assistant, Content: "hello"},
			wantErr: false,
		},
		{
			name: "tool calls alone are enough",
			opts: SaveMessageOpts{ChatID: "c", Thread: "t", Role: assistant,
				ToolCalls: []ToolCall{{ID: "tc1", Name: "bash"}}},
			wantErr: false,
		},
		{
			// createAssistantContentBlocks emits a thinking block, so a
			// reasoning-only turn is NOT blockless and must stay allowed.
			name: "thinking alone is enough",
			opts: SaveMessageOpts{ChatID: "c", Thread: "t", Role: assistant,
				Thinking: &ThinkingContent{Content: "reasoning"}},
			wantErr: false,
		},
		{
			// A signature with no thinking text produces no block.
			name: "empty thinking with only a signature is still blockless",
			opts: SaveMessageOpts{ChatID: "c", Thread: "t", Role: assistant,
				Thinking: &ThinkingContent{Signature: "sig"}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSaveMessageOpts(tc.opts)
			if tc.wantErr && err == nil {
				t.Fatal("expected the write to be refused")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected the write to be allowed, got: %v", err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "no content") {
				t.Fatalf("error should explain the cause, got: %v", err)
			}
		})
	}
}
