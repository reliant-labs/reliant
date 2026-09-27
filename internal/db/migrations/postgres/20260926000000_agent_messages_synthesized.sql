-- +goose Up
--
-- Mark mailbox rows the reconciler FABRICATED, so a sub-agent's real report
-- can replace one.
--
-- idx_agent_messages_one_terminal_report_per_spawn allows exactly one
-- terminal report per spawn call, and the stranded-background-spawn sweep
-- writes a stand-in into that slot when a spawn looks like it ended without
-- reporting. A stand-in written for a spawn that later reports for real — a
-- resume that reset-and-replayed the run re-executes its background spawns —
-- permanently occupied the slot, and the real report died on the unique index
-- (SQLSTATE 23505) after its retries. Observed on chat e6c09159: two finished
-- sub-agents' results lost, each replaced by "this report was never
-- delivered" for a parent that was, by then, running again.
--
-- A column rather than a body match: the stand-in's wording is prose, and a
-- behaviour gated on prose breaks the first time someone edits a sentence.
-- The live enqueue upserts over a row with synthesized = true and never over a
-- real one (see EnqueueTerminalAgentReport).
ALTER TABLE agent_messages
    ADD COLUMN synthesized BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE agent_messages DROP COLUMN IF EXISTS synthesized;
