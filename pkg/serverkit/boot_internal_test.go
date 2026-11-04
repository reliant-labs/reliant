package serverkit

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
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
