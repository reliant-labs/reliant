// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// LastAssistantMessageResult is the final assistant turn extracted from a
// thread's current (fork/compaction-resolved) message list -- the canonical
// "what did this agent say" extraction, shared by anything that needs a
// thread's outcome rather than its full transcript (FetchThreadResult
// activity, spawn_status tool).
type LastAssistantMessageResult struct {
	// Found is false when the thread has no assistant message yet (empty
	// thread, or only user/system/tool messages so far).
	Found bool
	// Content is the concatenated text of the assistant message's text
	// blocks. Empty if the message carried no text (e.g. tool-call-only).
	Content string
	// Warning is the text of a display_style=warning message immediately
	// following the assistant message -- the thread ended abnormally (e.g.
	// max turns reached). Empty when there was none.
	Warning string
}

// LastAssistantMessage finds the last assistant message in threadID's
// current message list, and any warning message that immediately follows it.
func (s *Service) LastAssistantMessage(ctx context.Context, threadID string) (LastAssistantMessageResult, error) {
	messages, err := s.LoadCurrentMessages(ctx, threadID)
	if err != nil {
		return LastAssistantMessageResult{}, fmt.Errorf("failed to load thread messages: %w", err)
	}

	lastIndex := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			lastIndex = i
			break
		}
	}
	if lastIndex == -1 {
		return LastAssistantMessageResult{}, nil
	}

	blocks, err := s.repo.ListContentBlocks(ctx, messages[lastIndex].ID)
	if err != nil {
		return LastAssistantMessageResult{}, fmt.Errorf("failed to load assistant message content: %w", err)
	}
	result := LastAssistantMessageResult{Found: true, Content: joinTextBlocks(blocks)}

	for i := lastIndex + 1; i < len(messages); i++ {
		if messages[i].DisplayStyle != nil && *messages[i].DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING {
			if warningBlocks, err := s.repo.ListContentBlocks(ctx, messages[i].ID); err == nil {
				result.Warning = joinTextBlocks(warningBlocks)
			}
			break
		}
	}
	return result, nil
}

func joinTextBlocks(blocks []*db.MessageContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && b.Content != nil {
			parts = append(parts, *b.Content)
		}
	}
	return strings.Join(parts, "\n")
}
