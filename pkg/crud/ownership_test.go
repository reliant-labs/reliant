package crud

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// renderOwnerScope renders the predicate against the postgres dialect with
// no database, over a bare table expression.
func renderOwnerScope(t *testing.T, opt orm.QueryOption) string {
	t.Helper()
	q := bun.NewDB(nil, pgdialect.New()).NewSelect().TableExpr("plans")
	opt(q)
	return q.String()
}

func staticOwners(vals map[string][]string) OwnerResolver {
	return OwnerResolverFunc(func(_ context.Context, kind string) ([]string, error) {
		return vals[kind], nil
	})
}

func TestOwnershipScope_RendersNestedInSubquery(t *testing.T) {
	own := Ownership{Entity: "plan", Terms: []OwnerTerm{{
		Kind: "user", Column: "thread_id", Constraint: "plans_thread_id_fkey",
		Hops: []OwnerHop{
			{Table: "threads", Key: "id", Column: "chat_id"},
			{Table: "chats", Key: "id", Column: "user_id"},
		},
	}}}
	scope, err := own.Scope(context.Background(), staticOwners(map[string][]string{"user": {"u1"}}))
	if err != nil {
		t.Fatal(err)
	}
	got := renderOwnerScope(t, scope)
	want := `SELECT * FROM plans WHERE (("thread_id" IN (` +
		`SELECT "forge_owner_1"."id" FROM "threads" AS "forge_owner_1" WHERE "forge_owner_1"."chat_id" IN (` +
		`SELECT "forge_owner_2"."id" FROM "chats" AS "forge_owner_2" WHERE "forge_owner_2"."user_id" IN ('u1')` +
		`))))`
	if got != want {
		t.Errorf("rendered scope\n got: %s\nwant: %s", got, want)
	}
}

func TestOwnershipScope_DirectAlternativesAreORdAndSkipEmptyKinds(t *testing.T) {
	own := Ownership{Entity: "account", Terms: []OwnerTerm{
		{Kind: "user", Column: "user_id"},
		{Kind: "org", Column: "org_id"},
	}}
	ctx := context.Background()

	both, err := own.Scope(ctx, staticOwners(map[string][]string{"user": {"u1"}, "org": {"o1", "o2"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := renderOwnerScope(t, both),
		`SELECT * FROM plans WHERE (("user_id" IN ('u1')) OR ("org_id" IN ('o1', 'o2')))`; got != want {
		t.Errorf("both kinds\n got: %s\nwant: %s", got, want)
	}

	// A kind with no values drops its term: an empty IN () would be a
	// syntax error, and an empty org list must not hide personal rows.
	userOnly, err := own.Scope(ctx, staticOwners(map[string][]string{"user": {"u1"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := renderOwnerScope(t, userOnly),
		`SELECT * FROM plans WHERE (("user_id" IN ('u1')))`; got != want {
		t.Errorf("user only\n got: %s\nwant: %s", got, want)
	}
}

func TestOwnershipScope_SchemaQualifiesHopTables(t *testing.T) {
	own := Ownership{Entity: "plan", Terms: []OwnerTerm{{
		Kind: "user", Column: "thread_id",
		Hops: []OwnerHop{
			{Schema: "cp", Table: "threads", Key: "id", Column: "chat_id"},
			{Schema: "public", Table: "chats", Key: "id", Column: "user_id"},
		},
	}}}
	scope, err := own.Scope(context.Background(), staticOwners(map[string][]string{"user": {"u1"}}))
	if err != nil {
		t.Fatal(err)
	}
	got := renderOwnerScope(t, scope)
	if !strings.Contains(got, `FROM "cp"."threads" AS "forge_owner_1"`) {
		t.Errorf("a non-public schema must qualify the hop table: %s", got)
	}
	if !strings.Contains(got, `FROM "chats" AS "forge_owner_2"`) {
		t.Errorf("schema public must leave the hop table unqualified: %s", got)
	}
}

func TestOwnershipScope_NoValuesMatchesNothing(t *testing.T) {
	own := Ownership{Entity: "account", Terms: []OwnerTerm{
		{Kind: "user", Column: "user_id"},
		{Kind: "org", Column: "org_id"},
	}}
	scope, err := own.Scope(context.Background(), staticOwners(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := renderOwnerScope(t, scope), `SELECT * FROM plans WHERE (FALSE)`; got != want {
		t.Errorf("no values\n got: %s\nwant: %s", got, want)
	}
}

func TestOwnershipScope_FailsClosed(t *testing.T) {
	own := Ownership{Entity: "plan", Terms: []OwnerTerm{{Kind: "user", Column: "user_id"}}}
	ctx := context.Background()

	if _, err := own.Scope(ctx, nil); svcerr.Code(err) != svcerr.Code(svcerr.Internal("x")) {
		t.Errorf("nil resolver: %v, want Internal", err)
	}

	noCaller := OwnerResolverFunc(func(context.Context, string) ([]string, error) {
		return nil, ErrNoCaller
	})
	_, err := own.Scope(ctx, noCaller)
	if svcerr.Code(err) != svcerr.Code(svcerr.Unauthenticated("x")) {
		t.Errorf("no caller: %v, want Unauthenticated", err)
	}
	if !errors.Is(err, ErrNoCaller) {
		t.Errorf("the ErrNoCaller cause was dropped: %v", err)
	}

	boom := errors.New("resolver down")
	_, err = own.Scope(ctx, OwnerResolverFunc(func(context.Context, string) ([]string, error) { return nil, boom }))
	if !errors.Is(err, boom) {
		t.Errorf("other resolver errors pass through: %v", err)
	}
}

func TestOwnershipScope_OneResolveCallPerKind(t *testing.T) {
	own := Ownership{Entity: "x", Terms: []OwnerTerm{
		{Kind: "user", Column: "a"}, {Kind: "user", Column: "b"}, {Kind: "org", Column: "c"},
	}}
	calls := map[string]int{}
	_, err := own.Scope(context.Background(), OwnerResolverFunc(func(_ context.Context, kind string) ([]string, error) {
		calls[kind]++
		return []string{"v"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if calls["user"] != 1 || calls["org"] != 1 {
		t.Errorf("resolver calls = %v, want one per kind", calls)
	}
}

func TestOwnerKinds(t *testing.T) {
	r := OwnerKinds{"user": func(context.Context) ([]string, error) { return []string{"u1"}, nil }}
	if got, err := r.CallerOwners(context.Background(), "user"); err != nil || len(got) != 1 || got[0] != "u1" {
		t.Fatalf("user = %v, %v", got, err)
	}
	if _, err := r.CallerOwners(context.Background(), "org"); !errors.Is(err, svcerr.ErrInternal) {
		t.Fatalf("an unresolved kind = %v, want Internal", err)
	}
}
