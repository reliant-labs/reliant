package changes

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// everyoneSnapshot is a snapshot at revision rev whose change "banner" gives
// everyone the variant named on.
func everyoneSnapshot(rev int, on string) string {
	return fmt.Sprintf(`{"schema":"forge.changes/v1","revision":%d,"changes":{"banner":{
		"variants":{"off":"","blue":"blue","green":"green"},"default_variant":"off",
		"delivery":{"mode":"everyone","variant":%q}}}}`, rev, on)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	// Write and rename, as the platform does, so a reader never sees half.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer is a log sink the poll goroutine and the test share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testStore is a Store over path whose poll loop runs on tick, recording
// nothing, with its log in the returned buffer.
func testStore(t *testing.T, path string, tick chan time.Time) (*Store, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	s := newStore(path, Options{
		OnEvaluate: func(context.Context, string, Result) {},
		Logger:     slog.New(slog.NewTextHandler(logs, nil)),
	})
	s.start(context.Background(), tick, func() {})
	t.Cleanup(func() { _ = s.Close() })
	return s, logs
}

// polled makes the poll loop run one full reload: the second send on the
// unbuffered tick is received only once the loop is back from the first.
func polled(tick chan time.Time) {
	tick <- time.Time{}
	tick <- time.Time{}
}

func banner(s *Store) string {
	return String(s.Evaluate(context.Background(), "banner", EvalContext{}), "code-default")
}

func TestStoreFileAppearsLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	tick := make(chan time.Time)
	s, _ := testStore(t, path, tick)

	if got := s.Evaluate(context.Background(), "banner", EvalContext{}); got.Reason != ReasonUnknown {
		t.Fatalf("missing file: reason %q, want unknown", got.Reason)
	}
	if banner(s) != "code-default" || s.Revision() != 0 {
		t.Fatalf("missing file: %q at revision %d, want the code default at 0", banner(s), s.Revision())
	}

	writeFile(t, path, everyoneSnapshot(3, "blue"))
	polled(tick)
	if banner(s) != "blue" || s.Revision() != 3 {
		t.Fatalf("after the file appeared: %q at revision %d, want blue at 3", banner(s), s.Revision())
	}
}

func TestStoreFileRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, everyoneSnapshot(1, "blue"))
	tick := make(chan time.Time)
	s, _ := testStore(t, path, tick)
	if banner(s) != "blue" {
		t.Fatalf("opened: %q, want blue (OpenFile reads before returning)", banner(s))
	}

	writeFile(t, path, everyoneSnapshot(2, "green"))
	polled(tick)
	if banner(s) != "green" || s.Revision() != 2 {
		t.Fatalf("rewritten: %q at revision %d, want green at 2", banner(s), s.Revision())
	}
}

func TestStoreInvalidRewriteKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, everyoneSnapshot(1, "blue"))
	tick := make(chan time.Time)
	s, logs := testStore(t, path, tick)

	writeFile(t, path, `{"schema":"forge.changes/v9","revision":2,"changes":{}}`)
	polled(tick)
	polled(tick)
	if banner(s) != "blue" || s.Revision() != 1 {
		t.Fatalf("after an invalid rewrite: %q at revision %d, want the last good blue at 1", banner(s), s.Revision())
	}
	if n := strings.Count(logs.String(), "cannot parse"); n != 1 {
		t.Fatalf("one bad file polled twice warned %d times, want once:\n%s", n, logs.String())
	}

	writeFile(t, path, `not json`)
	polled(tick)
	if n := strings.Count(logs.String(), "cannot parse"); n != 2 {
		t.Fatalf("a second, different error warned %d times in all, want 2:\n%s", n, logs.String())
	}

	writeFile(t, path, everyoneSnapshot(3, "green"))
	polled(tick)
	if banner(s) != "green" || s.Revision() != 3 {
		t.Fatalf("after a good rewrite: %q at revision %d, want green at 3", banner(s), s.Revision())
	}
}

func TestStoreFileRemovedIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, everyoneSnapshot(1, "blue"))
	tick := make(chan time.Time)
	s, _ := testStore(t, path, tick)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	polled(tick)
	if banner(s) != "code-default" || s.Revision() != 0 {
		t.Fatalf("file removed: %q at revision %d, want the code default at 0", banner(s), s.Revision())
	}
}

func TestStoreEmptyPath(t *testing.T) {
	s := OpenFile(context.Background(), "", Options{OnEvaluate: func(context.Context, string, Result) {}})
	if banner(s) != "code-default" || s.Revision() != 0 {
		t.Fatalf("empty path: %q at revision %d, want the code default at 0", banner(s), s.Revision())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCloseStopsPolling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, everyoneSnapshot(1, "blue"))
	tick := make(chan time.Time)
	s, _ := testStore(t, path, tick)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Close returned only after the loop exited, so nothing receives a tick.
	select {
	case tick <- time.Time{}:
		t.Fatal("the poll loop received a tick after Close")
	default:
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	writeFile(t, path, everyoneSnapshot(2, "green"))
	if banner(s) != "blue" {
		t.Fatalf("after Close: %q, want the last snapshot, blue", banner(s))
	}
}

func TestStoreContextCancelStopsPolling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	ctx, cancel := context.WithCancel(context.Background())
	s := newStore(path, Options{OnEvaluate: func(context.Context, string, Result) {}})
	s.start(ctx, make(chan time.Time), func() {})
	cancel()
	<-s.done
}

func TestOpenFilePolls(t *testing.T) {
	// The real ticker, end to end: the only test here that waits on a clock.
	// It waits for an outcome, never for a fixed time, so a slow machine
	// makes it slower, not red.
	path := filepath.Join(t.TempDir(), "snapshot.json")
	s := OpenFile(context.Background(), path, Options{
		PollInterval: time.Millisecond,
		OnEvaluate:   func(context.Context, string, Result) {},
	})
	defer s.Close()
	writeFile(t, path, everyoneSnapshot(5, "green"))
	deadline := time.Now().Add(time.Minute)
	for s.Revision() != 5 {
		if time.Now().After(deadline) {
			t.Fatal("OpenFile's poll loop never picked up the new file")
		}
		time.Sleep(time.Millisecond)
	}
}
