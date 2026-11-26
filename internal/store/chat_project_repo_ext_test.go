// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func seedAttachableProject(t *testing.T, env *storetest.Env, id string) {
	t.Helper()
	if err := store.InsertProject(context.Background(), env.ORM, &core.Project{
		ID: id, UserID: storetest.SeedUserID, Name: id, Path: "/srv/" + id,
		CreatedAt: l2Base, UpdatedAt: l2Base, LastActive: l2Base,
	}); err != nil {
		t.Fatalf("InsertProject(%s): %v", id, err)
	}
}

func attachedIDs(t *testing.T, env *storetest.Env, chatID string) []string {
	t.Helper()
	rows, err := store.ListChatProjectsForChat(context.Background(), env.ORM, chatID)
	if err != nil {
		t.Fatalf("ListChatProjectsForChat: %v", err)
	}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ProjectID)
	}
	return ids
}

func TestChatProjectsAttachListDetach(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-a")
	seedAttachableProject(t, env, "p-forge")
	seedAttachableProject(t, env, "p-cp")

	if err := env.Repo.AttachChatProject(ctx, &core.ChatProject{ChatID: "chat-a", ProjectID: "p-forge"}); err != nil {
		t.Fatalf("attach p-forge: %v", err)
	}
	if err := env.Repo.AttachChatProject(ctx, &core.ChatProject{ChatID: "chat-a", ProjectID: "p-cp"}); err != nil {
		t.Fatalf("attach p-cp: %v", err)
	}
	// A repeat changes nothing, and is not an error.
	if err := env.Repo.AttachChatProject(ctx, &core.ChatProject{ChatID: "chat-a", ProjectID: "p-forge"}); err != nil {
		t.Fatalf("re-attach p-forge: %v", err)
	}
	if got, want := attachedIDs(t, env, "chat-a"), []string{"p-forge", "p-cp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached = %v, want %v (oldest first)", got, want)
	}

	if err := env.Repo.DetachChatProject(ctx, "chat-a", "p-forge"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	// Detaching what is not attached is not an error.
	if err := env.Repo.DetachChatProject(ctx, "chat-a", "p-forge"); err != nil {
		t.Fatalf("detach again: %v", err)
	}
	if got, want := attachedIDs(t, env, "chat-a"), []string{"p-cp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached after detach = %v, want %v", got, want)
	}

	// Deleting the attached project drops the attachment with it.
	if _, err := env.SQL.ExecContext(ctx, `DELETE FROM projects WHERE id = 'p-cp'`); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if got := attachedIDs(t, env, "chat-a"); len(got) != 0 {
		t.Fatalf("attached after the project was deleted = %v, want none", got)
	}
}

func TestChatProjectsCap(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-cap")
	for i := 0; i <= core.MaxChatProjects; i++ {
		id := fmt.Sprintf("p-%02d", i)
		seedAttachableProject(t, env, id)
		err := env.Repo.AttachChatProject(ctx, &core.ChatProject{ChatID: "chat-cap", ProjectID: id})
		switch {
		case i < core.MaxChatProjects && err != nil:
			t.Fatalf("attach %s: %v", id, err)
		case i == core.MaxChatProjects && !errors.Is(err, core.ErrTooManyChatProjects):
			t.Fatalf("attach past the cap = %v, want ErrTooManyChatProjects", err)
		}
	}
	// One already attached is still a no-op at the cap.
	if err := env.Repo.AttachChatProject(ctx, &core.ChatProject{ChatID: "chat-cap", ProjectID: "p-00"}); err != nil {
		t.Fatalf("re-attach at the cap: %v", err)
	}
	if got := attachedIDs(t, env, "chat-cap"); len(got) != core.MaxChatProjects {
		t.Fatalf("attached = %d, want %d", len(got), core.MaxChatProjects)
	}
}

func TestCopyChatProjectsKeepsMachines(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-src")
	seedChat(t, env, "chat-branch")
	seedAttachableProject(t, env, "p-forge")
	seedAttachableProject(t, env, "p-cp")
	mustExec(t, env, `INSERT INTO daemons (id, user_id, name) VALUES ('d-1', $1, 'box')`, storetest.SeedUserID)
	daemon := "d-1"
	for _, p := range []string{"p-forge", "p-cp"} {
		if err := env.Repo.AttachChatProject(ctx, &core.ChatProject{ChatID: "chat-src", ProjectID: p, DaemonID: &daemon}); err != nil {
			t.Fatalf("attach %s: %v", p, err)
		}
	}
	if err := env.Repo.CopyChatProjects(ctx, "chat-src", "chat-branch"); err != nil {
		t.Fatalf("CopyChatProjects: %v", err)
	}
	rows, err := store.ListChatProjectsForChat(ctx, env.ORM, "chat-branch")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("branch attachments = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.DaemonID == nil || *r.DaemonID != daemon {
			t.Fatalf("branch attachment %s machine = %v, want %s", r.ProjectID, r.DaemonID, daemon)
		}
	}

	// Deleting the machine keeps the attachment and forgets the machine.
	if _, err := env.SQL.ExecContext(ctx, `DELETE FROM daemons WHERE id = 'd-1'`); err != nil {
		t.Fatalf("delete daemon: %v", err)
	}
	rows, err = store.ListChatProjectsForChat(ctx, env.ORM, "chat-src")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].DaemonID != nil {
		t.Fatalf("after the machine was deleted = %+v, want both attachments with no machine", rows)
	}
}
