-- name: GetNodeMessages :many
-- Get all messages in a specific context window
-- Ordered by seq for deterministic ordering
SELECT * FROM messages
WHERE context_window_id = $1
ORDER BY seq ASC;

-- name: GetNodeContentBlocks :many
-- Get all content blocks for messages in a specific context window
SELECT
    mcb.id,
    mcb.message_id,
    mcb.position,
    mcb.block_type,
    mcb.content,
    mcb.tool_name,
    mcb.tool_input,
    mcb.tool_call_id,
    mcb.is_error,
    mcb.version,
    mcb.created_at,
    mcb.updated_at
FROM message_content_blocks mcb
INNER JOIN messages m ON m.id = mcb.message_id
WHERE m.context_window_id = $1
ORDER BY m.seq ASC, mcb.position ASC;

-- name: GetMessagesWithContentBlocks :many
-- Get messages with their content blocks in a single query for efficiency
-- Returns denormalized rows that need to be assembled into message objects
SELECT
    m.id as msg_id,
    m.chat_id,
    m.ordinal,
    m.context_window_id,
    m.role,
    m.model,
    m.agent,
    m.token_count,
    m.cost,
    m.workflow_id,
    m.run_id,
    m.created_at as msg_created_at,
    m.updated_at as msg_updated_at,
    mcb.id as block_id,
    mcb.position as block_position,
    mcb.block_type,
    mcb.content,
    mcb.tool_name,
    mcb.tool_input,
    mcb.tool_call_id,
    mcb.is_error
FROM messages m
LEFT JOIN message_content_blocks mcb ON mcb.message_id = m.id
WHERE m.context_window_id = $1
ORDER BY m.seq ASC, mcb.position ASC;

-- name: GetMessagesForThread :many
-- Get all messages for a thread across all context windows
-- Useful for UI display showing full history
SELECT m.*, cw.sequence as context_sequence
FROM messages m
JOIN context_windows cw ON cw.id = m.context_window_id
WHERE cw.thread_id = $1
ORDER BY cw.sequence ASC, m.seq ASC;