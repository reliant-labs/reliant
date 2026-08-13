// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

// The snapshot must identify a spawned thread as a spawn even when the stored
// chat_update never said so.
//
// A chat's thread records reach a reloading client through
// GetLatestNonMessageUpdatesPerEntity, which keeps only the NEWEST update per
// entity. Unlike the live stream — where the client merges successive updates
// and carries missing fields forward from the previous record — the snapshot
// hands the client ONE row with no history behind it. A thread update written
// without `origin` therefore arrives as origin=undefined, isSpawnOrigin() goes
// false, and BackgroundWorkPill drops every running sub-agent: the composer
// shows no agent pill while six spawns are still working.
//
// Both emitters now always write origin, but chat_updates is append-only —
// rows written before that fix keep the gap permanently, and those are exactly
// the rows a reload of an existing chat reads. threads.origin is the authority,
// so the snapshot reconciles against it.
//
// The payload below is verbatim from chat 84321199, where this was observed.
func TestChatSnapshot_ThreadUpdateCarriesOriginFromThreadsTable(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	spawnThreadID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "spawns", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: spawnThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	// A legacy lifecycle update: running, correct in every respect EXCEPT that
	// it never states the origin.
	legacy, err := json.Marshal(map[string]any{
		"update_type":    "thread",
		"id":             spawnThreadID,
		"chat_id":        chatID,
		"thread":         spawnThreadID,
		"workflow_id":    spawnThreadID,
		"origin_node_id": "spawn-toolu_01NwJUt1E1vQs4RFmC1xUJ5V",
		"status":         "running",
		"thread_title":   "Fix blank pause screen",
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, spawnThreadID, string(legacy)))

	svc := NewStreamingService(repo, nil, nil, nil)
	snapshot, _, err := svc.buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	var payload map[string]any
	found := false
	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var candidate map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &candidate))
		if candidate["thread"] == spawnThreadID {
			payload = candidate
			found = true
		}
	}
	require.True(t, found, "snapshot must carry the spawn thread's update")

	require.Equal(t, db.ThreadOriginSpawn, payload["origin"],
		"a reloading client sees this row alone; without origin the background-work "+
			"pill drops running spawns and the chat looks idle")
}

// A stored origin is authoritative — reconciliation fills gaps, it does not
// overwrite what an emitter deliberately stated.
func TestChatSnapshot_ThreadUpdateKeepsStoredOrigin(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	threadID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "forks", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: threadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		Origin: db.ThreadOriginNode, CreatedAt: now,
	})
	require.NoError(t, err)

	stored, err := json.Marshal(map[string]any{
		"update_type": "thread",
		"id":          threadID,
		"chat_id":     chatID,
		"thread":      threadID,
		"workflow_id": threadID,
		"origin":      db.ThreadOriginFork,
		"status":      "running",
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, threadID, string(stored)))

	svc := NewStreamingService(repo, nil, nil, nil)
	snapshot, _, err := svc.buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var candidate map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &candidate))
		if candidate["thread"] == threadID {
			require.Equal(t, db.ThreadOriginFork, candidate["origin"],
				"reconciliation must not overwrite an origin the emitter stated")
		}
	}
}
