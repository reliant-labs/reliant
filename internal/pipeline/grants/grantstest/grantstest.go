// Copyright (c) 2025 Reliant Labs

// Package grantstest seeds what a capability grant is judged against, in a
// test's real database: an activation on a machine pool, a machine its owner
// labelled into the pool, and a chat in the activation's project.
//
//forge:exclude-contract: test fixtures over a real database; nothing to mock
package grantstest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// Fixture is one owner's activation, pool, member machine and chat.
type Fixture struct {
	UserID, OrgID, ProjectID string
	TriggerID, PoolID        string
	// PoolSelector is the pool's selector, which DaemonID's labels match.
	PoolSelector map[string]string
	DaemonID     string
	ChatID       string
}

// Seed writes a Fixture for userID in projectID (which must exist): a
// webhook activation of builtin://pr on a pool selecting pool=gate, a machine labelled
// pool=gate by its owner, and a chat.
func Seed(t testing.TB, repo *db.Repo, userID, orgID, projectID string) Fixture {
	t.Helper()
	ctx := context.Background()
	f := Fixture{
		UserID: userID, OrgID: orgID, ProjectID: projectID,
		TriggerID: "trigger-" + uuid.NewString(), PoolID: "pool-" + uuid.NewString(),
		PoolSelector: map[string]string{"pool": "gate"}, ChatID: "chat-" + uuid.NewString(),
	}
	exec(t, repo, `INSERT INTO machine_pools (id, user_id, name, selector) VALUES (?, ?, ?, '{"pool":"gate"}'::jsonb)`,
		f.PoolID, userID, "gate-"+f.PoolID[len(f.PoolID)-8:])
	exec(t, repo, `INSERT INTO triggers (id, user_id, org_id, project_id, name, kind, workflow, machine_pool_id)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, 'webhook', 'builtin://pr', ?)`, f.TriggerID, userID, orgID, projectID, "pr-"+f.TriggerID[len(f.TriggerID)-8:], f.PoolID)
	f.DaemonID = Machine(t, repo, userID, f.PoolSelector)
	now := time.Now().UTC()
	if err := repo.CreateChat(ctx, &db.Chat{ID: f.ChatID, UserID: userID, ProjectID: projectID, Title: "pr",
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now}); err != nil {
		t.Fatalf("grantstest: chat: %v", err)
	}
	return f
}

// Machine writes a machine of userID's with the durable labels its owner
// set, and returns its id. nil labels leave it unlabelled.
func Machine(t testing.TB, repo *db.Repo, userID string, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if err := repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}); err != nil {
		t.Fatalf("grantstest: machine: %v", err)
	}
	if labels != nil {
		if _, err := store.SetDaemonLabels(ctx, repo.ORM(), userID, id, labels); err != nil {
			t.Fatalf("grantstest: machine labels: %v", err)
		}
	}
	return id
}

// Exec runs one statement against the test's database.
func Exec(t testing.TB, repo *db.Repo, query string, args ...any) {
	t.Helper()
	exec(t, repo, query, args...)
}

func exec(t testing.TB, repo *db.Repo, query string, args ...any) {
	t.Helper()
	if _, err := repo.ORM().Bun().NewRaw(query, args...).Exec(context.Background()); err != nil {
		t.Fatalf("grantstest: %s: %v", query, err)
	}
}
