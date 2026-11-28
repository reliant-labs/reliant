package db

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestPRPipelineTablesConstraints proves, against a database, what
// db/migrations/20261126105949_pr_pipeline_tables.up.sql enforces: the
// defaults, every closed-set CHECK accepting its Go set and refusing a value
// outside it, the cross-column CHECKs, the unique keys, the foreign keys, the
// partial due-scan index and pr_head_verdicts refusing an UPDATE. The
// hermetic pin that each CHECK lists exactly its Go set is
// `forge lint --closed-sets`.
func TestPRPipelineTablesConstraints(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	const project = "test-project"
	exec := func(query string, args ...any) error {
		_, err := raw.Exec(query, args...)
		return err
	}
	count := func(query string) int {
		t.Helper()
		var n int
		require.NoError(t, raw.QueryRow(query).Scan(&n))
		return n
	}
	// The needs_person columns of a valid needs_person row, appended to
	// pr_pipeline's insert, and the columns an extra fills.
	const needsPerson = ", now(), 'fix attempts exhausted'"
	cols := func(extra string) string {
		switch extra {
		case "":
			return ""
		case ", now()":
			return ", needs_person_since"
		default:
			return ", needs_person_since, needs_person_reason"
		}
	}
	insertRecord := func(id string, number int) {
		t.Helper()
		require.NoError(t, exec(
			`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state, snapshot_at, observed_at)
			 VALUES ($1, 'github', 'inst-1', 'rid-1', 'acme/app', $2, 'open', now(), now())`, id, number))
	}
	require.NoError(t, exec(
		`INSERT INTO pipeline_repos (id, org_id, project_id, provider, account_key, repository_id, repository, created_by_user_id)
		 VALUES ('repo-1', 'org-1', $1, 'github', 'inst-1', 'rid-1', 'acme/app', 'user-1')`, project))

	t.Run("pr_pipeline", func(t *testing.T) {
		insert := func(recordID, state string, extra string) error {
			return exec(
				`INSERT INTO pr_pipeline (record_id, org_id, project_id, repo_id, state, state_head_sha`+cols(extra)+`)
				 VALUES ($1, 'org-1', $2, 'repo-1', $3, 'sha-1'`+extra+`)`, recordID, project, state)
		}
		for i, state := range core.AllPRPipelineStates {
			id := "rec-state-" + string(state)
			insertRecord(id, 100+i)
			extra := ""
			if state == core.PRPipelineNeedsPerson {
				extra = needsPerson
			}
			require.NoError(t, insert(id, string(state), extra), "state %q", state)
		}

		insertRecord("rec-pp", 1)
		err := insert("rec-pp", "merged", "")
		require.Error(t, err, "a state outside the closed set is refused")
		assert.Equal(t, "pr_pipeline_state", orm.ConstraintName(err))

		err = insert("rec-pp", string(core.PRPipelineNeedsPerson), "")
		require.Error(t, err, "needs_person without a since is refused")
		assert.Equal(t, "pr_pipeline_needs_person_since", orm.ConstraintName(err))

		err = insert("rec-pp", string(core.PRPipelineAwaitingReview), needsPerson)
		require.Error(t, err, "a since outside needs_person is refused")
		assert.Equal(t, "pr_pipeline_needs_person_since", orm.ConstraintName(err))

		err = insert("rec-pp", string(core.PRPipelineNeedsPerson), ", now()")
		require.Error(t, err, "needs_person without a reason is refused")
		assert.Equal(t, "pr_pipeline_needs_person_reason", orm.ConstraintName(err))

		require.NoError(t, insert("rec-pp", string(core.PRPipelineAwaitingReview), ""))
		var priority, reviewRound, fixAttempts int
		var prioritySource string
		var version int64
		require.NoError(t, raw.QueryRow(
			`SELECT priority, priority_source, review_round, fix_attempts, version FROM pr_pipeline WHERE record_id = 'rec-pp'`).
			Scan(&priority, &prioritySource, &reviewRound, &fixAttempts, &version))
		assert.Equal(t, 50, priority)
		assert.Equal(t, string(core.PRPriorityDefault), prioritySource)
		assert.Zero(t, reviewRound)
		assert.Zero(t, fixAttempts)
		assert.Zero(t, version)

		for _, src := range core.AllPRPrioritySources {
			require.NoError(t, exec(`UPDATE pr_pipeline SET priority_source = $1 WHERE record_id = 'rec-pp'`, string(src)), "source %q", src)
		}
		err = exec(`UPDATE pr_pipeline SET priority_source = 'author' WHERE record_id = 'rec-pp'`)
		require.Error(t, err)
		assert.Equal(t, "pr_pipeline_priority_source", orm.ConstraintName(err))

		require.NoError(t, exec(`UPDATE pr_pipeline SET priority = 0 WHERE record_id = 'rec-pp'`))
		require.NoError(t, exec(`UPDATE pr_pipeline SET priority = 100 WHERE record_id = 'rec-pp'`))
		for _, p := range []int{-1, 101} {
			err = exec(`UPDATE pr_pipeline SET priority = $1 WHERE record_id = 'rec-pp'`, p)
			require.Error(t, err, "priority %d is out of range", p)
			assert.Equal(t, "pr_pipeline_priority_range", orm.ConstraintName(err))
		}

		err = exec(`UPDATE pr_pipeline SET next_action_at = now() WHERE record_id = 'rec-pp'`)
		require.Error(t, err, "a due time with no action is refused")
		assert.Equal(t, "pr_pipeline_due_names_action", orm.ConstraintName(err))
		require.NoError(t, exec(`UPDATE pr_pipeline SET next_action = 'review' WHERE record_id = 'rec-pp'`),
			"an action may wait with no due time")
		require.NoError(t, exec(`UPDATE pr_pipeline SET next_action_at = now() WHERE record_id = 'rec-pp'`))

		var indexDef string
		require.NoError(t, raw.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'pr_pipeline_due_idx'`).Scan(&indexDef))
		assert.Contains(t, indexDef, "(next_action_at) WHERE (next_action_at IS NOT NULL)")

		err = insert("rec-missing", string(core.PRPipelineDraft), "")
		require.Error(t, err, "a row needs its pr_records row")
		assert.Equal(t, "pr_pipeline_record_id_fkey", orm.ConstraintName(err))

		insertRecord("rec-norepo", 2)
		err = exec(`INSERT INTO pr_pipeline (record_id, org_id, project_id, repo_id, state, state_head_sha)
			VALUES ('rec-norepo', 'org-1', $1, 'repo-missing', 'draft', 'sha-1')`, project)
		require.Error(t, err, "a row needs its enrolled repository")
		assert.Equal(t, "pr_pipeline_repo_id_fkey", orm.ConstraintName(err))

		require.NoError(t, exec(`DELETE FROM pr_records WHERE id = 'rec-pp'`))
		assert.Zero(t, count(`SELECT count(*) FROM pr_pipeline WHERE record_id = 'rec-pp'`),
			"the state goes with its record")
	})

	t.Run("pr_head_verdicts", func(t *testing.T) {
		insertRecord("rec-v", 10)
		insert := func(id, sha string, round int, verdict, rule, tier string) error {
			return exec(
				`INSERT INTO pr_head_verdicts (id, org_id, project_id, record_id, head_sha, round, verdict, rule, risk_tier, run_id, node_id, reviewer_principal)
				 VALUES ($1, 'org-1', $2, 'rec-v', $3, $4, $5, $6, $7, 'run-1', 'review', 'reviewer')`,
				id, project, sha, round, verdict, rule, tier)
		}
		round := 1
		next := func() int { round++; return round }
		for _, v := range core.AllPRHeadVerdicts {
			require.NoError(t, insert("v-"+string(v), "sha-a", next(), string(v), "model", "low"), "verdict %q", v)
		}
		for _, r := range core.AllPRVerdictRules {
			require.NoError(t, insert("r-"+string(r), "sha-a", next(), "pass", string(r), "low"), "rule %q", r)
		}
		for _, tier := range core.AllPRRiskTiers {
			require.NoError(t, insert("t-"+string(tier), "sha-a", next(), "pass", "model", string(tier)), "tier %q", tier)
		}

		for _, c := range []struct{ verdict, rule, tier, constraint string }{
			{"approve", "model", "low", "pr_head_verdicts_verdict"},
			{"pass", "label", "low", "pr_head_verdicts_rule"},
			{"pass", "model", "critical", "pr_head_verdicts_risk_tier"},
		} {
			err := insert("bad-"+c.constraint, "sha-a", next(), c.verdict, c.rule, c.tier)
			require.Error(t, err, c.constraint)
			assert.Equal(t, c.constraint, orm.ConstraintName(err))
		}

		err := insert("v-round0", "sha-a", 0, "pass", "model", "low")
		require.Error(t, err, "rounds count from 1")

		require.NoError(t, insert("v-dup1", "sha-b", 1, "changes", "model", "high"))
		err = insert("v-dup2", "sha-b", 1, "pass", "model", "high")
		require.Error(t, err, "one verdict per (head, round)")
		assert.Equal(t, "pr_head_verdicts_head_round_key", orm.ConstraintName(err))
		require.NoError(t, insert("v-dup3", "sha-c", 1, "pass", "model", "high"), "a new head needs a new row")

		err = exec(`INSERT INTO pr_head_verdicts (id, org_id, project_id, record_id, head_sha, round, verdict, rule, risk_tier, reviewer_principal)
			VALUES ('v-noderun', 'org-1', $1, 'rec-v', 'sha-d', 1, 'pass', 'model', 'low', 'reviewer')`, project)
		require.Error(t, err, "a model verdict names its run and node")
		assert.Equal(t, "pr_head_verdicts_model_names_node", orm.ConstraintName(err))
		require.NoError(t, exec(`INSERT INTO pr_head_verdicts (id, org_id, project_id, record_id, head_sha, round, verdict, rule, risk_tier, reviewer_principal)
			VALUES ('v-structural', 'org-1', $1, 'rec-v', 'sha-d', 1, 'pass', 'generated_only', 'low', 'queue')`, project),
			"a structural rule's verdict may name neither")

		var findings string
		var required []byte
		require.NoError(t, raw.QueryRow(`SELECT findings_json::text, required_decisions::text FROM pr_head_verdicts WHERE id = 'v-structural'`).
			Scan(&findings, &required))
		assert.Equal(t, "[]", findings)
		assert.Equal(t, "{}", string(required))

		err = exec(`UPDATE pr_head_verdicts SET verdict = 'pass' WHERE id = 'v-dup1'`)
		require.Error(t, err, "a verdict is never updated")
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "%v", err)
		assert.Equal(t, "55000", pgErr.Code)

		require.NoError(t, exec(`DELETE FROM pr_records WHERE id = 'rec-v'`), "verdicts still go with their record")
		assert.Zero(t, count(`SELECT count(*) FROM pr_head_verdicts WHERE record_id = 'rec-v'`))
	})

	t.Run("pipeline_attempts", func(t *testing.T) {
		insert := func(id, kind, stage, signature string, attempt int) error {
			return exec(
				`INSERT INTO pipeline_attempts (id, org_id, project_id, subject_kind, subject_id, stage, signature, attempt)
				 VALUES ($1, 'org-1', $2, $3, 'subject-1', $4, $5, $6)`, id, project, kind, stage, signature, attempt)
		}
		for _, k := range core.AllPipelineAttemptSubjectKinds {
			for _, s := range core.AllPipelineAttemptStages {
				require.NoError(t, insert("a-"+string(k)+"-"+string(s), string(k), string(s), "", 1), "%q/%q", k, s)
			}
		}
		err := insert("a-bad-kind", "release", "fix", "", 1)
		require.Error(t, err)
		assert.Equal(t, "pipeline_attempts_subject_kind", orm.ConstraintName(err))
		err = insert("a-bad-stage", "pr", "deploy", "", 1)
		require.Error(t, err)
		assert.Equal(t, "pipeline_attempts_stage", orm.ConstraintName(err))

		err = insert("a-dup", "pr", "fix", "", 1)
		require.Error(t, err, "an attempt number is taken once per (subject, stage, signature)")
		assert.Equal(t, "pipeline_attempts_number_key", orm.ConstraintName(err))
		require.NoError(t, insert("a-sig", "pr", "fix", "sig-2", 1), "a new signature starts again at 1")
		require.Error(t, insert("a-zero", "pr", "fix", "sig-3", 0), "attempts count from 1")

		for _, o := range core.AllPipelineAttemptOutcomes {
			require.NoError(t, exec(`UPDATE pipeline_attempts SET outcome = $1, finished_at = now() WHERE id = 'a-sig'`, string(o)), "outcome %q", o)
		}
		err = exec(`UPDATE pipeline_attempts SET outcome = 'flaky' WHERE id = 'a-sig'`)
		require.Error(t, err)
		assert.Equal(t, "pipeline_attempts_outcome", orm.ConstraintName(err))
		err = exec(`UPDATE pipeline_attempts SET outcome = NULL WHERE id = 'a-sig'`)
		require.Error(t, err, "a finished attempt has an outcome")
		assert.Equal(t, "pipeline_attempts_finished", orm.ConstraintName(err))
		err = exec(`UPDATE pipeline_attempts SET outcome = 'failed' WHERE id = 'a-pr-review'`)
		require.Error(t, err, "an outcome needs a finish time")
		assert.Equal(t, "pipeline_attempts_finished", orm.ConstraintName(err))
	})
}

// TestPRRecordsTenancyConstraints proves what
// db/migrations/20261126105940_pr_records_tenancy.up.sql and its validate
// migration enforce on every pr_records* table: nullable org_id and
// project_id, a blank org refused, an unknown project refused, a deleted
// project unstamping the row rather than deleting it, and every constraint
// validated.
func TestPRRecordsTenancyConstraints(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	count := func(query string) int {
		t.Helper()
		var n int
		require.NoError(t, raw.QueryRow(query).Scan(&n))
		return n
	}
	tables := []string{"pr_records", "pr_record_heads", "pr_record_reviews", "pr_record_checks", "pr_record_deliveries"}
	var validated bool
	var n int
	require.NoError(t, raw.QueryRow(
		`SELECT bool_and(convalidated), count(*) FROM pg_constraint
		 WHERE conrelid::regclass::text = ANY($1) AND (conname LIKE '%\_org\_id\_not\_blank' OR conname LIKE '%\_project\_id\_fkey')`,
		tables).Scan(&validated, &n))
	assert.Equal(t, 2*len(tables), n)
	assert.True(t, validated, "every tenancy constraint is validated")

	_, err := raw.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
		VALUES ('p-tenant', 'u1', 'p', '/p/tenant', now(), now(), now())`)
	require.NoError(t, err)

	insertRecord := func(id string, org, project any) error {
		_, err := raw.Exec(
			`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state, snapshot_at, observed_at, org_id, project_id)
			 VALUES ($1, 'github', 'inst-1', 'rid-t', 'acme/app', (SELECT count(*) + 1 FROM pr_records), 'open', now(), now(), $2, $3)`,
			id, org, project)
		return err
	}
	require.NoError(t, insertRecord("rec-untenanted", nil, nil), "a record of an unenrolled repository has no tenant")
	require.NoError(t, insertRecord("rec-tenanted", "org-1", "p-tenant"))

	err = insertRecord("rec-blank", "", "p-tenant")
	require.Error(t, err)
	assert.Equal(t, "pr_records_org_id_not_blank", orm.ConstraintName(err))
	err = insertRecord("rec-unknown", "org-1", "p-missing")
	require.Error(t, err)
	assert.Equal(t, "pr_records_project_id_fkey", orm.ConstraintName(err))

	children := []struct{ table, insert string }{
		{"pr_record_heads", `INSERT INTO pr_record_heads (id, record_id, head_sha, first_seen_at, org_id, project_id)
			VALUES ('h-' || $1, 'rec-tenanted', 'sha-' || $1, now(), $2, $3)`},
		{"pr_record_reviews", `INSERT INTO pr_record_reviews (id, record_id, review_id, commit_sha, marker_sha, kind, submitted_at, observed_at, org_id, project_id)
			VALUES ('r-' || $1, 'rec-tenanted', 'rv-' || $1, 'sha', 'sha', 'passed', now(), now(), $2, $3)`},
		{"pr_record_checks", `INSERT INTO pr_record_checks (id, provider, repository_id, head_sha, name, check_run_id, observed_at, org_id, project_id)
			VALUES ('c-' || $1, 'github', 'rid-t', 'sha', 'ci-' || $1, 1, now(), $2, $3)`},
		{"pr_record_deliveries", `INSERT INTO pr_record_deliveries (provider, delivery_key, event_index, event_type, outcome, applied_at, org_id, project_id)
			VALUES ('github', 'd-' || $1, 0, 'pull_request', 'applied', now(), $2, $3)`},
	}
	for _, c := range children {
		_, err := raw.Exec(c.insert, "ok", "org-1", "p-tenant")
		require.NoError(t, err, c.table)
		_, err = raw.Exec(c.insert, "blank", "", "p-tenant")
		require.Error(t, err, c.table)
		assert.Equal(t, c.table+"_org_id_not_blank", orm.ConstraintName(err))
		_, err = raw.Exec(c.insert, "unknown", "org-1", "p-missing")
		require.Error(t, err, c.table)
		assert.Equal(t, c.table+"_project_id_fkey", orm.ConstraintName(err))
	}

	_, err = raw.Exec(`DELETE FROM projects WHERE id = 'p-tenant'`)
	require.NoError(t, err)
	for _, table := range tables {
		assert.Zero(t, count(`SELECT count(*) FROM `+table+` WHERE project_id IS NOT NULL`),
			"%s: a deleted project unstamps its rows", table)
		assert.NotZero(t, count(`SELECT count(*) FROM `+table+` WHERE org_id = 'org-1'`),
			"%s: and keeps them", table)
	}
}
