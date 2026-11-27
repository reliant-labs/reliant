-- forge:backward-compatible — new tables nothing older reads

-- Migration: pr_pipeline_tables
-- Created: 2026-11-26T10:59:49Z
--
-- The PR pipeline's per-PR state (pipeline redesign §4.2; build plan task
-- R2): the state machine (pr_pipeline), the review role's verdicts per head
-- (pr_head_verdicts) and every review, fix, gate or respond attempt
-- (pipeline_attempts). Inert: nothing reads or writes these tables until the
-- pipeline ships behind `pipeline.enabled`.
--
-- Tenancy as on every pipeline table (20261125211730_pipeline_repos_and_settings):
-- org_id is control-plane's (no foreign key); project_id references projects
-- and goes with the project.
--
-- Each closed vocabulary is a named CHECK that declares the Go set it mirrors
-- (internal/db/core/pipeline_pr.go), so `forge lint --closed-sets` fails the
-- moment the two drift.
--
-- The tables are new and empty, so their indexes are built here, in the
-- transaction that creates them, rather than CONCURRENTLY in files of their
-- own.
--
-- Post-deploy verification (prod): the three tables exist and are empty:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pr pipeline tables" \
--     "SELECT (SELECT count(*) FROM pr_pipeline), (SELECT count(*) FROM pr_head_verdicts), (SELECT count(*) FROM pipeline_attempts)"
--
-- Rollback: none needed. The previous release never names these tables; they
-- stay in place, unread.

-- One row per PR of an enrolled repository: the state machine. Every write
-- is UPDATE … WHERE version = $v (compare-and-set), so a run that has been
-- superseded cannot write; the transitions are a closed table in Go.
CREATE TABLE pr_pipeline (
    record_id TEXT PRIMARY KEY REFERENCES pr_records (id) ON DELETE CASCADE,
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES pipeline_repos (id) ON DELETE CASCADE,
    state TEXT NOT NULL,
    -- The head the state is about. A new head resets the state to
    -- awaiting_review.
    state_head_sha TEXT NOT NULL CHECK (state_head_sha <> ''),
    -- The effective priority, 0–100. 90 and above is urgent (it gates
    -- alone); 100 is an emergency.
    priority INTEGER NOT NULL DEFAULT 50,
    -- What set the effective priority.
    priority_source TEXT NOT NULL DEFAULT 'default',
    review_round INTEGER NOT NULL DEFAULT 0 CHECK (review_round >= 0),
    -- Counted per failure signature: reset when fix_signature changes.
    fix_attempts INTEGER NOT NULL DEFAULT 0 CHECK (fix_attempts >= 0),
    -- Hash of the failing checks and tests, or of the blocking findings.
    fix_signature TEXT,
    -- The run acting on the PR, when one is.
    owner_run_id TEXT,
    -- Compare-and-set: every state transition is UPDATE … WHERE version = $v.
    version BIGINT NOT NULL DEFAULT 0,
    -- What pr.next computed the PR waits for, and when it is due. The due
    -- scan reads next_action_at; an action may wait on an event with no due
    -- time, but a due time always names its action.
    next_action TEXT,
    next_action_at TIMESTAMPTZ,
    -- Set exactly while the state is needs_person.
    needs_person_reason TEXT,
    needs_person_since TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pr_pipeline_state CHECK (state IN (
        'draft', 'awaiting_review', 'reviewing', 'changes_requested', 'fixing',
        'approved', 'queued', 'gating', 'landed', 'needs_person', 'closed')),
    CONSTRAINT pr_pipeline_priority_source
        CHECK (priority_source IN ('default', 'incident', 'release', 'decision', 'aging')),
    CONSTRAINT pr_pipeline_priority_range CHECK (priority BETWEEN 0 AND 100),
    CONSTRAINT pr_pipeline_due_names_action CHECK (next_action_at IS NULL OR next_action IS NOT NULL),
    CONSTRAINT pr_pipeline_needs_person_since
        CHECK ((state = 'needs_person') = (needs_person_since IS NOT NULL)),
    CONSTRAINT pr_pipeline_needs_person_reason
        CHECK (needs_person_since IS NULL OR (needs_person_reason IS NOT NULL AND needs_person_reason <> ''))
);

COMMENT ON TABLE pr_pipeline IS 'forge:no-entity';
COMMENT ON CONSTRAINT pr_pipeline_state ON pr_pipeline IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPRPipelineStates';
COMMENT ON CONSTRAINT pr_pipeline_priority_source ON pr_pipeline IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPRPrioritySources';
-- The due scan: WHERE next_action_at <= now(), oldest first.
CREATE INDEX pr_pipeline_due_idx ON pr_pipeline (next_action_at) WHERE next_action_at IS NOT NULL;
CREATE INDEX pr_pipeline_repo_idx ON pr_pipeline (repo_id, state);
CREATE INDEX pr_pipeline_project_idx ON pr_pipeline (project_id);

