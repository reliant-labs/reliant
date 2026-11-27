// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestRunMachinePlacements runs the placement record and the live-run counts
// over it against Postgres: a member's live runs are the distinct chats the
// pool placed there (a trigger's) or with a live run placement there (a
// workflow's), and a labels read counts every live run on the machine.
func TestRunMachinePlacements(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

	labels := map[string]map[string]string{
		"rp-a": {"pool": "gate", "role": "gate"},
		"rp-b": {"pool": "gate", "role": "gate"},
		"rp-c": {"role": "gate"},
	}
	for _, id := range []string{"rp-a", "rp-b", "rp-c"} {
		host := id
		if err := env.Repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: storetest.SeedUserID, Hostname: &host}); err != nil {
			t.Fatalf("UpsertDaemon %s: %v", id, err)
		}
		if ok, err := store.SetDaemonLabels(ctx, env.ORM, storetest.SeedUserID, id, labels[id]); err != nil || !ok {
			t.Fatalf("SetDaemonLabels %s = %v, %v", id, ok, err)
		}
	}
	pool, err := store.PutMachinePool(ctx, env.ORM, &core.MachinePool{UserID: storetest.SeedUserID, Name: "gate", Selector: map[string]string{"pool": "gate"}, MaxRunsPerMember: 5}, uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PutMachinePool: %v", err)
	}

	daemonA, poolID := "rp-a", pool.ID
	mkChat := func(id string, triggerPlaced bool, status core.WorkflowStatus) {
		t.Helper()
		chat := &db.Chat{ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID, ActiveDaemonID: &daemonA,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now}
		if triggerPlaced {
			chat.MachinePoolID = &poolID
		}
		if err := env.Repo.CreateChat(ctx, chat); err != nil {
			t.Fatalf("CreateChat %s: %v", id, err)
		}
		if err := env.Repo.CreateWorkflow(ctx, &db.Workflow{ID: id, ChatID: id, WorkflowName: "gate", Thread: id, Status: status, CreatedAt: now}); err != nil {
			t.Fatalf("CreateWorkflow %s: %v", id, err)
		}
	}
	place := func(chat, scope, pool, daemon string) *core.RunMachinePlacement {
		t.Helper()
		got, err := store.RecordRunMachinePlacement(ctx, env.ORM, &core.RunMachinePlacement{WorkflowID: chat, Scope: scope, UserID: storetest.SeedUserID,
			ChatID: chat, MachinePoolID: pool, Selector: map[string]string{"pool": "gate"}, DaemonID: daemon})
		if err != nil {
			t.Fatalf("RecordRunMachinePlacement %s/%q: %v", chat, scope, err)
		}
		return got
	}

	// On a: a trigger-placed live chat whose run also recorded its member
	// (one chat), a workflow-placed live run, a live run with two placed
	// nodes (one chat), a finished placed run, and a live placement on no
	// pool (its pool deleted), which no pool counts.
	mkChat("rp-trigger", true, core.Active())
	place("rp-trigger", "", poolID, "rp-a")
	mkChat("rp-run", false, core.Paused())
	place("rp-run", "", poolID, "rp-a")
	mkChat("rp-nodes", false, core.Active())
	place("rp-nodes", "build", poolID, "rp-a")
	place("rp-nodes", "test", poolID, "rp-a")
	mkChat("rp-done", false, core.Completed())
	place("rp-done", "", poolID, "rp-a")
	mkChat("rp-labels", false, core.Active())
	place("rp-labels", "", "", "rp-a")

	tests := []struct {
		name string
		got  func() (int, error)
		want int
	}{
		{"the pool's live runs on a: distinct live chats, the trigger's and the workflows'", func() (int, error) {
			return store.CountMachinePoolMemberLiveRuns(ctx, env.ORM, pool.ID, "rp-a")
		}, 3},
		{"none on b", func() (int, error) { return store.CountMachinePoolMemberLiveRuns(ctx, env.ORM, pool.ID, "rp-b") }, 0},
		{"pool members carry the same count", func() (int, error) {
			ms, err := store.ListMachinePoolMembers(ctx, env.ORM, pool, storetest.SeedProjectID)
			return liveRunsOf(ms, "rp-a"), err
		}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.got()
			if err != nil || got != tt.want {
				t.Fatalf("got %d, %v; want %d", got, err, tt.want)
			}
		})
	}

	// The members a chat already holds a live run on, which placing it again
	// takes no extra slot of: its trigger's member while its root run is
	// live, and its live placements' members; never a finished run's.
	for _, tt := range []struct {
		chat string
		want []string
	}{
		{"rp-trigger", []string{"rp-a"}},
		{"rp-nodes", []string{"rp-a"}},
		{"rp-run", []string{"rp-a"}},
		{"rp-done", nil},
		{"rp-labels", nil},
		{"rp-missing", nil},
	} {
		got, err := store.ListChatHeldPoolMembers(ctx, env.ORM, pool.ID, tt.chat)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Fatalf("ListChatHeldPoolMembers(%s) = %v, %v; want %v", tt.chat, got, err, tt.want)
		}
	}

	// A placement, once recorded, never moves: a second record returns it.
	if again := place("rp-run", "", poolID, "rp-b"); again.DaemonID != "rp-a" || again.MachinePoolID != pool.ID || again.Selector["pool"] != "gate" {
		t.Fatalf("re-record = %+v, want the first placement on a", again)
	}
	if got, err := store.GetRunMachinePlacement(ctx, env.ORM, "rp-nodes", "test"); err != nil || got.DaemonID != "rp-a" || got.ChatID != "rp-nodes" {
		t.Fatalf("GetRunMachinePlacement = %+v, %v", got, err)
	}
	if _, err := store.GetRunMachinePlacement(ctx, env.ORM, "rp-nodes", "deploy"); !errors.Is(err, core.ErrRunMachinePlacementNotFound) {
		t.Fatalf("an unplaced node = %v, want not found", err)
	}

	// The one move: a node's preflight failed on its member, so the
	// placement goes to another, and its slot with it. A move from a member
	// it is no longer on (a concurrent move won) answers where it is; a move
	// of nothing recorded records it.
	move := func(chat, scope, from, to string) *core.RunMachinePlacement {
		t.Helper()
		got, err := store.MoveRunMachinePlacement(ctx, env.ORM, &core.RunMachinePlacement{WorkflowID: chat, Scope: scope, UserID: storetest.SeedUserID,
			ChatID: chat, MachinePoolID: poolID, Selector: map[string]string{"pool": "gate"}, DaemonID: to}, from)
		if err != nil {
			t.Fatalf("MoveRunMachinePlacement %s/%q: %v", chat, scope, err)
		}
		return got
	}
	if got := move("rp-nodes", "test", "rp-a", "rp-b"); got.DaemonID != "rp-b" || got.MachinePoolID != pool.ID {
		t.Fatalf("move = %+v, want the placement on b", got)
	}
	if got := move("rp-nodes", "test", "rp-a", "rp-c"); got.DaemonID != "rp-b" {
		t.Fatalf("a stale move = %+v, want the placement left on b", got)
	}
	if n, err := store.CountMachinePoolMemberLiveRuns(ctx, env.ORM, pool.ID, "rp-b"); err != nil || n != 1 {
		t.Fatalf("live runs on b after the move = %d, %v; want 1", n, err)
	}
	if got := move("rp-nodes", "lint", "rp-a", "rp-b"); got.DaemonID != "rp-b" || got.Scope != "lint" {
		t.Fatalf("a move of nothing recorded = %+v, want it recorded on b", got)
	}

	// The chat read placement makes: owner, project, trigger placement.
	chat, err := store.GetRunPlacementChat(ctx, env.ORM, "rp-trigger")
	if err != nil || chat.UserID != storetest.SeedUserID || chat.ProjectID != storetest.SeedProjectID || chat.MachinePoolID != pool.ID || chat.ActiveDaemonID != "rp-a" {
		t.Fatalf("GetRunPlacementChat = %+v, %v", chat, err)
	}
	if chat, err := store.GetRunPlacementChat(ctx, env.ORM, "rp-run"); err != nil || chat.MachinePoolID != "" {
		t.Fatalf("an unplaced chat = %+v, %v; want no pool", chat, err)
	}
	if _, err := store.GetRunPlacementChat(ctx, env.ORM, "rp-missing"); !errors.Is(err, core.ErrChatNotFound) {
		t.Fatalf("a missing chat = %v, want not found", err)
	}

	// Tenant isolation: placement reads the pool and its members by the
	// chat's owner. Another user's pool of the same name is not this one,
	// and another user's machine carrying the pool's labels is never a
	// member of it.
	const otherUser, otherHost = "rp-other-user", "rp-x"
	if err := env.Repo.UpsertDaemon(ctx, &db.Daemon{ID: otherHost, UserID: otherUser, Hostname: new(string)}); err != nil {
		t.Fatalf("UpsertDaemon %s: %v", otherHost, err)
	}
	if ok, err := store.SetDaemonLabels(ctx, env.ORM, otherUser, otherHost, map[string]string{"pool": "gate"}); err != nil || !ok {
		t.Fatalf("SetDaemonLabels %s = %v, %v", otherHost, ok, err)
	}
	if ok, err := store.SetDaemonLabels(ctx, env.ORM, storetest.SeedUserID, otherHost, map[string]string{"pool": "gate"}); err != nil || ok {
		t.Fatalf("labelling another user's machine = %v, %v; want refused", ok, err)
	}
	if _, err := store.GetMachinePoolByName(ctx, env.ORM, otherUser, "gate"); !errors.Is(err, core.ErrMachinePoolNotFound) {
		t.Fatalf("another user's lookup of the pool = %v, want not found", err)
	}
	members, err := store.ListMachinePoolMembers(ctx, env.ORM, pool, storetest.SeedProjectID)
	if err != nil {
		t.Fatalf("ListMachinePoolMembers: %v", err)
	}
	for _, m := range members {
		if m.DaemonID == otherHost {
			t.Fatalf("another user's machine %s is a member of the pool: %+v", otherHost, members)
		}
	}

	// Deleting the pool keeps the placements, naming no pool.
	if _, err := env.ORM.Bun().NewRaw(`UPDATE chats SET machine_pool_id = NULL WHERE machine_pool_id = ?0`, pool.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMachinePoolByName(ctx, env.ORM, storetest.SeedUserID, "gate"); err != nil {
		t.Fatalf("DeleteMachinePoolByName: %v", err)
	}
	if got, err := store.GetRunMachinePlacement(ctx, env.ORM, "rp-run", ""); err != nil || got.MachinePoolID != "" || got.DaemonID != "rp-a" {
		t.Fatalf("placement after its pool was deleted = %+v, %v", got, err)
	}
}

func liveRunsOf(ms []core.MachinePoolMemberRow, daemonID string) int {
	for _, m := range ms {
		if m.DaemonID == daemonID {
			return m.LiveRuns
		}
	}
	return -1
}
