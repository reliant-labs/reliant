// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEndTenantStampConstraints proves, against a database, what
// db/migrations/20261126053410_end_tenants_run_stamps.up.sql enforces on
// end_tenants and on the end_tenant_id stamp of chats, workflows and triggers
// (GTM-R6, R6-2a): a stamp cannot name another host's tenant, a stamped row
// has an org, the hsp_ prefix and the stamp go together, a tenant row never
// uses a machine, a service principal's chat is never shared, and a run
// inherits its chat's stamp or raises.
func TestEndTenantStampConstraints(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	const (
		hostA = "org-host-a"
		hostB = "org-host-b"
		tenA  = "et-a"
		tenB  = "et-b"
		hspA  = "hsp_01HZZZZZZZZZZZZZZZZZZZZZZA"
	)
	mustExec := func(q string, args ...any) {
		t.Helper()
		_, err := raw.Exec(q, args...)
		require.NoError(t, err, q)
	}
	mustExec(`INSERT INTO end_tenants (id, host_org_id, external_ref) VALUES ($1, $2, 'cust-1'), ($3, $4, 'cust-1')`,
		tenA, hostA, tenB, hostB)

	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	insertChat := func(id, user, org, tenant string, noMachine bool, visibility string) error {
		_, err := raw.Exec(
			`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active, org_id, end_tenant_id, no_machine, visibility)
			 VALUES ($1, 't', 'test-project', $2, now(), now(), now(), $3, $4, $5, $6)`,
			id, user, nullable(org), nullable(tenant), noMachine, nullable(visibility))
		return err
	}
	insertTrigger := func(id, user, org, tenant string, noMachine bool) error {
		_, err := raw.Exec(
			`INSERT INTO triggers (id, user_id, project_id, name, kind, workflow, org_id, end_tenant_id, no_machine)
			 VALUES ($1, $2, 'test-project', $1, 'webhook', 'wf', $3, $4, $5)`,
			id, user, nullable(org), nullable(tenant), noMachine)
		return err
	}
	insertRun := func(id, chat, owner, org, tenant string) error {
		_, err := raw.Exec(
			`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, owner_user_id, org_id, end_tenant_id)
			 VALUES ($1, $2, 'wf', 'main', now(), $3, $4, $5)`,
			id, chat, nullable(owner), nullable(org), nullable(tenant))
		return err
	}
	refused := func(err error, constraint, why string) {
		t.Helper()
		require.Error(t, err, why)
		assert.Equal(t, constraint, orm.ConstraintName(err), why)
	}

	t.Run("end_tenants", func(t *testing.T) {
		_, err := raw.Exec(`INSERT INTO end_tenants (id, host_org_id, external_ref) VALUES ('et-dup', $1, 'cust-1')`, hostA)
		refused(err, "end_tenants_host_org_id_external_ref_key", "the host's external_ref is unique within the host")

		_, err = raw.Exec(`INSERT INTO end_tenants (id, host_org_id, external_ref, status) VALUES ('et-bad', $1, 'x', 'paused')`, hostA)
		refused(err, "end_tenants_status", "a status outside active/deleting is refused")

		_, err = raw.Exec(`INSERT INTO end_tenants (id, host_org_id, external_ref, status) VALUES ('et-del', $1, 'y', 'deleting')`, hostA)
		refused(err, "end_tenants_deleting_has_delete_after", "a deleting tenant names when its grace ends")

		_, err = raw.Exec(`INSERT INTO end_tenants (id, host_org_id, external_ref, delete_after) VALUES ('et-act', $1, 'z', now())`, hostA)
		refused(err, "end_tenants_deleting_has_delete_after", "an active tenant has no delete_after")

		_, err = raw.Exec(`INSERT INTO end_tenants (id, host_org_id, external_ref, status, delete_after) VALUES ('et-ok', $1, 'w', 'deleting', now() + interval '7 days')`, hostA)
		require.NoError(t, err)
	})

	t.Run("cross-host stamp is refused", func(t *testing.T) {
		refused(insertChat("c-x", hspA, hostB, tenA, true, ""), "chats_end_tenant_fk", "chat in host B stamped with host A's tenant")
		refused(insertTrigger("t-x", hspA, hostB, tenA, true), "triggers_end_tenant_fk", "trigger in host B stamped with host A's tenant")

		require.NoError(t, insertChat("c-own", hspA, hostA, tenA, true, ""))
		refused(insertRun("w-x", "c-own", hspA, hostB, tenA), "workflows_end_tenant_fk", "run in host B stamped with host A's tenant")
	})

	t.Run("NULL org stamp is refused", func(t *testing.T) {
		refused(insertChat("c-noorg", hspA, "", tenA, true, ""), "chats_end_tenant_has_org_check", "a stamped chat has an org")
		refused(insertTrigger("t-noorg", hspA, "", tenA, true), "triggers_end_tenant_has_org_check", "a stamped trigger has an org")
		// A run's org is filled from its chat's, so a stamped run only loses
		// its org by an update.
		require.NoError(t, insertChat("c-run", hspA, hostA, tenA, true, ""))
		require.NoError(t, insertRun("w-ok", "c-run", hspA, "", ""), "a run inherits the chat's stamp and org")
		_, err := raw.Exec(`UPDATE workflows SET org_id = NULL WHERE id = 'w-ok'`)
		refused(err, "workflows_end_tenant_has_org_check", "a stamped run cannot lose its org")
	})

	t.Run("hsp_ prefix and stamp go together", func(t *testing.T) {
		refused(insertChat("c-hsp-unstamped", hspA, hostA, "", true, ""), "chats_end_tenant_hsp_check", "hsp_ chat with no stamp")
		refused(insertChat("c-stamped-person", "person-1", hostA, tenA, true, ""), "chats_end_tenant_hsp_check", "stamped chat owned by a person")
		refused(insertTrigger("t-hsp-unstamped", hspA, hostA, "", true), "triggers_end_tenant_hsp_check", "hsp_ trigger with no stamp")
		refused(insertTrigger("t-stamped-person", "person-1", hostA, tenA, true), "triggers_end_tenant_hsp_check", "stamped trigger owned by a person")

		require.NoError(t, insertChat("c-hsp-ok", hspA, hostA, tenA, true, ""))
		require.NoError(t, insertTrigger("t-hsp-ok", hspA, hostA, tenA, true))

		// A person's own chat in the same org is untouched.
		require.NoError(t, insertChat("c-person", "person-1", hostA, "", false, "org"))
		require.NoError(t, insertTrigger("t-person", "person-1", hostA, "", false))
	})

	t.Run("a tenant never uses a machine", func(t *testing.T) {
		refused(insertChat("c-machine", hspA, hostA, tenA, false, ""), "chats_end_tenant_no_machine_check", "stamped chat with a machine")
		refused(insertTrigger("t-machine", hspA, hostA, tenA, false), "triggers_end_tenant_no_machine_check", "stamped trigger with a machine")
	})

	t.Run("a service principal's chat is never shared", func(t *testing.T) {
		refused(insertChat("c-shared", hspA, hostA, tenA, true, "org"), "chats_hsp_visibility_check", "hsp_ chat shared with the org")
	})

	t.Run("a run inherits its chat's stamp", func(t *testing.T) {
		require.NoError(t, insertChat("c-parent", hspA, hostA, tenA, true, ""))
		require.NoError(t, insertRun("w-inherit", "c-parent", hspA, "", ""))
		var tenant, org string
		require.NoError(t, raw.QueryRow(`SELECT end_tenant_id, org_id FROM workflows WHERE id = 'w-inherit'`).Scan(&tenant, &org))
		assert.Equal(t, tenA, tenant)
		assert.Equal(t, hostA, org)

		require.NoError(t, insertRun("w-agree", "c-parent", hspA, hostA, tenA), "an explicit stamp that agrees is kept")

		var tenantNil sql.NullString
		require.NoError(t, insertRun("w-person-chat", "c-person", "", "", ""))
		require.NoError(t, raw.QueryRow(`SELECT end_tenant_id FROM workflows WHERE id = 'w-person-chat'`).Scan(&tenantNil))
		assert.False(t, tenantNil.Valid, "a run in an unstamped chat stays unstamped")

		// A stamped chat's run owned by a person is refused by the owner CHECK.
		refused(insertRun("w-admin", "c-parent", "person-1", "", ""), "workflows_end_tenant_hsp_check", "a stamped run owned by a host admin")
	})

	t.Run("inherit trigger raises on disagreement", func(t *testing.T) {
		require.NoError(t, insertChat("c-b", "hsp_01HZZZZZZZZZZZZZZZZZZZZZZB", hostB, tenB, true, ""))
		// The run names tenant B's stamp in a chat of tenant A.
		err := insertRun("w-disagree", "c-parent", hspA, "", tenB)
		refused(err, "workflows_end_tenant_matches_chat", "explicit stamp differs from the chat's")
		// The run names a stamp in a chat that has none.
		err = insertRun("w-disagree-none", "c-person", "", "", tenA)
		refused(err, "workflows_end_tenant_matches_chat", "explicit stamp on a chat that has none")
	})

	t.Run("a stamp cannot be moved after the fact", func(t *testing.T) {
		const hspA2 = "hsp_01HZZZZZZZZZZZZZZZZZZZZZ2A"
		mustExec(`INSERT INTO end_tenants (id, host_org_id, external_ref) VALUES ('et-a2', $1, 'cust-2')`, hostA)
		require.NoError(t, insertChat("c-mv-a", hspA, hostA, tenA, true, ""))
		require.NoError(t, insertChat("c-mv-a2", hspA2, hostA, "et-a2", true, ""))
		require.NoError(t, insertChat("c-mv-person", "person-1", hostA, "", false, ""))
		require.NoError(t, insertRun("w-mv", "c-mv-a", hspA, "", ""))
		require.NoError(t, insertRun("w-mv-null-owner", "c-mv-a", "", "", ""))

		// 1. Re-stamping a chat after its runs exist (same host, other tenant).
		_, err := raw.Exec(`UPDATE chats SET end_tenant_id = 'et-a2', user_id = $2 WHERE id = $1`, "c-mv-a", hspA2)
		refused(err, "chats_end_tenant_immutable", "a chat's stamp cannot move to another tenant")
		// ... nor be cleared, nor set on a chat written without one.
		_, err = raw.Exec(`UPDATE chats SET end_tenant_id = NULL, user_id = 'person-1' WHERE id = 'c-mv-a'`)
		refused(err, "chats_end_tenant_immutable", "a chat's stamp cannot be cleared")
		_, err = raw.Exec(`UPDATE chats SET end_tenant_id = $1, user_id = $2, no_machine = true WHERE id = 'c-mv-person'`, tenA, hspA)
		refused(err, "chats_end_tenant_immutable", "an unstamped chat cannot be adopted into a tenant")

		// 2. NULLing a run's stamp when its owner is NULL and its chat is stamped.
		_, err = raw.Exec(`UPDATE workflows SET end_tenant_id = NULL WHERE id = 'w-mv-null-owner'`)
		refused(err, "workflows_end_tenant_matches_chat", "a run in a stamped chat cannot lose its stamp")

		// 3. Moving a run to another tenant's chat (same host).
		_, err = raw.Exec(`UPDATE workflows SET chat_id = 'c-mv-a2' WHERE id = 'w-mv'`)
		refused(err, "workflows_end_tenant_matches_chat", "a run cannot move to another tenant's chat")
		_, err = raw.Exec(`UPDATE workflows SET chat_id = 'c-mv-a2', end_tenant_id = 'et-a2' WHERE id = 'w-mv-null-owner'`)
		refused(err, "workflows_end_tenant_immutable", "a run cannot move to another tenant's chat even with that chat's stamp")

		// 4. Moving a run to an unstamped chat, and a stamp onto an unstamped chat's run.
		_, err = raw.Exec(`UPDATE workflows SET chat_id = 'c-mv-person' WHERE id = 'w-mv-null-owner'`)
		refused(err, "workflows_end_tenant_matches_chat", "a stamped run cannot move to an unstamped chat")
		require.NoError(t, insertRun("w-mv-person", "c-mv-person", "", "", ""))
		_, err = raw.Exec(`UPDATE workflows SET end_tenant_id = $1, org_id = $2 WHERE id = 'w-mv-person'`, tenA, hostA)
		refused(err, "workflows_end_tenant_matches_chat", "an unstamped chat's run cannot be stamped")

		// Nothing moved, and writes that do not touch the stamp still work.
		var tenant string
		require.NoError(t, raw.QueryRow(`SELECT end_tenant_id FROM chats WHERE id = 'c-mv-a'`).Scan(&tenant))
		assert.Equal(t, tenA, tenant)
		_, err = raw.Exec(`UPDATE chats SET title = 'renamed' WHERE id = 'c-mv-a'`)
		require.NoError(t, err)
		_, err = raw.Exec(`UPDATE chats SET end_tenant_id = $1 WHERE id = 'c-mv-a'`, tenA)
		require.NoError(t, err, "writing the same stamp is not a change")
		_, err = raw.Exec(`UPDATE workflows SET chat_id = 'c-mv-a', end_tenant_id = $1 WHERE id = 'w-mv'`, tenA)
		require.NoError(t, err, "a run re-pointed at the same stamp is fine")
		_, err = raw.Exec(`UPDATE workflows SET outcome = 'done' WHERE id = 'w-mv'`)
		require.NoError(t, err)
	})

	t.Run("a trigger's stamp is fixed at creation", func(t *testing.T) {
		const hspA2 = "hsp_01HZZZZZZZZZZZZZZZZZZZZZ2A"
		mustExec(`INSERT INTO end_tenants (id, host_org_id, external_ref) VALUES ('et-a3', $1, 'cust-3') ON CONFLICT DO NOTHING`, hostA)
		require.NoError(t, insertTrigger("t-fix", hspA2, hostA, "et-a3", true))
		require.NoError(t, insertTrigger("t-fix-person", "person-1", hostA, "", true))

		_, err := raw.Exec(`UPDATE triggers SET end_tenant_id = $1 WHERE id = 't-fix'`, tenA)
		refused(err, "triggers_end_tenant_immutable", "a trigger's stamp cannot move to another tenant")
		_, err = raw.Exec(`UPDATE triggers SET end_tenant_id = NULL, user_id = 'person-1' WHERE id = 't-fix'`)
		refused(err, "triggers_end_tenant_immutable", "a trigger's stamp cannot be cleared")
		_, err = raw.Exec(`UPDATE triggers SET end_tenant_id = $1, user_id = $2 WHERE id = 't-fix-person'`, tenA, hspA)
		refused(err, "triggers_end_tenant_immutable", "an unstamped trigger cannot be adopted into a tenant")

		_, err = raw.Exec(`UPDATE triggers SET name = 'renamed', end_tenant_id = 'et-a3' WHERE id = 't-fix'`)
		require.NoError(t, err, "writing the same stamp is not a change")
	})

	t.Run("every constraint is validated", func(t *testing.T) {
		var unvalidated int
		require.NoError(t, raw.QueryRow(
			`SELECT count(*) FROM pg_constraint
			 WHERE NOT convalidated
			   AND conrelid IN ('chats'::regclass, 'workflows'::regclass, 'triggers'::regclass,
			                    'connection_events'::regclass, 'integration_writes'::regclass, 'llm_call_usage'::regclass)
			   AND (conname LIKE '%end_tenant%' OR conname IN ('chats_hsp_visibility_check', 'integration_writes_org_id_not_blank'))`).
			Scan(&unvalidated))
		assert.Zero(t, unvalidated, "end_tenants_run_stamps_validate validated every NOT VALID constraint it added")
	})
}

