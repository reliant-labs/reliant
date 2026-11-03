package obsquery

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// Session replay pages. The recorder (otel-web-session-recorder, scope
// rum.rr-web) sends each rrweb event as one OTLP log record, or as several when
// the event is over 950 KiB: the JSON text is cut into byte slices, one record
// per slice, all carrying the event's timestamp and its rr-web.event index,
// numbered rr-web.chunk 1..rr-web.total-chunks. The control plane stores them in
// rum_replay, one row per chunk, ordered by (SessionId, Timestamp, EventIndex,
// Chunk).
//
// ReadReplay serves a session in pages:
//
//   - chunks are reassembled into whole events, in (timestamp, index) order;
//   - a page is bounded by bytes: it holds whole events totalling at most
//     MaxBytes, or exactly one event when that event alone is larger (a full
//     snapshot of a heavy page), never more than MaxReplayEventBytes. An event
//     above that is skipped;
//   - an event is returned only when every chunk of it is stored. An incomplete
//     event at the live edge (newer than ReplayPendingGrace before the session's
//     newest chunk) is withheld together with everything after it, so the next
//     page picks it up once its last chunk arrives; an incomplete event older
//     than that has lost a chunk for good and is skipped, so one lost chunk does
//     not stall a session;
//   - the bytes are the stored JSON text, untouched. Masking happens at record
//     time (maskAllInputs, maskTextSelector '*'); the server never unmasks, and
//     never opens an event beyond checking that it is a JSON object.
//
// rr-web.event restarts at 1 on every page load while rum.sessionId persists in
// a cookie, so an event is identified by (timestamp, index), not by index alone.
// Two tabs of one session that record an event in the same millisecond with the
// same index would share an identity; the chunk completeness check then fails and
// the event is withheld rather than returned mixed.

// Replay limits.
const (
	// DefaultReplayPageBytes is a page's byte budget when the request names none.
	DefaultReplayPageBytes = 4 << 20
	// MaxReplayPageBytes is the largest budget a request may name.
	MaxReplayPageBytes = 16 << 20
	// MaxReplayEventBytes is the largest event a page will carry. The recorder
	// shrinks an event over 5 MB by replacing data: URIs, so a real event is
	// below it.
	MaxReplayEventBytes = 16 << 20
	// MaxReplayPageEvents bounds the events of one page: many tiny mutations
	// must not make a page of a million rows inside the byte budget.
	MaxReplayPageEvents = 10000
	// MaxRangeReplay is the longest window a replay read covers (rum_replay
	// retention is capped at 30 days).
	MaxRangeReplay = MaxRangeSessions

	// ReplayPendingGrace is how long after an incomplete event the session must
	// have kept recording before the event's missing chunk is considered lost.
	// The recorder batches for 5 s; this leaves room for retries and ingest lag.
	ReplayPendingGrace = 30 * time.Second
)

// ReplayQuery is a ReadReplay request: one page of one session's rrweb events.
type ReplayQuery struct {
	// Scope bounds the read: the database, the environments, the services and
	// the time range (which should cover the session; rum_replay is partitioned
	// by day). Scope.Filters do not apply to a read of one named session.
	Scope     Scope
	SessionID string
	// MaxBytes is the page's byte budget; 0 = DefaultReplayPageBytes.
	MaxBytes int
	Cursor   *ReplayCursor
}

// ReplayCursor is the position after the last event of a page.
type ReplayCursor struct {
	TsNano int64  `json:"t"`
	Index  uint32 `json:"e"`
	Finger string `json:"f"`
}

// Fingerprint identifies the session a cursor belongs to. The time range and
// the byte budget are deliberately not part of it.
func (q ReplayQuery) Fingerprint() string {
	return fingerprint(append(append([]string{"replay"}, q.Scope.Fingerprint()...), q.SessionID)...)
}

// NextToken builds the page token that continues after the event at ts with the
// given rr-web.event index.
func (q ReplayQuery) NextToken(ts time.Time, index uint32) string {
	return encodeCursor(ReplayCursor{TsNano: ts.UnixNano(), Index: index, Finger: q.Fingerprint()})
}

// DecodeReplayCursor parses a page token; "" is no cursor.
func DecodeReplayCursor(tok string) (*ReplayCursor, error) {
	if tok == "" {
		return nil, nil
	}
	var c ReplayCursor
	if err := decodeCursor(tok, &c); err != nil || c.Finger == "" {
		return nil, invalid("page_token is malformed")
	}
	return &c, nil
}