-- The review role's verdict on one head, per round. Written only by
-- pr.verdict@1 from a node with role: reviewer, or by the queue's structural
-- rules (§4.6). Append-only: a row is never updated, and a new head needs a
-- new row; the trigger below refuses an UPDATE outright. Rows still go with
-- their record or project.
CREATE TABLE pr_head_verdicts (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    record_id TEXT NOT NULL REFERENCES pr_records (id) ON DELETE CASCADE,
    head_sha TEXT NOT NULL CHECK (head_sha <> ''),
    -- Review rounds count from 1.
    round INTEGER NOT NULL CHECK (round >= 1),
    verdict TEXT NOT NULL,
    -- How the verdict was reached: a model reviewer, or a structural rule
    -- the server computed.
    rule TEXT NOT NULL,
    risk_tier TEXT NOT NULL,
    -- The blockers, with ids, so a later round blocks only on the unfixed
    -- ones or on the new diff.
    findings_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- pipeline_decisions kinds the head needs before it may land, e.g.
    -- {approve_breaking_proto}. Validated in Go against that table's kinds.
    required_decisions TEXT[] NOT NULL DEFAULT '{}',
    -- The run and node that reviewed. A model verdict always names both; a
    -- structural rule's may name neither.
    run_id TEXT,
    node_id TEXT,
    reviewer_principal TEXT NOT NULL CHECK (reviewer_principal <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pr_head_verdicts_verdict CHECK (verdict IN ('pass', 'changes', 'close')),
    CONSTRAINT pr_head_verdicts_rule
        CHECK (rule IN ('model', 'no_model_low_risk', 'exact_revert', 'generated_only')),
    CONSTRAINT pr_head_verdicts_risk_tier CHECK (risk_tier IN ('low', 'medium', 'high')),
    CONSTRAINT pr_head_verdicts_model_names_node
        CHECK (rule <> 'model' OR (run_id IS NOT NULL AND node_id IS NOT NULL)),
    CONSTRAINT pr_head_verdicts_head_round_key UNIQUE (record_id, head_sha, round)
);

COMMENT ON TABLE pr_head_verdicts IS 'forge:no-entity';
COMMENT ON CONSTRAINT pr_head_verdicts_verdict ON pr_head_verdicts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPRHeadVerdicts';
COMMENT ON CONSTRAINT pr_head_verdicts_rule ON pr_head_verdicts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPRVerdictRules';
COMMENT ON CONSTRAINT pr_head_verdicts_risk_tier ON pr_head_verdicts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPRRiskTiers';
CREATE INDEX pr_head_verdicts_project_idx ON pr_head_verdicts (project_id);

CREATE FUNCTION pr_head_verdicts_refuse_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'pr_head_verdicts is append-only: a verdict is never updated'
   USING ERRCODE = '55000';
END $$;

CREATE TRIGGER pr_head_verdicts_refuse_update BEFORE UPDATE ON pr_head_verdicts
 FOR EACH ROW EXECUTE FUNCTION pr_head_verdicts_refuse_update();

-- Every review, fix, gate or respond attempt on a PR, an incident or a queue
-- batch. Attempts are numbered per (subject, stage, signature): a new failure
-- signature starts again at 1. It replaces attempts counted from comments and
-- the per-head re-kick markers.
CREATE TABLE pipeline_attempts (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    subject_kind TEXT NOT NULL,
    -- pr_records.id, incidents.id or queue_batches.id, by subject_kind.
    subject_id TEXT NOT NULL CHECK (subject_id <> ''),
    -- The head the attempt was about, when the subject has one.
    head_sha TEXT,
    stage TEXT NOT NULL,
    -- The failure signature the attempt answers; '' for a first attempt
    -- with nothing failing yet.
    signature TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL CHECK (attempt >= 1),
    run_id TEXT,
    -- Null while the attempt runs; set, with finished_at, when it ends.
    outcome TEXT,
    detail_json JSONB,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    CONSTRAINT pipeline_attempts_subject_kind CHECK (subject_kind IN ('pr', 'incident', 'batch')),
    CONSTRAINT pipeline_attempts_stage CHECK (stage IN ('review', 'fix', 'gate', 'respond')),
    CONSTRAINT pipeline_attempts_outcome
        CHECK (outcome IN ('succeeded', 'failed', 'unproven', 'superseded', 'cancelled')),
    CONSTRAINT pipeline_attempts_finished CHECK ((outcome IS NULL) = (finished_at IS NULL)),
    CONSTRAINT pipeline_attempts_number_key UNIQUE (subject_kind, subject_id, stage, signature, attempt)
);

COMMENT ON TABLE pipeline_attempts IS 'forge:no-entity';
COMMENT ON CONSTRAINT pipeline_attempts_subject_kind ON pipeline_attempts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineAttemptSubjectKinds';
COMMENT ON CONSTRAINT pipeline_attempts_stage ON pipeline_attempts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineAttemptStages';
COMMENT ON CONSTRAINT pipeline_attempts_outcome ON pipeline_attempts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineAttemptOutcomes';
CREATE INDEX pipeline_attempts_project_idx ON pipeline_attempts (project_id);
