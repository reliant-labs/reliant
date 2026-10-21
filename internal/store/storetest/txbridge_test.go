// Copyright (c) 2025 Reliant Labs
package storetest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// One transaction, either layer (internal/db/SCHEMA.md): a transaction opened
// by the Repository (Repo.RunTx) or by the ORM (orm.Client.RunTx) is the
// transaction every sqlc/Repository call AND every generated-store call made
// with its ctx runs in. These pin it from both sides against Postgres.

var errRollback = errors.New("roll the transaction back")

// newChat is a committed chat for a test to write to.
func newChat(t *testing.T, env *storetest.Env, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.CreateChat(context.Background(), env.ORM, &store.Chat{
		ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("seed chat %s: %v", id, err)
	}
}

// writeBoth makes one sqlc write (a project_daemons upsert through the
// Repository) and one generated-store write (a chat) with ctx.
func writeBoth(ctx context.Context, env *storetest.Env, tag string) error {
	if err := env.Repo.UpsertProjectDaemon(ctx, storetest.SeedProjectID, "daemon-"+tag, "/srv/"+tag, nil); err != nil {
		return err
	}
	now := time.Now().UTC()
	return store.CreateChat(ctx, env.ORM, &store.Chat{
		ID: "chat-" + tag, Title: tag, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	})
}

// committedRows counts, as an outside observer on the pool, the rows
// writeBoth(tag) left.
func committedRows(t *testing.T, env *storetest.Env, tag string) (daemons, chats int) {
	t.Helper()
	ctx := context.Background()
	if err := env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM project_daemons WHERE daemon_id = $1`, "daemon-"+tag).Scan(&daemons); err != nil {
		t.Fatalf("count project_daemons: %v", err)
	}
	if err := env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM chats WHERE id = $1`, "chat-"+tag).Scan(&chats); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	return daemons, chats
}

func TestRepoRunTxRollsBackStoreAndSQLCWritesTogether(t *testing.T) {
	env := storetest.New(t)
	err := env.Repo.RunTx(context.Background(), func(ctx context.Context) error {
		if !orm.InTx(ctx) {
			t.Error("the ctx Repo.RunTx hands its fn carries no forge transaction")
		}
		if err := writeBoth(ctx, env, "repo-rollback"); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("Repo.RunTx = %v, want the fn's error", err)
	}
	if d, c := committedRows(t, env, "repo-rollback"); d != 0 || c != 0 {
		t.Fatalf("after rollback: %d project_daemons, %d chats; want neither (a write through one layer escaped the transaction)", d, c)
	}
}

func TestStoreRunTxRollsBackStoreAndSQLCWritesTogether(t *testing.T) {
	env := storetest.New(t)
	err := env.ORM.RunTx(context.Background(), func(ctx context.Context) error {
		if err := writeBoth(ctx, env, "store-rollback"); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("orm RunTx = %v, want the fn's error", err)
	}
	if d, c := committedRows(t, env, "store-rollback"); d != 0 || c != 0 {
		t.Fatalf("after rollback: %d project_daemons, %d chats; want neither (a write through one layer escaped the transaction)", d, c)
	}

	// And committed, both land.
	if err := env.ORM.RunTx(context.Background(), func(ctx context.Context) error {
		return writeBoth(ctx, env, "store-commit")
	}); err != nil {
		t.Fatalf("orm RunTx: %v", err)
	}
	if d, c := committedRows(t, env, "store-commit"); d != 1 || c != 1 {
		t.Fatalf("after commit: %d project_daemons, %d chats; want one of each", d, c)
	}
}

// Each layer reads the other's uncommitted write inside the transaction,
// while an observer on the pool sees neither until it commits.
func TestEachLayerSeesTheOthersUncommittedWrite(t *testing.T) {
	env := storetest.New(t)
	err := env.Repo.RunTx(context.Background(), func(ctx context.Context) error {
		if err := writeBoth(ctx, env, "visible"); err != nil {
			return err
		}
		pd, err := store.GetProjectDaemonByPK(ctx, env.ORM, storetest.SeedProjectID, "daemon-visible")
		if err != nil {
			t.Errorf("store read of the sqlc write: %v", err)
		} else if pd.Path != "/srv/visible" {
			t.Errorf("store read path %q, want /srv/visible", pd.Path)
		}
		chat, err := env.Repo.GetChat(ctx, "chat-visible")
		if err != nil {
			t.Errorf("Repository read of the store write: %v", err)
		} else if chat.Title != "visible" {
			t.Errorf("Repository read title %q, want visible", chat.Title)
		}
		if d, c := committedRows(t, env, "visible"); d != 0 || c != 0 {
			t.Errorf("an observer on the pool sees %d project_daemons, %d chats before commit", d, c)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Repo.RunTx: %v", err)
	}
	if d, c := committedRows(t, env, "visible"); d != 1 || c != 1 {
		t.Fatalf("after commit: %d project_daemons, %d chats; want one of each", d, c)
	}
}

// A row the transaction has locked is written again through the OTHER
// layer. On a second connection that write would wait on the lock for good
// (one goroutine, two connections: a deadlock Postgres cannot see); joined,
// it completes. The deadline turns a regression into a failure, not a hang.
func TestNoSelfDeadlockOnARowTheTransactionLocked(t *testing.T) {
	env := storetest.New(t)
	newChat(t, env, "chat-locked")
	daemon := "daemon-1"

	t.Run("Repo.RunTx locks, the store writes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := env.Repo.RunTx(ctx, func(ctx context.Context) error {
			if _, err := env.Repo.DB.DB(ctx).ExecContext(ctx, `SELECT 1 FROM chats WHERE id = $1 FOR UPDATE`, "chat-locked"); err != nil {
				return err
			}
			return store.UpdateChatMasked(ctx, env.ORM, &store.Chat{ID: "chat-locked", Title: "from the store"}, []string{store.ChatFieldTitle})
		})
		if err != nil {
			t.Fatalf("Repo.RunTx: %v (a deadline here means the store write ran outside the transaction)", err)
		}
	})

	t.Run("orm RunTx locks, the Repository writes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := env.ORM.RunTx(ctx, func(ctx context.Context) error {
			if _, err := env.ORM.Exec(ctx, `SELECT 1 FROM chats WHERE id = $1 FOR UPDATE`, "chat-locked"); err != nil {
				return err
			}
			return env.Repo.UpdateChatActiveDaemon(ctx, "chat-locked", &daemon)
		})
		if err != nil {
			t.Fatalf("orm RunTx: %v (a deadline here means the Repository write ran outside the transaction)", err)
		}
	})

	chat, err := env.Repo.GetChat(context.Background(), "chat-locked")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if chat.Title != "from the store" || chat.ActiveDaemonID == nil || *chat.ActiveDaemonID != daemon {
		t.Fatalf("chat = {Title %q, ActiveDaemonID %v}; want both transactions' writes committed", chat.Title, chat.ActiveDaemonID)
	}
}
