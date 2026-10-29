// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
)

// headsGateRepo holds the update-heads read until the window's content blocks
// have been asked for, or until a deadline.
type headsGateRepo struct {
	Repository
	blocksAsked chan struct{}
	once        sync.Once
	heldUp      bool
}

func (r *headsGateRepo) ListContentBlocksForMessages(ctx context.Context, ids []string) ([]*db.MessageContentBlock, error) {
	r.once.Do(func() { close(r.blocksAsked) })
	return r.Repository.ListContentBlocksForMessages(ctx, ids)
}

func (r *headsGateRepo) GetLatestNonMessageUpdatesPerEntity(ctx context.Context, chatID string) ([]db.ChatUpdate, error) {
	select {
	case <-r.blocksAsked:
	case <-time.After(2 * time.Second):
		r.heldUp = true
		return nil, errors.New("the content blocks were not read while the update heads were")
	}
	return r.Repository.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
}

// The update heads cost what the chat's whole entity history costs (prod:
// p50 0.2s, p95 2.5s), and nothing in the transcript depends on them. The
// snapshot used to wait for them before reading the window's content blocks,
// so a chat open paid the two in sequence. This holds the heads read until the
// blocks are asked for: it deadlocks (until the deadline) whenever the blocks
// wait on the heads.
func TestChatSnapshot_UpdateHeadsDoNotHoldUpTheTranscript(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	fixture := dbtest.SeedToolCall(t, ctx, repo, `{"command":"ls"}`)
	chat, err := repo.GetChat(ctx, fixture.ChatID)
	require.NoError(t, err)
	chat.WorkflowID = &fixture.ThreadID
	require.NoError(t, repo.UpdateChat(ctx, chat))

	gate := &headsGateRepo{Repository: repo, blocksAsked: make(chan struct{})}
	snapshot, _, err := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: gate})).buildChatSnapshotByID(ctx, fixture.ChatID)
	require.NoError(t, err)
	require.False(t, gate.heldUp, "the window's content blocks waited for the update heads")
	require.NotEmpty(t, snapshot.Messages)
}

// A chat open does not carry workflow state: every run a chat ever made added
// node entities, and the snapshot read them all on every open. A run-status
// view reads its run's nodes itself (ChatService.GetWorkflowNodeExecutions);
// live node events still stream.
func TestChatSnapshot_CarriesNoWorkflowState(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	fixture := dbtest.SeedToolCall(t, ctx, repo, `{"command":"ls"}`)
	require.NoError(t, repo.CreateChatUpdate(ctx, fixture.ChatID, db.UpdateTypeNodeExecution, "run-1:call_llm", `{"node_id":"call_llm"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, fixture.ChatID, db.UpdateTypeWorkflowStatus, "run-1", `{"status":"completed"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, fixture.ChatID, db.UpdateTypeApproval, "approval-1", `{"id":"approval-1","status":"pending"}`))

	snapshot, _, err := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo})).buildChatSnapshotByID(ctx, fixture.ChatID)
	require.NoError(t, err)
	approvals := 0
	for _, u := range snapshot.OtherUpdates {
		require.NotEqual(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION, u.UpdateType)
		require.NotEqual(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS, u.UpdateType)
		if u.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL {
			approvals++
		}
	}
	require.Equal(t, 1, approvals, "the rest of the chat's state still comes with the snapshot")
}