func (q ReplayQuery) budget() int {
	if q.MaxBytes == 0 {
		return DefaultReplayPageBytes
	}
	return q.MaxBytes
}

// ReplayColumns names the select list of Compile's statement, in order, for the
// code that scans it: one row per stored chunk of a selected event.
var ReplayColumns = []string{"Timestamp", "EventIndex", "Chunk", "TotalChunks", "Body", "more"}

// Compile builds the page statement. Columns: ReplayColumns, rows ordered by
// (Timestamp, EventIndex, Chunk).
//
// The statement first reads only the SIZE of each chunk (length(Body) is read
// from the column's size stream, not its bytes) to plan the page: per event its
// chunk count, completeness and bytes, then a running total picks the events
// that fit. Only the chunks of the picked events are read for their bodies.
// Every caller-supplied value is a bound parameter.
func (q ReplayQuery) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeReplay); err != nil {
		return Stmt{}, err
	}
	if err := CheckSessionID(q.SessionID); err != nil {
		return Stmt{}, err
	}
	if q.MaxBytes < 0 || q.MaxBytes > MaxReplayPageBytes {
		return Stmt{}, invalid("max_bytes must be 1-%d", MaxReplayPageBytes)
	}
	if q.Cursor != nil && q.Cursor.Finger != q.Fingerprint() {
		return Stmt{}, invalid("page_token does not belong to this session")
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCond("Timestamp", q.Scope), "SessionId = " + b.str(q.SessionID)}
	if e := b.browserEnvCond("EnvId", q.Scope); e != "" {
		conds = append(conds, e)
	}
	if sv := b.serviceIn(q.Scope.Services); sv != "" {
		conds = append(conds, sv)
	}
	base := where(conds)
	// The planning read starts after the cursor; the chunk read needs no cursor,
	// since it is restricted to the picked events.
	planConds := conds
	if c := q.Cursor; c != nil {
		planConds = append(append([]string{}, conds...), "(Timestamp, EventIndex) > (fromUnixTimestamp64Nano("+b.i64(c.TsNano)+"), "+b.u32(c.Index)+")")
	}
	graceMs := strconv.FormatInt(ReplayPendingGrace.Milliseconds(), 10)
	eventCap := strconv.Itoa(MaxReplayEventBytes)
	maxEvents := strconv.Itoa(MaxReplayPageEvents)
	budget := b.u64(uint64(q.budget()))
	table := b.table(q.Scope, "rum_replay")

	// events: one row per (timestamp, index), complete when chunks 1..N are all
	// stored and agree on N. latest is the newest chunk of the session.
	events := "SELECT Timestamp AS ts, EventIndex AS ei, sum(length(Body)) AS bytes,\n" +
		"         (min(TotalChunks) = max(TotalChunks) AND max(TotalChunks) > 0 AND min(Chunk) = 1 AND max(Chunk) = max(TotalChunks) AND uniqExact(Chunk) = max(TotalChunks)) AS complete,\n" +
		"         max(Timestamp) OVER () AS latest\n" +
		"  FROM " + table + " " + where(planConds) + "\n  GROUP BY Timestamp, EventIndex"
	// pending: an incomplete event close enough to the live edge that its last
	// chunk may still be in flight. Nothing from the first one on is returned.
	pending := "SELECT ts, ei, bytes, complete,\n" +
		"         countIf(NOT complete AND toUnixTimestamp64Milli(latest) - toUnixTimestamp64Milli(ts) <= " + graceMs + ") OVER (ORDER BY ts, ei ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS pend\n" +
		"  FROM (" + events + ")"
	eligible := "SELECT ts, ei, bytes, (complete AND bytes <= " + eventCap + " AND pend = 0) AS ok FROM (" + pending + ")"
	running := "SELECT ts, ei, ok,\n" +
		"         sumIf(bytes, ok) OVER (ORDER BY ts, ei ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cum,\n" +
		"         countIf(ok) OVER (ORDER BY ts, ei ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS rn\n" +
		"  FROM (" + eligible + ")"
	picked := "SELECT ts, ei, ok, (ok AND rn <= " + maxEvents + " AND (cum <= " + budget + " OR rn = 1)) AS pick FROM (" + running + ")"
	plan := "SELECT ts, ei, pick, (countIf(ok) OVER () > countIf(pick) OVER ()) AS more FROM (" + picked + ")"

	sql := "WITH sel AS (SELECT ts, ei, more FROM (" + plan + ") WHERE pick)\n" +
		"SELECT Timestamp, EventIndex, Chunk, TotalChunks, Body, toUInt8((SELECT max(more) FROM sel)) AS more\n" +
		"FROM " + table + " " + base + "\n  AND (Timestamp, EventIndex) IN (SELECT ts, ei FROM sel)\n" +
		"ORDER BY Timestamp, EventIndex, Chunk"
	return b.stmt(sql), nil
}