// TestEndTenantColumnsAreInvisibleToThePreviousRelease inserts rows the way
// the previous release does, naming none of the new columns, and reads them
// back: nothing it writes trips a new constraint, and every new column is NULL.
func TestEndTenantColumnsAreInvisibleToThePreviousRelease(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	exec := func(q string, args ...any) {
		t.Helper()
		_, err := raw.Exec(q, args...)
		require.NoError(t, err, q)
	}

	// A person's chat and trigger, with and without an org, shared and not.
	exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active, org_id, visibility)
	      VALUES ('c1', 't', 'test-project', 'person-1', now(), now(), now(), 'org-1', 'org')`)
	exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
	      VALUES ('c2', 't', 'test-project', 'person-2', now(), now(), now())`)
	exec(`INSERT INTO triggers (id, user_id, project_id, name, kind, workflow, no_machine)
	      VALUES ('t1', 'person-1', 'test-project', 't1', 'webhook', 'wf', true)`)
	exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, owner_user_id)
	      VALUES ('w1', 'c1', 'wf', 'main', now(), 'person-1')`)
	exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at)
	      VALUES ('w2', 'c2', 'wf', 'main', now())`)
	exec(`INSERT INTO connection_events (connection_id, user_id, kind, actor) VALUES ('conn-1', 'person-1', 'use', 'owner')`)
	exec(`INSERT INTO integration_writes (id, user_id, integration, action, scope, target, status, created_at)
	      VALUES ('iw1', 'person-1', 'github', 'comment', 's', 'tg', 'intent', now())`)
	exec(`INSERT INTO llm_call_usage (id, user_id, chat_id, provider, model, auth_mode, purpose, outcome, attempt)
	      VALUES ('u1', 'person-1', 'c1', 'anthropic', 'm', 'managed', 'chat', 'success', 1)`)

	for _, tc := range []struct{ table, where string }{
		{"chats", "id IN ('c1','c2')"},
		{"triggers", "id = 't1'"},
		{"workflows", "id IN ('w1','w2')"},
		{"connection_events", "connection_id = 'conn-1'"},
		{"integration_writes", "id = 'iw1'"},
		{"llm_call_usage", "id = 'u1'"},
	} {
		var stamped int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM `+tc.table+` WHERE `+tc.where+` AND end_tenant_id IS NOT NULL`).Scan(&stamped), tc.table)
		assert.Zero(t, stamped, "%s: a row the previous release wrote is unstamped", tc.table)
	}
	var org sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT org_id FROM integration_writes WHERE id = 'iw1'`).Scan(&org))
	assert.False(t, org.Valid, "integration_writes.org_id is NULL for a write that names none")
}
