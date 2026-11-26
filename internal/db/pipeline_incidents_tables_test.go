package db

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestPipelineIncidentTablesConstraints proves, against a database, what
// db/migrations/20261126161542_pipeline_incidents_and_releases.up.sql enforces: the defaults, every closed set, the
// cross-column CHECKs, one unresolved incident per fingerprint (the partial
// unique index), the per-artifact scan rules, and the foreign keys. The
// hermetic pin that each CHECK lists exactly its Go set is
// `forge lint --closed-sets`.
func TestPipelineIncidentTablesConstraints(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	const project = "test-project"
	exec := func(query string, args ...any) error {
		_, err := raw.Exec(query, args...)
		return err
	}
	refused := func(t *testing.T, err error, constraint, why string) {
		t.Helper()
		require.Error(t, err, why)
		assert.Equal(t, constraint, orm.ConstraintName(err), why)
	}
	require.NoError(t, exec(
		`INSERT INTO pipeline_repos (id, org_id, project_id, provider, account_key, repository_id, repository, created_by_user_id)
		 VALUES ('repo-1', 'org-1', $1, 'github', 'inst-1', 'rid-1', 'acme/app', 'user-1')`, project))
	require.NoError(t, exec(
		`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state, snapshot_at, observed_at)
		 VALUES ('rec-1', 'github', 'inst-1', 'rid-1', 'acme/app', 1, 'open', now(), now())`))

	insertIncident := func(id, source, fingerprint, state, severity string, extra string) error {
		cols, vals := "", ""
		switch extra {
		case "needs_person":
			cols, vals = ", needs_person_since", ", now()"
		case "resolved_run":
			cols, vals = ", resolved_at, resolved_by_run_id", ", now(), 'run-1'"
		case "resolved_user":
			cols, vals = ", resolved_at, resolved_by_user_id", ", now(), 'user-1'"
		case "resolved_nobody":
			cols, vals = ", resolved_at", ", now()"
		case "resolver_only":
			cols, vals = ", resolved_by_user_id", ", 'user-1'"
		}
		return exec(
			`INSERT INTO incidents (id, org_id, project_id, source, fingerprint, state, severity`+cols+`)
			 VALUES ($1, 'org-1', $2, $3, $4, $5, $6`+vals+`)`, id, project, source, fingerprint, state, severity)
	}

	t.Run("incidents", func(t *testing.T) {
		for _, s := range core.AllIncidentSources {
			require.NoError(t, insertIncident("i-src-"+string(s), string(s), "fp-src", "open", "high", ""), "source %q", s)
		}
		for _, s := range core.AllIncidentStates {
			extra := ""
			switch s {
			case core.IncidentNeedsPerson:
				extra = "needs_person"
			case core.IncidentResolved:
				extra = "resolved_run"
			}
			require.NoError(t, insertIncident("i-state-"+string(s), "main", "fp-state-"+string(s), string(s), "low", extra), "state %q", s)
		}
		for _, sev := range core.AllIncidentSeverities {
			require.NoError(t, insertIncident("i-sev-"+string(sev), "infra", "fp-sev-"+string(sev), "open", string(sev), ""), "severity %q", sev)
		}

		var state, subject string
		var attempts int
		var version int64
		require.NoError(t, raw.QueryRow(`SELECT state, subject_json::text, attempts, version FROM incidents WHERE id = 'i-src-vuln'`).
			Scan(&state, &subject, &attempts, &version))
		assert.Equal(t, string(core.IncidentOpen), state)
		assert.Equal(t, "{}", subject)
		assert.Zero(t, attempts)
		assert.Zero(t, version)

		refused(t, insertIncident("i-bad-src", "pagerduty", "fp", "open", "high", ""), "incidents_source", "a source outside the set")
		refused(t, insertIncident("i-bad-state", "main", "fp", "closed", "high", ""), "incidents_state", "a state outside the set")
		refused(t, insertIncident("i-bad-sev", "main", "fp", "open", "sev1", ""), "incidents_severity", "a severity outside the set")
		refused(t, insertIncident("i-np", "main", "fp-np", "needs_person", "high", ""), "incidents_needs_person_since", "needs_person has a since")
		refused(t, insertIncident("i-np2", "main", "fp-np2", "open", "high", "needs_person"), "incidents_needs_person_since", "only needs_person has one")
		refused(t, insertIncident("i-res", "main", "fp-res", "resolved", "high", ""), "incidents_resolved", "a resolution has a time")
		refused(t, insertIncident("i-res2", "main", "fp-res2", "resolved", "high", "resolved_nobody"), "incidents_resolved", "and a resolver")
		refused(t, insertIncident("i-res3", "main", "fp-res3", "open", "high", "resolved_run"), "incidents_resolved", "only a resolved incident has one")
		refused(t, insertIncident("i-res4", "main", "fp-res4", "open", "high", "resolver_only"), "incidents_resolved", "a resolver without a resolution")
		require.NoError(t, insertIncident("i-res5", "main", "fp-res5", "resolved", "high", "resolved_user"), "a person may resolve it")

		refused(t, insertIncident("i-dup", "main", "fp-src", "responding", "high", ""), "incidents_open_fingerprint_key",
			"one unresolved incident per (project, source, fingerprint)")
		require.NoError(t, insertIncident("i-other-src", "vuln", "fp-state-open", "open", "high", ""), "the same fingerprint under another source is another incident")
		require.NoError(t, insertIncident("i-reopen", "main", "fp-state-resolved", "open", "high", ""), "a resolved incident's fingerprint may open again")
		require.NoError(t, insertIncident("i-reresolve", "main", "fp-state-resolved", "resolved", "high", "resolved_run"), "and resolved incidents may repeat")

		require.NoError(t, exec(`UPDATE incidents SET fix_record_id = 'rec-1' WHERE id = 'i-src-main'`))
		refused(t, exec(`UPDATE incidents SET fix_record_id = 'rec-missing' WHERE id = 'i-src-main'`), "incidents_fix_record_id_fkey", "the fix PR must exist")
		require.NoError(t, exec(`DELETE FROM pr_records WHERE id = 'rec-1'`))
		var fix *string
		require.NoError(t, raw.QueryRow(`SELECT fix_record_id FROM incidents WHERE id = 'i-src-main'`).Scan(&fix))
		assert.Nil(t, fix, "a deleted PR record unlinks the fix and keeps the incident")
	})

	t.Run("flaky_tests", func(t *testing.T) {
		insert := func(id, testID, state string, quarantined bool) error {
			until := "NULL"
			if quarantined {
				until = "now() + interval '7 days'"
			}
			return exec(
				`INSERT INTO flaky_tests (id, org_id, project_id, repo_id, test_id, state, first_seen_sha, incident_id, quarantined_until)
				 VALUES ($1, 'org-1', $2, 'repo-1', $3, $4, 'sha-1', 'i-src-flake', `+until+`)`, id, project, testID, state)
		}
		for _, s := range core.AllFlakyTestStates {
			require.NoError(t, insert("f-"+string(s), "TestX/"+string(s), string(s), s == core.FlakyTestQuarantined), "state %q", s)
		}
		refused(t, insert("f-bad", "TestBad", "flaky", false), "flaky_tests_state", "a state outside the set")
		refused(t, insert("f-q", "TestQ", "quarantined", false), "flaky_tests_quarantined", "a quarantine has an end")
		refused(t, insert("f-q2", "TestQ2", "suspected", true), "flaky_tests_quarantined", "only a quarantine has one")
		refused(t, insert("f-dup", "TestX/suspected", "suspected", false), "flaky_tests_repo_test_key", "one row per test per repository")

		var seen int
		require.NoError(t, raw.QueryRow(`SELECT seen_count FROM flaky_tests WHERE id = 'f-suspected'`).Scan(&seen))
		assert.Equal(t, 1, seen)
	})

	t.Run("releases and artifacts", func(t *testing.T) {
		insertRelease := func(id, version, state string) error {
			return exec(`INSERT INTO releases (id, org_id, project_id, version, components_json, state)
				VALUES ($1, 'org-1', $2, $3, '{"acme/app": "sha-1"}', $4)`, id, project, version, state)
		}
		for i, s := range core.AllReleaseStates {
			require.NoError(t, insertRelease("rel-"+string(s), "v1.0."+string(rune('0'+i)), string(s)), "state %q", s)
		}
		refused(t, insertRelease("rel-bad", "v9", "shipped"), "releases_state", "a state outside the set")
		refused(t, insertRelease("rel-dup", "v1.0.0", "cut"), "releases_project_version_key", "a version is released once per project")
		refused(t, exec(`INSERT INTO releases (id, org_id, project_id, version, components_json) VALUES ('rel-arr', 'org-1', $1, 'v2', '[]')`, project),
			"releases_components_object", "components are keyed by repository")

		insertArtifact := func(id, state string, sbom, verdict any, scanned bool) error {
			at := "NULL"
			if scanned {
				at = "now()"
			}
			return exec(`INSERT INTO release_artifacts (id, org_id, project_id, release_id, name, sbom_ref, scan_state, scan_verdict, scanned_at)
				VALUES ($1, 'org-1', $2, 'rel-cut', $1, $3, $4, $5, `+at+`)`, id, project, sbom, state, verdict)
		}
		for _, v := range core.AllReleaseArtifactScanVerdicts {
			require.NoError(t, insertArtifact("a-"+string(v), "scanned", "sbom://a", string(v), true), "verdict %q", v)
		}
		for _, st := range core.AllReleaseArtifactScanStates {
			var sbom, verdict any = "sbom://a", "pass"
			if st == core.ReleaseArtifactUnscanned {
				sbom, verdict = nil, nil
			}
			require.NoError(t, insertArtifact("a-state-"+string(st), string(st), sbom, verdict, sbom != nil), "scan state %q", st)
		}
		require.NoError(t, insertArtifact("a-stale-bare", "stale", nil, nil, false), "a stale artifact need not keep its old scan")
		var scanState string
		require.NoError(t, exec(`INSERT INTO release_artifacts (id, org_id, project_id, release_id, name) VALUES ('a-default', 'org-1', $1, 'rel-cut', 'site')`, project))
		require.NoError(t, raw.QueryRow(`SELECT scan_state FROM release_artifacts WHERE id = 'a-default'`).Scan(&scanState))
		assert.Equal(t, string(core.ReleaseArtifactUnscanned), scanState, "an artifact is unscanned until scanned")

		refused(t, insertArtifact("a-bad-state", "skipped", nil, nil, false), "release_artifacts_scan_state", "a scan state outside the set")
		refused(t, insertArtifact("a-bad-verdict", "scanned", "sbom://a", "fail", true), "release_artifacts_scan_verdict", "a verdict outside the set")
		refused(t, insertArtifact("a-nosbom", "scanned", nil, "pass", true), "release_artifacts_scanned", "a scan has an SBOM")
		refused(t, insertArtifact("a-noverdict", "scanned", "sbom://a", nil, true), "release_artifacts_scanned", "and a verdict")
		refused(t, insertArtifact("a-notime", "scanned", "sbom://a", "pass", false), "release_artifacts_scanned", "and a time")
		refused(t, insertArtifact("a-unscanned-verdict", "unscanned", nil, "pass", false), "release_artifacts_unscanned", "an unscanned artifact has no verdict")
		refused(t, exec(`INSERT INTO release_artifacts (id, org_id, project_id, release_id, name) VALUES ('a-dup', 'org-1', $1, 'rel-cut', 'site')`, project),
			"release_artifacts_release_name_key", "an artifact name once per release")
	})

	t.Run("release_env_steps", func(t *testing.T) {
		insert := func(env string, position int, state string, rolledBackTo any, soak bool) error {
			until := "NULL"
			if soak {
				until = "now() + interval '15 minutes'"
			}
			return exec(`INSERT INTO release_env_steps (release_id, env, org_id, project_id, position, state, rolled_back_to, soak_until)
				VALUES ('rel-promoting', $1, 'org-1', $2, $3, $4, $5, `+until+`)`, env, project, position, state, rolledBackTo)
		}
		for i, s := range core.AllReleaseEnvStepStates {
			var back any
			if s == core.ReleaseEnvRolledBack {
				back = "rel-promoted"
			}
			require.NoError(t, insert("env-"+string(s), i, string(s), back, s == core.ReleaseEnvSoaking), "state %q", s)
		}
		refused(t, insert("env-bad", 50, "deployed", nil, false), "release_env_steps_state", "a state outside the set")
		refused(t, insert("env-rb", 51, "rolled_back", nil, false), "release_env_steps_rolled_back", "a rollback names its target")
		refused(t, insert("env-rb2", 52, "failed", "rel-promoted", false), "release_env_steps_rolled_back", "only a rollback has one")
		refused(t, insert("env-rb3", 53, "rolled_back", "rel-promoting", false), "release_env_steps_not_self", "a release is not rolled back to itself")
		refused(t, insert("env-soak", 54, "soaking", nil, false), "release_env_steps_soaking", "a soak has an end")
		refused(t, insert("env-waiting", 55, "waiting", nil, false), "release_env_steps_pkey", "one step per (release, env)")
		refused(t, insert("env-other", 0, "waiting", nil, false), "release_env_steps_position_key", "one env per position")

		refused(t, exec(`DELETE FROM releases WHERE id = 'rel-promoted'`), "release_env_steps_rolled_back_to_fkey",
			"a release an env was rolled back to is not deleted on its own")

		require.NoError(t, exec(`DELETE FROM releases WHERE id = 'rel-promoting'`))
		var n int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM release_env_steps WHERE release_id = 'rel-promoting'`).Scan(&n))
		assert.Zero(t, n, "steps go with their release")

		require.NoError(t, exec(`DELETE FROM projects WHERE id = $1`, project), "a project takes its whole pipeline history")
		require.NoError(t, raw.QueryRow(`SELECT (SELECT count(*) FROM incidents) + (SELECT count(*) FROM flaky_tests) + (SELECT count(*) FROM releases) + (SELECT count(*) FROM release_artifacts) + (SELECT count(*) FROM release_env_steps)`).Scan(&n))
		assert.Zero(t, n)
	})
}
