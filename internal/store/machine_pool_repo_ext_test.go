// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestMachinePoolQueries runs the pool reads placement depends on against
// Postgres: membership by durable label, phase, install, the synced catalog,
// and live runs counted from pool-placed chats only.
func TestMachinePoolQueries(t *testing.T) {
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
		if ok, err := env.Repo.SetDaemonLabels(ctx, storetest.SeedUserID, id, map[string]string{"pool": "cicd", "size": "2xl"}); err != nil || !ok {
			t.Fatalf("SetDaemonLabels %s = %v, %v", id, ok, err)
		}
	}
	if ok, _ := env.Repo.SetDaemonLabels(ctx, "someone-else", "mp-a", map[string]string{}); ok {
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

	pool, err := env.Repo.PutMachinePool(ctx, &core.MachinePool{UserID: storetest.SeedUserID, Name: "cicd", Selector: map[string]string{"pool": "cicd"}, MaxRunsPerMember: 3})
	if err != nil {
		t.Fatalf("PutMachinePool: %v", err)
	}
	again, err := env.Repo.PutMachinePool(ctx, &core.MachinePool{UserID: storetest.SeedUserID, Name: "cicd", Selector: map[string]string{"pool": "cicd"}, MaxRunsPerMember: 4})
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

	n, err := env.Repo.CountMachinePoolMemberLiveRuns(ctx, pool.ID, "mp-a")
	if err != nil || n != 2 {
		t.Fatalf("live runs on a = %d, %v; want 2", n, err)
	}

	members, err := env.Repo.ListMachinePoolMembers(ctx, again, storetest.SeedProjectID)
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

	// A pool a trigger runs on cannot be deleted; an unused one can.
	trg := newStoreTrigger("trg-pool", "pooled", now)
	trg.DaemonID, trg.MachinePoolID = "", pool.ID
	if err := store.InsertTrigger(ctx, env.ORM, trg); err != nil {
		t.Fatalf("InsertTrigger: %v", err)
	}
	if got, err := env.Repo.GetTrigger(ctx, "trg-pool"); err != nil || got.MachinePoolID != pool.ID || got.MachinePoolName != "cicd" || got.MachineState != "pool" {
		t.Fatalf("pool trigger read back = %+v, %v", got, err)
	}
	if err := env.Repo.DeleteMachinePoolByName(ctx, storetest.SeedUserID, "cicd"); !errors.Is(err, core.ErrMachinePoolInUse) {
		t.Fatalf("delete a used pool = %v, want in use", err)
	}
	// The CHECK refuses a trigger naming a pool and a daemon.
	bad := newStoreTrigger("trg-both", "both", now)
	bad.MachinePoolID = pool.ID
	if err := store.InsertTrigger(ctx, env.ORM, bad); err == nil {
		t.Fatal("a trigger naming both a daemon and a pool was stored")
	}
	if err := env.Repo.DeleteMachinePoolByName(ctx, storetest.SeedUserID, "nope"); !errors.Is(err, core.ErrMachinePoolNotFound) {
		t.Fatalf("delete missing = %v", err)
	}
}
