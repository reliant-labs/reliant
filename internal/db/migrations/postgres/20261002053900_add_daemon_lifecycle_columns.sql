-- +goose Up

-- The registry becomes the one daemon list the UI calls
-- (docs/design/one-daemon-list.md, option (b)), so it has to be able to say
-- what a managed machine is DOING and not only whether a stream is attached
-- to it right now.
--
-- Attachment freshness answers "can I route work here", which is the whole of
-- what this table could say since the demote migration dropped its status
-- column. It cannot distinguish a machine that is mid-provision from one that
-- crashed — both have no attachment — and that difference is the entire
-- content of the spinner a user is watching. That vocabulary lives in the
-- Workspace CR, which only the control-plane operator watches, so it arrives
-- here as daemon.v1.state.<id>.lifecycle events.
--
-- These columns are deliberately NOT a second source of truth for liveness.
-- Liveness stays derived from daemon_attachment; lifecycle_phase is a mirror
-- of state this service does not own, and the registry handler composes the
-- two. Keeping them in separate columns is what makes that composition
-- auditable rather than a column whose meaning depends on who wrote it last.
ALTER TABLE daemons
    -- The public lifecycle vocabulary (provisioning / cloning / ready /
    -- suspending / suspended / failed), not the raw Kubernetes phase. NULL
    -- means "never reported", which is the correct and permanent state for
    -- every self-hosted daemon — they have no Workspace CR and no operator.
    ADD COLUMN IF NOT EXISTS lifecycle_phase TEXT,
    -- Provisioned machine size. NULL for self-hosted daemons.
    ADD COLUMN IF NOT EXISTS size TEXT,
    -- Human-readable reason for the most recent lifecycle transition, e.g.
    -- "image pull failed". Shown verbatim in the UI, so publishers must keep
    -- it user-safe.
    ADD COLUMN IF NOT EXISTS last_status_message TEXT NOT NULL DEFAULT '',
    -- When lifecycle_phase / last_status_message last changed, as observed by
    -- the publisher. This is the ordering key: a lifecycle event older than
    -- the stored value is dropped, which is how out-of-order delivery on a
    -- plain-NATS (no-redelivery, newest-wins) stream stays safe.
    ADD COLUMN IF NOT EXISTS last_status_changed_at TIMESTAMPTZ,
    -- OOM accounting mirrored from the Workspace CR status. NULL/0 when no
    -- OOM kill has been observed.
    ADD COLUMN IF NOT EXISTS last_oom_killed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS oom_kill_count INTEGER NOT NULL DEFAULT 0;

-- Constrain the phase to the vocabulary readers can interpret. An
-- unrecognized phase written by a future publisher should fail the write
-- rather than become a seventh state the UI silently renders as blank.
ALTER TABLE daemons
    DROP CONSTRAINT IF EXISTS daemons_lifecycle_phase_check;
ALTER TABLE daemons
    ADD CONSTRAINT daemons_lifecycle_phase_check
    CHECK (lifecycle_phase IS NULL OR lifecycle_phase IN (
        'provisioning', 'cloning', 'ready', 'suspending', 'suspended', 'failed'
    ));
