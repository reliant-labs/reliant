// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// allLaunchKinds is every TriggerEventKind a chat can be launched by.
var allLaunchKinds = []core.TriggerEventKind{
	core.TriggerEventKindChatStart,
	core.TriggerEventKindSchedule,
	core.TriggerEventKindWebhook,
	core.TriggerEventKindIntegration,
	core.TriggerEventKindWorkflowEvent,
	core.TriggerEventKindAgentStartRun,
	core.TriggerEventKindManualRun,
	core.TriggerEventKindBuilderTest,
	core.TriggerEventKindCLIRun,
}

// insertChatLikeAnOldRelease inserts a chat the way a release that predates
// chats.origin does: without naming the column.
func insertChatLikeAnOldRelease(t *testing.T, repo *Repo, chatID string) {
	t.Helper()
	_, err := repo.ORM().Bun().NewRaw(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		VALUES (?, 'run', 'test-project', 'test-user', now(), now(), now())`, chatID).Exec(context.Background())
	require.NoError(t, err)
	createTestRootThread(t, repo, chatID)
}

// attachLaunchEvent records chatID's launch event without classifying the
// chat (recordLaunchEvent stamps origin, as the current launcher does).
func attachLaunchEvent(t *testing.T, repo *Repo, chatID string, kind core.TriggerEventKind, at time.Time) {
	t.Helper()
	created, err := repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID:         "evt-" + chatID,
		UserID:     "test-user",
		Kind:       kind,
		DedupeKey:  "dedupe-" + chatID,
		OccurredAt: at,
		Payload:    map[string]any{},
		Outcome:    core.TriggerEventLaunched,
		ChatID:     &chatID,
		CreatedAt:  at,
	})
	require.NoError(t, err)
	require.True(t, created)
}

func storedChatOrigin(t *testing.T, repo *Repo, chatID string) *int32 {
	t.Helper()
	var origin *int32
	require.NoError(t, repo.ORM().Bun().NewRaw(`SELECT origin FROM chats WHERE id = ?`, chatID).
		Scan(context.Background(), &origin))
	return origin
}

// The SQL mapping the view derives an unrecorded origin with is the Go one
// the launcher records it with, kind for kind; no launch event at all is a
// person's chat.
func TestChatOriginOfLaunchKindMatchesChatOriginOf(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	for _, kind := range append(allLaunchKinds, core.TriggerEventKind("some.future_kind")) {
		var got int32
		require.NoError(t, repo.ORM().Bun().NewRaw(`SELECT chat_origin_of_launch_kind(?)`, string(kind)).Scan(ctx, &got))
		assert.Equal(t, int32(core.ChatOriginOf(kind)), got, "kind %q", kind)
	}
	var none int32
	require.NoError(t, repo.ORM().Bun().NewRaw(`SELECT chat_origin_of_launch_kind(NULL)`).Scan(ctx, &none))
	assert.Equal(t, int32(core.ChatOriginUser), none)
}

// A chat whose inserter recorded no origin (a release that predates the
// column, still running beside this one) is classified by its launch event:
// a trigger's run stays out of every chat-list surface — the list, search and
// the Archived tab — while a person's chat, or one with no launch event,
// lists as before.
func TestUnrecordedOriginFailsClosed(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	for _, kind := range allLaunchKinds {
		id := "old-" + string(kind)
		insertChatLikeAnOldRelease(t, repo, id)
		attachLaunchEvent(t, repo, id, kind, now)
	}
	insertChatLikeAnOldRelease(t, repo, "old-no-event")
	insertChatLikeAnOldRelease(t, repo, "old-adopted")
	attachLaunchEvent(t, repo, "old-adopted", core.TriggerEventKindIntegration, now)
	_, err := repo.SetChatAdopted(ctx, "old-adopted", "test-user", true)
	require.NoError(t, err)

	assert.Nil(t, storedChatOrigin(t, repo, "old-"+string(core.TriggerEventKindSchedule)),
		"an insert that does not name origin stores NULL, not USER")

	project := "test-project"
	sidebar, err := repo.ListChats(ctx, ChatFilters{UserID: "test-user", ProjectID: &project, Limit: 100, SidebarOnly: true})
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, c := range sidebar {
		listed[c.ID] = true
	}
	searched, err := repo.SearchChats(ctx, ChatSearchFilters{UserID: "test-user", SearchQuery: "run", Limit: 100})
	require.NoError(t, err)
	found := map[string]bool{}
	for _, c := range searched {
		found[c.ID] = true
	}

	for _, kind := range allLaunchKinds {
		id := "old-" + string(kind)
		chat, err := repo.GetChat(ctx, id)
		require.NoError(t, err)
		person := kind == core.TriggerEventKindChatStart
		assert.Equal(t, core.ChatOriginOf(kind), chat.Origin, "%s: the view derives the launcher's origin", id)
		assert.Equal(t, person, chat.ListInSidebar, "%s", id)
		assert.Equal(t, person, listed[id], "%s: ListChats", id)
		assert.Equal(t, person, found[id], "%s: SearchChats", id)
	}
	noEvent, err := repo.GetChat(ctx, "old-no-event")
	require.NoError(t, err)
	assert.Equal(t, core.ChatOriginUser, noEvent.Origin)
	assert.True(t, listed["old-no-event"], "a chat with no launch event is a conversation")
	assert.True(t, listed["old-adopted"], "an adopted run lists whatever its origin")

	// Archived runs stay out of the Archived tab too.
	require.NoError(t, repo.UpdateChatState(ctx, "old-"+string(core.TriggerEventKindSchedule), ChatStateArchived, ""))
	require.NoError(t, repo.UpdateChatState(ctx, "old-"+string(core.TriggerEventKindChatStart), ChatStateArchived, ""))
	archived, err := repo.ListArchivedChats(ctx, "test-user")
	require.NoError(t, err)
	archivedIDs := map[string]bool{}
	for _, a := range archived {
		archivedIDs[a.ID] = true
	}
	assert.False(t, archivedIDs["old-"+string(core.TriggerEventKindSchedule)])
	assert.True(t, archivedIDs["old-"+string(core.TriggerEventKindChatStart)])

	// A current release's chat keeps the origin it recorded.
	createActivityTestChat(t, repo, "new-user")
	setTestChatOrigin(t, repo, "new-user", core.ChatOriginUser)
	attachLaunchEvent(t, repo, "new-user", core.TriggerEventKindChatStart, now)
	recorded, err := repo.GetChat(ctx, "new-user")
	require.NoError(t, err)
	assert.Equal(t, core.ChatOriginUser, recorded.Origin)
}

// The migration re-classifies the chats the old USER default mislabelled (a
// trigger's run stored as a person's) from their launch event, and moves no
// chat a person started. It runs over seeded rows, so what ships is what is
// tested.
func TestChatOriginFailsClosedMigrationReclassifiesMislabelledRuns(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	now := time.Now().UTC()

	for _, kind := range allLaunchKinds {
		id := "ml-" + string(kind)
		createActivityTestChat(t, repo, id)
		attachLaunchEvent(t, repo, id, kind, now)
	}
	createActivityTestChat(t, repo, "ml-no-event")
	// A branch of an automation's run is a person's chat with no launch event.
	createActivityTestChat(t, repo, "ml-branch")
	setTestChatOrigin(t, repo, "ml-branch", core.ChatOriginUser)
	// A chat that recorded a run's origin keeps it.
	createActivityTestChat(t, repo, "ml-recorded-agent")
	attachLaunchEvent(t, repo, "ml-recorded-agent", core.TriggerEventKindAgentStartRun, now)
	setTestChatOrigin(t, repo, "ml-recorded-agent", core.ChatOriginAgent)

	// What the old default left: every chat the previous release inserted is
	// USER, whatever launched it.
	_, err := raw.Exec(`UPDATE chats SET origin = 1 WHERE id LIKE 'ml-%' AND id <> 'ml-recorded-agent'`)
	require.NoError(t, err)

	// Found by name, not version: the merge queue re-versions a branch's
	// migrations above main's when it lands.
	matches, err := fs.Glob(forgedb.MigrationsFS, "migrations/*_chat_origin_fails_closed.up.sql")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	body, err := forgedb.MigrationsFS.ReadFile(matches[0])
	require.NoError(t, err)
	_, err = raw.Exec(string(body))
	require.NoError(t, err)

	for _, kind := range allLaunchKinds {
		id := "ml-" + string(kind)
		stored := storedChatOrigin(t, repo, id)
		require.NotNil(t, stored, id)
		assert.Equal(t, int32(core.ChatOriginOf(kind)), *stored, "%s: re-classified from its launch event", id)
	}
	for _, id := range []string{"ml-no-event", "ml-branch"} {
		stored := storedChatOrigin(t, repo, id)
		require.NotNil(t, stored, id)
		assert.Equal(t, int32(core.ChatOriginUser), *stored, "%s: no launch event stays a person's", id)
	}
	stored := storedChatOrigin(t, repo, "ml-recorded-agent")
	require.NotNil(t, stored)
	assert.Equal(t, int32(core.ChatOriginAgent), *stored)
}
