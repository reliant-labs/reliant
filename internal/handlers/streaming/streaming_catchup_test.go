package streaming

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// The streams' two catch-up contracts, each owned by the server because only
// the server can see what the client cannot:
//
//   - caught_up (StreamChatUpdates). A reopened chat shows its cached transcript under a
//     "Syncing…" status until the initial sync is over. A replay of nothing
//     sends no chat frame at all, and replay batches are 100 rows each, so no
//     chat frame can tell the client it is the LAST one. The server says so
//     explicitly, exactly once, carrying the sequence the live loop dedups
//     from.
//   - UserUpdateBatch.latest_sequence. User sequences are per-user but the
//     stream is project-filtered, so the sequences a client receives skip by
//     design. A client that inferred loss from a skip reconnected on every
//     chat open. The server sees every sequence (filtered or not) and is the
//     only party that can tell a filtered sequence from a dropped one: it
//     backfills drops from the DB and reports its cursor on every batch.

// catchupChatRepo serves the chat-sync paths: replayGapRepo's configurable
// latest sequence and replay rows, and a main thread with no messages, so
// the snapshot path has nothing to read past its sequence high-water mark.
type catchupChatRepo struct {
	*replayGapRepo
}

func newCatchupChatRepo(latest int64) catchupChatRepo {
	return catchupChatRepo{replayGapRepo: newReplayGapRepo(latest)}
}

func (catchupChatRepo) GetLatestContextWindow(context.Context, string) (*db.ContextWindow, error) {
	return nil, sql.ErrNoRows
}

// The main thread's row is read alongside its context-window walk.
func (catchupChatRepo) GetThread(context.Context, string) (*db.Thread, error) {
	return nil, sql.ErrNoRows
}

// streamingTestClient serves svc over h2c, as the API server does, and
// returns a client for it. Everything is torn down at test end.
func streamingTestClient(t *testing.T, svc *Service) reliantv1connect.StreamingServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc)
	mux.Handle(path, authInjector(handler))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL)
}

// receiveAll forwards every message of an open server stream to a channel,
// closing it when the stream ends.
func receiveAll[E any](t *testing.T, stream *connect.ServerStreamForClient[E]) <-chan *E {
	t.Helper()
	t.Cleanup(func() { _ = stream.Close() })
	events := make(chan *E, 256)
	go func() {
		defer close(events)
		for stream.Receive() {
			events <- stream.Msg()
		}
	}()
	return events
}

// userStreamEvents opens a user stream with req and returns a channel of
// every event the server sends. The stream is closed at test end.
func userStreamEvents(t *testing.T, svc *Service, req *reliantv1.StreamUserUpdatesRequest) <-chan *reliantv1.UserStreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := streamingTestClient(t, svc).StreamUserUpdates(ctx, connect.NewRequest(req))
	require.NoError(t, err)
	return receiveAll(t, stream)
}

// chatStreamEvents opens a chat stream with req and returns a channel of
// every event the server sends. The stream is closed at test end.
func chatStreamEvents(t *testing.T, svc *Service, req *reliantv1.StreamChatUpdatesRequest) <-chan *reliantv1.ChatStreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := streamingTestClient(t, svc).StreamChatUpdates(ctx, connect.NewRequest(req))
	require.NoError(t, err)
	return receiveAll(t, stream)
}

// nextEvent returns the next event matching pred, failing on timeout.
func nextEvent[E any](t *testing.T, events <-chan E, what string, pred func(E) bool) E {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("stream ended before %s arrived", what)
			}
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// chatFramesUntilCaughtUp collects every chat-carrying frame up to and
// including the first caught_up, then asserts a second never follows.
func chatFramesUntilCaughtUp(t *testing.T, events <-chan *reliantv1.ChatStreamEvent) (frames []*reliantv1.ChatStreamEvent, caughtUp *reliantv1.ChatCaughtUp) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for caughtUp == nil {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream ended without chat_caught_up — the client would show Syncing until its next heartbeat")
			}
			switch {
			case ev.GetCaughtUp() != nil:
				caughtUp = ev.GetCaughtUp()
			case ev.GetSnapshot() != nil, ev.GetUpdates() != nil:
				frames = append(frames, ev)
			}
		case <-deadline:
			t.Fatal("no chat_caught_up within 10s — the client would show Syncing until its next heartbeat")
		}
	}

	// Exactly once: nothing else arrives in a quiet window after it.
	quiet := time.After(300 * time.Millisecond)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return frames, caughtUp
			}
			require.Nil(t, ev.GetCaughtUp(), "caught_up must be sent exactly once per stream")
		case <-quiet:
			return frames, caughtUp
		}
	}
}

func TestStreamChatUpdates_CaughtUpAfterSnapshot(t *testing.T) {
	// Cursor ahead of the chat: the server answers with a snapshot.
	repo := newCatchupChatRepo(50)
	chatID := raceTestChatID
	events := chatStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}})),
		&reliantv1.StreamChatUpdatesRequest{ChatId: chatID, SinceSeq: 5000})

	frames, caughtUp := chatFramesUntilCaughtUp(t, events)

	require.Len(t, frames, 1)
	require.NotNil(t, frames[0].GetSnapshot(), "the snapshot must precede caught_up")
	require.Equal(t, int64(50), caughtUp.GetLatestSequence(),
		"caught_up must carry the snapshot's high-water mark, which the live loop dedups from")
}

