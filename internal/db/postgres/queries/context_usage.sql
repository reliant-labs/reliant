-- name: GetThreadTokenCountAtOrdinal :one
-- Get the token count from the most recent message with token data at or before maxOrdinal.
-- This is THE UNIFIED function for getting token counts.
-- 
-- Parameters:
--   $1: thread ID
--   $2: context sequence (from context_windows.sequence)
--   $3: maxOrdinal (optional - pass NULL for current/latest)
--
-- Returns 0 if no messages have token data.
-- For fork resolution: pass the fork ordinal to get tokens at that point.
SELECT CAST(COALESCE(
    (
        SELECT COALESCE(m.token_count, 0)
        FROM messages m
        JOIN context_windows cw ON cw.id = m.context_window_id
        WHERE cw.thread_id = $1 AND cw.sequence = $2
          AND m.token_count IS NOT NULL
          AND ($3 IS NULL OR m.ordinal <= $3)
        ORDER BY m.ordinal DESC
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
        ORDER BY m.ordinal DESC
        LIMIT 1
    ), 0) AS BIGINT) AS total_tokens;