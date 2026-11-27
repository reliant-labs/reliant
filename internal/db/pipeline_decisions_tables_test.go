package db

import (
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestPipelineDecisionsConstraints proves, against a database, what
// db/migrations/20261126154939_pipeline_decisions.up.sql enforces: each closed set, §4.3's two CHECKs in
// both directions, the CHECKs that close their NULL gap, the request rules,
// the policy_change_id foreign key, and the deferred foreign key from
// pipeline_repos.queue_paused_by_decision_id. The hermetic pin that each
// annotated CHECK lists exactly its Go set is `forge lint --closed-sets`.
func TestPipelineDecisionsConstraints(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	const project = "test-project"
	exec := func(query string, args ...any) error {
		_, err := raw.Exec(query, args...)
		return err
	}
	require.NoError(t, exec(
		`INSERT INTO pipeline_setting_changes (id, org_id, project_id, setting, old_value, new_value, changed_by_user_id, changed_via)
		 VALUES ('change-1', 'org-1', $1, 'emergency_land_policy', 'ask', 'auto_allow', 'admin-1', 'web')`, project))

	// d is one decision row; insert writes it, with every column it sets.
	type d struct {
		id, subjectKind, kind, state string
		headSHA, requestedBy, user   any
		via, policyChange            any
		decided, revoked             bool
	}
	insert := func(r d) error {
		decidedAt, revokedAt := "NULL", "NULL"
		if r.decided {
			decidedAt = "now()"
		}
		if r.revoked {
			revokedAt = "now()"
		}
		return exec(
			`INSERT INTO pipeline_decisions (id, org_id, project_id, subject_kind, subject_id, head_sha, kind, state, reason,
			   requested_by_principal, decided_by_user_id, decided_via, policy_change_id, decided_at, revoked_at)
			 VALUES ($1, 'org-1', $2, $3, 'subject-1', $4, $5, $6, 'because', $7, $8, $9, $10, `+decidedAt+`, `+revokedAt+`)`,
			r.id, project, r.subjectKind, r.headSHA, r.kind, r.state, r.requestedBy, r.user, r.via, r.policyChange)
	}
	// byUser is a decision a signed-in person made directly.
	byUser := func(id string) d {
		return d{id: id, subjectKind: "pr", kind: "hold", state: "approved", user: "user-1", via: "web", decided: true}
	}
	refused := func(t *testing.T, r d, constraint, why string) {
		t.Helper()
		err := insert(r)
		require.Error(t, err, why)
		assert.Equal(t, constraint, orm.ConstraintName(err), why)
	}

	t.Run("closed sets", func(t *testing.T) {
		for _, k := range core.AllPipelineDecisionSubjectKinds {
			r := byUser("subject-" + string(k))
			r.subjectKind = string(k)
			r.headSHA = "sha-1"
			require.NoError(t, insert(r), "subject kind %q", k)
		}
		for _, k := range core.AllPipelineDecisionKinds {
			r := byUser("kind-" + string(k))
			r.kind = string(k)
			require.NoError(t, insert(r), "kind %q", k)
		}
		for _, s := range core.AllPipelineDecisionStates {
			r := byUser("state-" + string(s))
			r.state = string(s)
			switch s {
			case core.PipelineDecisionRequested:
				r = d{id: r.id, subjectKind: "pr", kind: "emergency_land", state: "requested", requestedBy: "agent"}
			case core.PipelineDecisionRevoked:
				r.revoked = true
			}
			require.NoError(t, insert(r), "state %q", s)
		}
		for _, v := range []core.PipelineDecidedVia{core.PipelineDecidedViaWeb, core.PipelineDecidedViaDesktop} {
			r := byUser("via-" + string(v))
			r.via = string(v)
			require.NoError(t, insert(r), "decided_via %q", v)
		}
		require.True(t, slices.Contains(core.AllPipelineDecidedVia, core.PipelineDecidedViaPolicy))

		bad := byUser("bad-subject")
		bad.subjectKind = "user"
		refused(t, bad, "pipeline_decisions_subject_kind", "a subject kind outside the set")
		bad = byUser("bad-kind")
		bad.kind = "merge"
		refused(t, bad, "pipeline_decisions_kind", "a kind outside the set")
		bad = byUser("bad-state")
		bad.state = "pending"
		refused(t, bad, "pipeline_decisions_state", "a state outside the set")
		bad = byUser("bad-via")
		bad.via = "label"
		refused(t, bad, "pipeline_decisions_decided_via", "no GitHub surface decides")
	})

	t.Run("policy decisions", func(t *testing.T) {
		policy := d{id: "policy-ok", subjectKind: "pr", kind: "emergency_land", state: "approved",
			requestedBy: "agent", via: "policy", policyChange: "change-1", decided: true}
		require.NoError(t, insert(policy), "auto_allow approves an emergency_land, naming the setting change")

		r := policy
		r.id, r.policyChange = "policy-nochange", nil
		refused(t, r, "pipeline_decisions_policy_change", "a policy decision names the setting change in force")

		r = byUser("web-change")
		r.policyChange = "change-1"
		refused(t, r, "pipeline_decisions_policy_change", "only a policy decision names a setting change")

		r = policy
		r.id, r.user = "policy-user", "user-1"
		refused(t, r, "pipeline_decisions_decided_by", "a policy decision names no user")

		r = policy
		r.id, r.kind, r.requestedBy = "policy-hold", "hold", nil
		refused(t, r, "pipeline_decisions_policy_kind", "the policy decides an emergency_land and nothing else")

		r = policy
		r.id, r.policyChange = "policy-unknown", "change-missing"
		refused(t, r, "pipeline_decisions_policy_change_id_fkey", "the setting change must exist")
	})

	t.Run("person decisions", func(t *testing.T) {
		r := byUser("web-nouser")
		r.user = nil
		refused(t, r, "pipeline_decisions_decided_by", "a decision not made by the policy names its user")

		r = byUser("desktop-nouser")
		r.via, r.user, r.state = "desktop", nil, "denied"
		refused(t, r, "pipeline_decisions_decided_by", "a denial names its user too")
	})

	t.Run("requests and the NULL gap", func(t *testing.T) {
		request := d{id: "req-ok", subjectKind: "pr", kind: "resolve_needs_person", state: "requested", requestedBy: "agent"}
		require.NoError(t, insert(request))

		r := request
		r.id, r.via, r.user, r.decided = "req-decided", "web", "user-1", true
		refused(t, r, "pipeline_decisions_decided_states", "an open request is undecided")

		r = request
		r.id, r.user = "req-user", "user-1"
		refused(t, r, "pipeline_decisions_undecided", "an undecided row names no user")

		r = request
		r.id, r.policyChange = "req-change", "change-1"
		refused(t, r, "pipeline_decisions_undecided", "an undecided row names no setting change")

		r = request
		r.id, r.decided = "req-decided-at", true
		refused(t, r, "pipeline_decisions_undecided", "an undecided row has no decision time")

		r = byUser("approved-undecided")
		r.via, r.user, r.decided = nil, nil, false
		refused(t, r, "pipeline_decisions_decided_states", "an approval says how it was decided")

		r = request
		r.id, r.state = "req-lapsed", "expired"
		require.NoError(t, insert(r), "a request nobody decided may lapse")

		r = request
		r.id, r.requestedBy = "req-anon", nil
		refused(t, r, "pipeline_decisions_request_names_requester", "a request names who asked")

		for _, k := range core.AllPipelineDecisionKinds {
			r := d{id: "req-" + string(k), subjectKind: "pr", kind: string(k), state: "requested", requestedBy: "agent"}
			err := insert(r)
			if slices.Contains(core.PipelineRequestableDecisionKinds, k) {
				require.NoError(t, err, "kind %q allows requests", k)
				continue
			}
			require.Error(t, err, "kind %q is decided only by a person", k)
			assert.Equal(t, "pipeline_decisions_requestable", orm.ConstraintName(err))
		}
	})

	t.Run("subjects and revocation", func(t *testing.T) {
		r := byUser("head-nosha")
		r.subjectKind = "pr_head"
		refused(t, r, "pipeline_decisions_pr_head_sha", "a pr_head decision binds to a head")

		r = byUser("revoked-notime")
		r.state = "revoked"
		refused(t, r, "pipeline_decisions_revoked", "a revocation has a time")
		r = byUser("approved-revoked")
		r.revoked = true
		refused(t, r, "pipeline_decisions_revoked", "only a revoked decision has a revocation time")

		err := exec(`INSERT INTO pipeline_decisions (id, org_id, project_id, subject_kind, subject_id, kind, state, reason, decided_by_user_id, decided_via, decided_at)
			VALUES ('no-reason', 'org-1', $1, 'pr', 's', 'hold', 'approved', '', 'user-1', 'web', now())`, project)
		require.Error(t, err, "a decision states its reason")
	})

	t.Run("pipeline_repos pause decision", func(t *testing.T) {
		require.NoError(t, insert(byUser("pause-1")))
		insertRepo := func(id, rid string, decision any) error {
			return exec(
				`INSERT INTO pipeline_repos (id, org_id, project_id, provider, account_key, repository_id, repository, queue_state, queue_paused_by_decision_id, created_by_user_id)
				 VALUES ($1, 'org-1', $2, 'github', 'inst-1', $3, 'acme/app', 'paused', $4, 'user-1')`, id, project, rid, decision)
		}
		require.NoError(t, insertRepo("repo-paused", "rid-1", "pause-1"))
		err := insertRepo("repo-bad", "rid-2", "decision-missing")
		require.Error(t, err)
		assert.Equal(t, "pipeline_repos_queue_paused_by_decision_id_fkey", orm.ConstraintName(err))

		require.NoError(t, exec(`DELETE FROM pipeline_decisions WHERE id = 'pause-1'`))
		var decision *string
		require.NoError(t, raw.QueryRow(`SELECT queue_paused_by_decision_id FROM pipeline_repos WHERE id = 'repo-paused'`).Scan(&decision))
		assert.Nil(t, decision, "a deleted decision clears the reference")
	})
}
