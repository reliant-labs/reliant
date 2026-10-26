package streaming

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// A client that reopens a chat resubscribes with the cursor it cached when it
// last had the chat open. When the chat moved on far enough since then, an
// incremental replay is the expensive way to catch up — 100-row batches, each
// message row enriched with its own message + content-block reads — and the
// result is worse than a snapshot: thousands of superseded intermediate states
// to apply instead of the latest one per entity. Past maxChatReplayGap the
// server sends a fresh snapshot instead.
//
// A cursor AHEAD of the chat's latest sequence cannot be replayed from at
// all: it came from a different database, or a chat whose updates were
// recreated. Replaying "nothing" would leave the client showing whatever it
// had cached, indefinitely. That also gets a snapshot.

// replayGapRepo serves a chat at a configurable latest sequence and records
// which catch-up path the handler took.
type replayGapRepo struct {
	snapshotGateRepo

	latestChatSeq int64
	replayReads   atomic.Int64
}

func (r *replayGapRepo) GetLatestUpdateSequence(context.Context, string) (int64, error) {
	return r.latestChatSeq, nil
}

func (r *replayGapRepo) GetUpdatesSince(_ context.Context, _ string, sinceSeq int64, limit int) ([]db.ChatUpdate, error) {
	r.replayReads.Add(1)
	updates := make([]db.ChatUpdate, 0, limit)
	for seq := sinceSeq + 1; seq <= r.latestChatSeq && len(updates) < limit; seq++ {
		updates = append(updates, db.ChatUpdate{
			ID:             "u",
			ChatID:         raceTestChatID,
			SequenceNumber: seq,
			UpdateType:     reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS,
			EntityID:       "wf",
			Data:           []byte(`{}`),
		})
	}
	return updates, nil
}

// noopHub stands in for the ephemeral delta hub, which this path never uses.
type noopHub struct{ streaming.StreamingHub }

func (noopHub) Subscribe(ctx context.Context, _ string) streaming.Subscription {
	return noopSubscription{}
}

type noopSubscription struct{}

func (noopSubscription) Events() <-chan streaming.StreamingDelta { return nil }
func (noopSubscription) Unsubscribe()                            {}

// firstChatSyncEvent opens a chat stream at chatSinceSeq and returns the
// first event that carries chat state: a snapshot or a batch.
func firstChatSyncEvent(t *testing.T, repo Repository, chatSinceSeq int64) *reliantv1.ChatStreamEvent {
	t.Helper()

	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	stream, err := streamingTestClient(t, svc).
		StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{
			ChatId:   raceTestChatID,
			SinceSeq: chatSinceSeq,
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	for stream.Receive() {
		msg := stream.Msg()
		if msg.GetSnapshot() != nil || msg.GetUpdates() != nil {
			return msg
		}
	}
	t.Fatalf("stream ended before any chat state arrived: %v", stream.Err())
	return nil
}

func newReplayGapRepo(latest int64) *replayGapRepo {
	released := make(chan struct{})
	close(released)
	return &replayGapRepo{
		snapshotGateRepo: snapshotGateRepo{snapshotEntered: make(chan struct{}), released: released},
		latestChatSeq:    latest,
	}
}

func TestStreamChatUpdates_ReplayGapBeyondCapSendsSnapshot(t *testing.T) {
	const cursor = 10
	repo := newReplayGapRepo(cursor + maxChatReplayGap + 1)

	event := firstChatSyncEvent(t, repo, cursor)

	snapshot := event.GetSnapshot()
	require.NotNil(t, snapshot,
		"a cursor %d updates behind must get a snapshot, not a replay; got %T", maxChatReplayGap+1, event.GetEvent())
	require.Equal(t, repo.latestChatSeq, snapshot.GetLatestSequence(),
		"the snapshot must carry the chat's latest sequence, which the live stream continues from")
	require.Zero(t, repo.replayReads.Load(), "no replay reads should run once the snapshot is chosen")
}

func TestStreamChatUpdates_CursorAheadOfChatSendsSnapshot(t *testing.T) {
	repo := newReplayGapRepo(50)

	event := firstChatSyncEvent(t, repo, 5000)

	snapshot := event.GetSnapshot()
	require.NotNil(t, snapshot,
		"a cursor ahead of the chat's latest sequence cannot be replayed from and must get a snapshot; got %T",
		event.GetEvent())
	require.Equal(t, int64(50), snapshot.GetLatestSequence())
	require.Zero(t, repo.replayReads.Load())
}

// Inside the cap, a reconnect keeps the cheap incremental path — including
// exactly AT the cap.
func TestStreamChatUpdates_ReplayGapWithinCapReplays(t *testing.T) {
	const cursor = 10
	repo := newReplayGapRepo(cursor + maxChatReplayGap)

	event := firstChatSyncEvent(t, repo, cursor)

	batch := event.GetUpdates()
	require.NotNil(t, batch, "a gap of exactly maxChatReplayGap must replay; got %T", event.GetEvent())
	require.Equal(t, int64(cursor+1), batch.GetUpdates()[0].GetSequenceNumber())
	require.Positive(t, repo.replayReads.Load())
}
