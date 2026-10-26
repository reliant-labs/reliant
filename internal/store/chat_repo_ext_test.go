// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The chats.sql port (P5b L1b), interleaved with the Repository: every query
// writes through one layer and reads through the other.

func convChat(id string, at time.Time) *core.Chat {
	return &core.Chat{
		ID: id, Title: "chat " + id, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}
}

// convWorkflow writes a root workflow row for chatID in the given state.
func convWorkflow(t *testing.T, env *storetest.Env, id, chatID string, state core.WorkflowState, reason core.WorkflowStopReason) {
	t.Helper()
	if _, err := env.SQL.Exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, state, stop_reason)
		VALUES ($1, $2, 'wf', $1, NOW(), $3, $4)`, id, chatID, int32(state), int32(reason)); err != nil {
		t.Fatalf("insert workflow %s: %v", id, err)
	}
	if _, err := env.SQL.Exec(`UPDATE chats SET workflow_id = $1 WHERE id = $2`, id, chatID); err != nil {
		t.Fatalf("point chat %s at workflow %s: %v", chatID, id, err)
	}
}

func convChatIDs(chats []*core.Chat) []string {
	out := make([]string, len(chats))
	for i, c := range chats {
		out[i] = c.ID
	}
	return out
}

// requireSameChat compares two chats field by field, instants normalised to
// UTC. Callers pass chats without an archived_worktree_name.
func requireSameChat(t *testing.T, label string, got, want *core.Chat) {
	t.Helper()
	g, w := *got, *want
	for _, c := range []*core.Chat{&g, &w} {
		if !c.CreatedAt.IsZero() {
			c.CreatedAt = c.CreatedAt.UTC()
		}
		c.UpdatedAt, c.LastActive = c.UpdatedAt.UTC(), c.LastActive.UTC()
		if c.LastMessageAt != nil {
			lm := c.LastMessageAt.UTC()
			c.LastMessageAt = &lm
		}
		if c.AdoptedAt != nil {
			at := c.AdoptedAt.UTC()
			c.AdoptedAt = &at
		}
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got  %+v\n want %+v", label, g, w)
	}
}

func TestChatCreateGetUpdateDelete(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	// Written by the store, read by the Repository: the caller's timestamps
	// survive (forge's Create would re-stamp updated_at), and an unset state
	// is IDLE.
	a := convChat("chat-a", at)
	a.UpdatedAt = at.Add(time.Second)
	a.SelectedPresets = map[string]string{"agent": "careful"}
	a.WorkflowName = convStr("ask")
	if err := store.InsertChat(ctx, env.ORM, a); err != nil {
		t.Fatalf("InsertChat: %v", err)
	}
	twin, err := env.Repo.GetChat(ctx, "chat-a")
	if err != nil {
		t.Fatalf("Repo.GetChat: %v", err)
	}
	if !twin.UpdatedAt.Equal(at.Add(time.Second)) || !twin.CreatedAt.Equal(at) || twin.State != core.ChatStateIdle ||
		twin.SelectedPresets["agent"] != "careful" || *twin.WorkflowName != "ask" {
		t.Fatalf("Repo.GetChat = %+v, want the store's write", twin)
	}
	got, err := store.GetChat(ctx, env.ORM, "chat-a")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	requireSameChat(t, "GetChat vs Repo.GetChat", got, twin)

	// Written by the Repository, read by the store.
	if err := env.Repo.CreateChat(ctx, convChat("chat-b", at)); err != nil {
		t.Fatalf("Repo.CreateChat: %v", err)
	}
	twin, err = env.Repo.GetChat(ctx, "chat-b")
	if err != nil {
		t.Fatalf("Repo.GetChat(b): %v", err)
	}
	got, err = store.GetChat(ctx, env.ORM, "chat-b")
	if err != nil {
		t.Fatalf("GetChat(b): %v", err)
	}
	requireSameChat(t, "GetChat(b)", got, twin)

	// Owner check: the owner reads it; anyone else, and a missing chat, is
	// ErrChatNotFound on both layers.
	if got, err := store.GetChatWithUserCheck(ctx, env.ORM, "chat-a", storetest.SeedUserID); err != nil || got.ID != "chat-a" {
		t.Fatalf("GetChatWithUserCheck(owner) = %v, %v", got, err)
	}
	for _, c := range []struct{ id, user string }{{"chat-a", "someone-else"}, {"chat-none", storetest.SeedUserID}} {
		if _, err := store.GetChatWithUserCheck(ctx, env.ORM, c.id, c.user); !errors.Is(err, core.ErrChatNotFound) {
			t.Fatalf("GetChatWithUserCheck(%s, %s) err = %v, want ErrChatNotFound", c.id, c.user, err)
		}
		if _, err := env.Repo.GetChatWithUserCheck(ctx, c.id, c.user); !errors.Is(err, core.ErrChatNotFound) {
			t.Fatalf("Repo.GetChatWithUserCheck(%s, %s) err = %v, want ErrChatNotFound", c.id, c.user, err)
		}
	}
	if _, err := store.GetChat(ctx, env.ORM, "chat-none"); !errors.Is(err, core.ErrChatNotFound) {
		t.Fatalf("GetChat(missing) err = %v, want ErrChatNotFound", err)
	}

	// SaveChat writes the editable columns and stamps updated_at, whichever
	// layer reads it back; an unknown chat is left alone.
	before := time.Now().UTC().Add(-time.Second)
	edit := convChat("chat-b", at)
	edit.Title, edit.State, edit.WorktreeID, edit.RunID = "renamed", core.ChatStateArchived, convStr("wt-1"), convStr("run-1")
	edit.LastActive = at.Add(time.Minute)
	if err := store.SaveChat(ctx, env.ORM, edit); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	twin, err = env.Repo.GetChat(ctx, "chat-b")
	if err != nil {
		t.Fatalf("Repo.GetChat after SaveChat: %v", err)
	}
	if twin.Title != "renamed" || twin.State != core.ChatStateArchived || *twin.WorktreeID != "wt-1" || *twin.RunID != "run-1" ||
		!twin.LastActive.Equal(at.Add(time.Minute)) || twin.UpdatedAt.Before(before) || !twin.CreatedAt.Equal(at) {
		t.Fatalf("after SaveChat Repo.GetChat = %+v", twin)
	}
	edit.Title = "renamed by repo"
	if err := env.Repo.UpdateChat(ctx, edit); err != nil {
		t.Fatalf("Repo.UpdateChat: %v", err)
	}
	if got, err := store.GetChat(ctx, env.ORM, "chat-b"); err != nil || got.Title != "renamed by repo" {
		t.Fatalf("GetChat after Repo.UpdateChat = %v, %v", got, err)
	}
	if err := store.SaveChat(ctx, env.ORM, convChat("chat-none", at)); err != nil {
		t.Fatalf("SaveChat(missing) = %v, want a no-op", err)
	}

	// UpdateChatState captures the archived worktree name once.
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.UpdateChatState(ctx, env.ORM, "chat-a", core.ChatStateArchived, convStr("first-name"), now); err != nil {
		t.Fatalf("UpdateChatState: %v", err)
	}
	if err := store.UpdateChatState(ctx, env.ORM, "chat-a", core.ChatStateArchived, convStr("second-name"), now); err != nil {
		t.Fatalf("UpdateChatState(again): %v", err)
	}
	got, err = store.GetChat(ctx, env.ORM, "chat-a")
	if err != nil || got.State != core.ChatStateArchived || got.ArchivedWorktreeName == nil || *got.ArchivedWorktreeName != "first-name" ||
		!got.LastActive.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("after UpdateChatState GetChat = %+v, %v", got, err)
	}

	// Unread, both directions, read by the Repository.
	if err := store.UpdateChatUnread(ctx, env.ORM, "chat-a", true); err != nil {
		t.Fatalf("UpdateChatUnread: %v", err)
	}
	if twin, err := env.Repo.GetChat(ctx, "chat-a"); err != nil || !twin.Unread {
		t.Fatalf("Repo.GetChat after unread = %v, %v", twin, err)
	}
	if err := store.UpdateChatUnread(ctx, env.ORM, "chat-a", false); err != nil {
		t.Fatalf("UpdateChatUnread(false): %v", err)
	}
	if twin, err := env.Repo.GetChat(ctx, "chat-a"); err != nil || twin.Unread {
		t.Fatalf("Repo.GetChat after read = %v, %v", twin, err)
	}

	// Delete, through the store, of a chat the Repository wrote; deleting
	// it again is a no-op.
	if err := store.RemoveChat(ctx, env.ORM, "chat-b"); err != nil {
		t.Fatalf("RemoveChat: %v", err)
	}
	if err := store.RemoveChat(ctx, env.ORM, "chat-b"); err != nil {
		t.Fatalf("RemoveChat(again) = %v, want a no-op", err)
	}
	if _, err := env.Repo.GetChat(ctx, "chat-b"); !errors.Is(err, core.ErrChatNotFound) {
		t.Fatalf("Repo.GetChat after RemoveChat err = %v, want ErrChatNotFound", err)
	}
}

func TestChatListAndSearch(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if _, err := env.SQL.Exec(`INSERT INTO projects (id, name, path, user_id, created_at, updated_at, last_active)
		VALUES ('project-2', 'second', '/srv/second', $1, NOW(), NOW(), NOW())`, storetest.SeedUserID); err != nil {
		t.Fatalf("insert project-2: %v", err)
	}
	for i, id := range []string{"l-1", "l-2", "l-3", "l-4", "l-5"} {
		c := convChat(id, base.Add(time.Duration(i)*time.Minute))
		if id == "l-4" {
			c.ProjectID = "project-2"
		}
		if id == "l-5" {
			c.Title = "needle in the title"
		}
		if err := store.InsertChat(ctx, env.ORM, c); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}
	if err := store.InsertChat(ctx, env.ORM, &core.Chat{ID: "l-other", Title: "needle", ProjectID: storetest.SeedProjectID,
		UserID: "other-user", CreatedAt: base, UpdatedAt: base, LastActive: base}); err != nil {
		t.Fatalf("InsertChat(other user): %v", err)
	}
	if err := env.Repo.UpdateChatState(ctx, "l-2", core.ChatStateArchived, "test"); err != nil {
		t.Fatalf("archive l-2: %v", err)
	}
	// An automation launch that is not adopted is not listed in the sidebar.
	if _, err := env.SQL.Exec(`INSERT INTO trigger_events (id, user_id, kind, dedupe_key, occurred_at, outcome, chat_id)
		VALUES ('ev-1', $1, 'schedule', 'ev-1', NOW(), 'launched', 'l-3')`, storetest.SeedUserID); err != nil {
		t.Fatalf("insert launch event: %v", err)
	}

	project := storetest.SeedProjectID
	archived := core.ChatStateArchived
	shapes := map[string]core.ChatFilters{
		"user":             {UserID: storetest.SeedUserID, Limit: 50},
		"project":          {UserID: storetest.SeedUserID, ProjectID: &project, Limit: 50},
		"archived":         {UserID: storetest.SeedUserID, State: &archived, Limit: 50},
		"exclude archived": {UserID: storetest.SeedUserID, ExcludeArchived: true, Limit: 50},
		"sidebar only":     {UserID: storetest.SeedUserID, SidebarOnly: true, Limit: 50},
		"page":             {UserID: storetest.SeedUserID, Limit: 2, Offset: 1},
	}
	want := map[string][]string{
		"user":             {"l-5", "l-4", "l-3", "l-2", "l-1"},
		"project":          {"l-5", "l-3", "l-2", "l-1"},
		"archived":         {"l-2"},
		"exclude archived": {"l-5", "l-4", "l-3", "l-1"},
		"sidebar only":     {"l-5", "l-4", "l-2", "l-1"},
		"page":             {"l-4", "l-3"},
	}
	// UpdateChatState stamped l-2's last_active now, so it sorts first.
	for _, k := range []string{"user", "project", "sidebar only"} {
		want[k] = append([]string{"l-2"}, without(want[k], "l-2")...)
	}
	want["page"] = []string{"l-5", "l-4"}
	for name, f := range shapes {
		got, err := store.ListChats(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("ListChats(%s): %v", name, err)
		}
		twin, err := env.Repo.ListChats(ctx, f)
		if err != nil {
			t.Fatalf("Repo.ListChats(%s): %v", name, err)
		}
		if !reflect.DeepEqual(convChatIDs(got), want[name]) || !reflect.DeepEqual(convChatIDs(twin), want[name]) {
			t.Fatalf("ListChats(%s) = %v, Repo = %v, want %v", name, convChatIDs(got), convChatIDs(twin), want[name])
		}
		for i := range got {
			requireSameChat(t, "ListChats("+name+")", got[i], twin[i])
		}
	}

	// Search matches the title, or the text of a message in the chat.
	if _, err := env.Repo.CreateThread(ctx, &core.Thread{ID: "thread-l-1", ChatID: "l-1", Origin: core.ThreadOriginMain, CreatedAt: base}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := env.Repo.SaveMessageToThread(ctx, "l-1", "thread-l-1", 1, "a NEEDLE? no: a needle in a message", nil, nil, nil); err != nil {
		t.Fatalf("SaveMessageToThread: %v", err)
	}
	for _, f := range []core.ChatSearchFilters{
		{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, SearchQuery: " needle ", Limit: 10},
		{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, SearchQuery: "needle", State: &archived, Limit: 10},
		{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, SearchQuery: "needle", Limit: 1, Offset: 1},
		{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, SearchQuery: "   ", Limit: 10},
	} {
		got, err := store.SearchChats(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("SearchChats(%+v): %v", f, err)
		}
		twin, err := env.Repo.SearchChats(ctx, f)
		if err != nil {
			t.Fatalf("Repo.SearchChats(%+v): %v", f, err)
		}
		if !reflect.DeepEqual(convChatIDs(got), convChatIDs(twin)) {
			t.Fatalf("SearchChats(%+v) = %v, Repo = %v", f, convChatIDs(got), convChatIDs(twin))
		}
	}
	got, err := store.SearchChats(ctx, env.ORM, core.ChatSearchFilters{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, SearchQuery: "needle", Limit: 10})
	matched := convChatIDs(got)
	sort.Strings(matched)
	if err != nil || !reflect.DeepEqual(matched, []string{"l-1", "l-5"}) {
		t.Fatalf("SearchChats(needle) = %v, %v; want l-1 (message text) and l-5 (title), one row per chat", matched, err)
	}
	// No ProjectID searches all of the user's projects; another user's search
	// finds none of these.
	allProjects, err := store.SearchChats(ctx, env.ORM, core.ChatSearchFilters{UserID: storetest.SeedUserID, SearchQuery: "needle", Limit: 10})
	allMatched := convChatIDs(allProjects)
	sort.Strings(allMatched)
	if err != nil || !reflect.DeepEqual(allMatched, matched) {
		t.Fatalf("SearchChats(needle, all projects) = %v, %v; want %v", allMatched, err, matched)
	}
	foreign, err := store.SearchChats(ctx, env.ORM, core.ChatSearchFilters{UserID: "someone-else", SearchQuery: "needle", Limit: 10})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("SearchChats(needle) as another user = %v, %v; want none", convChatIDs(foreign), err)
	}

	// Archived chats with their worktree name: the live worktree's, else the
	// name captured at archive time, else the project's.
	if _, err := env.SQL.Exec(`UPDATE chats SET worktree_id = 'wt-gone' WHERE id = 'l-2'`); err != nil {
		t.Fatalf("point l-2 at a missing worktree: %v", err)
	}
	archivedRows, err := store.ListArchivedChats(ctx, env.ORM, storetest.SeedUserID)
	if err != nil {
		t.Fatalf("ListArchivedChats: %v", err)
	}
	twinRows, err := env.Repo.ListArchivedChats(ctx, storetest.SeedUserID)
	if err != nil {
		t.Fatalf("Repo.ListArchivedChats: %v", err)
	}
	if len(archivedRows) != 1 || len(twinRows) != 1 || archivedRows[0].ID != "l-2" || *archivedRows[0].WorktreeName != *twinRows[0].WorktreeName ||
		*archivedRows[0].WorktreeName != "Test Project" || archivedRows[0].RootStatus != twinRows[0].RootStatus {
		t.Fatalf("ListArchivedChats = %+v, Repo = %+v", archivedRows, twinRows)
	}
}

func convStr(s string) *string { return &s }

func without(ids []string, drop string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

func TestChatMachineMarkers(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, id := range []string{"m-1", "m-2", "m-3"} {
		if err := store.InsertChat(ctx, env.ORM, convChat(id, at)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}

	// Active daemon: pinning ends no-machine; clearing never sets it.
	if _, err := env.SQL.Exec(`UPDATE chats SET no_machine = true WHERE id = 'm-1'`); err != nil {
		t.Fatalf("set no_machine: %v", err)
	}
	if err := store.UpdateChatActiveDaemon(ctx, env.ORM, "m-1", convStr("daemon-1")); err != nil {
		t.Fatalf("UpdateChatActiveDaemon(pin): %v", err)
	}
	twin, err := env.Repo.GetChat(ctx, "m-1")
	if err != nil || twin.ActiveDaemonID == nil || *twin.ActiveDaemonID != "daemon-1" || twin.NoMachine {
		t.Fatalf("after pin Repo.GetChat = %+v, %v", twin, err)
	}
	if err := store.UpdateChatActiveDaemon(ctx, env.ORM, "m-1", nil); err != nil {
		t.Fatalf("UpdateChatActiveDaemon(clear): %v", err)
	}
	if twin, err := env.Repo.GetChat(ctx, "m-1"); err != nil || twin.ActiveDaemonID != nil || twin.NoMachine {
		t.Fatalf("after clear Repo.GetChat = %+v, %v", twin, err)
	}

	// Adoption is the owner's and idempotent.
	if ok, err := store.SetChatAdopted(ctx, env.ORM, "m-1", "someone-else", true); err != nil || ok {
		t.Fatalf("SetChatAdopted(not owner) = %v, %v; want false", ok, err)
	}
	if ok, err := store.SetChatAdopted(ctx, env.ORM, "m-1", storetest.SeedUserID, true); err != nil || !ok {
		t.Fatalf("SetChatAdopted = %v, %v", ok, err)
	}
	first, err := env.Repo.GetChat(ctx, "m-1")
	if err != nil || first.AdoptedAt == nil {
		t.Fatalf("Repo.GetChat after adopt = %+v, %v", first, err)
	}
	if ok, err := env.Repo.SetChatAdopted(ctx, "m-1", storetest.SeedUserID, true); err != nil || !ok {
		t.Fatalf("Repo.SetChatAdopted(again) = %v, %v", ok, err)
	}
	if got, err := store.GetChat(ctx, env.ORM, "m-1"); err != nil || got.AdoptedAt == nil || !got.AdoptedAt.Equal(*first.AdoptedAt) {
		t.Fatalf("re-adopting moved adopted_at: %v, %v", got, err)
	}
	if ok, err := store.SetChatAdopted(ctx, env.ORM, "m-1", storetest.SeedUserID, false); err != nil || !ok {
		t.Fatalf("SetChatAdopted(false) = %v, %v", ok, err)
	}
	if got, err := store.GetChat(ctx, env.ORM, "m-1"); err != nil || got.AdoptedAt != nil {
		t.Fatalf("after un-adopt GetChat = %+v, %v", got, err)
	}

	// The three markers report a change exactly once per transition.
	markers := []struct {
		name string
		set  func(chatID string, on bool) (bool, error)
	}{
		{"daemon blocked", func(id string, on bool) (bool, error) { return store.SetChatDaemonBlocked(ctx, env.ORM, id, on) }},
		{"queued for machine", func(id string, on bool) (bool, error) { return store.SetChatQueuedForMachine(ctx, env.ORM, id, on) }},
		{"paused for machine", func(id string, on bool) (bool, error) { return store.SetChatPausedForMachine(ctx, env.ORM, id, on) }},
	}
	for _, m := range markers {
		for _, step := range []struct {
			on, changed bool
		}{{true, true}, {true, false}, {false, true}, {false, false}, {true, true}} {
			changed, err := m.set("m-2", step.on)
			if err != nil || changed != step.changed {
				t.Fatalf("%s(%v) = %v, %v; want changed=%v", m.name, step.on, changed, err, step.changed)
			}
		}
		if changed, err := m.set("chat-none", true); err != nil || changed {
			t.Fatalf("%s(missing chat) = %v, %v; want false", m.name, changed, err)
		}
	}
	if twin, err := env.Repo.GetChat(ctx, "m-2"); err != nil || twin.Activity == nil {
		t.Fatalf("Repo.GetChat(m-2) = %+v, %v", twin, err)
	}

	// The sweeps' reads. m-2 holds all three markers; m-3 is queued too but
	// archived, so it is left alone.
	if changed, err := store.SetChatQueuedForMachine(ctx, env.ORM, "m-3", true); err != nil || !changed {
		t.Fatalf("queue m-3 = %v, %v", changed, err)
	}
	if err := env.Repo.UpdateChatState(ctx, "m-3", core.ChatStateArchived, "test"); err != nil {
		t.Fatalf("archive m-3: %v", err)
	}
	for _, user := range []string{"", storetest.SeedUserID, "someone-else"} {
		got, err := store.ListChatsQueuedForMachine(ctx, env.ORM, user, 10)
		if err != nil {
			t.Fatalf("ListChatsQueuedForMachine(%q): %v", user, err)
		}
		twin, err := env.Repo.ListChatsQueuedForMachine(ctx, user, 10)
		if err != nil {
			t.Fatalf("Repo.ListChatsQueuedForMachine(%q): %v", user, err)
		}
		if !reflect.DeepEqual(got, twin) {
			t.Fatalf("ListChatsQueuedForMachine(%q) = %+v, Repo = %+v", user, got, twin)
		}
		if wantN := map[string]int{"": 1, storetest.SeedUserID: 1, "someone-else": 0}[user]; len(got) != wantN {
			t.Fatalf("ListChatsQueuedForMachine(%q) = %+v, want %d", user, got, wantN)
		}
	}

	// Paused: only while the root run still reads STOPPED/PAUSED.
	convWorkflow(t, env, "wf-m-2", "m-2", core.WorkflowStateStopped, core.StopReasonPaused)
	got, err := store.ListChatsPausedForMachine(ctx, env.ORM, "", 10)
	if err != nil {
		t.Fatalf("ListChatsPausedForMachine: %v", err)
	}
	paused, err := env.Repo.ListChatsPausedForMachine(ctx, "", 10)
	if err != nil {
		t.Fatalf("Repo.ListChatsPausedForMachine: %v", err)
	}
	if !reflect.DeepEqual(got, paused) || len(got) != 1 || got[0].WorkflowID != "wf-m-2" {
		t.Fatalf("ListChatsPausedForMachine = %+v, Repo = %+v", got, paused)
	}
	if got, err := store.ListChatsPausedForMachine(ctx, env.ORM, storetest.SeedUserID, 0); err != nil || len(got) != 0 {
		t.Fatalf("ListChatsPausedForMachine(limit 0) = %+v, %v; want none", got, err)
	}

	// Waiting: the daemon-pending marker on a LIVE root run.
	if waiting, err := store.ListChatsWaitingForMachine(ctx, env.ORM, storetest.SeedUserID); err != nil || len(waiting) != 0 {
		t.Fatalf("ListChatsWaitingForMachine(paused root) = %+v, %v; want none", waiting, err)
	}
	if _, err := env.SQL.Exec(`UPDATE workflows SET state = $1, stop_reason = 0 WHERE id = 'wf-m-2'`, int32(core.WorkflowStateActive)); err != nil {
		t.Fatalf("activate wf-m-2: %v", err)
	}
	waiting, err := store.ListChatsWaitingForMachine(ctx, env.ORM, storetest.SeedUserID)
	if err != nil {
		t.Fatalf("ListChatsWaitingForMachine: %v", err)
	}
	twinWaiting, err := env.Repo.ListChatsWaitingForMachine(ctx, storetest.SeedUserID)
	if err != nil {
		t.Fatalf("Repo.ListChatsWaitingForMachine: %v", err)
	}
	if !reflect.DeepEqual(waiting, twinWaiting) || len(waiting) != 1 || waiting[0] != (core.WaitingForMachineRun{ChatID: "m-2", WorkflowID: "wf-m-2"}) {
		t.Fatalf("ListChatsWaitingForMachine = %+v, Repo = %+v", waiting, twinWaiting)
	}

	// Computed activity, through both layers.
	activity, err := store.GetChatActivity(ctx, env.ORM, "m-2")
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	twinActivity, err := env.Repo.GetChatActivity(ctx, "m-2")
	if err != nil {
		t.Fatalf("Repo.GetChatActivity: %v", err)
	}
	if activity != twinActivity || activity != int(reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON) {
		t.Fatalf("GetChatActivity = %d, Repo = %d, want WAITING_FOR_DAEMON", activity, twinActivity)
	}
	if _, err := store.GetChatActivity(ctx, env.ORM, "chat-none"); err == nil {
		t.Fatalf("GetChatActivity(missing) = nil error, want one")
	}
}

func TestChatRootStatusMapsFromTheView(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	states := map[string]core.WorkflowStatus{
		"rs-none":      {},
		"rs-running":   core.Active(),
		"rs-paused":    {State: core.WorkflowStateStopped, StopReason: core.StopReasonPaused},
		"rs-completed": {State: core.WorkflowStateStopped, StopReason: core.StopReasonCompleted},
	}
	ids := make([]string, 0, len(states))
	for id, st := range states {
		ids = append(ids, id)
		if err := store.InsertChat(ctx, env.ORM, convChat(id, at)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
		if st != (core.WorkflowStatus{}) {
			convWorkflow(t, env, "wf-"+id, id, st.State, st.StopReason)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		got, err := store.GetChat(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("GetChat(%s): %v", id, err)
		}
		twin, err := env.Repo.GetChat(ctx, id)
		if err != nil {
			t.Fatalf("Repo.GetChat(%s): %v", id, err)
		}
		if got.RootStatus != states[id] || twin.RootStatus != states[id] {
			t.Fatalf("%s root status = %+v, Repo = %+v, want %+v", id, got.RootStatus, twin.RootStatus, states[id])
		}
		requireSameChat(t, "GetChat("+id+")", got, twin)
	}
}

// SetChatRunID records the run executing the chat's root workflow, reports a
// change only when the id moves, and never writes a chat whose root workflow
// is a different one.
func TestSetChatRunID(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	chat := convChat("r-1", at)
	chat.WorkflowID = convStr("r-1")
	chat.RunID = convStr("run-closed-by-continue-as-new")
	if err := store.InsertChat(ctx, env.ORM, chat); err != nil {
		t.Fatalf("InsertChat: %v", err)
	}
	runID := func() string {
		t.Helper()
		got, err := env.Repo.GetChat(ctx, "r-1")
		if err != nil || got.RunID == nil {
			t.Fatalf("GetChat = %+v, %v", got, err)
		}
		return *got.RunID
	}

	if changed, err := store.SetChatRunID(ctx, env.ORM, "r-1", "r-1", "run-successor"); err != nil || !changed {
		t.Fatalf("SetChatRunID = %v, %v; want changed", changed, err)
	}
	if got := runID(); got != "run-successor" {
		t.Fatalf("run_id = %q, want run-successor", got)
	}
	if changed, err := store.SetChatRunID(ctx, env.ORM, "r-1", "r-1", "run-successor"); err != nil || changed {
		t.Fatalf("SetChatRunID(same) = %v, %v; want unchanged", changed, err)
	}
	if changed, err := store.SetChatRunID(ctx, env.ORM, "r-1", "some-other-workflow", "run-other"); err != nil || changed {
		t.Fatalf("SetChatRunID(other workflow) = %v, %v; want no write", changed, err)
	}
	if got := runID(); got != "run-successor" {
		t.Fatalf("run_id = %q after another workflow's write, want run-successor", got)
	}
}
