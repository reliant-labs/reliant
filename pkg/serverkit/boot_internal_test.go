package serverkit

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for a logger's goroutine and the test's.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func checkNames(cs []readyzCheckStatus) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		s := c.Name
		if c.OK {
			s += "+"
		}
		if c.Detail != "" {
			s += "(" + c.Detail + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestBootProbes_Constructing_ListsBuiltThenBuildingThenPending(t *testing.T) {
	b := &BootProbes{logger: slog.Default(), pending: map[string]struct{}{}}
	b.pend("schema")
	doneOuter := b.Constructing("worker")
	doneInner := b.Constructing("db_repo")
	if got, want := checkNames(b.checks()), "worker(building) db_repo(building) schema"; got != want {
		t.Fatalf("checks = %q, want %q", got, want)
	}
	doneInner(nil)
	doneInner(errors.New("a second done is ignored"))
	b.clear("schema")
	if got, want := checkNames(b.checks()), "db_repo+ worker(building)"; got != want {
		t.Fatalf("checks = %q, want %q", got, want)
	}
	doneFailed := b.Constructing("cache")
	doneFailed(errors.New("dial refused"))
	doneOuter(nil)
	// A failed node is dropped, and its error never reaches the body.
	if got, want := checkNames(b.checks()), "db_repo+ worker+"; got != want {
		t.Fatalf("checks = %q, want %q", got, want)
	}
}

func TestBootProbes_Constructing_NilIsInert(t *testing.T) {
	var b *BootProbes
	b.Constructing("x")(nil)
}

func TestBootProbes_Constructing_LogsASlowNodeAtTheProgressRate(t *testing.T) {
	old := bootProgressEvery
	bootProgressEvery = 20 * time.Millisecond
	t.Cleanup(func() { bootProgressEvery = old })

	var out syncBuffer
	b := &BootProbes{srv: &http.Server{}, logger: slog.New(slog.NewTextHandler(&out, nil)), pending: map[string]struct{}{}}
	b.Constructing("fast")(nil) // done before the first tick: never logged
	doneOuter := b.Constructing("worker")
	doneInner := b.Constructing("db_repo")
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(out.String(), "still constructing") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("no repeated progress line; log:\n%s", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	doneInner(nil)
	doneOuter(nil)
	b.Close()
	log := out.String()
	if !strings.Contains(log, "node=db_repo") || !strings.Contains(log, "reached_from=[worker]") {
		t.Fatalf("the progress line does not name the innermost node and its path:\n%s", log)
	}
	if strings.Contains(log, "fast") {
		t.Fatalf("a node built before the first tick was logged:\n%s", log)
	}
	if !strings.Contains(log, `msg="boot: graph node built" node=db_repo`) {
		t.Fatalf("the slow node's end was not logged:\n%s", log)
	}
	// Idle and closed, the timer is gone: nothing more is logged. (One tick
	// already past its check may still be writing; let it land first.)
	time.Sleep(bootProgressEvery)
	n := strings.Count(out.String(), "still constructing")
	time.Sleep(4 * bootProgressEvery)
	if got := strings.Count(out.String(), "still constructing"); got != n {
		t.Fatalf("logged %d more progress lines after every node was built", got-n)
	}
}