func TestStreamChatUpdates_CaughtUpAfterMultiBatchReplay(t *testing.T) {
	// Two and a half batches is three replay frames. The client must not be
	// told it caught up after the first.
	const cursor = 10
	repo := newCatchupChatRepo(cursor + 2*chatReplayBatchSize + chatReplayBatchSize/2)
	chatID := raceTestChatID
	events := chatStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}})),
		&reliantv1.StreamChatUpdatesRequest{ChatId: chatID, SinceSeq: cursor})

	frames, caughtUp := chatFramesUntilCaughtUp(t, events)

	require.Len(t, frames, 3, "caught_up must follow the LAST replay batch, not the first")
	last := frames[len(frames)-1].GetUpdates()
	require.NotNil(t, last)
	require.Equal(t, repo.latestChatSeq, last.GetUpdates()[len(last.GetUpdates())-1].GetSequenceNumber())
	require.Equal(t, repo.latestChatSeq, caughtUp.GetLatestSequence())
}

func TestStreamChatUpdates_CaughtUpWhenCursorIsCurrent(t *testing.T) {
	// Nothing changed while the chat was closed: there is nothing to replay,
	// so this signal is the ONLY chat frame the client gets.
	repo := newCatchupChatRepo(77)
	chatID := raceTestChatID
	events := chatStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}})),
		&reliantv1.StreamChatUpdatesRequest{ChatId: chatID, SinceSeq: 77})

	frames, caughtUp := chatFramesUntilCaughtUp(t, events)

	require.Empty(t, frames)
	require.Equal(t, int64(77), caughtUp.GetLatestSequence())
	require.Zero(t, repo.replayReads.Load())
}

// --- User-update cursor contract (DB-backed) ---

const (
	catchupProject      = "test-project" // seeded by db.SetupTestDB
	catchupOtherProject = "other-project"
)

// setupCatchupDB returns a fresh test database holding both projects the
// user-update tests filter between.
func setupCatchupDB(t *testing.T) *db.Repo {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID: catchupOtherProject, Name: "Other Project", Path: "/tmp/reliant-test-other-project", UserID: raceTestUserID,
	}))
	return repo
}

// seedUserUpdates writes user updates for raceTestUserID, in order, and
// returns them with their allocated sequence numbers.
func seedUserUpdates(t *testing.T, repo *db.Repo, specs ...userUpdateSpec) []db.UserUpdate {
	t.Helper()
	out := make([]db.UserUpdate, 0, len(specs))
	for i, spec := range specs {
		project := spec.project
		u := &db.UserUpdate{
			UserID:     raceTestUserID,
			ProjectID:  &project,
			UpdateType: spec.updateType,
			EntityType: db.EntityTypeChat,
			EntityID:   fmt.Sprintf("entity-%d", i),
			Data:       []byte(`{}`),
		}
		require.NoError(t, repo.CreateUserUpdate(context.Background(), u))
		out = append(out, *u)
	}
	return out
}

type userUpdateSpec struct {
	project    string
	updateType db.UserUpdateType
}

func inProject(p string) userUpdateSpec {
	return userUpdateSpec{project: p, updateType: db.UserUpdateChatTitleChanged}
}

func TestStreamUserUpdates_CatchUpReportsCursorWhenEveryUpdateIsFiltered(t *testing.T) {
	repo := setupCatchupDB(t)

	seeded := seedUserUpdates(t, repo,
		inProject(catchupOtherProject), inProject(catchupOtherProject), inProject(catchupOtherProject))
	latest := seeded[len(seeded)-1].SequenceNumber

	project := catchupProject
	events := userStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}})),
		&reliantv1.StreamUserUpdatesRequest{SinceSeq: 0, ProjectId: &project})

	batch := nextEvent(t, events, "a user batch carrying the cursor", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetUpdates() != nil
	}).GetUpdates()

	require.Empty(t, batch.GetUpdates(), "every update belongs to another project")
	require.Equal(t, latest, batch.GetLatestSequence(),
		"catch-up must report the cursor even when it filtered out every update, or the client resumes from a stale one")
}

func TestStreamUserUpdates_CatchUpBatchesCarryCursorPastFilteredTail(t *testing.T) {
	repo := setupCatchupDB(t)

	seeded := seedUserUpdates(t, repo,
		inProject(catchupProject), inProject(catchupOtherProject), inProject(catchupOtherProject))
	latest := seeded[len(seeded)-1].SequenceNumber

	project := catchupProject
	events := userStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}})),
		&reliantv1.StreamUserUpdatesRequest{SinceSeq: 0, ProjectId: &project})

	// Whatever shape the catch-up takes, the client's cursor after it must be
	// the latest user sequence and it must hold the one in-project update.
	var delivered []int64
	var cursor int64
	for cursor < latest {
		batch := nextEvent(t, events, "user batches up to the latest sequence", func(ev *reliantv1.UserStreamEvent) bool {
			return ev.GetUpdates() != nil
		}).GetUpdates()
		for _, u := range batch.GetUpdates() {
			delivered = append(delivered, u.GetSequenceNumber())
		}
		require.GreaterOrEqual(t, batch.GetLatestSequence(), cursor, "the cursor must never move backwards")
		cursor = batch.GetLatestSequence()
	}
	require.Equal(t, []int64{seeded[0].SequenceNumber}, delivered)
}

