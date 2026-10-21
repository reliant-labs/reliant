// Copyright (c) 2025 Reliant Labs
package storetest_test

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestGeneratedStoreRoundTripsAgainstPostgres is the shape every ported query's
// test takes (internal/db/SCHEMA.md): write through one layer, read through the
// other, on one database. It also pins what the lanes rely on from the
// projection: a defaulted NOT NULL column left unset gets its DEFAULT, enum
// columns marked forge:go-type scan into the reliantv1 enum, the
// chats_with_activity view is a read-only entity, and a composite-PK table is
// keyed by its whole key.
func TestGeneratedStoreRoundTripsAgainstPostgres(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	stores := store.NewStore(env.ORM)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Generated store write, then the generated read: state is left unset,
	// so the column DEFAULT (2, CHAT_STATE_IDLE) applies.
	if err := stores.Chats().CreateChat(ctx, &store.Chat{
		ID: "chat-roundtrip", Title: "round trip", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	row, err := stores.Chats().GetChatByID(ctx, "chat-roundtrip")
	if err != nil {
		t.Fatalf("GetChatByID: %v", err)
	}
	if row.Title != "round trip" || !row.CreatedAt.Equal(now) {
		t.Fatalf("GetChatByID = {Title %q, CreatedAt %v}, want {round trip, %v}", row.Title, row.CreatedAt, now)
	}
	if row.State == nil || *row.State != reliantv1.ChatState_CHAT_STATE_IDLE {
		t.Fatalf("store state = %v, want the column default CHAT_STATE_IDLE", row.State)
	}

	// The sqlc twin reads the same row.
	twin, err := env.Repo.GetChat(ctx, "chat-roundtrip")
	if err != nil {
		t.Fatalf("Repo.GetChat: %v", err)
	}
	if twin.Title != row.Title || twin.State != *row.State {
		t.Fatalf("Repo.GetChat = {Title %q, State %v}, want the store's {%q, %v}", twin.Title, twin.State, row.Title, *row.State)
	}

	// The view entity (COMMENT ON VIEW chats_with_activity IS 'forge:entity').
	view, err := stores.ChatsWithActivities().GetChatsWithActivityByID(ctx, "chat-roundtrip")
	if err != nil {
		t.Fatalf("GetChatsWithActivityByID: %v", err)
	}
	if view.ListInSidebar == nil || !*view.ListInSidebar {
		t.Fatalf("view list_in_sidebar = %v, want true for a chat with no launch event", view.ListInSidebar)
	}
	if view.State == nil || *view.State != reliantv1.ChatState_CHAT_STATE_IDLE {
		t.Fatalf("view state = %v, want CHAT_STATE_IDLE", view.State)
	}

	// A composite-PK table: written by the sqlc twin, read by its whole key.
	// cloned_at and install_state were left to their DEFAULTs.
	if err := env.Repo.UpsertProjectDaemon(ctx, storetest.SeedProjectID, "daemon-roundtrip", "/srv/checkout", nil); err != nil {
		t.Fatalf("Repo.UpsertProjectDaemon: %v", err)
	}
	pd, err := store.GetProjectDaemonByPK(ctx, env.ORM, storetest.SeedProjectID, "daemon-roundtrip")
	if err != nil {
		t.Fatalf("GetProjectDaemonByPK: %v", err)
	}
	if pd.Path != "/srv/checkout" || pd.InstallState != string(core.ProjectInstallInstalled) || pd.ClonedAt.IsZero() {
		t.Fatalf("GetProjectDaemonByPK = {Path %q, InstallState %q, ClonedAt %v}, want {/srv/checkout, installed, set}", pd.Path, pd.InstallState, pd.ClonedAt)
	}

	// And the generated write with the defaulted columns unset reads back
	// the same way through the twin.
	if err := store.CreateProjectDaemon(ctx, env.ORM, &store.ProjectDaemon{
		ProjectID: storetest.SeedProjectID, DaemonID: "daemon-generated", Path: "/srv/generated",
	}); err != nil {
		t.Fatalf("CreateProjectDaemon: %v", err)
	}
	listed, err := env.Repo.ListProjectDaemonsForDaemon(ctx, "daemon-generated")
	if err != nil {
		t.Fatalf("Repo.ListProjectDaemonsForDaemon: %v", err)
	}
	if len(listed) != 1 || listed[0].InstallState != core.ProjectInstallInstalled || listed[0].ClonedAt.IsZero() {
		t.Fatalf("Repo.ListProjectDaemonsForDaemon = %+v, want one installed row with cloned_at set", listed)
	}
}
