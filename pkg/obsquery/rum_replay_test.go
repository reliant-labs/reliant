package obsquery

import (
	"strings"
	"testing"
	"time"
)

func replayRow(tsMs int64, idx uint32, chunk, total uint16, body string, more uint8) []any {
	return []any{time.UnixMilli(tsMs).UTC(), idx, chunk, total, body, more}
}

func TestReplayCompile(t *testing.T) {
	sc := rumScope()
	sc.Services = []string{rumHostile}
	st, err := ReplayQuery{Scope: sc, SessionID: "91b955b313a1a86644ea53730a92deab", MaxBytes: 1 << 20}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, rumHostile)
	for _, want := range []string{
		"FROM t_org_a.rum_replay", "SessionId = {", "EnvId = {", "ServiceName IN (",
		"length(Body)", "uniqExact(Chunk) = max(TotalChunks)", "ORDER BY Timestamp, EventIndex, Chunk",
		"(Timestamp, EventIndex) IN (SELECT ts, ei FROM sel)",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// the planning read must not touch the bodies
	plan := st.SQL[:strings.Index(st.SQL, "SELECT Timestamp, EventIndex, Chunk")]
	if strings.Contains(plan, "Body") && !strings.Contains(plan, "length(Body)") {
		t.Errorf("planning reads Body:\n%s", plan)
	}
	if strings.Count(plan, "Body") != strings.Count(plan, "length(Body)") {
		t.Errorf("planning reads a body besides its length:\n%s", plan)
	}
}

func TestReplayCompileValidation(t *testing.T) {
	for name, q := range map[string]ReplayQuery{
		"no session":     {Scope: rumScope()},
		"hostile id":     {Scope: rumScope(), SessionID: rumHostile},
		"too many bytes": {Scope: rumScope(), SessionID: "s", MaxBytes: MaxReplayPageBytes + 1},
		"negative bytes": {Scope: rumScope(), SessionID: "s", MaxBytes: -1},
		"range too long": {Scope: Scope{Database: "t_org_a", Start: t0, End: t0.Add(MaxRangeReplay + time.Hour)}, SessionID: "s"},
		"foreign cursor": {Scope: rumScope(), SessionID: "s", Cursor: &ReplayCursor{TsNano: 1, Finger: "x"}},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := (ReplayQuery{Scope: rumScope(), SessionID: "s", MaxBytes: MaxReplayPageBytes}).Compile(); err != nil {
		t.Errorf("the maximum budget must compile: %v", err)
	}
}

func TestReplayCursorIsBoundToTheSession(t *testing.T) {
	q := ReplayQuery{Scope: rumScope(), SessionID: "s1"}
	c, err := DecodeReplayCursor(q.NextToken(time.UnixMilli(1792410246484), 4))
	if err != nil || c == nil || c.Index != 4 || c.TsNano != 1792410246484*1_000_000 {
		t.Fatalf("cursor = %+v, %v", c, err)
	}
	q.Cursor = c
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "(Timestamp, EventIndex) > (fromUnixTimestamp64Nano(") {
		t.Errorf("no cursor condition:\n%s", st.SQL)
	}
	// the budget and the range may change between pages; the session may not
	q.MaxBytes = 1234
	q.Scope.End = q.Scope.End.Add(time.Hour)
	if _, err := q.Compile(); err != nil {
		t.Errorf("budget or range changed: %v", err)
	}
	q.SessionID = "s2"
	if _, err := q.Compile(); !isInvalid(err) {
		t.Errorf("another session's cursor applied: %v", err)
	}
	if c, err := DecodeReplayCursor(""); c != nil || err != nil {
		t.Errorf("empty token = %v, %v", c, err)
	}
	if _, err := DecodeReplayCursor("%%"); !isInvalid(err) {
		t.Errorf("malformed token: %v", err)
	}
}

