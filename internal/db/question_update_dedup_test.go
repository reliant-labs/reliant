// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A question's lifecycle writes TWO chat_updates rows — "pending" when the gate
// opens and "resolved" when the user answers — and EntityIDForQuestion embeds a
// timestamp, so the two rows land under DISTINCT entity_ids. Per-entity dedup
// therefore cannot collapse them, and the snapshot that a client receives when
// it opens a chat replays BOTH.
//
// The frontend applies the batch in order, so the terminal cache value is
// correct — but "pending" is applied first, and the already-answered question
// renders for the frames between the two writes. That is the flash the user
// reports when reopening a chat with an answered ask.
//
// Tool calls already solved exactly this problem by deduping on the id embedded
// in the entity_id rather than the whole entity_id; questions were left out.
func TestGetLatestNonMessageUpdatesPerEntity_CollapsesQuestionToLatestStatus(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	questionID := "question-regression-id"

	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, QuestionUpdate{
		QuestionID: questionID,
		ChatID:     chatID,
		Status:     "pending",
		Metadata:   `{"type":"ask_user"}`,
	}))
	// EntityIDForQuestion's timestamp has nanosecond precision; sleep so the
	// two ids are unambiguously distinct even on a coarse clock. Distinct ids
	// are the precondition this test is about.
	time.Sleep(time.Millisecond)
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, QuestionUpdate{
		QuestionID: questionID,
		ChatID:     chatID,
		Status:     "resolved",
	}))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	require.Len(t, updates, 1,
		"a question's pending+resolved transitions must collapse to the latest; "+
			"replaying the pending row makes an already-answered question flash on open")
	require.Contains(t, string(updates[0].Data), `"status":"resolved"`,
		"the surviving row must be the newest status, not the gate that opened it")
}

// Two DIFFERENT questions in one chat must not collapse into each other — the
// dedup key is per question id, not per chat. Without this, the fix above would
// hide a genuinely pending question behind an older resolved one.
func TestGetLatestNonMessageUpdatesPerEntity_KeepsDistinctQuestionsSeparate(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, QuestionUpdate{
		QuestionID: "question-answered", ChatID: chatID, Status: "resolved",
	}))
	time.Sleep(time.Millisecond)
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, QuestionUpdate{
		QuestionID: "question-open", ChatID: chatID, Status: "pending",
	}))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	require.Len(t, updates, 2,
		"distinct questions are distinct entities; collapsing them would hide a live gate")
}
