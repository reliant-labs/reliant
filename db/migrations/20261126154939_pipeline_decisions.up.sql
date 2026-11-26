-- forge:backward-compatible — a new table nothing older reads, and a foreign key on pipeline_repos.queue_paused_by_decision_id, a column nothing writes (every row has it NULL)

-- Migration: pipeline_decisions
-- Created: 2026-11-26T15:49:39Z
--
-- Owner decisions (pipeline redesign §4.3; build plan task R3). Inert:
-- nothing reads or writes this table until the pipeline ships behind
-- `pipeline.enabled`.
--
-- A decision is made only by a signed-in person (DecidePipeline: a browser
-- session plus the org or per-kind role), or, for an emergency_land alone, by
-- the project's auto_allow policy, which names the pipeline_setting_changes
-- row that set it. Agents may only insert a `requested` row of a kind that
-- allows requests. No GitHub input (label, comment, review) ever creates one.
--
-- Tenancy as on every pipeline table (20261125211730_pipeline_repos_and_settings).
-- Each closed vocabulary is a named CHECK that declares the Go set it mirrors
-- (internal/db/core/pipeline_decisions.go).
--
-- It also adds the foreign key R1 deferred, pipeline_repos.queue_paused_by_decision_id
-- → pipeline_decisions(id). It is added valid, here, not NOT VALID plus a
-- validate migration: the column is NULL in every row (nothing writes it, and
-- nothing could name a decision before this table existed), so the
-- validation scan finds nothing to check, and pipeline_repos itself is
-- unwritten until the pipeline ships. lock_timeout bounds the wait for its
-- SHARE ROW EXCLUSIVE lock.
--
-- Post-deploy verification (prod): the table exists and is empty, and the
-- foreign key is validated:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pipeline decisions" \
--     "SELECT (SELECT count(*) FROM pipeline_decisions), (SELECT convalidated FROM pg_constraint WHERE conname = 'pipeline_repos_queue_paused_by_decision_id_fkey')"
--   → 0, true
--
-- Rollback: none needed. The previous release never names this table or the
-- column; they stay in place, unread.
SET LOCAL lock_timeout = '5s';

CREATE TABLE pipeline_decisions (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    subject_kind TEXT NOT NULL,
    -- pr_records.id (pr_head, pr), pipeline_repos.id, releases.id or
    -- incidents.id, by subject_kind.
    subject_id TEXT NOT NULL CHECK (subject_id <> ''),
    -- The head a pr_head decision binds to: a push voids it.
    head_sha TEXT,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    -- The decision's value, e.g. {"priority": 95}.
    value_json JSONB,
    reason TEXT NOT NULL CHECK (reason <> ''),
    -- Set when an agent requested the decision.
    requested_by_run_id TEXT,
    requested_by_principal TEXT,
    -- The Reliant user who decided. Null only for a policy decision, or a
    -- request nobody has decided.
    decided_by_user_id TEXT CHECK (decided_by_user_id <> ''),
    -- How it was decided; null until it is.
    decided_via TEXT,
    -- The pipeline_setting_changes row that set the policy in force, for a
    -- policy decision: who set auto_allow, and when.
    policy_change_id TEXT REFERENCES pipeline_setting_changes (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoked_by_user_id TEXT CHECK (revoked_by_user_id <> ''),
    CONSTRAINT pipeline_decisions_subject_kind
        CHECK (subject_kind IN ('pr_head', 'pr', 'repo', 'release', 'incident')),
    CONSTRAINT pipeline_decisions_kind CHECK (kind IN (
        'approve_breaking_proto', 'approve_breaks_rollback', 'approve_prod_fix', 'hold',
        'release_hold', 'priority', 'emergency_land', 'release_now', 'release_promote',
        'pause_queue', 'resolve_needs_person', 'close_pr')),
    CONSTRAINT pipeline_decisions_state
        CHECK (state IN ('requested', 'approved', 'denied', 'expired', 'applied', 'revoked')),
    CONSTRAINT pipeline_decisions_decided_via CHECK (decided_via IN ('web', 'desktop', 'policy')),
    -- §4.3's two CHECKs, as written: a policy decision names the setting
    -- change in force, and only a policy decision does; every other decision
    -- names the user who made it, unless it is still a request.
    CONSTRAINT pipeline_decisions_policy_change
        CHECK ((decided_via = 'policy') = (policy_change_id IS NOT NULL)),
    CONSTRAINT pipeline_decisions_decided_by
        CHECK ((decided_via <> 'policy') = (decided_by_user_id IS NOT NULL OR state = 'requested')),
    -- Both are NULL when decided_via is (NULL compares to nothing), so these
    -- close that gap: an undecided row names no decider and no decision
    -- time, and only a request (open, or lapsed undecided) is undecided.
    CONSTRAINT pipeline_decisions_undecided CHECK (
        (decided_via IS NULL) = (decided_at IS NULL)
        AND (decided_via IS NOT NULL OR (decided_by_user_id IS NULL AND policy_change_id IS NULL))),
    CONSTRAINT pipeline_decisions_decided_states CHECK (CASE
        WHEN state = 'requested' THEN decided_via IS NULL
        WHEN state = 'expired' THEN true
        ELSE decided_via IS NOT NULL END),
    -- The policy decides an emergency_land, and nothing else (§3.8).
    CONSTRAINT pipeline_decisions_policy_kind CHECK (decided_via <> 'policy' OR kind = 'emergency_land'),
    -- Agents request only the kinds that allow requests
    -- (core.PipelineRequestableDecisionKinds), and an open request names who
    -- asked.
    CONSTRAINT pipeline_decisions_requestable CHECK (
        requested_by_principal IS NULL OR kind IN ('emergency_land', 'resolve_needs_person')),
    CONSTRAINT pipeline_decisions_request_names_requester
        CHECK (state <> 'requested' OR requested_by_principal IS NOT NULL),
    -- A pr_head decision binds to a head.
    CONSTRAINT pipeline_decisions_pr_head_sha
        CHECK (subject_kind <> 'pr_head' OR (head_sha IS NOT NULL AND head_sha <> '')),
    CONSTRAINT pipeline_decisions_revoked CHECK ((state = 'revoked') = (revoked_at IS NOT NULL))
);

COMMENT ON TABLE pipeline_decisions IS 'forge:no-entity';
COMMENT ON CONSTRAINT pipeline_decisions_subject_kind ON pipeline_decisions IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineDecisionSubjectKinds';
COMMENT ON CONSTRAINT pipeline_decisions_kind ON pipeline_decisions IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineDecisionKinds';
COMMENT ON CONSTRAINT pipeline_decisions_state ON pipeline_decisions IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineDecisionStates';
COMMENT ON CONSTRAINT pipeline_decisions_decided_via ON pipeline_decisions IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineDecidedVia';
-- A project's decisions inbox, newest first; a subject's decisions.
CREATE INDEX pipeline_decisions_project_idx ON pipeline_decisions (project_id, created_at DESC);
CREATE INDEX pipeline_decisions_subject_idx ON pipeline_decisions (subject_kind, subject_id);

-- The decision that paused a repository's queue. A deleted decision
-- unpauses nothing by itself; it only clears the reference.
ALTER TABLE pipeline_repos
    ADD CONSTRAINT pipeline_repos_queue_paused_by_decision_id_fkey
        FOREIGN KEY (queue_paused_by_decision_id) REFERENCES pipeline_decisions (id) ON DELETE SET NULL;
