-- forge:backward-compatible — additive private review authority; no production registration or policies enabled.
CREATE TABLE review_policies (
 id text PRIMARY KEY,
 owner_id text NOT NULL,
 project_id text NOT NULL,
 workflow_ref text NOT NULL CHECK (length(workflow_ref)>0),
 admission_scope text NOT NULL CHECK (admission_scope ~ '^[0-9a-f]{64}$'),
 repository_id bigint NOT NULL CHECK (repository_id > 0),
 revision bigint NOT NULL CHECK (revision > 0),
 enabled boolean NOT NULL DEFAULT false,
 workflow_digest text NOT NULL CHECK (workflow_digest ~ '^sha256:[0-9a-f]{64}$'),
 graph_snapshot jsonb NOT NULL,
 completion_node text NOT NULL CHECK (length(completion_node) BETWEEN 1 AND 200),
 check_name text NOT NULL CHECK (check_name = 'ReliantReview'),
 authorized_by text NOT NULL CHECK (length(authorized_by) > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (owner_id, repository_id)
);
CREATE TABLE review_assignments (
 id text PRIMARY KEY,
 policy_id text NOT NULL REFERENCES review_policies(id),
 binding_id text NOT NULL REFERENCES pr_handoff_bindings(id),
 admission_event_id text NOT NULL UNIQUE REFERENCES trigger_events(id),
 owner_id text NOT NULL,
 repository_id bigint NOT NULL CHECK (repository_id > 0),
 pr_number bigint NOT NULL CHECK (pr_number > 0),
 head_sha text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
 author_root_id text NOT NULL,
 author_run_id text NOT NULL,
 reviewer_root_id text NOT NULL,
 reviewer_run_id text NOT NULL,
 policy_revision bigint NOT NULL CHECK (policy_revision > 0),
 workflow_digest text NOT NULL CHECK (workflow_digest ~ '^sha256:[0-9a-f]{64}$'),
 graph_snapshot jsonb NOT NULL,
 completion_node text NOT NULL,
 generation bigint NOT NULL CHECK (generation > 0),
 cancelled boolean NOT NULL DEFAULT false,
 superseded boolean NOT NULL DEFAULT false,
 completion_state text NOT NULL DEFAULT 'none' CHECK (completion_state IN ('none','pending','terminal')),
 verdict text CHECK (verdict IN ('approve','changes','incomplete')),
 evidence_digest text CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
 completion_activity_id text,
 completion_run_id text,
 graph_succeeded boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (reviewer_root_id <> author_root_id),
 CHECK (length(author_run_id)>0 AND length(reviewer_run_id)>0),
 CHECK ((completion_state = 'none' AND verdict IS NULL AND evidence_digest IS NULL) OR
        (completion_state <> 'none' AND verdict IS NOT NULL AND evidence_digest IS NOT NULL AND completion_activity_id IS NOT NULL AND completion_run_id IS NOT NULL)),
 CHECK (NOT graph_succeeded OR completion_state = 'terminal'),
 UNIQUE (binding_id, generation),
 UNIQUE (reviewer_root_id, reviewer_run_id)
);
-- Initial semantics coalesce an already assigned source head, never silently rerun it.
CREATE UNIQUE INDEX review_assignments_source_once ON review_assignments (binding_id, head_sha);
CREATE TABLE review_publication_intents (
 id text PRIMARY KEY,
 assignment_id text NOT NULL UNIQUE REFERENCES review_assignments(id),
 repository_id bigint NOT NULL CHECK (repository_id > 0),
 payload jsonb NOT NULL,
 payload_digest text NOT NULL CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared','publishing','uncertain','confirmed','blocked')),
 check_id bigint CHECK (check_id > 0),
 check_url text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((state = 'confirmed' AND check_id IS NOT NULL AND check_url IS NOT NULL) OR state <> 'confirmed')
);
CREATE UNIQUE INDEX review_publication_check_identity ON review_publication_intents(repository_id,check_id) WHERE check_id IS NOT NULL;
COMMENT ON TABLE review_policies IS 'Private owner/admin-provisioned policy; no agent or public write surface';
COMMENT ON TABLE review_assignments IS 'Private policy-bound reviewer assignments referencing existing admission and original-author binding';
COMMENT ON TABLE review_publication_intents IS 'Immutable per-assignment check intent and nonsecret reconciliation receipt, not a generic event queue';
