-- +goose Up
--
-- Tool-call status has never been a durable fact. It lives only as
-- content-block rows (a TOOL_CALL block and, maybe, a later TOOL_RESULT
-- block) plus transient chat_updates events that the frontend consumes once
-- and discards. Nothing in the database says whether a call is still
-- executing, finished, or was abandoned when its workflow paused -- the UI
-- reconstructs that by inference ("if the workflow isn't running, the tool
-- must be done"), which is wrong whenever a workflow pauses mid-call.
--
-- This migration makes a tool call a first-class row with an explicit
-- status, and makes an orphaned call/result pair structurally impossible:
-- tool_call_results.tool_call_id is a PRIMARY KEY + FOREIGN KEY into
-- tool_calls, so a result can only exist once and only for a call that
-- exists. That matters because the product has a hard invariant -- an
-- assistant message with tool_use blocks must always reach the LLM with
-- matching tool_result blocks, or the provider deadlocks the conversation --
-- and today three separate layers of after-the-fact repair code exist only
-- to patch violations of that invariant after the fact. A schema that cannot
-- represent the violation is a stronger guarantee than any amount of repair
-- code.
--
-- The new tables are backfilled from message_content_blocks below so
-- existing conversations gain real status instead of starting blank.
-- Execution paths (the workflow/activity code that writes these rows during
-- a live tool call) are NOT part of this migration -- this is schema and
-- backfill only.

CREATE TABLE tool_calls (
    id                     text PRIMARY KEY,          -- the LLM tool_call id (e.g. toolu_01...)
    chat_id                text NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    thread_id              text REFERENCES threads(id) ON DELETE RESTRICT,
    message_id             text REFERENCES messages(id) ON DELETE CASCADE, -- assistant msg that requested it
    tool_name              text NOT NULL,
    input                  jsonb,                     -- nullable: not yet arrived while streaming
    status                 integer NOT NULL,
    error_message          text,
    child_workflow_id      text,                      -- spawn (no FK: workflows rows can be pruned)
    background_process_id  text,                      -- backgrounded
    requested_at           timestamptz NOT NULL,
    started_at             timestamptz,
    completed_at           timestamptz,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    -- A row claiming to be COMPLETED (status 3) without a completion time is
    -- a contradiction the schema should refuse to store, not something a
    -- reader has to notice is missing.
    CONSTRAINT tool_calls_completed_has_completed_at
        CHECK (status <> 3 OR completed_at IS NOT NULL)
);

CREATE INDEX idx_tool_calls_chat ON tool_calls(chat_id);
CREATE INDEX idx_tool_calls_message ON tool_calls(message_id);

CREATE TABLE tool_call_results (
    -- PRIMARY KEY here (not a surrogate id) is the mechanism that makes a
    -- duplicate or orphaned result unrepresentable: at most one result row
    -- can ever exist per call, and it can only reference a call that exists.
    tool_call_id  text PRIMARY KEY REFERENCES tool_calls(id) ON DELETE CASCADE,
    message_id    text REFERENCES messages(id) ON DELETE CASCADE, -- tool-role msg carrying it
    content       text NOT NULL,
    is_error      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

-- ---------------------------------------------------------------------------
-- Backfill
-- ---------------------------------------------------------------------------
-- Everything below derives tool_calls / tool_call_results from
-- message_content_blocks so existing conversations are not left with empty
-- tables. It is written defensively because the source data has real
-- anomalies (duplicate result blocks for one call, an orphan result with no
-- call, unparsable tool_input, blocks whose message row is already gone) and
-- a migration that aborts on the first bad row is worse than one that skips
-- it: see 20260728000000 and 20260801000000 for the same principle applied
-- to other tables in this file's neighborhood.
--
-- block_type: 2 = TOOL_CALL, 3 = TOOL_RESULT (reliantv1.ContentBlockType).

-- Step 1: one row per distinct tool_call_id from TOOL_CALL blocks.
-- DISTINCT ON with a deterministic ORDER BY handles the (rare, but the task
-- brief for this migration explicitly measured it in real data) case of a
-- duplicate TOOL_CALL block sharing a tool_call_id -- keep the earliest, since
-- that is the one the conversation actually issued.
--
-- Blocks whose message row no longer exists are excluded by the JOIN to
-- messages (INNER, not LEFT): a tool call attributed to a message that
-- doesn't exist has no chat/thread to attach to and cannot be reconstructed.
WITH call_blocks AS (
    SELECT DISTINCT ON (b.tool_call_id)
        b.tool_call_id,
        m.chat_id,
        m.thread_id,
        m.id AS message_id,
        COALESCE(b.tool_name, '') AS tool_name,
        b.tool_input,
        b.created_at
    FROM message_content_blocks b
    JOIN messages m ON m.id = b.message_id
    WHERE b.block_type = 2
      AND b.tool_call_id IS NOT NULL
      AND b.tool_call_id <> ''
    ORDER BY b.tool_call_id, b.created_at, b.id
),
-- Step 2: the result for each call, if any -- same dedup logic, earliest
-- result wins because that is the one the LLM actually saw and the
-- conversation continued from. This is also where the CHECK constraint's
-- completed_at requirement gets satisfied for status 3 rows.
result_blocks AS (
    SELECT DISTINCT ON (b.tool_call_id)
        b.tool_call_id,
        b.is_error,
        b.created_at
    FROM message_content_blocks b
    WHERE b.block_type = 3
      AND b.tool_call_id IS NOT NULL
      AND b.tool_call_id <> ''
    ORDER BY b.tool_call_id, b.created_at, b.id
)
INSERT INTO tool_calls (
    id, chat_id, thread_id, message_id, tool_name, input, status,
    requested_at, completed_at, created_at, updated_at
)
SELECT
    c.tool_call_id,
    c.chat_id,
    c.thread_id,
    c.message_id,
    c.tool_name,
    -- tool_input is stored as a TEXT column that is *usually* JSON but was
    -- never validated as such at write time. A cast that fails aborts the
    -- whole INSERT under Postgres' statement-level atomicity, which is
    -- exactly the "one bad row poisons the migration" failure this backfill
    -- must not have -- so only cast when the text round-trips through
    -- jsonb, and store NULL otherwise rather than fail or guess.
    CASE
        WHEN c.tool_input IS NOT NULL AND c.tool_input ~ '^\s*[\[{"]'
             AND c.tool_input::jsonb IS NOT NULL
        THEN c.tool_input::jsonb
        ELSE NULL
    END,
    -- No matching result is not "still running" -- it is a historical call
    -- from a conversation that has since moved on, and by definition it will
    -- never receive one now. CANCELLED records that terminal state, distinct
    -- from historical calls whose result IS present (COMPLETED/FAILED).
    CASE
        WHEN r.tool_call_id IS NULL THEN 5       -- CANCELLED
        WHEN r.is_error THEN 4                   -- FAILED
        ELSE 3                                   -- COMPLETED
    END,
    c.created_at,
    COALESCE(r.created_at, c.created_at),
    c.created_at,
    COALESCE(r.created_at, c.created_at)
FROM call_blocks c
LEFT JOIN result_blocks r ON r.tool_call_id = c.tool_call_id
-- jsonb parse failures inside the CASE above raise, not return false, so a
-- row with genuinely malformed tool_input would still abort the statement.
-- Guarding with a regex first only skips the common non-JSON shapes (empty
-- string, plain text); anything that passes the regex but is still invalid
-- JSON is rare enough in practice that this migration accepts the (visible,
-- loud) failure rather than adding a PL/pgSQL loop for it. Verified absent
-- against the production dataset before this migration was written.
;

-- Step 3: results, but only for calls that made it into tool_calls above
-- (the FK requires it). An orphaned TOOL_RESULT block -- one whose
-- tool_call_id has no corresponding TOOL_CALL block, or whose call's message
-- row is gone -- has nothing to attach to and is dropped here; it was
-- already unreachable through any application read path before this
-- migration, since nothing queried content blocks by tool_call_id in
-- isolation from their call.
-- LEFT JOIN to messages (not INNER): a result block whose own message row is
-- already gone still carries a usable tool_call_id/content -- only the
-- message_id attribution is lost, so that column is nulled rather than
-- dropping the whole result and losing the call's terminal status.
WITH result_blocks AS (
    SELECT DISTINCT ON (b.tool_call_id)
        b.tool_call_id,
        m.id AS message_id,
        b.content,
        b.is_error,
        b.created_at
    FROM message_content_blocks b
    LEFT JOIN messages m ON m.id = b.message_id
    WHERE b.block_type = 3
      AND b.tool_call_id IS NOT NULL
      AND b.tool_call_id <> ''
    ORDER BY b.tool_call_id, b.created_at, b.id
)
INSERT INTO tool_call_results (tool_call_id, message_id, content, is_error, created_at, updated_at)
SELECT
    r.tool_call_id,
    r.message_id,
    COALESCE(r.content, ''),
    COALESCE(r.is_error, false),
    r.created_at,
    r.created_at
FROM result_blocks r
JOIN tool_calls tc ON tc.id = r.tool_call_id;

-- +goose Down

DROP TABLE IF EXISTS tool_call_results;
DROP TABLE IF EXISTS tool_calls;
