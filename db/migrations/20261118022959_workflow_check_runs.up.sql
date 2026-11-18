-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: workflow_check_runs
-- Created: 2026-11-18T02:29:59Z
--
-- The GitHub check run a workflow's `check:` block declares, one row per
-- firing (internal/workflowcheck). The row is the engine's record of the
-- check: written when the firing resolves the workflow, stamped by the run
-- when its first node starts and when it ends, and synced to GitHub by the
-- worker's check syncer, which derives the check's state from the firing and
-- its run and concludes it on every ending.
--
--   trigger_event_id, name  the firing and the check's name: one check per
--                           firing, so a retried or resumed fire finds its row
--   user_id, org_id,        whose check it is and who writes it: the trigger's
--   acting_identity,        owner, org, acting identity and name, copied when
--   automation              the firing resolved
--   owner, repo, head_sha   where the check is posted
--   timeout_seconds         from the firing; past it the check concludes timed_out
--   started_at              the run's first node started
--   ended_status, verdict,  how the run saw its own end, and the check's
--   output, output_error    output it evaluated (or why it could not)
--   github_check_run_id,    what GitHub holds: the check run's id, its status
--   github_status,          and conclusion as last written, and how many of
--   github_conclusion,      the output's annotations were sent, and a digest
--   annotations_sent,       of what was last written, so only a change is
--   github_digest           written again
--   paused_since            when the syncer first saw the run paused
--   next_sync_at            the syncer's queue: when the row is next due
--   claimed_by,             the lease: which syncer holds the row, until when
--   claimed_until,          (renewed by its heartbeat while it syncs), and
--   lease_generation        the fencing token, bumped by every claim; a
--                           sync's save lands only while the generation it
--                           claimed is still the row's, so a syncer whose
--                           lease expired and was retaken writes nothing
--   attempts, last_error    consecutive failed GitHub writes, and the last one
--   finished_at             nothing is left to sync (concluded, or given up)
--
-- It goes with the firing (ON DELETE CASCADE) and with the owner's account
-- (account purge).
--
-- Post-deploy verification (prod): the table exists (a count, any value):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify workflow_check_runs" "SELECT count(*) FROM workflow_check_runs"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread, and a `check:` block is then an unknown workflow field
-- that release refuses to load, as it would without this table.
CREATE TABLE workflow_check_runs (
    id                  text NOT NULL PRIMARY KEY,
    trigger_event_id    text NOT NULL REFERENCES trigger_events(id) ON DELETE CASCADE,
    name                text NOT NULL,
    user_id             text NOT NULL,
    org_id              text,
    acting_identity     text NOT NULL DEFAULT '',
    automation          text NOT NULL DEFAULT '',
    owner               text NOT NULL,
    repo                text NOT NULL,
    head_sha            text NOT NULL,
    timeout_seconds     bigint NOT NULL,
    started_at          timestamptz,
    ended_status        text NOT NULL DEFAULT '',
    verdict             text NOT NULL DEFAULT '',
    output              jsonb,
    output_error        text NOT NULL DEFAULT '',
    github_check_run_id bigint,
    github_status       text NOT NULL DEFAULT '',
    github_conclusion   text NOT NULL DEFAULT '',
    annotations_sent    integer NOT NULL DEFAULT 0,
    github_digest       text NOT NULL DEFAULT '',
    paused_since        timestamptz,
    next_sync_at        timestamptz NOT NULL,
    claimed_by          text NOT NULL DEFAULT '',
    claimed_until       timestamptz,
    lease_generation    bigint NOT NULL DEFAULT 0,
    attempts            integer NOT NULL DEFAULT 0,
    last_error          text NOT NULL DEFAULT '',
    finished_at         timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workflow_check_runs_event_name UNIQUE (trigger_event_id, name),
    CONSTRAINT workflow_check_runs_fields_nonempty CHECK (name <> '' AND user_id <> '' AND owner <> '' AND repo <> '' AND head_sha <> ''),
    CONSTRAINT workflow_check_runs_timeout_positive CHECK (timeout_seconds > 0),
    CONSTRAINT workflow_check_runs_ended_status_check CHECK (ended_status IN ('', 'completed', 'failed', 'cancelled')),
    CONSTRAINT workflow_check_runs_github_status_check CHECK (github_status IN ('', 'queued', 'in_progress', 'completed'))
);
-- The indexes are built in this migration's transaction, not CONCURRENTLY:
-- the table is created empty just above, so nothing reads or writes it yet
-- and the build blocks nobody.
--
-- The syncer's queue: only rows with something left to sync.
CREATE INDEX workflow_check_runs_due ON workflow_check_runs (next_sync_at) WHERE finished_at IS NULL;
CREATE INDEX workflow_check_runs_user ON workflow_check_runs (user_id);
