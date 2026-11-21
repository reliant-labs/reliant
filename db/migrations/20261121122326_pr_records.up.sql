-- forge:backward-compatible — adds five new tables nothing in the previous release reads or writes.

-- PR records (dev-docs/pr-records.md): a mirror of a
-- provider's pull requests, kept current from verified webhook deliveries.
-- Facts only: who may pass a review, or what a label means, is the reader's
-- policy, applied when it reads.

-- One row per pull request.
CREATE TABLE pr_records (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    -- The installation (GitHub App) the deliveries came through: what access
    -- to a record is checked against, with repository_id.
    account_key TEXT NOT NULL,
    -- The provider's id of the repository, stable across renames.
    repository_id TEXT NOT NULL,
    -- owner/name as of the newest snapshot, for lookup and display.
    repository TEXT NOT NULL,
    number INTEGER NOT NULL CHECK (number > 0),
    state TEXT NOT NULL CHECK (state IN ('open', 'closed', 'merged')),
    draft BOOLEAN NOT NULL DEFAULT false,
    head_sha TEXT NOT NULL DEFAULT '',
    head_ref TEXT NOT NULL DEFAULT '',
    base_ref TEXT NOT NULL DEFAULT '',
    base_sha TEXT NOT NULL DEFAULT '',
    author_id TEXT NOT NULL DEFAULT '',
    author_login TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    -- The label names as last observed: what the provider shows, never state.
    labels JSONB NOT NULL DEFAULT '[]'::jsonb,
    merged_at TIMESTAMPTZ,
    merge_commit_sha TEXT NOT NULL DEFAULT '',
    -- The pull request's own updated_at in the newest applied snapshot: an
    -- older snapshot delivered late never overwrites a newer one.
    snapshot_at TIMESTAMPTZ NOT NULL,
    -- Whether that snapshot was the full pull request. A review event's
    -- slimmer one (no merged) is completed by a full one of the same second.
    snapshot_full BOOLEAN NOT NULL DEFAULT true,
    -- When Reliant last applied anything to the record.
    observed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, repository_id, number)
);

COMMENT ON TABLE pr_records IS 'forge:no-entity';
CREATE INDEX pr_records_repository_idx ON pr_records (provider, lower(repository), number);
CREATE INDEX pr_records_open_idx ON pr_records (provider, repository_id, updated_at) WHERE state = 'open';

-- One row per head a pull request has had.
CREATE TABLE pr_record_heads (
    id TEXT PRIMARY KEY,
    record_id TEXT NOT NULL REFERENCES pr_records (id) ON DELETE CASCADE,
    head_sha TEXT NOT NULL,
    -- The head a push replaced (a synchronize event's before), when known.
    previous_head_sha TEXT NOT NULL DEFAULT '',
    -- The snapshot time (the pull request's updated_at) of the push that
    -- recorded previous_head_sha. A same-second push is followed only along
    -- an edge of the record's own snapshot second, never an older one.
    pushed_snapshot_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL,
    superseded_at TIMESTAMPTZ,
    UNIQUE (record_id, head_sha)
);

COMMENT ON TABLE pr_record_heads IS 'forge:no-entity';

-- Reviews that carry a pipeline marker: the evidence a head's verdict is
-- computed from. One row per (review, marker kind).
CREATE TABLE pr_record_reviews (
    id TEXT PRIMARY KEY,
    record_id TEXT NOT NULL REFERENCES pr_records (id) ON DELETE CASCADE,
    review_id TEXT NOT NULL,
    -- The commit the review was submitted on (GitHub's commit_id).
    commit_sha TEXT NOT NULL,
    -- The sha the marker names; a verdict counts only where it equals commit_sha.
    marker_sha TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('passed', 'changes', 'close', 'rereview')),
    -- A re-review's of=: the review it re-decided.
    of_review_id TEXT NOT NULL DEFAULT '',
    author_id TEXT NOT NULL DEFAULT '',
    author_login TEXT NOT NULL DEFAULT '',
    submitted_at TIMESTAMPTZ NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    UNIQUE (record_id, review_id, kind)
);

COMMENT ON TABLE pr_record_reviews IS 'forge:no-entity';
CREATE INDEX pr_record_reviews_head_idx ON pr_record_reviews (record_id, commit_sha);

-- Check runs, per commit: a check belongs to a sha, and a merge-group sha has
-- no pull request at all. The newest run of a name and app wins.
CREATE TABLE pr_record_checks (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    name TEXT NOT NULL,
    app_slug TEXT NOT NULL DEFAULT '',
    check_run_id BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT '',
    conclusion TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL,
    UNIQUE (provider, repository_id, head_sha, name, app_slug)
);

COMMENT ON TABLE pr_record_checks IS 'forge:no-entity';

-- Each event a delivery carried, once applied: a projection retried after a
-- partial failure applies nothing twice, and the shadow report reads how
-- current a record is from it. Pruned after a retention window.
CREATE TABLE pr_record_deliveries (
    provider TEXT NOT NULL,
    delivery_key TEXT NOT NULL,
    event_index INTEGER NOT NULL CHECK (event_index >= 0),
    event_type TEXT NOT NULL,
    record_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL CHECK (outcome IN ('applied', 'stale', 'ignored')),
    applied_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (provider, delivery_key, event_index)
);

COMMENT ON TABLE pr_record_deliveries IS 'forge:no-entity';
COMMENT ON COLUMN pr_record_deliveries.delivery_key IS 'forge:secret';
CREATE INDEX pr_record_deliveries_applied_idx ON pr_record_deliveries (applied_at);
