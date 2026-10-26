package chat

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// statementCounter counts every statement the pool sends, ORM and hand SQL
// alike, by tracing the pgx connections under database/sql.
type statementCounter struct{ n atomic.Int64 }

func (c *statementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *statementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

const (
	costMainMessages    = 3000 // main-thread messages across three context windows
	costSpawnThreads    = 30
	costMessagesPerSpan = 60
	costPage            = 200
)

// seedLongCompactedChat builds a chat shaped like the ones users find slow: a
// main thread compacted twice (three context windows, each parented on the
// last) and 30 spawn threads whose messages interleave with, and outlast, the
// main thread's newest ones. It returns the chat id.
func seedLongCompactedChat(t *testing.T, repo *db.Repo, raw *sql.DB) (ctx context.Context, chatID string) {
	t.Helper()
	ctx = context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC().Truncate(time.Microsecond)

	chatID = fmt.Sprintf("cost-chat-%d", now.UnixNano())
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "long", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	// The main thread's id is the chat's id when it has no workflow id.
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, Origin: db.ThreadOriginMain, CreatedAt: now})
	require.NoError(t, err)

	// Three windows of costMainMessages/3 main messages each, ids <chat>-m<i>,
	// seq 2*i. A window is created after the one it compacts is written, so
	// its summary message already exists.
	per := costMainMessages / 3
	for n := 0; n < 3; n++ {
		window := &db.ContextWindow{ID: fmt.Sprintf("%s-cw%d", chatID, n), ThreadID: chatID, Sequence: n, CreatedAt: now}
		if n > 0 {
			parent := fmt.Sprintf("%s-cw%d", chatID, n-1)
			summary := fmt.Sprintf("%s-m%d", chatID, n*per) // the last message of the window it summarizes
			window.ParentContextWindowID, window.CompactionSummaryMessageID = &parent, &summary
		}
		_, err := repo.CreateContextWindow(ctx, window)
		require.NoError(t, err)
		seedMain(t, raw, chatID, window.ID, n*per+1, (n+1)*per, now)
	}

	for g := 0; g < costSpawnThreads; g++ {
		tid := fmt.Sprintf("%s-spawn%d", chatID, g)
		_, err := repo.CreateThread(ctx, &db.Thread{ID: tid, ChatID: chatID, ParentThreadID: &chatID, Origin: db.ThreadOriginSpawn, CreatedAt: now})
		require.NoError(t, err)
		_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: tid + "-cw", ThreadID: tid, Sequence: 0, CreatedAt: now})
		require.NoError(t, err)
		// Seqs 2*(base+j)+1 sit between the main thread's even seqs, and with
		// base 1500+g*60 they run to the end of the main thread's range.
		_, err = raw.Exec(`
			INSERT INTO messages(id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq)
			SELECT $1 || '-m' || j, $2, j, $1, $3, 1, $4, $4, 2*($5 + j)+1 FROM generate_series(1, $6) j`,
			tid, chatID, tid+"-cw", now, 1500+g*costMessagesPerSpan, costMessagesPerSpan)
		require.NoError(t, err)
		_, err = raw.Exec(`
			INSERT INTO message_content_blocks(id, message_id, position, block_type, content, created_at, updated_at)
			SELECT id || '-b', id, 0, $2, 'spawn output', $3, $3 FROM messages WHERE thread_id = $1`,
			tid, int32(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT), now)
		require.NoError(t, err)
	}
	return ctx, chatID
}

// seedMain inserts main-thread messages from..to (inclusive) into one window,
// ids <chat>-m<i>, ordinal i, seq 2*i, one text block each.
func seedMain(t *testing.T, raw *sql.DB, chatID, cwID string, from, to int, now time.Time) {
	t.Helper()
	if to < from {
		return
	}
	_, err := raw.Exec(`
		INSERT INTO messages(id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq)
		SELECT $1 || '-m' || i, $1, i, $1, $2, 1, $3, $3, 2*i FROM generate_series($4::int, $5::int) i
		ON CONFLICT (id) DO NOTHING`, chatID, cwID, now, from, to)
	require.NoError(t, err)
	_, err = raw.Exec(`
		INSERT INTO message_content_blocks(id, message_id, position, block_type, content, created_at, updated_at)
		SELECT id || '-b', id, 0, $2, 'main output', $3, $3 FROM messages WHERE context_window_id = $1
		ON CONFLICT (id) DO NOTHING`, cwID, int32(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT), now)
	require.NoError(t, err)
}

// ListMessages' cost on a long, compacted, spawn-heavy chat. The statement
// count must not grow with the chat's thread count (it used to run ~3 queries
// per thread, ~100 round trips with 30 spawns), and a compacted chat's newest
// page and scroll-back page must come from bounded reads.
func TestListMessages_StatementCount_LongCompactedSpawnHeavyChat(t *testing.T) {
	t.Parallel()

	rawBase, dsn := db.NewTestDatabase(t)
	_ = rawBase
	counter := &statementCounter{}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.Tracer = counter
	raw := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = raw.Close() })
	repo, err := db.NewRepoWithDriver(raw, db.DriverPostgres)
	require.NoError(t, err)

	ctx, chatID := seedLongCompactedChat(t, repo, raw)
	svc := &Service{deps: Deps{Repo: repo}}

	list := func(before int64) (*reliantv1.ListMessagesResponse, int64, time.Duration) {
		recent := int32(costPage)
		req := &reliantv1.ListMessagesRequest{ChatId: chatID, Recent: &recent}
		if before > 0 {
			req.BeforeSeq = &before
		}
		start := counter.n.Load()
		t0 := time.Now()
		resp, err := svc.ListMessages(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg, counter.n.Load() - start, time.Since(t0)
	}

	newest, newestStatements, _ := list(0)
	require.Len(t, newest.Messages, costPage, "the newest page is %d main-thread messages", costPage)
	require.True(t, newest.HasMore)

	older, olderStatements, _ := list(newest.OldestSeq)
	require.NotEmpty(t, older.Messages)

	// Median wall time over repeats, for the report.
	var times []time.Duration
	for i := 0; i < 15; i++ {
		_, _, d := list(0)
		times = append(times, d)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("MEASURE newest-page statements=%d scroll-back-page statements=%d median-newest-page=%s (30 spawn threads, 3 context windows, %d main messages)",
		newestStatements, olderStatements, times[len(times)/2], costMainMessages)

	// Main-thread messages only: no spawn message may ride along.
	for _, m := range newest.Messages {
		require.NotContains(t, m.Id, "-spawn", "a spawn-thread message reached a transcript page")
	}

	// A fixed handful of statements regardless of thread count. Before this
	// was bounded the figure was in the hundreds for this chat.
	const budget = 25
	if newestStatements > budget || olderStatements > budget {
		t.Errorf("ListMessages used %d statements for the newest page and %d for the scroll-back page; budget %d",
			newestStatements, olderStatements, budget)
	}
}
