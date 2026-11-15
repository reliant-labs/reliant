package testkit_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

type placeholderDeps struct {
	SQL    *sql.DB
	Tags   []string
	Index  map[string]int
	Events chan string
	Send   func(string) error
	Name   string
}

func TestPlaceholder_FillsEveryNilableKind(t *testing.T) {
	var d placeholderDeps
	testkit.Placeholder(&d.SQL, "Deps.SQL")
	testkit.Placeholder(&d.Tags, "Deps.Tags")
	testkit.Placeholder(&d.Index, "Deps.Index")
	testkit.Placeholder(&d.Events, "Deps.Events")
	testkit.Placeholder(&d.Send, "Deps.Send")
	testkit.Placeholder(&d.Name, "Deps.Name") // not nil-able: left alone

	if d.SQL == nil || d.Tags == nil || d.Index == nil || d.Events == nil || d.Send == nil {
		t.Fatalf("a nil-able field stayed nil: %+v", d)
	}
	if d.Name != "" {
		t.Fatalf("a non-nil-able field was touched: %q", d.Name)
	}
}

func TestPlaceholder_KeepsWhatTheTestSet(t *testing.T) {
	db := &sql.DB{}
	d := placeholderDeps{SQL: db}
	testkit.Placeholder(&d.SQL, "Deps.SQL")
	if d.SQL != db {
		t.Fatal("Placeholder replaced a dependency the test supplied")
	}
}

func TestPlaceholder_FuncPanicsNamingTheField(t *testing.T) {
	var d placeholderDeps
	testkit.Placeholder(&d.Send, "Deps.Send")
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "Deps.Send") {
			t.Fatalf("calling a placeholder func must panic naming the field, got %v", r)
		}
	}()
	_ = d.Send("x")
}

func TestUnfilledDepsHint(t *testing.T) {
	if got := testkit.UnfilledDepsHint("WithAccountDeps"); got != "" {
		t.Fatalf("no unfilled deps must add nothing to the failure, got %q", got)
	}
	got := testkit.UnfilledDepsHint("WithAccountDeps", "Deps.SQL (*sql.DB)")
	for _, want := range []string{"Deps.SQL (*sql.DB)", "WithAccountDeps(Deps{...})", "WithPlaceholderDeps()"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q is missing %q", got, want)
		}
	}
}
