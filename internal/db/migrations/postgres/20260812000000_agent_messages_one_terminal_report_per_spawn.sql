-- +goose Up
--
-- One terminal mailbox report per spawn call (spec:
-- async-spawn-and-agent-messaging.md, §7.1's mailbox-anchored reconciler
-- sweep).
--
-- The sweep this migration enables and the detached spawn goroutine that
-- normally reports a background spawn's outcome
-- (dispatchSpawnBackground/workflow.go) both write into the SAME mailbox row
-- shape: a terminal-kind (2=completion, 3=cancelled, 4=failed) agent_messages
-- row keyed by the spawn's tool_call_id. Two writers that can both decide "no
-- report exists yet, I should write one" -- the goroutine finishing late and
-- a reconciler pass running concurrently, or two reconciler passes racing
-- each other across replicas -- is a plain TOCTOU: a SELECT-then-INSERT in
-- application code has a window between the check and the write that a
-- concurrent pass can land in, producing two completion notifications for
-- one spawn. The parent would then see the sub-agent's result delivered
-- twice in its drained mailbox.
--
-- A DB constraint closes that window unconditionally, because Postgres
-- evaluates it at INSERT time under the row lock, not at some earlier check
-- time. The sweep can then use INSERT ... ON CONFLICT ... DO NOTHING and be
-- correct under arbitrary concurrency without any additional locking on its
-- own: this is the same choice AgentMessage's sibling queue-shaped tables
-- (questions, approvals) already made for their own create-once semantics.
--
-- Partial (kind IN (2,3,4) only): kind=1 (spawn_send messages) and kind=5
-- (human messages) can legitimately repeat many times against the same
-- to_thread_id and are not scoped to a tool_call_id at all -- most such rows
-- have tool_call_id NULL, which a plain UNIQUE(tool_call_id) index would not
-- even see (NULLs are always distinct in a Postgres unique index), but the
-- restriction is written explicitly rather than relied on implicitly so the
-- invariant reads as "one terminal report", not "one row per non-null
-- tool_call_id".
CREATE UNIQUE INDEX idx_agent_messages_one_terminal_report_per_spawn
    ON agent_messages(tool_call_id)
    WHERE kind IN (2, 3, 4);

-- +goose Down

DROP INDEX IF EXISTS idx_agent_messages_one_terminal_report_per_spawn;
