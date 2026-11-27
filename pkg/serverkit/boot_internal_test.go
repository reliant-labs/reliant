package serverkit

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

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

// The progress log, driven tick by tick: the period is an hour, so no timer
// fires during the test, and each tick is a direct logProgress call. This
// replaces a wall-clock version deleted as flaky (forge#1634).
func TestBootProbes_Constructing_LogsASlowNodeAtEachTick(t *testing.T) {
	old := bootProgressEvery
	bootProgressEvery = time.Hour
	t.Cleanup(func() { bootProgressEvery = old })

	var out bytes.Buffer
	b := &BootProbes{srv: &http.Server{}, logger: slog.New(slog.NewTextHandler(&out, nil)), pending: map[string]struct{}{}}
	t.Cleanup(b.Close)
	b.Constructing("fast")(nil) // done before any tick: never logged
	doneOuter := b.Constructing("worker")
	doneInner := b.Constructing("db_repo")
	if b.progress == nil {
		t.Fatal("constructing a node scheduled no progress tick")
	}

	b.logProgress()
	b.logProgress()
	log := out.String()
	if got := strings.Count(log, "still constructing"); got != 2 {
		t.Fatalf("two ticks logged %d progress lines:\n%s", got, log)
	}
	if !strings.Contains(log, "node=db_repo") || !strings.Contains(log, "reached_from=[worker]") || !strings.Contains(log, "built=1") {
		t.Fatalf("the progress line does not name the innermost node, its path and what is built:\n%s", log)
	}
	if strings.Contains(log, "fast") {
		t.Fatalf("a node built before the first tick was logged:\n%s", log)
	}

	// db_repo has been constructing longer than the period: its end is logged.
	// worker has not: its end is not.
	b.mu.Lock()
	b.building[1].since = time.Now().Add(-2 * bootProgressEvery)
	b.mu.Unlock()
	doneInner(nil)
	doneOuter(nil)
	log = out.String()
	if !strings.Contains(log, `msg="boot: graph node built" node=db_repo`) {
		t.Fatalf("the slow node's end was not logged:\n%s", log)
	}
	if strings.Contains(log, `msg="boot: graph node built" node=worker`) {
		t.Fatalf("a node faster than the period had its end logged:\n%s", log)
	}

	// Idle, a tick logs nothing and does not reschedule itself.
	n := strings.Count(log, "still constructing")
	b.logProgress()
	if got := strings.Count(out.String(), "still constructing"); got != n {
		t.Fatalf("an idle tick logged %d progress lines", got-n)
	}
	if b.progress != nil {
		t.Fatal("an idle tick rescheduled itself")
	}

	// Closed, constructing schedules nothing and a tick logs nothing.
	b.Close()
	b.Constructing("late")
	b.logProgress()
	if b.progress != nil || strings.Count(out.String(), "still constructing") != n {
		t.Fatalf("a closed BootProbes scheduled or logged progress:\n%s", out.String())
	}
}
