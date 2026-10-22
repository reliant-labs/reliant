// Copyright (c) 2025 Reliant Labs
package db

import core "github.com/reliant-labs/reliant/internal/db/core"

// ContextUsage contains context usage info for the compaction indicator
type ContextUsage struct {
	ThreadTokenCount    int64 `json:"thread_token_count"`   // Sum of output_tokens in current context
	CompactionThreshold int64 `json:"compaction_threshold"` // Threshold at which compaction triggers
	CurrentContextSeq   int64 `json:"current_context_seq"`  // Current context_sequence
}

// ChatFilters contains options for filtering chats.
type ChatFilters = core.ChatFilters

// ChatSearchFilters contains options for searching chats.
type ChatSearchFilters = core.ChatSearchFilters

// MessageListOptions contains options for filtering messages.
type MessageListOptions = core.MessageListOptions

// ProjectFilters contains options for filtering projects.
type ProjectFilters = core.ProjectFilters

// WorktreeFilters contains options for filtering worktrees.
type WorktreeFilters = core.WorktreeFilters

// BackgroundProcessFilters contains options for filtering background processes.
type BackgroundProcessFilters = core.BackgroundProcessFilters

// Attachment represents a file attachment.
// Kept as a facade alias for external API compatibility.
type Attachment = core.Attachment
