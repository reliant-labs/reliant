// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestMachinePoolQueries runs the pool reads placement depends on against
// Postgres: membership by durable label, phase, install, the synced catalog,
// and live runs counted from pool-placed chats only.
func TestMachinePoolQueries(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, id := range []string{"mp-a", "mp-b", "mp-other"} {
		host := id
		if err := env.Repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: storetest.SeedUserID, Hostname: &host}); err != nil {
			t.Fatalf("UpsertDaemon %s: %v", id, err)
		}
	}
	for _, id := range []string{"mp-a", "mp-b"} {
		if ok, err := store.SetDaemonLabels(ctx, env.ORM, storetest.SeedUserID, id, map[string]string{"pool": "cicd", "size": "2xl"}); err != nil || !ok {
			t.Fatalf("SetDaemonLabels %s = %v, %v", id, ok, err)
		}
	}
	if ok, _ := store.SetDaemonLabels(ctx, env.ORM, "someone-else", "mp-a", map[string]string{}); ok {
		t.Fatal("another user relabelled the machine")
	}
	// Labels survive a re-registration (the upsert names its columns).
	host := "mp-a-renamed"
	if err := env.Repo.UpsertDaemon(ctx, &db.Daemon{ID: "mp-a", UserID: storetest.SeedUserID, Hostname: &host}); err != nil {
		t.Fatal(err)
	}
	if d, err := env.Repo.GetDaemon(ctx, "mp-a"); err != nil || d.Labels["pool"] != "cicd" {
		t.Fatalf("labels after re-registration = %v, %v", d, err)
	}

	pool, err := store.PutMachinePool(ctx, env.ORM, &core.MachinePool{UserID: storetest.SeedUserID, Name: "cicd", Selector: map[string]string{"pool": "cicd"}, MaxRunsPerMember: 3}, uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PutMachinePool: %v", err)
	}
	again, err := store.PutMachinePool(ctx, env.ORM, &core.MachinePool{UserID: storetest.SeedUserID, Name: "cicd", Selector: map[string]string{"pool": "cicd"}, MaxRunsPerMember: 4}, uuid.NewString(), time.Now().UTC())
	if err != nil || again.ID != pool.ID || again.MaxRunsPerMember != 4 {
		t.Fatalf("re-put = %+v, %v; want the same id, updated", again, err)
	}

	if err := env.Repo.UpsertProjectDaemon(ctx, storetest.SeedProjectID, "mp-a", "/srv/a", nil); err != nil {
		t.Fatal(err)
	}

	// Two live pool-placed chats on a, one finished; one live chat on a the
	// pool did not place (it must not count).
	poolID := pool.ID
	mkChat := func(id string, placed bool, status core.WorkflowStatus) {
		daemon := "mp-a"
		chat := &db.Chat{ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID, ActiveDaemonID: &daemon,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now}
		if placed {
			chat.MachinePoolID = &poolID
		}
		if err := env.Repo.CreateChat(ctx, chat); err != nil {
			t.Fatalf("CreateChat %s: %v", id, err)
		}
		if err := env.Repo.CreateWorkflow(ctx, &db.Workflow{ID: id, ChatID: id, WorkflowName: "pr-review", Thread: id, Status: status, CreatedAt: now}); err != nil {
			t.Fatalf("CreateWorkflow %s: %v", id, err)
		}
	}
	mkChat("mp-live-1", true, core.Active())
	mkChat("mp-live-2", true, core.Paused())
	mkChat("mp-done", true, core.Completed())
	mkChat("mp-unpooled", false, core.Active())

	n, err := store.CountMachinePoolMemberLiveRuns(ctx, env.ORM, pool.ID, "mp-a")
	if err != nil || n != 2 {
		t.Fatalf("live runs on a = %d, %v; want 2", n, err)
	}

	// Held capacity: one admitted, launched, unreleased pr-review slot on a
	// (chat mp-live-1); one of another workflow; one released.
	scope := store.TriggerCapacityScope{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, WorkflowRef: "project:pr-review"}
	other := scope
	other.WorkflowRef = "project:cp-merge"
	for i, c := range []struct {
		chat     string
		scope    store.TriggerCapacityScope
		released bool
	}{{"mp-live-1", scope, false}, {"mp-live-2", other, false}, {"mp-done", scope, true}} {
		ev := &core.TriggerEvent{ID: "cap-" + c.chat, UserID: storetest.SeedUserID, Kind: core.TriggerEventKindSchedule,
			DedupeKey: "cap-" + c.chat, OccurredAt: now, CreatedAt: now, Outcome: core.TriggerEventPending}
		if err := store.EnqueueTriggerCapacity(ctx, env.ORM, ev, c.scope); err != nil {
			t.Fatalf("EnqueueTriggerCapacity %d: %v", i, err)
		}
		if _, err := env.ORM.Bun().NewRaw(`UPDATE trigger_events SET outcome = 'launched', chat_id = ?0, capacity_released = ?1 WHERE id = ?2`,
			c.chat, c.released, ev.ID).Exec(ctx); err != nil {
			t.Fatalf("mark %d launched: %v", i, err)
		}
	}
	held, err := store.CountTriggerCapacityHeldByMember(ctx, env.ORM, pool.ID, scope.UserID, scope.ProjectID, scope.WorkflowRef)
	if err != nil || len(held) != 1 || held["mp-a"] != 1 {
		t.Fatalf("held = %v, %v; want only pr-review's one unreleased slot on a", held, err)
	}
	if again.MemberIdleTimeout != core.DefaultMemberIdleTimeout {
		t.Fatalf("member idle timeout = %q, want the 10m default", again.MemberIdleTimeout)
	}

	members, err := store.ListMachinePoolMembers(ctx, env.ORM, again, storetest.SeedProjectID)
	if err != nil {
		t.Fatalf("ListMachinePoolMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %+v, want a and b (not the unlabelled machine)", members)
	}
	byID := map[string]core.MachinePoolMemberRow{}
	for _, m := range members {
		byID[m.DaemonID] = m
	}
	if a := byID["mp-a"]; !a.Installed || a.LiveRuns != 2 || a.Phase != "disconnected" {
		t.Fatalf("a = %+v, want installed, 2 live runs, disconnected (no lease)", a)
	}
	if b := byID["mp-b"]; b.Installed || b.LiveRuns != 0 {
		t.Fatalf("b = %+v, want not installed, no runs", b)
	}

	// Verified: a's checkout was seen (UpsertProjectDaemon, above) since its
	// current connection began, and stops being so the moment a newer
	// connection begins, until the connect-time reconcile sees it again.
	memberA := func() core.MachinePoolMemberRow {
		t.Helper()
		ms, err := store.ListMachinePoolMembers(ctx, env.ORM, again, storetest.SeedProjectID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.DaemonID == "mp-a" {
				return m
			}
		}
		t.Fatal("a is not a member")
		return core.MachinePoolMemberRow{}
	}
	if _, err := store.UpsertDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "mp-a", UserID: storetest.SeedUserID,
		Source: core.DaemonAttachmentSourceInbound, AttachedAt: now.Add(-time.Hour), LastStreamActivity: time.Now()}); err != nil {
		t.Fatalf("attach a: %v", err)
	}
	if a := memberA(); a.Phase != "connected" || !a.Verified {
		t.Fatalf("a = %+v, want connected and verified", a)
	}
	if _, err := env.ORM.Bun().NewRaw(`UPDATE daemon_attachment SET attached_at = NOW() WHERE daemon_id = 'mp-a'`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if a := memberA(); a.Phase != "connected" || !a.Installed || a.Verified {
		t.Fatalf("a after a new connection = %+v, want installed but not verified", a)
	}
	if err := env.Repo.UpsertProjectDaemon(ctx, storetest.SeedProjectID, "mp-a", "/srv/a", nil); err != nil {
		t.Fatal(err)
	}
	if a := memberA(); !a.Verified {
		t.Fatalf("a after the reconcile saw its checkout = %+v, want verified", a)
	}

	// A pool a trigger runs on cannot be deleted; an unused one can.
	trg := newStoreTrigger("trg-pool", "pooled", now)
	trg.DaemonID, trg.MachinePoolID = "", pool.ID
	if err := store.InsertTrigger(ctx, env.ORM, trg); err != nil {
		t.Fatalf("InsertTrigger: %v", err)
	}
	if got, err := store.GetTrigger(ctx, env.Repo.ORM(), "trg-pool"); err != nil || got.MachinePoolID != pool.ID || got.MachinePoolName != "cicd" || got.MachineState != "pool" {
		t.Fatalf("pool trigger read back = %+v, %v", got, err)
	}
	if err := store.DeleteMachinePoolByName(ctx, env.ORM, storetest.SeedUserID, "cicd"); !errors.Is(err, core.ErrMachinePoolInUse) {
		t.Fatalf("delete a used pool = %v, want in use", err)
	}
	// The CHECK refuses a trigger naming a pool and a daemon.
	bad := newStoreTrigger("trg-both", "both", now)
	bad.MachinePoolID = pool.ID
	if err := store.InsertTrigger(ctx, env.ORM, bad); err == nil {
		t.Fatal("a trigger naming both a daemon and a pool was stored")
	}
	if err := store.DeleteMachinePoolByName(ctx, env.ORM, storetest.SeedUserID, "nope"); !errors.Is(err, core.ErrMachinePoolNotFound) {
		t.Fatalf("delete missing = %v", err)
	}
}

