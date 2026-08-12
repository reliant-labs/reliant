-- name: GetThreadTokenCountAtSeq :one
-- Get the token count from the most recent message with token data at or before maxSeq.
-- This is THE UNIFIED function for getting token counts.
-- 
-- Parameters:
--   $1: thread ID
--   $2: context sequence (from context_windows.sequence)
--   $3: maxSeq (optional - pass NULL for current/latest)
--
-- Returns 0 if no messages have token data.
-- For fork resolution: pass the fork message's seq to get tokens at that point.
-- $3 is a fork cursor, not the ordering key -- it bounds how much of the
-- parent's history the fork inherited. It moved from ordinal to seq with
-- 20260803010000_fork_points_reference_messages.sql, which made fork points
-- message references; seq is the chat-global order every other read path uses.
SELECT CAST(COALESCE(
    (
        SELECT COALESCE(m.token_count, 0)
        FROM messages m
        JOIN context_windows cw ON cw.id = m.context_window_id
        WHERE cw.thread_id = $1 AND cw.sequence = $2
          AND m.token_count IS NOT NULL
          AND ($3 IS NULL OR m.seq <= $3)
        ORDER BY m.seq DESC
        LIMIT 1
    ), 0) AS BIGINT) AS total_tokens;

-- name: GetCurrentContextSequence :one
-- Get the maximum context_window.sequence for a thread (current context after compactions)
SELECT CAST(COALESCE(MAX(cw.sequence), 0) AS BIGINT) AS context_sequence
FROM context_windows cw
WHERE cw.thread_id = $1;

-- name: GetContextWindowTokenCount :one
-- Get the token count for a specific context window
SELECT CAST(COALESCE(
    (
        SELECT COALESCE(m.token_count, 0)
        FROM messages m
        WHERE m.context_window_id = $1
          AND m.token_count IS NOT NULL
        ORDER BY m.seq DESC
        LIMIT 1
    ), 0) AS BIGINT) AS total_tokens;