// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// projectDeleteWriterNow is the fixtures' timestamp: data, not a clock.
var projectDeleteWriterNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

// writeChatUpdate is a chat update as every release writes it, in tx: the
// counter upsert, the log row and its head. Raw SQL, so it stands for the
// previous release's writers as well as this one's.
func writeChatUpdate(ctx context.Context, tx *sql.Tx, chat string) error {
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq, heads_through_seq)
		VALUES ('chat', $1, 1, 1)
		ON CONFLICT (stream_kind, stream_id) DO UPDATE
		SET last_assigned_seq = update_stream_counters.last_assigned_seq + 1
		RETURNING last_assigned_seq`, chat).Scan(&seq); err != nil {
		return err
	}
	id := fmt.Sprintf("%s-%d", chat, seq)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		VALUES ($1,$2,$3,2,'ent','{}',$4)`, id, chat, seq, projectDeleteWriterNow); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO chat_update_heads (chat_id, head_key, update_id, sequence_number, update_type, entity_id, data, created_at)
		VALUES ($1,'e:ent',$2,$3,2,'ent','{}',$4)
		ON CONFLICT (chat_id, head_key) DO UPDATE SET update_id = excluded.update_id, sequence_number = excluded.sequence_number`,
		chat, id, seq, projectDeleteWriterNow)
	return err
}

func seedProjectWithChat(t *testing.T, env *storetest.Env, project, chat string) {
	t.Helper()
	ctx := context.Background()
	if err := store.InsertProject(ctx, env.ORM, &core.Project{
		ID: project, UserID: "owner", Name: project, Path: "/srv/" + project,
		CreatedAt: projectDeleteWriterNow, UpdatedAt: projectDeleteWriterNow, LastActive: projectDeleteWriterNow,
	}); err != nil {
		t.Fatalf("InsertProject: %v", err)
	}
	if _, err := env.SQL.ExecContext(ctx, `INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		VALUES ($1,$1,$2,'owner',$3,$3,$3)`, chat, project, projectDeleteWriterNow); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
}

func assertNothingOfChat(t *testing.T, env *storetest.Env, chat string) {
	t.Helper()
	for _, q := range []string{
		`SELECT count(*) FROM chats WHERE id = $1`,
		`SELECT count(*) FROM chat_updates WHERE chat_id = $1`,
		`SELECT count(*) FROM chat_update_heads WHERE chat_id = $1`,
		`SELECT count(*) FROM update_stream_counters WHERE stream_kind = 'chat' AND stream_id = $1`,
	} {
		var n int
		if err := env.SQL.QueryRowContext(context.Background(), q, chat).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("%s = %d after the project was deleted, want 0 (orphaned)", q, n)
		}
	}
}

// A writer to a chat that is mid-transaction when DeleteUserProject reaches
// the chat is waited for, and what it wrote goes with the chat: chat_updates,
// chat_update_heads and the stream counter have no foreign key to cascade
// them, so a purge that did not wait left them behind for good.
func TestDeleteUserProjectWaitsForAnInFlightChatWriter(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const project, chat = "proj-inflight-writer", "chat-inflight-writer"
	seedProjectWithChat(t, env, project, chat)

	tx, err := env.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeChatUpdate(ctx, tx, chat); err != nil {
		t.Fatalf("write: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- store.DeleteUserProject(ctx, env.ORM, project, "owner") }()

	// The delete must block on the writer's key share. Until it is seen
	// waiting, it must not have finished: finishing first is the defect.
	for {
		select {
		case err := <-done:
			t.Fatalf("DeleteUserProject finished (err %v) while a writer to its chat had not committed", err)
		default:
		}
		var waiting bool
		if err := env.SQL.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the writer: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("DeleteUserProject: %v", err)
	}
	assertNothingOfChat(t, env, chat)
}

// A writer that reaches a chat after DeleteUserProject purged it — a workflow
// still running, of this release or the previous one — is refused, and
// recreates neither the chat's updates nor its stream counter.
func TestDeleteUserProjectRefusesALateChatWriter(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const project, chat = "proj-late-writer", "chat-late-writer"
	seedProjectWithChat(t, env, project, chat)

	if err := store.DeleteUserProject(ctx, env.ORM, project, "owner"); err != nil {
		t.Fatalf("DeleteUserProject: %v", err)
	}
	tx, err := env.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = writeChatUpdate(ctx, tx, chat)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("write to a deleted chat: err = %v, want a foreign_key_violation", err)
	}
	_ = tx.Rollback()
	assertNothingOfChat(t, env, chat)
}

// A chat that never existed is not refused: the three tables have no foreign
// key on purpose, and only a purged chat is tombstoned.
func TestChatWriteAdmissionAdmitsAChatThatWasNeverPurged(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	tx, err := env.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeChatUpdate(ctx, tx, "chat-never-created"); err != nil {
		t.Fatalf("write: %v", err)
	}
}
