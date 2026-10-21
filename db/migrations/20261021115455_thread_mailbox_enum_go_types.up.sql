-- forge:backward-compatible — catalog comments only; no table or column definition changes, and the previous release never reads comments.

-- Integer columns of the thread and mailbox tables (P5b lane L1b) that hold an
-- internal/db/core enum project to that type on the generated entity
-- (internal/store), so the ported queries map the field without a cast and
-- name their filters (AgentMessageStatusQueued, not 1).
COMMENT ON COLUMN threads.status IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.ThreadStatus';
COMMENT ON COLUMN agent_messages.kind IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.AgentMessageKind';
COMMENT ON COLUMN agent_messages.status IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.AgentMessageStatus';