func TestReplayParseReassemblesChunksInOrder(t *testing.T) {
	// one event of three chunks, stored out of order, between two whole events
	full := `{"type":2,"data":{"node":{"id":1,"text":"********"}},"timestamp":1792410246484}`
	cut1, cut2 := 20, 50
	q := ReplayQuery{Scope: rumScope(), SessionID: "s1"}
	page, err := q.Parse(&catalogTestRows{rows: [][]any{
		replayRow(1000, 1, 1, 1, `{"type":4,"data":{},"timestamp":1000}`, 0),
		replayRow(2000, 2, 2, 3, full[cut1:cut2], 0),
		replayRow(2000, 2, 1, 3, full[:cut1], 0),
		replayRow(2000, 2, 3, 3, full[cut2:], 0),
		replayRow(3000, 3, 1, 1, `{"type":3,"data":{},"timestamp":3000}`, 0),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 || page.Withheld != 0 {
		t.Fatalf("events = %d withheld = %d", len(page.Events), page.Withheld)
	}
	if got := string(page.Events[1].Data); got != full {
		t.Errorf("reassembled\n got %s\nwant %s", got, full)
	}
	if e := page.Events[1]; e.Chunks != 3 || e.Index != 2 || e.Time.UnixMilli() != 2000 {
		t.Errorf("event = %+v", e)
	}
	if page.More || page.NextToken != "" {
		t.Errorf("last page has a next token: %+v", page)
	}
	c, err := DecodeReplayCursor(page.Resume)
	if err != nil || c == nil || c.Index != 3 || c.TsNano != 3000*1_000_000 {
		t.Errorf("resume = %+v, %v", c, err)
	}
	if page.Bytes != len(page.Events[0].Data)+len(full)+len(page.Events[2].Data) {
		t.Errorf("bytes = %d", page.Bytes)
	}
}

func TestReplayParseNeverReturnsHalfAnEvent(t *testing.T) {
	q := ReplayQuery{Scope: rumScope(), SessionID: "s1"}
	whole := `{"type":3,"data":{},"timestamp":1}`
	for name, rows := range map[string][][]any{
		"missing last chunk":   {replayRow(1, 1, 1, 2, `{"type":2,"data":`, 0)},
		"missing first chunk":  {replayRow(1, 1, 2, 2, `{}}`, 0)},
		"missing middle chunk": {replayRow(1, 1, 1, 3, `{"type":2,`, 0), replayRow(1, 1, 3, 3, `"data":{}}`, 0)},
		"disagreeing totals":   {replayRow(1, 1, 1, 2, `{"type":2,`, 0), replayRow(1, 1, 2, 3, `"data":{}}`, 0)},
		"chunk beyond total":   {replayRow(1, 1, 1, 1, `{"a":1}`, 0), replayRow(1, 1, 2, 1, `{"b":2}`, 0)},
		"zero total":           {replayRow(1, 1, 1, 0, whole, 0)},
		"truncated json":       {replayRow(1, 1, 1, 1, `{"type":3,"data":{"a":`, 0)},
		"not an object":        {replayRow(1, 1, 1, 1, `[1,2,3]`, 0)},
		"empty body":           {replayRow(1, 1, 1, 1, ``, 0)},
		"duplicate chunk only": {replayRow(1, 1, 1, 2, `{"type":2,`, 0), replayRow(1, 1, 1, 2, `{"type":2,`, 0)},
	} {
		page, err := q.Parse(&catalogTestRows{rows: rows})
		if err != nil {
			t.Fatal(name, err)
		}
		if len(page.Events) != 0 || page.Withheld != 1 || page.Resume != "" {
			t.Errorf("%s: events = %d, withheld = %d, resume = %q", name, len(page.Events), page.Withheld, page.Resume)
		}
	}
	// a duplicated chunk (a retried export) does not break the event
	page, err := q.Parse(&catalogTestRows{rows: [][]any{
		replayRow(1, 1, 1, 2, `{"type":2,`, 0), replayRow(1, 1, 1, 2, `{"type":2,`, 0), replayRow(1, 1, 2, 2, `"data":{}}`, 0),
	}})
	if err != nil || len(page.Events) != 1 || string(page.Events[0].Data) != `{"type":2,"data":{}}` {
		t.Errorf("duplicate chunk: %+v, %v", page, err)
	}
	// and a bad event does not hide its neighbours
	page, err = q.Parse(&catalogTestRows{rows: [][]any{
		replayRow(1, 1, 1, 1, whole, 0), replayRow(2, 2, 1, 2, `{"type":`, 0), replayRow(3, 3, 1, 1, whole, 0),
	}})
	if err != nil || len(page.Events) != 2 || page.Withheld != 1 {
		t.Errorf("neighbours: %+v, %v", page, err)
	}
}

func TestReplayParseKeepsBytesOpaque(t *testing.T) {
	// masked text, escapes, multi-byte runes and a chunk boundary inside an
	// escape must come back byte for byte
	body := `{"type":2,"data":{"t":"******** \u00e9 \"q\" é名前 ✓","m":"\\n"},"timestamp":1}`
	cut := strings.Index(body, `\"q\"`) + 1
	q := ReplayQuery{Scope: rumScope(), SessionID: "s1"}
	page, err := q.Parse(&catalogTestRows{rows: [][]any{
		replayRow(1, 1, 1, 2, body[:cut], 0), replayRow(1, 1, 2, 2, body[cut:], 0),
	}})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("%+v, %v", page, err)
	}
	if string(page.Events[0].Data) != body {
		t.Errorf("bytes changed:\n got %s\nwant %s", page.Events[0].Data, body)
	}
}

func TestReplayParseMoreAndEmpty(t *testing.T) {
	q := ReplayQuery{Scope: rumScope(), SessionID: "s1"}
	page, err := q.Parse(&catalogTestRows{rows: [][]any{replayRow(1, 1, 1, 1, `{"a":1}`, 1)}})
	if err != nil || !page.More || page.NextToken == "" || page.NextToken != page.Resume {
		t.Errorf("more page = %+v, %v", page, err)
	}
	page, err = q.Parse(&catalogTestRows{})
	if err != nil || page.Events == nil || len(page.Events) != 0 || page.More || page.NextToken != "" || page.Resume != "" {
		t.Errorf("empty page = %+v, %v", page, err)
	}
	if _, err := q.Parse(&catalogTestRows{rows: [][]any{{"x"}}}); err == nil {
		t.Error("a short row parsed")
	}
}
