-- +goose Up
-- +goose StatementBegin

-- synthesized marks a terminal spawn report (kind 2/3/4) the reconciler
-- fabricated because the real one never arrived. A real report supersedes a
-- synthesized one (EnqueueSpawnReport); a real report is never overwritten.
-- From here on the COLUMN is the contract -- nothing may infer it from body
-- text. See docs/incidents/2026-10-04-spawn-report-collision.md.
--
-- IF NOT EXISTS because migrations are replayed against databases whose schema
-- already has this column (access_tokens_renumber_repair_test.go); the backfill
-- below is idempotent for the same reason.
ALTER TABLE agent_messages ADD COLUMN IF NOT EXISTS synthesized boolean NOT NULL DEFAULT false;

-- One-time backfill: the body prefixes below are every placeholder wording the
-- reconciler ever shipped. Body matching exists ONLY here.
UPDATE agent_messages SET synthesized = true
WHERE kind IN (2, 3, 4)
  AND tool_call_id IS NOT NULL
  AND (
       body LIKE 'Sub-agent finished while its result was lost in transit%'
    OR body LIKE 'Sub-agent "%" finished, but the thread that spawned it had already exited%'
    OR body LIKE 'Sub-agent''s run expired before it could report back%'
    OR body LIKE 'Sub-agent "%" expired before it could report back%'
  );

-- +goose StatementEnd
