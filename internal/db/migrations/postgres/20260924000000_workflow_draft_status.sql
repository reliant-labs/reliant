-- +goose Up

-- Workflow drafts get an explicit lifecycle instead of a cached validity
-- verdict (specs/workflow-draft-lifecycle.md):
--   draft    — work in progress; may be invalid; never runnable.
--   complete — passed validation when it was marked complete; runnable.
-- Validity itself is no longer stored: it went stale whenever the validator got
-- stricter. It is computed on read and at the two gates (mark-complete and
-- load-for-execution).
ALTER TABLE workflow_drafts
    ADD COLUMN status TEXT NOT NULL DEFAULT 'draft'
    CONSTRAINT workflow_drafts_status_valid CHECK (status IN ('draft', 'complete'));

-- Existing rows keep their runnability: the stored flag is the best signal SQL
-- has. The execution gate re-validates at run start anyway.
UPDATE workflow_drafts SET status = 'complete' WHERE is_valid = 1;

ALTER TABLE workflow_drafts DROP COLUMN is_valid;
ALTER TABLE workflow_drafts DROP COLUMN validation_errors;

