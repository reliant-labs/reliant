# Conversation Recovery System

## Overview

The conversation recovery system provides automatic recovery from interrupted LLM streaming and tool execution. It derives state purely from database structure (no heartbeat checks), is idempotent (safe to run multiple times), and handles all failure scenarios gracefully.

## Key Features

1. **Pure Database-Driven Recovery**: No external state or heartbeat tracking required
2. **Idempotent**: Can be safely called multiple times without creating duplicates
3. **Automatic Error Handling**: Adds appropriate error messages for interrupted operations
4. **Context-Aware**: Different error messages based on interruption point

## Database Schema

### assistant_message_streams Table

Tracks streaming completion for assistant messages:

```sql
CREATE TABLE assistant_message_streams (
    message_id TEXT PRIMARY KEY,
    streaming_started_at TIMESTAMP NOT NULL,
    streaming_completed_at TIMESTAMP,  -- NULL = interrupted, non-NULL = complete
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);
```

### tool_calls.cancellation_reason Column

Added to track why a tool call was cancelled:

```sql
ALTER TABLE tool_calls ADD COLUMN cancellation_reason TEXT;
```

## Core Recovery Function

### RecoverConversationState

Main entry point called at workflow start:

```go
err := recovery.RecoverConversationState(ctx, repo, temporalClient, chatID, thread)
```

**Logic:**

1. Get the last message in the chat
2. If not an assistant message, no recovery needed
3. Check if streaming was completed
4. If interrupted: add error results + user interruption message
5. If completed but tools incomplete: add error results for orphaned tools

## Recovery Scenarios

### Scenario 1-5: Clean States (No Recovery Needed)

- No messages in chat
- Last message is user or tool message
- Assistant message with completed streaming and no tool calls
- Assistant message with completed streaming and all tool results present

### Scenario 6: Streaming Interrupted, No Tool Calls

**State:**

- Last message: assistant (role=assistant)
- Stream: streaming_completed_at = NULL
- Content: partial text only

**Recovery Actions:**

1. Mark stream as complete
2. Add user message: "[Request interrupted by system restart]"

### Scenario 7: Streaming Interrupted, Partial Tool Calls

**State:**

- Last message: assistant
- Stream: streaming_completed_at = NULL
- Content: text + tool_call blocks without results

**Recovery Actions:**

1. Add error tool_result blocks for each orphaned tool_call
2. Mark tool_call entries as cancelled
3. Mark stream as complete
4. Add user interruption message

### Scenario 8: Tool Execution Interrupted (Was Running)

**State:**

- Last message: assistant
- Stream: streaming_completed_at SET (completed)
- Tool calls: status="executing"

**Recovery Actions:**

1. Add error tool_result: "Tool execution interrupted by system restart. State unknown."
2. Mark tool_call as cancelled with reason

### Scenario 9: Tool Never Started

**State:**

- Last message: assistant
- Stream: streaming_completed_at SET
- Tool calls: status="pending"

**Recovery Actions:**

1. Add error tool_result: "Tool execution cancelled (system restart before execution)."
2. Mark tool_call as cancelled

### Scenario 10: Multiple Tool Calls, Mixed States

**State:**

- Tool A: has result (clean)
- Tool B: status="executing" (interrupted)
- Tool C: status="pending" (never started)

**Recovery Actions:**

- Tool A: no recovery (has result)
- Tool B: add error result "interrupted, state unknown"
- Tool C: add error result "cancelled before execution"

### Scenario 11-12: Idempotency

Recovery is safe to run multiple times:

- Won't create duplicate error results (checks existence first)
- Won't add duplicate interruption messages
- Won't re-cancel already cancelled tools

## Error Messages

Three distinct error messages based on interruption point:

```go
const (
    msgToolInterrupted        = "Tool execution interrupted by system restart. State unknown."
    msgToolCancelledBeforeRun = "Tool execution cancelled (system restart before execution)."
    msgUserInterruption       = "[Request interrupted by system restart]"
)
```

## Implementation Details

### Repository Interface

Minimal interface required for recovery:

```go
type Repository interface {
    GetLastMessageInChat(ctx context.Context, chatID string) (*Message, error)
    CreateMessage(ctx context.Context, msg *Message) error
    ListContentBlocks(ctx context.Context, messageID string) ([]*MessageContentBlock, error)
    GetToolResultBlock(ctx context.Context, toolCallID string) (*MessageContentBlock, error)
    CreateContentBlock(ctx context.Context, block *MessageContentBlock) error
    GetToolCallByToolCallID(ctx context.Context, toolCallID string) (*ToolCall, error)
    MarkToolCallCancelled(ctx context.Context, toolCallID string, reason string) error
    GetMessageStream(ctx context.Context, messageID string) (*AssistantMessageStream, error)
    CompleteMessageStream(ctx context.Context, messageID string, completedAt time.Time) error
}
```

### Helper Functions

- `findOrphanedToolCalls`: Identifies tool_call blocks without matching tool_result blocks
- `determineToolErrorMessage`: Creates appropriate error message based on tool call status
- `recoverInterruptedStream`: Handles mid-streaming interruption
- `recoverOrphanedToolCalls`: Handles post-streaming tool execution interruption

## Testing

Comprehensive unit tests cover all 12 scenarios:

```bash
go test -v ./internal/recovery/...
```

Test coverage includes:

- All clean states (no recovery needed)
- Streaming interruption scenarios
- Tool execution interruption scenarios
- Mixed state scenarios
- Idempotency verification
- Helper function unit tests

## Usage Example

```go
import "github.com/reliant-labs/reliant/internal/recovery"

// At workflow start, before any processing
func StartWorkflow(ctx context.Context, repo db.Repository, temporalClient client.Client, chatID string, thread string) error {
    // Recover from any interrupted state
    if err := recovery.RecoverConversationState(ctx, repo, temporalClient, chatID, thread); err != nil {
        return fmt.Errorf("recovery failed: %w", err)
    }

    // Continue with normal workflow processing...
    return nil
}
```

## Migration

Apply migration 041 to add required schema changes:

```bash
goose -dir internal/db/migrations/postgres up
```

This adds:

- `assistant_message_streams` table
- `cancellation_reason` column to `tool_calls` table