// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// largeChatShape sizes one seeded chat. The defaults are the shape the
// owner's slow chat-open trace came from: a long main thread compacted several
// times, a fleet of spawns that out-write it, a few workflow-node threads, and
// a chat_updates log dominated by tool-call and node-execution churn.
type largeChatShape struct {
	mainMessages   int // across mainWindows compaction windows
	mainWindows    int
	spawnThreads   int
	spawnMessages  int // per spawn thread
	nodeThreads    int
	nodeMessages   int // per node thread
	nodeExecutions int // NODE_EXECUTION chat_updates
}

var bigChat = largeChatShape{
	mainMessages: 2000, mainWindows: 4,
	spawnThreads: 15, spawnMessages: 300,
	nodeThreads: 4, nodeMessages: 50,
	nodeExecutions: 20000,
}

// seedLargeChat writes one chat of the given shape straight into Postgres with
// set-based SQL (a few seconds for tens of thousands of rows, where the Repo's
// one-row writes would take minutes). Rows are shaped like production's: two
// content blocks per message, a tool call per assistant turn, and the
// chat_updates every write path appends.
func seedLargeChat(tb testing.TB, raw *sql.DB, chatID string, s largeChatShape) {
	tb.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		tb.Helper()
		if _, err := raw.ExecContext(ctx, q, args...); err != nil {
			tb.Fatalf("seed %s: %v\n%s", chatID, err, q)
		}
	}

	exec(`INSERT INTO chats (id, title, project_id, user_id, workflow_id, created_at, updated_at, last_active)
		VALUES ($1, 'large chat', 'test-project', 'test-user', $1, now(), now(), now())`, chatID)

	// Threads: the main thread (id = chat id = workflow id), spawns, nodes.
	exec(`INSERT INTO threads (id, chat_id, origin, created_at, status) VALUES ($1, $1, 'main', now(), 2)`, chatID)
	exec(`INSERT INTO threads (id, chat_id, parent_thread_id, workflow_id, title, origin, created_at, status, completed_at)
		SELECT $1 || '-s' || k, $1, $1, $1 || '-s' || k, 'spawn ' || k, 'spawn', now() + k * interval '1 second', 3, now()
		FROM generate_series(1, $2::int) k`, chatID, s.spawnThreads)
	exec(`INSERT INTO threads (id, chat_id, parent_thread_id, origin, origin_node_id, created_at, status)
		SELECT $1 || '-n' || k, $1, $1, 'node', 'node-' || k, now() + k * interval '1 second', 3
		FROM generate_series(1, $2::int) k`, chatID, s.nodeThreads)
	exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, completed_at, state)
		SELECT id, $1, 'spawn', id, now(), now(), 3 FROM threads WHERE chat_id = $1 AND origin = 'spawn'`, chatID)

	// Context windows: a compaction chain on the main thread, one per sibling.
	exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at)
		SELECT $1 || '-cw' || w, $1, w, now() FROM generate_series(0, $2::int - 1) w`, chatID, s.mainWindows)
	exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at)
		SELECT id || '-cw0', id, 0, now() FROM threads WHERE chat_id = $1 AND origin <> 'main'`, chatID)

	// Messages. Every thread's messages are spread evenly over the chat's seq
	// range (ordered by ordinal / thread length), so spawn turns interleave
	// the main thread's the way concurrent agents do.
	exec(`WITH plan AS (
			SELECT $1::text AS thread_id, o, $2::int AS n,
			       $1 || '-cw' || ((o - 1) * $3::int / $2::int) AS cw
			FROM generate_series(1, $2::int) o
			UNION ALL
			SELECT t.id, o, $4::int, t.id || '-cw0'
			FROM threads t, generate_series(1, $4::int) o WHERE t.chat_id = $1 AND t.origin = 'spawn'
			UNION ALL
			SELECT t.id, o, $5::int, t.id || '-cw0'
			FROM threads t, generate_series(1, $5::int) o WHERE t.chat_id = $1 AND t.origin = 'node'
		), ordered AS (
			SELECT *, row_number() OVER (ORDER BY o::float8 / n, thread_id) AS seq FROM plan
		)
		INSERT INTO messages (id, chat_id, ordinal, thread_id, context_window_id, role, model, token_count,
		                      created_at, updated_at, seq)
		SELECT thread_id || '-m' || o, $1, o, thread_id, cw,
		       CASE WHEN o % 2 = 1 THEN 2 ELSE 1 END,
		       CASE WHEN o % 2 = 1 THEN 'claude-sonnet-4' END,
		       CASE WHEN o % 2 = 1 THEN 1000 + o END,
		       now() + seq * interval '1 millisecond', now() + seq * interval '1 millisecond', seq
		FROM ordered`,
		chatID, s.mainMessages, s.mainWindows, s.spawnMessages, s.nodeMessages)

	// Each main window after the first is a compaction of the one before it.
	exec(`UPDATE context_windows cw SET parent_context_window_id = $1 || '-cw' || (cw.sequence - 1),
			compaction_summary_message_id = (SELECT m.id FROM messages m WHERE m.context_window_id = cw.id ORDER BY m.seq LIMIT 1)
		WHERE cw.thread_id = $1 AND cw.sequence > 0`, chatID)

	// Content blocks: a text block on every message, then a tool call on
	// assistant turns and the matching tool result on the user turn after.
	exec(`INSERT INTO message_content_blocks (id, message_id, position, block_type, content, created_at, updated_at)
		SELECT m.id || '-b0', m.id, 0, 1, repeat('lorem ipsum dolor sit amet ', 16), m.created_at, m.created_at
		FROM messages m WHERE m.chat_id = $1`, chatID)
	exec(`INSERT INTO message_content_blocks (id, message_id, position, block_type, tool_name, tool_input, tool_call_id, created_at, updated_at)
		SELECT m.id || '-b1', m.id, 1, 2, 'bash', '{"command":"' || repeat('x', 300) || '"}', 'tc-' || m.id, m.created_at, m.created_at
		FROM messages m WHERE m.chat_id = $1 AND m.role = 2`, chatID)
	exec(`INSERT INTO message_content_blocks (id, message_id, position, block_type, tool_name, content, tool_call_id, created_at, updated_at)
		SELECT m.id || '-b1', m.id, 1, 3, 'bash', repeat('output line\n', 200), 'tc-' || m.thread_id || '-m' || (m.ordinal - 1), m.created_at, m.created_at
		FROM messages m WHERE m.chat_id = $1 AND m.role = 1`, chatID)
	exec(`INSERT INTO tool_calls (id, chat_id, thread_id, message_id, tool_name, input, status, requested_at, started_at, completed_at, created_at, updated_at)
		SELECT 'tc-' || m.id, $1, m.thread_id, m.id, 'bash', jsonb_build_object('command', repeat('x', 300)), 3,
		       m.created_at, m.created_at, m.created_at, m.created_at, m.created_at
		FROM messages m WHERE m.chat_id = $1 AND m.role = 2`, chatID)
	// The spawn calls that started each spawn thread.
	exec(`INSERT INTO tool_calls (id, chat_id, thread_id, tool_name, input, status, child_workflow_id, requested_at, completed_at, created_at, updated_at)
		SELECT 'spawn-' || t.id, $1, $1, 'spawn', '{"title":"x"}', 3, t.id, t.created_at, now(), now(), now()
		FROM threads t WHERE t.chat_id = $1 AND t.origin = 'spawn'`, chatID)

	// chat_updates, in the proportions a real log has.
	exec(`WITH u AS (
			SELECT 1 AS update_type, m.id AS entity_id,
			       json_build_object('update_type', 'message', 'id', m.id, 'role', m.role, 'seq', m.seq,
			                         'ordinal', m.ordinal, 'thread', m.thread_id, 'created_at', m.created_at,
			                         'content_blocks', json_build_array(
			                             json_build_object('id', m.id || '-b0', 'type', 1, 'index', 0, 'content', repeat('lorem ipsum dolor sit amet ', 16)),
			                             json_build_object('id', m.id || '-b1', 'type', CASE WHEN m.role = 2 THEN 2 ELSE 3 END, 'index', 1,
			                                               'tool_name', 'bash', 'content', repeat('output line\n', 200))))::text AS data,
			       m.seq * 10 AS k FROM messages m WHERE m.chat_id = $1
			UNION ALL
			SELECT 19, m.id, '{"message_id":"' || m.id || '"}', m.seq * 10 + 1 FROM messages m WHERE m.chat_id = $1
			UNION ALL
			SELECT 4, 'tool_call-' || tc.id, '{"tool_call_id":"' || tc.id || '","status":"' || st || '"}', m.seq * 10 + 2 + i
			FROM tool_calls tc JOIN messages m ON m.id = tc.message_id,
			     unnest(ARRAY['pending','executing','completed']) WITH ORDINALITY AS s(st, i)
			WHERE tc.chat_id = $1
			UNION ALL
			SELECT 9, 'node-' || (g % 200), '{"node_id":"node-' || (g % 200) || '","status":"completed","output":"' || repeat('y', 200) || '"}', g * 3
			FROM generate_series(1, $2::int) g
			UNION ALL
			SELECT 3, t.workflow_id_or_id, '{"thread":"' || t.id || '","status":"running"}', 7 + r
			FROM (SELECT id, coalesce(workflow_id, $1) AS workflow_id_or_id FROM threads WHERE chat_id = $1) t,
			     generate_series(1, 3) r
			UNION ALL
			SELECT 5, coalesce(t.workflow_id, $1), '{"status":"completed"}', 11 + r
			FROM threads t, generate_series(1, 5) r WHERE t.chat_id = $1
		)
		INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		SELECT $1 || '-u' || row_number() OVER (ORDER BY k, update_type, entity_id), $1,
		       row_number() OVER (ORDER BY k, update_type, entity_id), update_type, entity_id, data, now()
		FROM u`, chatID, s.nodeExecutions)
}

// rttProxy forwards TCP connections to target, holding every chunk for
// oneWay in each direction: a stand-in for the network between the API server
// and Postgres (through PgBouncer, in production), which a database on
// localhost does not have. Serial round trips are what a real deployment pays
// for, and they are invisible without it.
func rttProxy(tb testing.TB, target string, oneWay time.Duration) string {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = ln.Close() })
	pipe := func(dst, src net.Conn) {
		defer func() { _ = dst.Close() }()
		type chunk struct {
			b  []byte
			at time.Time
		}
		ch := make(chan chunk, 1024)
		go func() {
			defer close(ch)
			for {
				buf := make([]byte, 64<<10)
				n, err := src.Read(buf)
				if n > 0 {
					ch <- chunk{buf[:n], time.Now().Add(oneWay)}
				}
				if err != nil {
					return
				}
			}
		}()
		for c := range ch {
			time.Sleep(time.Until(c.at))
			if _, err := dst.Write(c.b); err != nil {
				return
			}
		}
	}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			go pipe(server, client)
			go pipe(client, server)
		}
	}()
	return ln.Addr().String()
}

// perfRepo opens a Repo on dsn through an rttProxy, with production's pool
// settings (internal/db/connect.go: 25 open, 10 idle).
func perfRepo(tb testing.TB, dsn string, oneWay time.Duration) *db.Repo {
	tb.Helper()
	u, err := url.Parse(dsn)
	require.NoError(tb, err)
	u.Host = rttProxy(tb, u.Host, oneWay)
	pool, err := sql.Open("pgx", u.String())
	require.NoError(tb, err)
	pool.SetMaxOpenConns(25)
	pool.SetMaxIdleConns(10)
	tb.Cleanup(func() { _ = pool.Close() })
	repo, err := db.NewRepo(pool)
	require.NoError(tb, err)
	return repo
}

// timeToCaughtUp opens a chat stream at sinceSeq and returns how long the
// server took to deliver the initial sync, up to its caught_up frame, and the
// bytes of chat frames it sent before it.
func timeToCaughtUp(t *testing.T, svc *Service, chatID string, sinceSeq int64) (time.Duration, int) {
	t.Helper()
	client := streamingTestClient(t, svc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	stream, err := client.StreamChatUpdates(ctx, connect.NewRequest(&reliantv1.StreamChatUpdatesRequest{ChatId: chatID, SinceSeq: sinceSeq}))
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	size := 0
	for stream.Receive() {
		if stream.Msg().GetCaughtUp() != nil {
			return time.Since(start), size
		}
		size += proto.Size(stream.Msg())
	}
	t.Fatalf("stream ended before caught_up: %v", stream.Err())
	return 0, 0
}

// TestChatOpenLargeChatPerf measures opening a seeded large chat end to end —
// run it with
//
//	SNAPSHOT_PERF=1 [SNAPSHOT_PERF_RTT=2ms] [SNAPSHOT_PERF_EXPLAIN=1] \
//	  go test -count=1 -v -run TestChatOpenLargeChatPerf ./internal/handlers/streaming/
//
// It times StreamChatUpdates from request to caught_up on both initial-sync
// paths: a fresh open (snapshot) and a reopen from a cached cursor (replay),
// and logs the chat_snapshot.* / chat_stream.* span breakdown. It runs against a database that also holds other chats' history, through a
// proxy that adds SNAPSHOT_PERF_RTT (default 1ms) of network round trip.
func TestChatOpenLargeChatPerf(t *testing.T) {
	testkit.Slow(t, "seeds tens of thousands of rows into a DB-backed chat and times opening it")
	if os.Getenv("SNAPSHOT_PERF") == "" {
		t.Skip("a measurement, not a check: set SNAPSHOT_PERF=1 to run it")
	}

	raw, dsn := db.NewTestDatabase(t)
	seedStart := time.Now()
	for i := 0; i < 10; i++ {
		seedLargeChat(t, raw, "bg-"+strconv.Itoa(i), largeChatShape{
			mainMessages: 800, mainWindows: 2, spawnThreads: 6, spawnMessages: 300,
			nodeThreads: 2, nodeMessages: 50, nodeExecutions: 10000,
		})
	}
	const chatID = "large-chat"
	seedLargeChat(t, raw, chatID, bigChat)
	// streamingTestClient authenticates every request as raceTestUserID.
	_, err := raw.Exec(`UPDATE chats SET user_id = $1 WHERE id = $2`, raceTestUserID, chatID)
	require.NoError(t, err)
	_, err = raw.Exec(`VACUUM ANALYZE`)
	require.NoError(t, err)
	var latest int64
	require.NoError(t, raw.QueryRow(`SELECT max(sequence_number) FROM chat_updates WHERE chat_id = $1`, chatID).Scan(&latest))
	t.Logf("seeded in %s; chat %s has %d chat_updates", time.Since(seedStart).Round(time.Millisecond), chatID, latest)

	if os.Getenv("SNAPSHOT_PERF_EXPLAIN") != "" {
		explainSnapshotReads(t, raw, chatID)
	}

	rtt := time.Millisecond
	if v := os.Getenv("SNAPSHOT_PERF_RTT"); v != "" {
		rtt, err = time.ParseDuration(v)
		require.NoError(t, err)
	}
	repo := perfRepo(t, dsn, rtt/2)
	svc := NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo}))

	// The chat_snapshot.* / chat_stream.* spans, averaged per name.
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	spanReport := func(runs int) {
		total := map[string]time.Duration{}
		count := map[string]int{}
		for _, sp := range recorder.Ended() {
			total[sp.Name()] += sp.EndTime().Sub(sp.StartTime())
			count[sp.Name()]++
		}
		names := make([]string, 0, len(total))
		for n := range total {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			t.Logf("    %-34s x%-3d avg %8s", n, count[n]/runs, (total[n] / time.Duration(count[n])).Round(10*time.Microsecond))
		}
	}

	measure := func(name string, sinceSeq int64) {
		for i := 0; i < 10; i++ { // warm the pool and every connection's statement cache
			timeToCaughtUp(t, svc, chatID, sinceSeq)
		}
		const runs = 21
		recorder.Reset()
		durations := make([]time.Duration, 0, runs)
		var size int
		for i := 0; i < runs; i++ {
			d, n := timeToCaughtUp(t, svc, chatID, sinceSeq)
			durations = append(durations, d)
			size = n
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		t.Logf("%-28s rtt=%s  median %8s  min %8s  max %8s  | %d bytes",
			name, rtt, durations[runs/2].Round(100*time.Microsecond), durations[0].Round(100*time.Microsecond),
			durations[runs-1].Round(100*time.Microsecond), size)
		spanReport(runs)
	}
	measure("open (snapshot)", 0)
	measure("reopen, 1,500 behind (replay)", latest-1500)
}

// explainSnapshotReads logs the plan and timing of the snapshot's chat-wide
// reads — the ones whose cost grows with the chat rather than the window.
func explainSnapshotReads(t *testing.T, raw *sql.DB, chatID string) {
	t.Helper()
	for name, q := range map[string]string{
		"update heads":     store.SnapshotHeadsSQL,
		"chat (authorize)": `SELECT * FROM chats_with_activity WHERE id = $1`,
		"message count":    `SELECT count(*) FROM messages WHERE chat_id = $1`,
		"live tool calls":  `SELECT id FROM tool_calls WHERE chat_id = $1 AND status IN (1, 2, 6) ORDER BY requested_at`,
		"spawn tool calls": `SELECT w.thread, tc.id FROM tool_calls tc JOIN workflows w ON w.id = tc.child_workflow_id WHERE tc.chat_id = $1 AND tc.child_workflow_id IS NOT NULL ORDER BY tc.requested_at`,
		"threads":          `SELECT id FROM threads WHERE chat_id = $1 ORDER BY created_at`,
		"latest seq":       `SELECT COALESCE(MAX(sequence_number), 0) FROM chat_updates WHERE chat_id = $1`,
		"siblings": `SELECT id FROM messages WHERE chat_id = $1 AND thread_id <> $1 AND seq >= 6000
			AND thread_id NOT IN (SELECT id FROM threads WHERE chat_id = $1 AND origin = 'spawn') ORDER BY seq DESC LIMIT 200`,
	} {
		rows, err := raw.Query(`EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) `+strings.ReplaceAll(q, "?0", "$1"), chatID)
		require.NoError(t, err, name)
		var plan []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			plan = append(plan, line)
		}
		require.NoError(t, rows.Close())
		t.Logf("EXPLAIN %s:\n%s", name, strings.Join(plan, "\n"))
	}
}
