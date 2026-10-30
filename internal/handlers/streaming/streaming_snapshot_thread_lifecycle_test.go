// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

const lifecycleSpawnThreadID = "thread-spawn-1"

// threadCompletesDuringSnapshotRepo completes a spawn thread while the
// snapshot reads the entity heads: the heads read sees the completion's
// update, and the thread row reads "running" before it and "completed" after.
// When a thread-row read overlaps the heads read, the heads read lets it go
// first, so a snapshot that reads the two concurrently reliably reads the row
// before the completion.
type threadCompletesDuringSnapshotRepo struct {
	*snapshotGateRepo

	mu            sync.Mutex
	completed     bool
	threadsCalled chan struct{}
	threadsOnce   sync.Once
}

func (r *threadCompletesDuringSnapshotRepo) GetLatestUpdateSequence(context.Context, string) (int64, error) {
	return 5, nil
}

func (r *threadCompletesDuringSnapshotRepo) GetLatestNonMessageUpdatesPerEntity(context.Context, string) ([]db.ChatUpdate, error) {
	select {
	case <-r.threadsCalled:
	case <-time.After(200 * time.Millisecond):
	}
	r.mu.Lock()
	r.completed = true
	r.mu.Unlock()
	data, err := json.Marshal(map[string]any{
		"update_type": "thread",
		"id":          lifecycleSpawnThreadID,
		"chat_id":     raceTestChatID,
		"thread":      lifecycleSpawnThreadID,
		"origin":      db.ThreadOriginSpawn,
		"status":      "completed",
	})
	if err != nil {
		return nil, err
	}
	return []db.ChatUpdate{{
		ID: "update-5", ChatID: raceTestChatID, SequenceNumber: 5,
		UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD,
		EntityID:   lifecycleSpawnThreadID, Data: data, CreatedAt: time.Now(),
	}}, nil
}

func (r *threadCompletesDuringSnapshotRepo) ListThreadsByConversation(context.Context, string) ([]*db.Thread, error) {
	r.threadsOnce.Do(func() { close(r.threadsCalled) })
	r.mu.Lock()
	defer r.mu.Unlock()
	row := &db.Thread{ID: lifecycleSpawnThreadID, ChatID: raceTestChatID, Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusRunning}
	if r.completed {
		completedAt := time.Now()
		row.Status, row.CompletedAt = db.ThreadStatusCompleted, &completedAt
	}
	return []*db.Thread{row}, nil
}

// The thread row that reconciles a thread head must be read after the head.
// withThreadMetadata overwrites the head's status with the row's, so a row read
// before a completion committed would show the completed head as "running".
// The live loop drops that completion as already delivered (it is at or below
// the snapshot's high-water mark), so the spawn would show as running forever.
func TestChatSnapshot_ThreadCompletingDuringSnapshotIsNotShownRunning(t *testing.T) {
	released := make(chan struct{})
	close(released)
	repo := &threadCompletesDuringSnapshotRepo{
		snapshotGateRepo: &snapshotGateRepo{snapshotEntered: make(chan struct{}), released: released},
		threadsCalled:    make(chan struct{}),
	}
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, raceTestUserID)

	snapshot, latestSeq, err := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo})).buildChatSnapshotByID(ctx, raceTestChatID)
	require.NoError(t, err)
	require.Equal(t, int64(5), latestSeq)

	var status any
	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &payload))
		if payload["thread"] == lifecycleSpawnThreadID {
			status = payload["status"]
		}
	}
	require.Equal(t, "completed", status,
		"a thread row read before the heads overwrote the completed head with a stale running status")
}
