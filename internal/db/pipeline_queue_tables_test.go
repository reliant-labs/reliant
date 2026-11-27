package db

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestPipelineQueueTablesConstraints proves, against a database, what
// db/migrations/20261126160301_pipeline_queue.up.sql enforces: the defaults, every closed set, the
// cross-column CHECKs, one active entry per PR (the partial unique index),
// the foreign keys and their SET NULL / CASCADE. The hermetic pin that each
// CHECK lists exactly its Go set is `forge lint --closed-sets`.
func TestPipelineQueueTablesConstraints(t *testing.T) {
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
	for i, id := range []string{"rec-1", "rec-2", "rec-3", "rec-4"} {
		require.NoError(t, exec(
			`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state, snapshot_at, observed_at)
			 VALUES ($1, 'github', 'inst-1', 'rid-1', 'acme/app', $2, 'open', now(), now())`, id, i+1))
	}

	insertBatch := func(id, kind, state string, parent, landedSHA any) error {
		return exec(
			`INSERT INTO queue_batches (id, org_id, project_id, repo_id, base_sha, kind, state, parent_batch_id, landed_sha)
			 VALUES ($1, 'org-1', $2, 'repo-1', 'base', $3, $4, $5, $6)`, id, project, kind, state, parent, landedSHA)
	}

	t.Run("queue_batches", func(t *testing.T) {
		require.NoError(t, exec(`INSERT INTO queue_batches (id, org_id, project_id, repo_id, base_sha) VALUES ('b-defaults', 'org-1', $1, 'repo-1', 'base')`, project))
		var kind, state, checks string
		var attempt int
		require.NoError(t, raw.QueryRow(`SELECT kind, state, attempt, check_names::text FROM queue_batches WHERE id = 'b-defaults'`).
			Scan(&kind, &state, &attempt, &checks))
		assert.Equal(t, string(core.QueueBatchNormal), kind)
		assert.Equal(t, string(core.QueueBatchBuilding), state)
		assert.Equal(t, 1, attempt)
		assert.Equal(t, "{}", checks)

		for _, k := range core.AllQueueBatchKinds {
			require.NoError(t, insertBatch("b-kind-"+string(k), string(k), "building", "b-defaults", nil), "kind %q", k)
		}
		for _, s := range core.AllQueueBatchStates {
			var landed any
			if s == core.QueueBatchLanded {
				landed = "main-sha"
			}
			require.NoError(t, insertBatch("b-state-"+string(s), "normal", string(s), nil, landed), "state %q", s)
		}
		for _, b := range core.AllQueueIsolationBackends {
			require.NoError(t, exec(`UPDATE queue_batches SET isolation_backend = $1 WHERE id = 'b-defaults'`, string(b)), "backend %q", b)
		}
		for _, r := range core.AllQueueGateRunners {
			require.NoError(t, exec(`UPDATE queue_batches SET gate_runner = $1 WHERE id = 'b-defaults'`, string(r)), "runner %q", r)
		}

		refused(t, insertBatch("b-bad-kind", "speculative", "building", nil, nil), "queue_batches_kind", "a kind outside the set")
		refused(t, insertBatch("b-bad-state", "normal", "merged", nil, nil), "queue_batches_state", "a state outside the set")
		refused(t, exec(`UPDATE queue_batches SET isolation_backend = 'docker' WHERE id = 'b-defaults'`),
			"queue_batches_isolation_backend", "a backend outside the set")
		refused(t, exec(`UPDATE queue_batches SET gate_runner = 'actions' WHERE id = 'b-defaults'`),
			"queue_batches_gate_runner", "a runner outside the set")

		require.NoError(t, insertBatch("b-runc", "normal", "passed", nil, nil))
		require.NoError(t, exec(`UPDATE queue_batches SET isolation_backend = 'runc' WHERE id = 'b-runc'`), "a runc batch may pass (shadow)")
		refused(t, exec(`UPDATE queue_batches SET state = 'landed', landed_sha = 'main-sha' WHERE id = 'b-runc'`),
			"queue_batches_runc_never_lands", "a batch gated outside an isolation boundary never lands")
		refused(t, exec(`UPDATE queue_batches SET state = 'landed', landed_sha = 'main-sha', verdict_json = '{"unisolated_accepted": false}' WHERE id = 'b-runc'`),
			"queue_batches_runc_never_lands", "an explicit false is no opt-in")
		require.NoError(t, insertBatch("b-runc-ok", "normal", "passed", nil, nil))
		require.NoError(t, exec(`UPDATE queue_batches SET isolation_backend = 'runc', verdict_json = '{"unisolated_accepted": true}' WHERE id = 'b-runc-ok'`))
		require.NoError(t, exec(`UPDATE queue_batches SET state = 'landed', landed_sha = 'main-sha' WHERE id = 'b-runc-ok'`),
			"a runc batch the repo opted in for lands, marked unisolated (owner, 2026-11-26 21:12Z)")
		require.NoError(t, exec(`UPDATE queue_batches SET isolation_backend = 'sysbox', state = 'landed', landed_sha = 'main-sha' WHERE id = 'b-runc'`),
			"the same batch on sysbox may land")

		refused(t, insertBatch("b-landed-nosha", "normal", "landed", nil, nil), "queue_batches_landed", "a landed batch names main's sha")
		refused(t, insertBatch("b-passed-sha", "normal", "passed", nil, "main-sha"), "queue_batches_landed", "only a landed batch has a landed sha")
		refused(t, insertBatch("b-bisect-orphan", "bisect", "building", nil, nil), "queue_batches_bisect_parent", "a bisect half names its parent")
		refused(t, insertBatch("b-self", "bisect", "building", "b-self", nil), "queue_batches_parent_not_self", "a batch is not its own parent")
		refused(t, insertBatch("b-noparent", "rerun", "building", "b-missing", nil), "queue_batches_parent_batch_id_fkey", "the parent must exist")

		require.NoError(t, exec(`UPDATE queue_batches SET stages_json = '{"lint": {"verdict": "pass"}, "sbom_scan": {"verdict": "off"}}', expected_trees_json = '["t1", "t2"]' WHERE id = 'b-defaults'`))
		refused(t, exec(`UPDATE queue_batches SET stages_json = '[]' WHERE id = 'b-defaults'`), "queue_batches_stages_object", "stages are keyed by stage")
		refused(t, exec(`UPDATE queue_batches SET expected_trees_json = '{}' WHERE id = 'b-defaults'`), "queue_batches_expected_trees_array", "the expected trees are in order")

		require.NoError(t, insertBatch("b-parent", "normal", "failed", nil, nil))
		require.NoError(t, insertBatch("b-half", "bisect", "gating", "b-parent", nil))
		refused(t, exec(`DELETE FROM queue_batches WHERE id = 'b-parent'`), "queue_batches_parent_batch_id_fkey",
			"a bisected batch is not deleted from under its halves")
	})

	t.Run("queue_entries", func(t *testing.T) {
		require.NoError(t, insertBatch("b-e", "normal", "gating", nil, nil))
		type e struct {
			id, record, state  string
			batch, reason, sha any
			landedAt           bool
		}
		insert := func(r e) error {
			landedAt := "NULL"
			if r.landedAt {
				landedAt = "now()"
			}
			return exec(
				`INSERT INTO queue_entries (id, org_id, project_id, repo_id, record_id, head_sha, priority, state, batch_id, eject_reason, landed_sha, landed_at)
				 VALUES ($1, 'org-1', $2, 'repo-1', $3, 'head', 50, $4, $5, $6, $7, `+landedAt+`)`,
				r.id, project, r.record, r.state, r.batch, r.reason, r.sha)
		}
		valid := map[core.QueueEntryState]e{
			core.QueueEntryWaiting: {state: "waiting"},
			core.QueueEntryBatched: {state: "batched", batch: "b-e"},
			core.QueueEntryLanded:  {state: "landed", batch: "b-e", sha: "main-1", landedAt: true},
			core.QueueEntryEjected: {state: "ejected", reason: "conflict"},
			core.QueueEntryRemoved: {state: "removed"},
		}
		for _, s := range core.AllQueueEntryStates {
			r, ok := valid[s]
			require.True(t, ok, "state %q has a valid row here", s)
			r.id, r.record = "e-"+string(s), "rec-3"
			if s == core.QueueEntryBatched {
				r.record = "rec-2" // rec-3 is already waiting
			}
			require.NoError(t, insert(r), "state %q", s)
		}
		for _, reason := range core.AllQueueEjectReasons {
			require.NoError(t, insert(e{id: "e-eject-" + string(reason), record: "rec-1", state: "ejected", reason: string(reason)}), "reason %q", reason)
		}

		refused(t, insert(e{id: "e-bad-state", record: "rec-1", state: "parked"}), "queue_entries_state", "a state outside the set")
		refused(t, insert(e{id: "e-bad-reason", record: "rec-1", state: "ejected", reason: "flaky"}), "queue_entries_eject_reason", "a reason outside the set")
		refused(t, insert(e{id: "e-eject-noreason", record: "rec-1", state: "ejected"}), "queue_entries_ejected", "an ejection names its reason")
		refused(t, insert(e{id: "e-reason-waiting", record: "rec-1", state: "removed", reason: "hold"}), "queue_entries_ejected", "only an ejection has a reason")
		refused(t, insert(e{id: "e-landed-nosha", record: "rec-1", state: "landed"}), "queue_entries_landed", "a landing names main's sha")
		refused(t, insert(e{id: "e-landed-notime", record: "rec-1", state: "landed", sha: "main-2"}), "queue_entries_landed", "and when")
		refused(t, insert(e{id: "e-sha-waiting", record: "rec-1", state: "removed", sha: "main-3", landedAt: true}), "queue_entries_landed", "only a landing has a landed sha")
		refused(t, insert(e{id: "e-batched-nobatch", record: "rec-1", state: "batched"}), "queue_entries_batched", "a batched entry names its batch")
		refused(t, exec(`UPDATE queue_entries SET priority = 101 WHERE id = 'e-waiting'`), "queue_entries_priority_range", "priority is 0–100")

		refused(t, insert(e{id: "e-dup-waiting", record: "rec-3", state: "waiting"}), "queue_entries_active_record_key", "a waiting PR is not enqueued twice")
		refused(t, insert(e{id: "e-dup-batched", record: "rec-2", state: "waiting"}), "queue_entries_active_record_key", "nor a batched one")
		require.NoError(t, insert(e{id: "e-again", record: "rec-1", state: "waiting"}), "a PR that left the queue may enter it again")

		refused(t, exec(`DELETE FROM queue_batches WHERE id = 'b-e'`), "queue_entries_batch_id_fkey",
			"a batch its entries name is not deleted on its own")

		require.NoError(t, exec(`DELETE FROM pr_records WHERE id = 'rec-1'`))
		var n int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM queue_entries WHERE record_id = 'rec-1'`).Scan(&n))
		assert.Zero(t, n, "entries go with their record")
	})

	t.Run("main_health", func(t *testing.T) {
		insert := func(id, source, verdict string) error {
			return exec(
				`INSERT INTO main_health (id, org_id, project_id, repo_id, sha, source, verdict) VALUES ($1, 'org-1', $2, 'repo-1', 'main-1', $3, $4)`,
				id, project, source, verdict)
		}
		for _, s := range core.AllMainHealthSources {
			for _, v := range core.AllMainHealthVerdicts {
				require.NoError(t, insert("h-"+string(s)+"-"+string(v), string(s), string(v)), "%q/%q", s, v)
			}
		}
		refused(t, insert("h-bad-source", "ci", "pass"), "main_health_source", "a source outside the set")
		refused(t, insert("h-bad-verdict", "gate", "green"), "main_health_verdict", "a verdict outside the set")
		refused(t, exec(`INSERT INTO main_health (id, org_id, project_id, repo_id, sha, source, verdict, batch_id)
			VALUES ('h-nobatch', 'org-1', $1, 'repo-1', 'main-1', 'gate', 'pass', 'b-missing')`, project),
			"main_health_batch_id_fkey", "the batch must exist")

		require.NoError(t, insertBatch("b-h", "normal", "gating", nil, nil))
		require.NoError(t, exec(`INSERT INTO queue_entries (id, org_id, project_id, repo_id, record_id, head_sha, priority, state, batch_id)
			VALUES ('e-h', 'org-1', $1, 'repo-1', 'rec-4', 'head', 50, 'batched', 'b-h')`, project))
		require.NoError(t, exec(`DELETE FROM pipeline_repos WHERE id = 'repo-1'`),
			"unenrolling removes the queue's state, a batched entry and its batch included")
		var n int
		require.NoError(t, raw.QueryRow(`SELECT (SELECT count(*) FROM main_health) + (SELECT count(*) FROM queue_entries) + (SELECT count(*) FROM queue_batches)`).Scan(&n))
		assert.Zero(t, n)
	})
}
