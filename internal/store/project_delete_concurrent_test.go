// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// A chat created after DeleteUserProject's batch loops passed it — and its
// chat_updates, which no foreign key cascades — must not be left to the
// project row's cascade: the final transaction sees it and sends the delete
// back through the loops, so nothing of the chat survives the project.
//
// Not parallel: it installs DeleteUserProject's package-wide hook (keyed by
// project id, so other tests' deletes pass it by).
func TestDeleteUserProjectPurgesAChatThatArrivesAfterTheLoops(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	const project = "proj-late-chat"

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := env.SQL.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.SQL.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	seedChat := func(chat string) {
		exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		      VALUES ($1,$1,$2,'owner',$3,$3,$3)`, chat, project, now)
		exec(`INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		      VALUES ($1,$2,1,2,$2,'{}',$3)`, chat+"-upd", chat, now)
	}

	if err := store.InsertProject(ctx, env.ORM, &core.Project{
		ID: project, UserID: "owner", Name: project, Path: "/srv/" + project,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("InsertProject: %v", err)
	}
	seedChat("early-chat")

	calls := 0
	defer store.SetProjectDeleteBeforeFinal(func(_ context.Context, id string) {
		if id != project {
			return
		}
		calls++
		if calls == 1 {
			seedChat("late-chat")
		}
	})()

	if err := store.DeleteUserProject(ctx, env.ORM, project, "owner"); err != nil {
		t.Fatalf("DeleteUserProject: %v", err)
	}
	if calls != 2 {
		t.Errorf("the final transaction ran %d times, want 2: the late chat must send the delete back through the loops", calls)
	}
	if n := count(`SELECT count(*) FROM projects WHERE id = $1`, project); n != 0 {
		t.Errorf("project survived: %d", n)
	}
	for _, chat := range []string{"early-chat", "late-chat"} {
		if n := count(`SELECT count(*) FROM chats WHERE id = $1`, chat); n != 0 {
			t.Errorf("%s survived", chat)
		}
		if n := count(`SELECT count(*) FROM chat_updates WHERE chat_id = $1`, chat); n != 0 {
			t.Errorf("%s's chat_updates were orphaned: %d rows", chat, n)
		}
	}
}

// A project that gains a chat every round is not deleted by cascading the
// unpurged chats away: the delete gives up after its bounded rounds with an
// error, leaving the project (and the chat arriving last) in place for the next
// attempt.
func TestDeleteUserProjectGivesUpWhenChatsKeepArriving(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	const project = "proj-busy"

	if err := store.InsertProject(ctx, env.ORM, &core.Project{
		ID: project, UserID: "owner", Name: project, Path: "/srv/" + project,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("InsertProject: %v", err)
	}
	calls := 0
	defer store.SetProjectDeleteBeforeFinal(func(ctx context.Context, id string) {
		if id != project {
			return
		}
		calls++
		chat := fmt.Sprintf("busy-chat-%d", calls)
		if _, err := env.SQL.ExecContext(ctx, `INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		      VALUES ($1,$1,$2,'owner',$3,$3,$3)`, chat, project, now); err != nil {
			t.Errorf("insert %s: %v", chat, err)
		}
	})()

	if err := store.DeleteUserProject(ctx, env.ORM, project, "owner"); err == nil {
		t.Fatal("DeleteUserProject succeeded while chats kept arriving")
	}
	var n int
	if err := env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = $1`, project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("projects = %d, want the project kept for the next attempt", n)
	}
	if calls < 2 {
		t.Errorf("hook ran %d times: the delete did not retry", calls)
	}
}
