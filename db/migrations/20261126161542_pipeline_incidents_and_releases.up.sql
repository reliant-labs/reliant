-- forge:backward-compatible — new tables nothing older reads

-- Migration: pipeline_incidents_and_releases
-- Created: 2026-11-26T16:15:42Z
--
-- The responder's and the release workflow's state (pipeline redesign §4.5,
-- §6.2, §6.5, owner D14 and D16; build plan task R5): incidents from every
-- alert source (vuln included), flaky tests, releases with each artifact's
-- SBOM and scan verdict, and each release's per-environment steps. Inert:
-- nothing reads or writes these tables until the pipeline ships behind
-- `pipeline.enabled`.
--
-- Tenancy as on every pipeline table (20261125211730_pipeline_repos_and_settings).
-- Each closed vocabulary is a named CHECK that declares the Go set it mirrors
-- (internal/db/core/pipeline_incidents.go).
--
-- The tables are new and empty, so their indexes are built here, in the
-- transaction that creates them.
--
-- Post-deploy verification (prod): the five tables exist and are empty:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify pipeline incident and release tables" \
--     "SELECT (SELECT count(*) FROM incidents), (SELECT count(*) FROM flaky_tests), (SELECT count(*) FROM releases), (SELECT count(*) FROM release_artifacts), (SELECT count(*) FROM release_env_steps)"
--   → 0, 0, 0, 0, 0
--
-- Rollback: none needed. The previous release never names these tables; they
-- stay in place, unread.

-- One incident per open (project, source, fingerprint): detectors open them,
-- respond's actions transition them, under compare-and-set on version.
CREATE TABLE incidents (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    -- Source-defined: repo+lane+signature, the alert rule id, the obs issue
    -- id, the advisory and artifact.
    fingerprint TEXT NOT NULL CHECK (fingerprint <> ''),
    state TEXT NOT NULL DEFAULT 'open',
    severity TEXT NOT NULL,
    -- The sha, release id, env or alert payload reference.
    subject_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- The current failure signature. A change re-arms attempts.
    signature TEXT,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    culprit_json JSONB,
    -- The PR fixing it. Linking it, by respond's server action only, is what
    -- grants the fix the incident's priority.
    fix_record_id TEXT REFERENCES pr_records (id) ON DELETE SET NULL,
    responder_run_id TEXT,
    -- Set exactly while the state is needs_person.
    needs_person_since TIMESTAMPTZ,
    opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set exactly when resolved, by a run or a person.
    resolved_at TIMESTAMPTZ,
    resolved_by_run_id TEXT,
    resolved_by_user_id TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Compare-and-set: every transition is UPDATE … WHERE version = $v.
    version BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT incidents_source CHECK (source IN (
        'main', 'release', 'prod_alert', 'obs_issue', 'nightly', 'flake', 'infra', 'recurring', 'vuln')),
    CONSTRAINT incidents_state CHECK (state IN ('open', 'responding', 'mitigating', 'resolved', 'needs_person')),
    CONSTRAINT incidents_severity CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT incidents_needs_person_since CHECK ((state = 'needs_person') = (needs_person_since IS NOT NULL)),
    CONSTRAINT incidents_resolved CHECK (
        (state = 'resolved') = (resolved_at IS NOT NULL)
        AND (resolved_at IS NULL OR resolved_by_run_id IS NOT NULL OR resolved_by_user_id IS NOT NULL)
        AND (resolved_at IS NOT NULL OR (resolved_by_run_id IS NULL AND resolved_by_user_id IS NULL)))
);

COMMENT ON TABLE incidents IS 'forge:no-entity';
COMMENT ON CONSTRAINT incidents_source ON incidents IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllIncidentSources';
COMMENT ON CONSTRAINT incidents_state ON incidents IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllIncidentStates';
COMMENT ON CONSTRAINT incidents_severity ON incidents IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllIncidentSeverities';
-- One unresolved incident per fingerprint: a duplicate opener is refused.
CREATE UNIQUE INDEX incidents_open_fingerprint_key ON incidents (project_id, source, fingerprint)
    WHERE state <> 'resolved';
CREATE INDEX incidents_project_idx ON incidents (project_id, opened_at DESC);
CREATE INDEX incidents_fix_record_idx ON incidents (fix_record_id) WHERE fix_record_id IS NOT NULL;

-- A test the gate has seen fail and then pass on the same tree.
CREATE TABLE flaky_tests (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES pipeline_repos (id) ON DELETE CASCADE,
    -- The runner's test id, e.g. ./internal/db.TestFoo/sub.
    test_id TEXT NOT NULL CHECK (test_id <> ''),
    state TEXT NOT NULL DEFAULT 'suspected',
    first_seen_sha TEXT NOT NULL CHECK (first_seen_sha <> ''),
    seen_count INTEGER NOT NULL DEFAULT 1 CHECK (seen_count >= 1),
    -- The flake incident tracking it.
    incident_id TEXT REFERENCES incidents (id) ON DELETE SET NULL,
    -- Set exactly while quarantined.
    quarantined_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT flaky_tests_state CHECK (state IN ('suspected', 'quarantined', 'deleted', 'fixed')),
    CONSTRAINT flaky_tests_quarantined CHECK ((state = 'quarantined') = (quarantined_until IS NOT NULL)),
    CONSTRAINT flaky_tests_repo_test_key UNIQUE (repo_id, test_id)
);

COMMENT ON TABLE flaky_tests IS 'forge:no-entity';
COMMENT ON CONSTRAINT flaky_tests_state ON flaky_tests IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllFlakyTestStates';
CREATE INDEX flaky_tests_project_idx ON flaky_tests (project_id);
-- The sweep's flake_quarantine_expiring check.
CREATE INDEX flaky_tests_quarantine_idx ON flaky_tests (quarantined_until) WHERE quarantined_until IS NOT NULL;