// TestMergeDeclaredDaemonLabels pins the guard on the labels a daemon
// declares at registration: merged into its own row only, never another
// user's or (for an unbound credential) a reserved one; a no-op when already
// applied; refused past the label limit.
func TestMergeDeclaredDaemonLabels(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	for _, d := range []*db.Daemon{
		{ID: "dl-box", UserID: storetest.SeedUserID},
		{ID: "dl-pod", UserID: storetest.SeedUserID, RequiresBoundToken: true},
	} {
		if err := env.Repo.UpsertDaemon(ctx, d); err != nil {
			t.Fatalf("UpsertDaemon %s: %v", d.ID, err)
		}
	}
	labels := func(id string) map[string]string {
		t.Helper()
		d, err := env.Repo.GetDaemon(ctx, id)
		if err != nil {
			t.Fatalf("GetDaemon %s: %v", id, err)
		}
		return d.Labels
	}
	merge := func(userID, id string, bound bool, l map[string]string, max int) bool {
		t.Helper()
		ok, err := store.MergeDeclaredDaemonLabels(ctx, env.ORM, userID, id, bound, l, max)
		if err != nil {
			t.Fatalf("MergeDeclaredDaemonLabels %s: %v", id, err)
		}
		return ok
	}

	if ok, _ := store.SetDaemonLabels(ctx, env.ORM, storetest.SeedUserID, "dl-box", map[string]string{"team": "ci"}); !ok {
		t.Fatal("SetDaemonLabels dl-box wrote nothing")
	}
	if !merge(storetest.SeedUserID, "dl-box", false, map[string]string{"pool": "pipeline"}, 32) {
		t.Fatal("the owner's merge wrote nothing")
	}
	if got := labels("dl-box"); len(got) != 2 || got["team"] != "ci" || got["pool"] != "pipeline" {
		t.Fatalf("labels after merge = %v, want team=ci,pool=pipeline", got)
	}
	if merge(storetest.SeedUserID, "dl-box", false, map[string]string{"pool": "pipeline"}, 32) {
		t.Fatal("a merge already applied wrote again")
	}
	if merge("someone-else", "dl-box", true, map[string]string{"pool": "evil"}, 32) {
		t.Fatal("another user's credential relabelled the machine")
	}
	if merge(storetest.SeedUserID, "dl-box", false, map[string]string{"pool": "gate"}, 2) != true {
		t.Fatal("a merge within the limit (overwriting a key) was refused")
	}
	if merge(storetest.SeedUserID, "dl-box", false, map[string]string{"extra": "x"}, 2) {
		t.Fatal("a merge past the label limit was written")
	}
	if got := labels("dl-box"); len(got) != 2 || got["pool"] != "gate" {
		t.Fatalf("labels = %v, want team=ci,pool=gate", got)
	}

	if merge(storetest.SeedUserID, "dl-pod", false, map[string]string{"pool": "pipeline"}, 32) {
		t.Fatal("an unbound credential labelled a reserved machine")
	}
	if !merge(storetest.SeedUserID, "dl-pod", true, map[string]string{"pool": "pipeline"}, 32) {
		t.Fatal("a bound credential could not label its own reserved machine")
	}
	if merge(storetest.SeedUserID, "dl-box", false, nil, 32) {
		t.Fatal("declaring nothing wrote")
	}
}
