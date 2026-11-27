package testkit

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
)

const maxLogRecords = 1000

// Logger returns a test-bound slog logger suitable for a component's
// Deps.Logger. It keeps the latest records (including Debug) and prints them
// with t.Log on failure. Passing tests stay quiet, even under -v. Call
// Logger before starting the component so later cleanups run before the
// buffer is flushed. Records written after the flush are dropped.
//
// To see a component's logs even on a pass, use a logger writing to
// t.Output() instead. To discard them entirely, use [DiscardLogger].
func Logger(t testing.TB) *slog.Logger {
	t.Helper()
	log := &testLog{}
	t.Cleanup(func() { log.flush(t) })
	return slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// slog.TextHandler makes one Write per record, including from derived
// handlers (WithAttrs and WithGroup). Keep a bounded tail for chatty tests.
type testLog struct {
	mu      sync.Mutex
	records []string
	next    int
	dropped int
	closed  bool
}

func (l *testLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		record := strings.TrimSuffix(string(p), "\n")
		if len(l.records) < maxLogRecords {
			l.records = append(l.records, record)
		} else {
			l.records[l.next] = record
			l.next = (l.next + 1) % maxLogRecords
			l.dropped++
		}
	}
	return len(p), nil
}

func (l *testLog) flush(t testing.TB) {
	l.mu.Lock()
	l.closed = true
	records := l.records
	l.records = nil
	l.mu.Unlock()
	if !t.Failed() || len(records) == 0 {
		return
	}
	if l.dropped > 0 {
		t.Logf("testkit.Logger: dropped %d earlier records", l.dropped)
	}
	var out strings.Builder
	out.WriteString("server log (testkit.Logger):")
	for i := range records {
		out.WriteString("\n  ")
		out.WriteString(records[(l.next+i)%len(records)])
	}
	t.Log(out.String())
}
