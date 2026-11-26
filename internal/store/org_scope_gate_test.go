// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// holdOrgLock takes user's org lock in a transaction of its own, as an
// org-sensitive write does, and holds it until release is called.
func holdOrgLock(t *testing.T, env *storetest.Env, user string) (release func()) {
	t.Helper()
	ctx := context.Background()
	locked, unlock := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- env.Repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			if err := env.Repo.LockUserOrgs(ctx, user); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("the write ended before it held the org lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the write never took the org lock")
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		close(unlock)
		if err := <-done; err != nil {
			t.Errorf("the write holding the org lock: %v", err)
		}
	}
	t.Cleanup(release)
	return release
}

// advisoryWaiters counts the sessions of this test's database queued on an
// advisory lock.
func advisoryWaiters(t *testing.T, env *storetest.Env) int {
	t.Helper()
	var n int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND NOT l.granted AND a.datname = current_database()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A list's backfill pass with nothing to stamp must not take the owner's
// exclusive org lock: it returns while a write holds it. Rows the
// backfill can never stamp (a project whose path the default org already
// holds, a chat waiting on that project) do not count as something to stamp.
// A row written after such a pass is stamped by the next one, which takes the
// lock; and a row written while that pass waits for the lock is stamped too,
// because the batch reads its rows under the lock, not at the check.
func TestAssignUnassignedOrg_TakesTheOrgLockOnlyWhenARowNeedsAnOrg(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	const user = "u-backfill-gate"

	for _, p := range []*core.Project{
		{ID: "p-home", Name: "home", Path: "/srv/app", OrgID: orgp("home")},
		{ID: "p-other", Name: "other", Path: "/srv/app", OrgID: orgp("other")},
		// Created with no org at a path either default already holds: the
		// backfill leaves it unassigned rather than stamp it into a collision.
		{ID: "p-stuck", Name: "stuck", Path: "/srv/app"},
	} {
		p.UserID, p.CreatedAt, p.UpdatedAt, p.LastActive = user, at, at, at
		if err := env.Repo.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", p.ID, err)
		}
	}
	insertChat := func(id, project string, org *string) {
		t.Helper()
		c := &core.Chat{ID: id, ProjectID: project, OrgID: org, UserID: user, Title: id, CreatedAt: at, UpdatedAt: at, LastActive: at}
		if err := store.InsertChat(ctx, env.ORM, c); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}
	insertChat("c-home", "p-home", orgp("home"))
	insertChat("c-stuck", "p-stuck", nil) // waits for its project, which never gets an org
	orgOf := func(table, id string) *string {
		t.Helper()
		var org *string
		if err := env.SQL.QueryRow(`SELECT org_id FROM `+table+` WHERE id = $1`, id).Scan(&org); err != nil {
			t.Fatalf("org of %s %s: %v", table, id, err)
		}
		return org
	}

	release := holdOrgLock(t, env, user)

	// Nothing to stamp: the pass returns while the write holds the lock. Had
	// it taken the lock, it would wait out the deadline.
	quick, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := env.Repo.AssignUnassignedOrg(quick, user, "home"); err != nil {
		t.Fatalf("AssignUnassignedOrg with nothing to stamp: %v (it took the org lock)", err)
	}
	if org := orgOf("projects", "p-stuck"); org != nil {
		t.Fatalf("a project colliding with home's was stamped %q", *org)
	}

	// A chat created with no org after that pass: two concurrent backfills
	// both observe it and queue on the lock before either can stamp it.
	insertChat("c-new", "p-home", nil)
	done := make(chan error, 2)
	for _, def := range []string{"home", "other"} {
		go func() { done <- env.Repo.AssignUnassignedOrg(ctx, user, def) }()
	}
	deadline := time.Now().Add(10 * time.Second)
	for advisoryWaiters(t, env) < 2 {
		select {
		case err := <-done:
			t.Fatalf("a pass with a chat to stamp ended without waiting for the org lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("both passes did not queue on the org lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// These rows are committed after both probes but before either lock.
	// The project-only chat uses its project's org regardless of which
	// pass locks first. The draft gets exactly one of the two defaults.
	insertChat("c-late", "p-home", nil)
	if _, err := env.SQL.Exec(`INSERT INTO workflow_drafts (id, user_id, name, slug, definition, is_hidden, version, created_at, updated_at)
		VALUES ('d-late', $1, 'Late', 'late', 'name: late', false, 1, $2, $2)`, user, at); err != nil {
		t.Fatal(err)
	}
	if org := orgOf("chats", "c-new"); org != nil {
		t.Fatalf("stamped %q while a write held the org lock", *org)
	}
	release()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("AssignUnassignedOrg: %v", err)
		}
	}
	for _, id := range []string{"c-new", "c-late"} {
		if org := orgOf("chats", id); org == nil || *org != "home" {
			t.Fatalf("chat %s org = %v, want its project's (home)", id, org)
		}
	}
	if org := orgOf("workflow_drafts", "d-late"); org == nil || (*org != "home" && *org != "other") {
		t.Fatalf("draft org = %v, want one of the two defaults", org)
	}
	if org := orgOf("chats", "c-stuck"); org != nil {
		t.Fatalf("a chat in an unassigned project was stamped %q", *org)
	}

	// A run with no org in a chat that has one is found by the check on its
	// own, with nothing else to stamp.
	convWorkflow(t, env, "w-legacy", "c-home", core.WorkflowStateActive, core.StopReasonUnspecified)
	if _, err := env.SQL.Exec(`UPDATE workflows SET org_id = NULL WHERE id = 'w-legacy'`); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.AssignUnassignedOrg(ctx, user, "home"); err != nil {
		t.Fatalf("AssignUnassignedOrg: %v", err)
	}
	if org := orgOf("workflows", "w-legacy"); org == nil || *org != "home" {
		t.Fatalf("legacy run org = %v, want its chat's (home)", org)
	}
}