// chanUserHub is a user-update hub the test publishes into directly, so it can
// model the hub dropping events (NATS slow consumer) deterministically.
type chanUserHub struct {
	streaming.UpdateHub[db.UserUpdate]
	subscribed chan struct{}
	events     chan streaming.UpdateEvent[db.UserUpdate]
}

func newChanUserHub() *chanUserHub {
	return &chanUserHub{
		subscribed: make(chan struct{}),
		events:     make(chan streaming.UpdateEvent[db.UserUpdate], 16),
	}
}

func (h *chanUserHub) Subscribe(context.Context, string) streaming.UpdateSubscription[db.UserUpdate] {
	close(h.subscribed)
	return chanUserSub{events: h.events}
}

type chanUserSub struct {
	events chan streaming.UpdateEvent[db.UserUpdate]
}

func (s chanUserSub) Events() <-chan streaming.UpdateEvent[db.UserUpdate] { return s.events }
func (chanUserSub) Unsubscribe()                                          {}

func TestStreamUserUpdates_LiveSequenceGapIsBackfilledBeforeTheEvent(t *testing.T) {
	repo := setupCatchupDB(t)

	hub := newChanUserHub()
	project := catchupProject
	events := userStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, UserUpdates: hub})),
		&reliantv1.StreamUserUpdatesRequest{SinceSeq: 0, ProjectId: &project})

	// The stream is live (catch-up over: nothing to replay) before anything
	// is written, so every update below can ONLY arrive via the hub — or the
	// server's backfill of what the hub dropped.
	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	<-hub.subscribed

	seeded := seedUserUpdates(t, repo,
		inProject(catchupProject),      // dropped by the hub
		inProject(catchupOtherProject), // dropped by the hub, filtered anyway
		userUpdateSpec{project: catchupProject, updateType: db.UserUpdateRefetch}, // dropped; REFETCH is never replayed
		inProject(catchupProject), // dropped by the hub
		inProject(catchupProject), // delivered by the hub
	)
	last := seeded[len(seeded)-1]
	hub.events <- streaming.UpdateEvent[db.UserUpdate]{Key: raceTestUserID, SequenceNumber: last.SequenceNumber, Payload: last}

	// Read until the live event itself arrives; everything before it is
	// whatever the server chose to send first.
	var delivered []int64
	var cursors []int64
	for len(delivered) == 0 || delivered[len(delivered)-1] != last.SequenceNumber {
		batch := nextEvent(t, events, "the live event", func(ev *reliantv1.UserStreamEvent) bool {
			return ev.GetUpdates() != nil
		}).GetUpdates()
		for _, u := range batch.GetUpdates() {
			delivered = append(delivered, u.GetSequenceNumber())
		}
		cursors = append(cursors, batch.GetLatestSequence())
	}

	require.Equal(t,
		[]int64{seeded[0].SequenceNumber, seeded[3].SequenceNumber, last.SequenceNumber},
		delivered,
		"the in-project updates the hub dropped must be backfilled from the DB, in order, before the live event")
	require.IsIncreasing(t, cursors, "every batch must advance the client's cursor")
	require.Equal(t, last.SequenceNumber, cursors[len(cursors)-1])
}

func TestStreamUserUpdates_FilteredLiveEventAdvancesCursorWithoutBackfill(t *testing.T) {
	repo := setupCatchupDB(t)

	hub := newChanUserHub()
	project := catchupProject
	events := userStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, UserUpdates: hub})),
		&reliantv1.StreamUserUpdatesRequest{SinceSeq: 0, ProjectId: &project})
	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	<-hub.subscribed

	// Contiguous hub delivery: another project's update, then ours. The
	// filtered one is never sent, and the next batch's cursor covers it, so
	// the client sees the skip with a cursor that explains it.
	seeded := seedUserUpdates(t, repo, inProject(catchupOtherProject), inProject(catchupProject))
	for _, u := range seeded {
		hub.events <- streaming.UpdateEvent[db.UserUpdate]{Key: raceTestUserID, SequenceNumber: u.SequenceNumber, Payload: u}
	}

	batch := nextEvent(t, events, "the in-project live update", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetUpdates() != nil && len(ev.GetUpdates().GetUpdates()) > 0
	}).GetUpdates()
	require.Len(t, batch.GetUpdates(), 1)
	require.Equal(t, seeded[1].SequenceNumber, batch.GetUpdates()[0].GetSequenceNumber())
	require.Equal(t, seeded[1].SequenceNumber, batch.GetLatestSequence())
}
