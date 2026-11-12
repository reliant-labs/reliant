package obsbackend

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocalTraceExtentIsOneAggregateOverTheTrace(t *testing.T) {
	fr := &fakeRunner{answers: []fakeAnswer{{"count() AS spans", Result{Rows: [][]any{{num64("158864"), "2026-10-22 10:15:20.143000000", num64("1761128150203000000")}}}}}}
	ext, err := (Local{Env: "dev", Runner: fr}).TraceExtent(context.Background(), TraceRequest{Narrow: rng(), TraceID: "6CDCC2B8D409E05528B82B2687A58AF2"})
	if err != nil {
		t.Fatal(err)
	}
	want := TraceExtent{Spans: 158_864, Start: time.Date(2026, 10, 22, 10, 15, 20, 143_000_000, time.UTC), End: time.Unix(0, 1761128150203000000).UTC()}
	if ext != want {
		t.Errorf("extent = %+v, want %+v", ext, want)
	}
	if len(fr.ran) != 1 || !strings.Contains(fr.ran[0].SQL, "TraceId = {") || !strings.Contains(fr.ran[0].SQL, "default.otel_traces") {
		t.Errorf("one parameterized aggregate over the local traces: %+v", fr.ran)
	}

	// No span in the range: an aggregate still answers one row, of zeros.
	none := &fakeRunner{answers: []fakeAnswer{{"count() AS spans", Result{Rows: [][]any{{num64("0"), "1970-01-01 00:00:00.000000000", num64("0")}}}}}}
	if ext, err := (Local{Env: "dev", Runner: none}).TraceExtent(context.Background(), TraceRequest{Narrow: rng(), TraceID: "ab"}); err != nil || ext != (TraceExtent{}) {
		t.Errorf("no spans = %+v, %v", ext, err)
	}
	if _, err := (Local{Env: "dev", Runner: none}).TraceExtent(context.Background(), TraceRequest{Narrow: rng(), TraceID: "not hex"}); exitOf(err) != ExitWrong {
		t.Errorf("a malformed id is the caller's fault: %v", err)
	}
}

func TestCloudTraceExtentIsTheSpansGetTraceReturns(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetTrace: `{"spans":[
		{"spanId":"b","startTime":"2026-10-22T10:15:20.200Z","durationNs":"1000000"},
		{"spanId":"a","startTime":"2026-10-22T10:15:20.143Z","durationNs":"30060000000"}]}`}}
	ext, err := cloudWith(f).TraceExtent(context.Background(), TraceRequest{Narrow: narrowC(), TraceID: "6cdcc2b8d409e05528b82b2687a58af2"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 22, 10, 15, 20, 143_000_000, time.UTC)
	if ext.Spans != 2 || !ext.Start.Equal(start) || !ext.End.Equal(start.Add(30060*time.Millisecond)) {
		t.Errorf("extent = %+v", ext)
	}
	f.replies[procGetTrace] = `{"spans":[]}`
	if ext, err := cloudWith(f).TraceExtent(context.Background(), TraceRequest{Narrow: narrowC(), TraceID: "ab"}); err != nil || ext.Spans != 0 {
		t.Errorf("a trace with no spans is not an error here, the caller widens: %+v %v", ext, err)
	}
}
