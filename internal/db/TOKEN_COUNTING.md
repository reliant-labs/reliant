# Token Counting Architecture

This document explains how token counting works in Reliant, including fork inheritance.

## Overview

Token counts flow from LLM API responses to messages to queries:

```
LLM API Response (Anthropic/OpenAI/etc.)
    ↓
message.input_tokens, output_tokens, cache_*_tokens
    ↓
GetThreadTokenCount(thread, maxOrdinal)
    ↓
Compaction threshold check / UI display
```

## Key Insight: LLM Tokens Are Cumulative

When an LLM responds, the `input_tokens` field represents the **total context it saw**, not just the new message. This includes:
- System prompts
- Tool definitions
- All prior messages in the conversation
- The new user message

So if you have 10 messages and the LLM responds, `input_tokens` might be 50,000 - representing the entire context window size at that point.

## The Single Source of Truth

**Messages own token data.** We don't cache tokens elsewhere because:
1. Messages are where tokens originate (from LLM responses)
2. Caching creates sync issues
3. The query is simple and indexed

## GetThreadTokenCount: The Unified Function

```go
// GetThreadTokenCount returns the cumulative token count for a thread.
// This handles fork inheritance automatically by walking up the fork chain.
//
// Parameters:
// - thread: the thread ID
// - maxOrdinal: optional - if set, returns tokens at that ordinal (for fork points)
//               if nil, returns current tokens (most recent message with data)
//
// Returns 0 if no token data exists (caller should estimate if needed).
func (r *Repo) GetThreadTokenCount(ctx context.Context, thread string, maxOrdinal *int64) (int64, error)
```

### Algorithm

1. Get the Thread record to find conversation_id and fork metadata
2. Get current context sequence for the thread
3. Query for the last message with token data at or before `maxOrdinal`:
   ```sql
   SELECT input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens
   FROM messages
   WHERE thread = ? AND context_sequence = ?
     AND input_tokens IS NOT NULL
     AND (? IS NULL OR ordinal <= ?)
   ORDER BY ordinal DESC
   LIMIT 1
   ```
4. If local token data exists, return it (it's cumulative, includes inherited context)
5. If no local data AND thread has ParentThreadID + ForkAtOrdinal:
   - Recursively call `GetThreadTokenCount(parentThread, forkAtOrdinal)`
6. Return 0 if no token data anywhere

## Fork Inheritance

When you fork at message 5 of a conversation:

```
Parent Thread (messages 1-10)
    │
    ├─ msg 1 (user)
    ├─ msg 2 (assistant, tokens: 5000)
    ├─ msg 3 (user)
    ├─ msg 4 (assistant, tokens: 12000)  ← cumulative at this point
    ├─ msg 5 (user)  ← FORK HERE
    ├─ msg 6 (assistant, tokens: 18000)
    ...

Child Thread (forked at ordinal 5)
    │
    └─ No messages yet → GetThreadTokenCount returns 12000 (from parent's msg 4)
```

### Why "return local if exists" works

When the LLM responds in the forked thread, `input_tokens` includes:
- All inherited messages from parent (1-5)
- Any new messages in the fork

So the child's token count is always >= parent's token count at fork point. We don't need `max()` - if local data exists, it's already cumulative.

## Token Estimation

For cases with no LLM response yet (pre-call estimation), use:

```go
tokens.EstimateTokens(text string) int  // 4 chars per token
```

This is used in `TrimMessagesToFitContextWithFullEstimate` to prevent context overflow before the first LLM call.

## ContextWindow Table

The `context_windows` table tracks compaction boundaries, **not tokens**:

```sql
CREATE TABLE context_windows (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    sequence INTEGER NOT NULL DEFAULT 0,  -- 0 for initial, increments on compaction
    compaction_summary_message_id TEXT,   -- Links to summary message
    created_at DATETIME NOT NULL
);
```

Token columns were removed because:
1. They duplicated data from messages
2. They could get out of sync
3. Forks at arbitrary ordinals don't work with aggregate totals

## Compaction

When `GetThreadTokenCount(thread, nil) > 185000`:
1. Compaction is triggered
2. Summary message is created
3. New context_window with incremented sequence
4. Old messages are preserved but not loaded (new context_sequence)

## Files Changed

- `internal/db/repository.go` - `GetThreadTokenCount` interface
- `internal/db/repository_impl.go` - Implementation with fork recursion
- `internal/db/sqlite/queries/context_usage.sql` - `GetThreadTokenCountAtOrdinal` query
- `internal/tokens/tokens.go` - Canonical estimation function