-- One release of a project: a set of landed component shas, cut from main
-- (every sha with a main_health pass) and promoted env by env.
CREATE TABLE releases (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    version TEXT NOT NULL CHECK (version <> ''),
    -- {repo: landed sha} for every component.
    components_json JSONB NOT NULL,
    -- For a forge project, forge's own release record (digests, SBOM) in its
    -- ledger, which is never duplicated here.
    forge_release_ref TEXT,
    state TEXT NOT NULL DEFAULT 'cut',
    -- The release incident, when the release failed.
    incident_id TEXT REFERENCES incidents (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Compare-and-set: every transition is UPDATE … WHERE version_seq = $v.
    -- (Named version_seq: `version` is the release's semver.)
    version_seq BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT releases_state CHECK (state IN (
        'cut', 'building', 'gating', 'green', 'red', 'promoting', 'promoted', 'rolled_back', 'abandoned')),
    CONSTRAINT releases_components_object CHECK (jsonb_typeof(components_json) = 'object'),
    CONSTRAINT releases_project_version_key UNIQUE (project_id, version)
);

COMMENT ON TABLE releases IS 'forge:no-entity';
COMMENT ON CONSTRAINT releases_state ON releases IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllReleaseStates';
CREATE INDEX releases_project_idx ON releases (project_id, created_at DESC);

-- One artifact a release ships, with its SBOM and scan verdict (D14): the
-- gate's sbom_scan stage, or a fresh scan at cut time. release.cut refuses an
-- artifact that is not scanned, or whose verdict is block; a warn goes in
-- the notes. Unscanned (no SBOM or no scan, or sbom_scan turned off) and
-- stale (the scanner database was past forge's freshness rule) are
-- unproven, never clean.
CREATE TABLE release_artifacts (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release_id TEXT NOT NULL REFERENCES releases (id) ON DELETE CASCADE,
    -- The artifact's name within the release (image, binary, static site),
    -- and its content digest.
    name TEXT NOT NULL CHECK (name <> ''),
    digest TEXT,
    -- Where its SBOM is stored (forge's release record or the vuln store).
    sbom_ref TEXT,
    scan_state TEXT NOT NULL DEFAULT 'unscanned',
    scan_verdict TEXT,
    -- forge's vuln_gate evaluation: the rule, the findings it judged, the
    -- exemptions applied.
    vuln_gate_json JSONB,
    scanned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT release_artifacts_scan_state CHECK (scan_state IN ('scanned', 'unscanned', 'stale')),
    CONSTRAINT release_artifacts_scan_verdict CHECK (scan_verdict IN ('pass', 'warn', 'block')),
    -- A scanned artifact has an SBOM, a verdict and a scan time; an
    -- unscanned one has no verdict. A stale one keeps what its old scan
    -- said, which no longer counts.
    CONSTRAINT release_artifacts_scanned CHECK (
        scan_state <> 'scanned'
        OR (sbom_ref IS NOT NULL AND sbom_ref <> '' AND scan_verdict IS NOT NULL AND scanned_at IS NOT NULL)),
    CONSTRAINT release_artifacts_unscanned CHECK (scan_state <> 'unscanned' OR scan_verdict IS NULL),
    CONSTRAINT release_artifacts_release_name_key UNIQUE (release_id, name)
);

COMMENT ON TABLE release_artifacts IS 'forge:no-entity';
COMMENT ON CONSTRAINT release_artifacts_scan_state ON release_artifacts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllReleaseArtifactScanStates';
COMMENT ON CONSTRAINT release_artifacts_scan_verdict ON release_artifacts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllReleaseArtifactScanVerdicts';
CREATE INDEX release_artifacts_project_idx ON release_artifacts (project_id);

-- One step of a release's promotion chain: one row per (release, env).
CREATE TABLE release_env_steps (
    release_id TEXT NOT NULL REFERENCES releases (id) ON DELETE CASCADE,
    env TEXT NOT NULL CHECK (env <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The env's place in the chain, from 0.
    position INTEGER NOT NULL CHECK (position >= 0),
    state TEXT NOT NULL DEFAULT 'waiting',
    -- The `forge obs check --json` document that judged the step.
    obs_check_json JSONB,
    soak_until TIMESTAMPTZ,
    -- The release_promote pipeline_decisions row that approved the step. A
    -- foreign key joins it once pipeline_decisions and this table are both on
    -- main (they land in separate migrations).
    approval_decision_id TEXT,
    -- The release the env was rolled back to. Not SET NULL: that would
    -- break release_env_steps_rolled_back mid-cascade. Releases are
    -- history, deleted only with their project.
    rolled_back_to TEXT REFERENCES releases (id),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT release_env_steps_state CHECK (state IN (
        'waiting', 'awaiting_approval', 'deploying', 'soaking', 'passed', 'failed', 'inconclusive',
        'rolled_back', 'skipped')),
    CONSTRAINT release_env_steps_rolled_back CHECK ((state = 'rolled_back') = (rolled_back_to IS NOT NULL)),
    CONSTRAINT release_env_steps_soaking CHECK (state <> 'soaking' OR soak_until IS NOT NULL),
    CONSTRAINT release_env_steps_not_self CHECK (rolled_back_to <> release_id),
    PRIMARY KEY (release_id, env),
    CONSTRAINT release_env_steps_position_key UNIQUE (release_id, position)
);

COMMENT ON TABLE release_env_steps IS 'forge:no-entity';
COMMENT ON CONSTRAINT release_env_steps_state ON release_env_steps IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllReleaseEnvStepStates';
CREATE INDEX release_env_steps_project_idx ON release_env_steps (project_id);
