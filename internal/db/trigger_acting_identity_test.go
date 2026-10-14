// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func storedIdentity(t *testing.T, repo *Repo, id string) core.ActingIdentity {
	t.Helper()
	got, err := repo.GetTrigger(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTrigger(%s): %v", id, err)
	}
	return got.ActingIdentity
}

func TestTriggerActingIdentityRoundTrip(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	app := newTestTrigger("trg-app", "test-user", "test-project", "as the bot")
	app.ActingIdentity = core.ActingIdentityApp
	if err := repo.CreateTrigger(ctx, app); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if got := storedIdentity(t, repo, app.ID); got != core.ActingIdentityApp {
		t.Fatalf("created as app, read back %q", got)
	}

	// Update writes it, both ways.
	app.ActingIdentity = core.ActingIdentityOwner
	if err := repo.UpdateTrigger(ctx, app); err != nil {
		t.Fatalf("UpdateTrigger: %v", err)
	}
	if got := storedIdentity(t, repo, app.ID); got != core.ActingIdentityOwner {
		t.Fatalf("updated to owner, read back %q", got)
	}
	app.ActingIdentity = core.ActingIdentityApp
	if err := repo.UpdateTrigger(ctx, app); err != nil {
		t.Fatalf("UpdateTrigger: %v", err)
	}
	if got := storedIdentity(t, repo, app.ID); got != core.ActingIdentityApp {
		t.Fatalf("updated to app, read back %q", got)
	}
}

// A trigger nobody gave an identity (a fixture, a caller that predates the
// field) is stored and read as the owner: the zero value never reaches the
// NOT NULL column as an empty string the CHECK would reject.
func TestTriggerActingIdentityEmptyIsTheOwner(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	trigger := newTestTrigger("trg-default", "test-user", "test-project", "default")
	if err := repo.CreateTrigger(context.Background(), trigger); err != nil {
		t.Fatalf("CreateTrigger with no identity: %v", err)
	}
	if got := storedIdentity(t, repo, trigger.ID); got != core.ActingIdentityOwner {
		t.Fatalf("read back %q, want owner", got)
	}
}

// Every row that existed before the column acts as its owner: the migration's
// DEFAULT is what makes the previous release's activations keep working.
func TestTriggerActingIdentityPreMigrationRowIsTheOwner(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	if _, err := raw.Exec(`INSERT INTO triggers (id, user_id, project_id, name, kind, workflow, daemon_id)
		VALUES ('trg-old', 'test-user', 'test-project', 'old', 'schedule', 'wf', 'd1')`); err != nil {
		t.Fatalf("insert a row without acting_identity: %v", err)
	}
	if got := storedIdentity(t, repo, "trg-old"); got != core.ActingIdentityOwner {
		t.Fatalf("a row written without the column reads %q, want owner", got)
	}
}

// The CHECK is the last line of defence: nothing but owner|app is storable.
func TestTriggerActingIdentityRejectsJunk(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	for _, bad := range []string{"admin", "", "App", "owner,app"} {
		_, err := raw.Exec(`INSERT INTO triggers (id, user_id, project_id, name, kind, workflow, daemon_id, acting_identity)
			VALUES ('trg-bad-`+strings.ReplaceAll(bad, ",", "_")+`', 'test-user', 'test-project', 'bad', 'schedule', 'wf', 'd1', $1)`, bad)
		if err == nil {
			t.Errorf("acting_identity %q was accepted", bad)
		}
	}
}

func TestActingIdentityValidAndDefault(t *testing.T) {
	for id, want := range map[core.ActingIdentity]bool{core.ActingIdentityOwner: true, core.ActingIdentityApp: true, "": false, "admin": false} {
		if id.Valid() != want {
			t.Errorf("%q.Valid() = %v, want %v", id, id.Valid(), want)
		}
	}
	if core.ActingIdentity("").OrOwner() != core.ActingIdentityOwner || core.ActingIdentityApp.OrOwner() != core.ActingIdentityApp {
		t.Error("OrOwner must default only the empty value")
	}
}