// ReplayEvent is one whole rrweb event.
type ReplayEvent struct {
	// Time is the event's timestamp (rrweb's own, to the millisecond).
	Time time.Time
	// Index is the recorder's rr-web.event counter. It restarts on every page
	// load, so it orders events only within one timestamp.
	Index uint32
	// Chunks is how many stored records the event was reassembled from.
	Chunks int
	// Data is the event's JSON text exactly as recorded: opaque, masked at
	// record time, never altered.
	Data []byte
}

// ReplayPage is one page of ReadReplay.
type ReplayPage struct {
	Events []ReplayEvent
	// More says more complete events follow now. NextToken is set exactly then.
	More      bool
	NextToken string
	// Resume continues after the last returned event and is set whenever the
	// page holds one, even on the last page: passing it later returns the events
	// recorded since (a session still being recorded). Empty for an empty page.
	Resume string
	// Bytes is the total size of Events' Data.
	Bytes int
	// Withheld counts events the statement picked that Parse refused because
	// they were not whole JSON objects. It should be zero; a nonzero value is a
	// storage fault worth logging.
	Withheld int
}

// Parse reassembles the rows of Compile's statement into whole events. The
// caller closes rows.
func (q ReplayQuery) Parse(rows Rows) (ReplayPage, error) {
	page := ReplayPage{Events: []ReplayEvent{}}
	var (
		cur    *chunkRun
		more   bool
		finish = func() {
			if cur == nil {
				return
			}
			if ev, ok := cur.event(); ok {
				page.Events = append(page.Events, ev)
				page.Bytes += len(ev.Data)
			} else {
				page.Withheld++
			}
			cur = nil
		}
	)
	for rows.Next() {
		var (
			ts       time.Time
			ei       uint32
			chunk    uint16
			total    uint16
			body     string
			moreFlag uint8
		)
		if err := rows.Scan(&ts, &ei, &chunk, &total, &body, &moreFlag); err != nil {
			return ReplayPage{}, err
		}
		more = moreFlag != 0
		if cur == nil || !cur.ts.Equal(ts) || cur.index != ei {
			finish()
			cur = &chunkRun{ts: ts, index: ei, total: int(total), parts: map[int]string{}}
		}
		if int(total) != cur.total {
			cur.broken = true
		}
		if _, dup := cur.parts[int(chunk)]; !dup {
			cur.parts[int(chunk)] = body
		}
	}
	finish()
	if err := rows.Err(); err != nil {
		return ReplayPage{}, err
	}
	if n := len(page.Events); n > 0 {
		last := page.Events[n-1]
		page.Resume = q.NextToken(last.Time, last.Index)
		if more {
			page.More = true
			page.NextToken = page.Resume
		}
	}
	return page, nil
}

// chunkRun collects the stored chunks of one event.
type chunkRun struct {
	ts     time.Time
	index  uint32
	total  int
	broken bool
	parts  map[int]string
}

// event joins the chunks in order. It refuses an event with a missing or extra
// chunk, chunks that disagree on their count, or text that is not a JSON
// object: half an event must never leave this package.
func (c *chunkRun) event() (ReplayEvent, bool) {
	if c.broken || c.total <= 0 || len(c.parts) != c.total {
		return ReplayEvent{}, false
	}
	keys := make([]int, 0, len(c.parts))
	size := 0
	for k, v := range c.parts {
		if k < 1 || k > c.total {
			return ReplayEvent{}, false
		}
		keys = append(keys, k)
		size += len(v)
	}
	sort.Ints(keys)
	data := make([]byte, 0, size)
	for _, k := range keys {
		data = append(data, c.parts[k]...)
	}
	if len(data) == 0 || data[0] != '{' || !json.Valid(data) {
		return ReplayEvent{}, false
	}
	return ReplayEvent{Time: c.ts, Index: c.index, Chunks: c.total, Data: data}, true
}

// ReadReplay compiles q, runs it on run and reassembles the page.
func ReadReplay(ctx context.Context, run Runner, q ReplayQuery) (ReplayPage, error) {
	st, err := q.Compile()
	if err != nil {
		return ReplayPage{}, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return ReplayPage{}, err
	}
	defer rows.Close()
	return q.Parse(rows)
}
