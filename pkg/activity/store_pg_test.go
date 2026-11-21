package activity

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestWithSchema_TableName(t *testing.T) {
	for _, tc := range []struct {
		opts []StoreOption
		want string
	}{
		{nil, `activity_events`},
		{[]StoreOption{WithSchema("")}, `activity_events`},
		{[]StoreOption{WithSchema("app")}, `"app".activity_events`},
		{[]StoreOption{WithSchema(`we"ird`)}, `"we""ird".activity_events`},
	} {
		if got := NewStore(nil, tc.opts...).table; got != tc.want {
			t.Errorf("table = %s, want %s", got, tc.want)
		}
	}
}

// newPGStore boots postgres, applies SchemaSQL and returns a Store over an
// orm client, the way a forge project wires it.
func newPGStore(t *testing.T) (*Store, *orm.Client) {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), SchemaSQL); err != nil {
		t.Fatalf("apply SchemaSQL: %v", err)
	}
	client, err := orm.NewClientWithDB(db, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(client), client
}

func TestStore_WriteAndList(t *testing.T) {
	store, _ := newPGStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	evs := []Event{
		{OccurredAt: base, Scope: "p1", Actor: User("sean", "sean"), Verb: "promote", Target: Target{Kind: "release", ID: "r1"}, Env: "prod", Via: "cli", Detail: map[string]string{"from_env": "e2e"}},
		{OccurredAt: base.Add(time.Minute), Scope: "p1", Actor: Machine("ci", "ci"), Verb: "check", Target: Target{Kind: "commit", ID: "0a07"}, Via: "ci"},
		{OccurredAt: base.Add(2 * time.Minute), Scope: "p1", Actor: System("converger"), Verb: "observe", Target: Target{Kind: "deploy_tree", ID: "t1"}, Env: "prod", Via: "reliant"},
		{OccurredAt: base.Add(3 * time.Minute), Scope: "p2", Actor: User("ana", "ana"), Verb: "promote", Target: Target{Kind: "release", ID: "r9"}, Env: "prod", Via: "console"},
	}
	for _, ev := range evs {
		if err := store.Write(ctx, ev); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	page, err := store.List(ctx, Filter{Scope: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 || page.NextPageToken != "" {
		t.Fatalf("scope p1: %d events, token %q; want 3 and no next page", len(page.Events), page.NextPageToken)
	}
	if page.Events[0].Verb != "observe" || page.Events[2].Verb != "promote" {
		t.Errorf("not newest first: %s .. %s", page.Events[0].Verb, page.Events[2].Verb)
	}
	first := page.Events[2]
	if first.ID == "" || first.Target.Label != "r1" || first.Actor.Kind != ActorUser || first.Detail["from_env"] != "e2e" {
		t.Errorf("round trip lost data: %+v", first)
	}

	for _, tc := range []struct {
		name string
		f    Filter
		want int
	}{
		{"by verb", Filter{Scope: "p1", Verb: "promote"}, 1},
		{"by actor kind", Filter{Scope: "p1", ActorKind: ActorMachine}, 1},
		{"by target", Filter{Scope: "p1", TargetKind: "release", TargetID: "r1"}, 1},
		{"by env", Filter{Scope: "p1", Env: "prod"}, 2},
		{"by via", Filter{Scope: "p1", Via: "ci"}, 1},
		{"since", Filter{Scope: "p1", Since: base.Add(time.Minute)}, 2},
		{"until", Filter{Scope: "p1", Until: base.Add(time.Minute)}, 1},
		{"other scope", Filter{Scope: "p2"}, 1},
	} {
		page, err := store.List(ctx, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(page.Events) != tc.want {
			t.Errorf("%s: %d events, want %d", tc.name, len(page.Events), tc.want)
		}
	}

	// Keyset paging walks every row once.
	var seen []string
	f := Filter{Scope: "p1", PageSize: 2}
	for {
		page, err := store.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range page.Events {
			seen = append(seen, ev.Target.ID)
		}
		if page.NextPageToken == "" {
			break
		}
		f.PageToken = page.NextPageToken
	}
	if len(seen) != 3 || seen[0] != "t1" || seen[2] != "r1" {
		t.Errorf("paged = %v, want [t1 0a07 r1]", seen)
	}

	if _, err := store.List(ctx, Filter{Scope: "p1", PageToken: "garbage"}); !errors.Is(err, ErrBadPageToken) {
		t.Errorf("bad token: err = %v, want ErrBadPageToken", err)
	}
}

// A scope is a path: an org's events and its projects' events are read
// together (descendants) or a project's with its org's (ancestors), and a
// sibling whose name shares a prefix, or a LIKE wildcard in a name, never
// leaks in.
func TestStore_HierarchicalScopes(t *testing.T) {
	store, _ := newPGStore(t)
	ctx := context.Background()
	for _, sc := range []struct{ scope, env string }{
		{"org_1", ""}, {"org_1/billing", "prod"}, {"org_1/billing", "e2e"}, {"org_1/web", "prod"},
		{"org_10", ""}, {"org_10/billing", "prod"}, {"org_%", ""}, {"org_%/x", "prod"},
	} {
		ev := Event{Scope: sc.scope, Env: sc.env, Actor: User("u", "u"), Verb: "promote", Target: Target{Kind: "release", ID: sc.scope + "@" + sc.env}}
		if err := store.Write(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(f Filter) []string {
		t.Helper()
		page, err := store.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, ev := range page.Events {
			out = append(out, ev.Target.ID)
		}
		sort.Strings(out)
		return out
	}
	for _, tc := range []struct {
		name string
		f    Filter
		want []string
	}{
		{"exact", Filter{Scope: "org_1/billing"}, []string{"org_1/billing@e2e", "org_1/billing@prod"}},
		{"project with its org", Filter{Scope: "org_1/billing", IncludeAncestors: true}, []string{"org_1/billing@e2e", "org_1/billing@prod", "org_1@"}},
		{"org with its projects", Filter{Scope: "org_1", IncludeDescendants: true}, []string{"org_1/billing@e2e", "org_1/billing@prod", "org_1/web@prod", "org_1@"}},
		{"wildcard is literal", Filter{Scope: "org_%", IncludeDescendants: true}, []string{"org_%/x@prod", "org_%@"}},
		{"envs any-of", Filter{Scope: "org_1", IncludeDescendants: true, Envs: []string{"prod"}}, []string{"org_1/billing@prod", "org_1/web@prod"}},
	} {
		if got := ids(tc.f); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestScopeSet(t *testing.T) {
	if got := scopeSet(Filter{Scope: "a/b/c", IncludeAncestors: true}); !slices.Equal(got, []string{"a/b/c", "a/b", "a"}) {
		t.Errorf("ancestors = %v", got)
	}
	if got := scopeSet(Filter{Scope: "a/b"}); !slices.Equal(got, []string{"a/b"}) {
		t.Errorf("exact = %v", got)
	}
}

// TestStore_JoinsTheOrmTransaction proves the HANDLER writer's guarantee:
// an event recorded inside RunTx rolls back with the transaction.
func TestStore_JoinsTheOrmTransaction(t *testing.T) {
	store, client := newPGStore(t)
	ctx := context.Background()
	ev := Event{Scope: "p1", Actor: User("sean", "sean"), Verb: "rotate", Target: Target{Kind: "secret", ID: "s1"}}

	boom := errors.New("mutation failed")
	err := client.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := Record(ctx, store, ev); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("RunTx = %v, want the mutation's error", err)
	}
	if page, _ := store.List(ctx, Filter{Scope: "p1"}); len(page.Events) != 0 {
		t.Fatalf("rolled-back transaction left %d events", len(page.Events))
	}

	if err := client.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { return Record(ctx, store, ev) }); err != nil {
		t.Fatal(err)
	}
	if page, _ := store.List(ctx, Filter{Scope: "p1"}); len(page.Events) != 1 {
		t.Fatalf("committed transaction left %d events, want 1", len(page.Events))
	}
}
