package crud

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Real-postgres proof of the Owned* helpers: the declared ownership policy
// scopes every read and write, answers "not yours" like "does not exist",
// and rolls back a write whose result the caller would not own.

type ownChat struct {
	bun.BaseModel `bun:"table:chats,alias:chats"`

	ID     string `bun:"id,pk"`
	UserID string `bun:"user_id,notnull"`
}

type ownThread struct {
	bun.BaseModel `bun:"table:threads,alias:threads"`

	ID     string `bun:"id,pk"`
	ChatID string `bun:"chat_id,notnull"`
}

type ownPlan struct {
	bun.BaseModel `bun:"table:plans,alias:plans"`

	ID       string `bun:"id,pk"`
	ThreadID string `bun:"thread_id,notnull"`
	Title    string `bun:"title"`
}

type ownAccount struct {
	bun.BaseModel `bun:"table:accounts,alias:accounts"`

	ID     string `bun:"id,pk"`
	UserID string `bun:"user_id,nullzero"`
	OrgID  string `bun:"org_id,nullzero"`
	Name   string `bun:"name"`
}

type ownCaller struct {
	users []string
	orgs  []string
}

type ownCallerKey struct{}

func withOwnCaller(ctx context.Context, users []string, orgs ...string) context.Context {
	return context.WithValue(ctx, ownCallerKey{}, ownCaller{users: users, orgs: orgs})
}

// ownResolver resolves the caller carried by ctx; a ctx with none is ErrNoCaller.
var ownResolver = OwnerResolverFunc(func(ctx context.Context, kind string) ([]string, error) {
	c, ok := ctx.Value(ownCallerKey{}).(ownCaller)
	if !ok {
		return nil, ErrNoCaller
	}
	switch kind {
	case "user":
		return c.users, nil
	case "org":
		return c.orgs, nil
	}
	return nil, nil
})

var planOwnership = Ownership{Entity: "plan", Terms: []OwnerTerm{{
	Kind: "user", Column: "thread_id", Constraint: "plans_thread_id_fkey",
	Hops: []OwnerHop{
		{Table: "threads", Key: "id", Column: "chat_id"},
		{Table: "chats", Key: "id", Column: "user_id"},
	},
}}}

var accountOwnership = Ownership{Entity: "account", Terms: []OwnerTerm{
	{Kind: "user", Column: "user_id"},
	{Kind: "org", Column: "org_id"},
}}

// ownFixture wires a Repo's delegates the way a generated op does.
type ownFixture struct {
	t  *testing.T
	db orm.Context

	plans    *Repo[ownPlan]
	accounts *Repo[ownAccount]
}

func newOwnFixture(t *testing.T) *ownFixture {
	t.Helper()
	db := newRepoTestDB(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE chats (id TEXT PRIMARY KEY, user_id TEXT NOT NULL)`,
		`CREATE TABLE threads (id TEXT PRIMARY KEY, chat_id TEXT NOT NULL REFERENCES chats(id))`,
		`CREATE TABLE plans (id TEXT PRIMARY KEY, thread_id TEXT NOT NULL CONSTRAINT plans_thread_id_fkey REFERENCES threads(id), title TEXT)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, user_id TEXT NULL, org_id TEXT NULL, name TEXT)`,
		`INSERT INTO chats VALUES ('c1', 'u1'), ('c2', 'u2')`,
		`INSERT INTO threads VALUES ('t1', 'c1'), ('t1b', 'c1'), ('t2', 'c2')`,
		`INSERT INTO plans VALUES ('p1', 't1', 'mine'), ('p2', 't2', 'theirs')`,
		`INSERT INTO accounts VALUES
			('a-u1', 'u1', NULL, 'u1 personal'),
			('a-u2', 'u2', NULL, 'u2 personal'),
			('a-o1', NULL, 'o1', 'org o1'),
			('a-o2', NULL, 'o2', 'org o2'),
			('a-o3', NULL, 'o3', 'org o3')`,
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return &ownFixture{t: t, db: db, plans: NewRepo[ownPlan](), accounts: NewRepo[ownAccount]()}
}

func (f *ownFixture) planList(ctx context.Context, opts ...orm.QueryOption) ([]*ownPlan, error) {
	return f.plans.List(ctx, f.db, opts...)
}

func (f *ownFixture) accountList(ctx context.Context, opts ...orm.QueryOption) ([]*ownAccount, error) {
	return f.accounts.List(ctx, f.db, opts...)
}

func (f *ownFixture) getPlan(ctx context.Context, r OwnerResolver, id string) (*ownPlan, error) {
	return OwnedGet(ctx, r, planOwnership, "id", id, nil,
		func(ctx context.Context, id string) (*ownPlan, error) { return f.plans.Get(ctx, f.db, id) },
		f.planList)
}

func (f *ownFixture) deletePlan(ctx context.Context, r OwnerResolver, id string) error {
	return OwnedDelete(ctx, f.db, r, planOwnership, "id", id, nil,
		func(ctx context.Context, id string) error { return f.plans.Delete(ctx, f.db, id) },
		f.planList)
}

func (f *ownFixture) updatePlan(ctx context.Context, r OwnerResolver, p *ownPlan) error {
	return OwnedUpdate(ctx, f.db, r, planOwnership, "id", p.ID, p, nil,
		func(ctx context.Context, e *ownPlan) error { return f.plans.Update(ctx, f.db, e) },
		f.planList)
}

func (f *ownFixture) updatePlanMasked(ctx context.Context, r OwnerResolver, p *ownPlan, fields []string) error {
	return OwnedUpdateMasked(ctx, f.db, r, planOwnership, "id", p.ID, p, fields, nil,
		func(ctx context.Context, e *ownPlan, fields []string) error {
			return f.plans.UpdateMasked(ctx, f.db, e, fields)
		},
		f.planList)
}

func (f *ownFixture) createPlan(ctx context.Context, r OwnerResolver, p *ownPlan) error {
	return OwnedCreate(ctx, f.db, r, planOwnership, "id", p,
		func(e *ownPlan) string { return e.ID }, nil,
		func(ctx context.Context, e *ownPlan) error { return f.plans.Create(ctx, f.db, e) },
		f.planList)
}

// stampUser mirrors the generated stamp closure for accounts: fill the user
// column only when the row names no owner at all.
func stampUser(a *ownAccount, v string) {
	if a.UserID == "" && a.OrgID == "" {
		a.UserID = v
	}
}

func (f *ownFixture) createAccount(ctx context.Context, r OwnerResolver, a *ownAccount) error {
	return OwnedCreate(ctx, f.db, r, accountOwnership, "id", a,
		func(e *ownAccount) string { return e.ID }, stampUser,
		func(ctx context.Context, e *ownAccount) error { return f.accounts.Create(ctx, f.db, e) },
		f.accountList)
}

func (f *ownFixture) rows(table, id string) int64 {
	f.t.Helper()
	n, err := f.db.Bun().NewSelect().Table(table).Where("id = ?", id).Count(context.Background())
	if err != nil {
		f.t.Fatalf("count %s %s: %v", table, id, err)
	}
	return int64(n)
}

func (f *ownFixture) planThread(id string) string {
	f.t.Helper()
	var thread string
	if err := f.db.Bun().NewSelect().Table("plans").Column("thread_id").Where("id = ?", id).Scan(context.Background(), &thread); err != nil {
		f.t.Fatalf("read plan %s: %v", id, err)
	}
	return thread
}

func (f *ownFixture) planTitle(id string) string {
	f.t.Helper()
	var title string
	if err := f.db.Bun().NewSelect().Table("plans").Column("title").Where("id = ?", id).Scan(context.Background(), &title); err != nil {
		f.t.Fatalf("read plan %s: %v", id, err)
	}
	return title
}

func wantCode(t *testing.T, what string, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error, want %v", what, want)
	}
	if got := svcerr.Code(err); got != want {
		t.Fatalf("%s: code = %v (%v), want %v", what, got, err, want)
	}
}

func planIDs(ps []*ownPlan) []string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids
}

func accountIDs(as []*ownAccount) map[string]bool {
	ids := map[string]bool{}
	for _, a := range as {
		ids[a.ID] = true
	}
	return ids
}

// 1. The join path plan → thread → chat.user_id.
func TestOwnedPlans_JoinPath(t *testing.T) {
	f := newOwnFixture(t)
	u1 := withOwnCaller(context.Background(), []string{"u1"})

	t.Run("get", func(t *testing.T) {
		got, err := f.getPlan(u1, ownResolver, "p1")
		if err != nil || got.Title != "mine" {
			t.Fatalf("own plan: %+v, %v", got, err)
		}
		_, otherErr := f.getPlan(u1, ownResolver, "p2")
		wantCode(t, "another tenant's plan", otherErr, connect.CodeNotFound)
		_, unknownErr := f.getPlan(u1, ownResolver, "no-such-plan")
		wantCode(t, "an unknown plan", unknownErr, connect.CodeNotFound)
		if svcerr.Code(otherErr) != svcerr.Code(unknownErr) {
			t.Errorf("not-yours and unknown differ: %v vs %v", otherErr, unknownErr)
		}
	})

	t.Run("list and count agree", func(t *testing.T) {
		opts, err := OwnedList(u1, ownResolver, planOwnership, []orm.QueryOption{orm.WithOrderBy("id", orm.Asc)})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.planList(u1, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if ids := planIDs(rows); len(ids) != 1 || ids[0] != "p1" {
			t.Errorf("list = %v, want [p1]", ids)
		}
		n, err := f.plans.Count(u1, f.db, opts...)
		if err != nil || n != 1 {
			t.Errorf("count = %d, %v; want 1", n, err)
		}
		// u2 sees the other row; the unscoped table has both.
		u2 := withOwnCaller(context.Background(), []string{"u2"})
		opts2, _ := OwnedList(u2, ownResolver, planOwnership, nil)
		rows2, err := f.planList(u2, opts2...)
		if err != nil || len(rows2) != 1 || rows2[0].ID != "p2" {
			t.Errorf("u2 list = %v, %v; want [p2]", planIDs(rows2), err)
		}
	})

	t.Run("update", func(t *testing.T) {
		err := f.updatePlan(u1, ownResolver, &ownPlan{ID: "p2", ThreadID: "t2", Title: "hijacked"})
		wantCode(t, "update of another's plan", err, connect.CodeNotFound)
		if got := f.planTitle("p2"); got != "theirs" {
			t.Errorf("another's plan was written: title = %q", got)
		}
		if err := f.updatePlan(u1, ownResolver, &ownPlan{ID: "p1", ThreadID: "t1", Title: "renamed"}); err != nil {
			t.Fatalf("own update: %v", err)
		}
		if got := f.planTitle("p1"); got != "renamed" {
			t.Errorf("own plan title = %q, want renamed", got)
		}
		if err := f.updatePlanMasked(u1, ownResolver, &ownPlan{ID: "p1", Title: "masked"}, []string{"title"}); err != nil {
			t.Fatalf("own masked update: %v", err)
		}
		if got := f.planTitle("p1"); got != "masked" {
			t.Errorf("own plan title = %q, want masked", got)
		}
		err = f.updatePlanMasked(u1, ownResolver, &ownPlan{ID: "p2", Title: "masked"}, []string{"title"})
		wantCode(t, "masked update of another's plan", err, connect.CodeNotFound)
		if got := f.planTitle("p2"); got != "theirs" {
			t.Errorf("another's plan was written by a masked update: title = %q", got)
		}
	})

	t.Run("delete", func(t *testing.T) {
		err := f.deletePlan(u1, ownResolver, "p2")
		wantCode(t, "delete of another's plan", err, connect.CodeNotFound)
		if f.rows("plans", "p2") != 1 {
			t.Fatal("another's plan was deleted")
		}
		if err := f.deletePlan(u1, ownResolver, "p1"); err != nil {
			t.Fatalf("own delete: %v", err)
		}
		if f.rows("plans", "p1") != 0 {
			t.Error("own plan survived its delete")
		}
	})
}

// 2. user-OR-org alternatives.
func TestOwnedAccounts_UserOrOrg(t *testing.T) {
	f := newOwnFixture(t)
	list := func(ctx context.Context) (map[string]bool, int64) {
		t.Helper()
		opts, err := OwnedList(ctx, ownResolver, accountOwnership, nil)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.accountList(ctx, opts...)
		if err != nil {
			t.Fatal(err)
		}
		n, err := f.accounts.Count(ctx, f.db, opts...)
		if err != nil {
			t.Fatal(err)
		}
		return accountIDs(rows), n
	}
	get := func(ctx context.Context, id string) error {
		_, err := OwnedGet(ctx, ownResolver, accountOwnership, "id", id, nil,
			func(ctx context.Context, id string) (*ownAccount, error) { return f.accounts.Get(ctx, f.db, id) },
			f.accountList)
		return err
	}

	t.Run("personal plus member orgs only", func(t *testing.T) {
		ctx := withOwnCaller(context.Background(), []string{"u1"}, "o1", "o2")
		ids, n := list(ctx)
		if len(ids) != 3 || !ids["a-u1"] || !ids["a-o1"] || !ids["a-o2"] || n != 3 {
			t.Errorf("rows = %v (count %d), want a-u1, a-o1, a-o2", ids, n)
		}
		if err := get(ctx, "a-o2"); err != nil {
			t.Errorf("a member org's account: %v", err)
		}
		wantCode(t, "another user's account", get(ctx, "a-u2"), connect.CodeNotFound)
		wantCode(t, "an org the caller is not in", get(ctx, "a-o3"), connect.CodeNotFound)
	})

	t.Run("zero orgs still sees personal", func(t *testing.T) {
		ctx := withOwnCaller(context.Background(), []string{"u1"})
		ids, n := list(ctx)
		if len(ids) != 1 || !ids["a-u1"] || n != 1 {
			t.Errorf("rows = %v (count %d), want only a-u1", ids, n)
		}
	})

	t.Run("no values at all sees nothing", func(t *testing.T) {
		ctx := withOwnCaller(context.Background(), nil)
		ids, n := list(ctx)
		if len(ids) != 0 || n != 0 {
			t.Errorf("rows = %v (count %d), want none", ids, n)
		}
		wantCode(t, "get with no owner values", get(ctx, "a-u1"), connect.CodeNotFound)
	})
}

// 3. A missing caller, or no resolver at all, fails closed before any write.
func TestOwned_MissingCallerFailsClosed(t *testing.T) {
	f := newOwnFixture(t)
	anon := context.Background()

	_, err := f.getPlan(anon, ownResolver, "p1")
	wantCode(t, "get", err, connect.CodeUnauthenticated)
	if !errors.Is(err, ErrNoCaller) {
		t.Errorf("get: the ErrNoCaller cause was dropped: %v", err)
	}
	_, err = OwnedList(anon, ownResolver, planOwnership, nil)
	wantCode(t, "list", err, connect.CodeUnauthenticated)

	wantCode(t, "create", f.createPlan(anon, ownResolver, &ownPlan{ID: "p-new", ThreadID: "t1"}), connect.CodeUnauthenticated)
	wantCode(t, "update", f.updatePlan(anon, ownResolver, &ownPlan{ID: "p1", ThreadID: "t1", Title: "x"}), connect.CodeUnauthenticated)
	wantCode(t, "masked update", f.updatePlanMasked(anon, ownResolver, &ownPlan{ID: "p1", Title: "x"}, []string{"title"}), connect.CodeUnauthenticated)
	wantCode(t, "delete", f.deletePlan(anon, ownResolver, "p1"), connect.CodeUnauthenticated)
	if f.rows("plans", "p-new") != 0 {
		t.Error("a caller-less create wrote a row")
	}
	if f.rows("plans", "p1") != 1 || f.planTitle("p1") != "mine" {
		t.Error("a caller-less update or delete changed p1")
	}

	// No resolver wired: Internal, and nothing is written.
	ctx := withOwnCaller(context.Background(), []string{"u1"})
	_, err = f.getPlan(ctx, nil, "p1")
	wantCode(t, "get with nil resolver", err, connect.CodeInternal)
	wantCode(t, "create with nil resolver", f.createPlan(ctx, nil, &ownPlan{ID: "p-new", ThreadID: "t1"}), connect.CodeInternal)
	wantCode(t, "update with nil resolver", f.updatePlan(ctx, nil, &ownPlan{ID: "p1", ThreadID: "t1", Title: "x"}), connect.CodeInternal)
	wantCode(t, "delete with nil resolver", f.deletePlan(ctx, nil, "p1"), connect.CodeInternal)
	if f.rows("plans", "p-new") != 0 || f.rows("plans", "p1") != 1 || f.planTitle("p1") != "mine" {
		t.Error("a nil-resolver write changed the table")
	}

	// Accounts: the same for the stamping create.
	err = f.createAccount(anon, ownResolver, &ownAccount{ID: "a-new", Name: "x"})
	wantCode(t, "account create", err, connect.CodeUnauthenticated)
	if f.rows("accounts", "a-new") != 0 {
		t.Error("a caller-less account create wrote a row")
	}
}

// 4. OwnedCreate.
func TestOwnedCreate_Plans(t *testing.T) {
	f := newOwnFixture(t)
	u1 := withOwnCaller(context.Background(), []string{"u1"})

	if err := f.createPlan(u1, ownResolver, &ownPlan{ID: "p-ok", ThreadID: "t1", Title: "new"}); err != nil {
		t.Fatalf("plan under own thread: %v", err)
	}
	if f.rows("plans", "p-ok") != 1 {
		t.Fatal("own create left no row")
	}

	otherErr := f.createPlan(u1, ownResolver, &ownPlan{ID: "p-x", ThreadID: "t2"})
	wantCode(t, "plan under another user's thread", otherErr, connect.CodePermissionDenied)
	if f.rows("plans", "p-x") != 0 {
		t.Error("a refused create left a row")
	}

	missingErr := f.createPlan(u1, ownResolver, &ownPlan{ID: "p-y", ThreadID: "no-such-thread"})
	wantCode(t, "plan under a nonexistent thread", missingErr, connect.CodePermissionDenied)
	if f.rows("plans", "p-y") != 0 {
		t.Error("a create under a missing thread left a row")
	}
	if svcerr.Code(otherErr) != svcerr.Code(missingErr) {
		t.Errorf("not-yours and nonexistent parents are distinguishable: %v vs %v", otherErr, missingErr)
	}
}

func TestOwnedCreate_AccountStampAndOrgs(t *testing.T) {
	f := newOwnFixture(t)
	ctx := withOwnCaller(context.Background(), []string{"u1"}, "o1")

	// No owner named: the caller's one user id is stamped.
	if err := f.createAccount(ctx, ownResolver, &ownAccount{ID: "a-new1", Name: "stamped"}); err != nil {
		t.Fatalf("create with no owner: %v", err)
	}
	var stored ownAccount
	if err := f.db.Bun().NewSelect().Model(&stored).Where("id = ?", "a-new1").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stored.UserID != "u1" || stored.OrgID != "" {
		t.Errorf("stored owner = user %q org %q, want user u1 and no org", stored.UserID, stored.OrgID)
	}

	// An org the caller is not in: refused and rolled back.
	err := f.createAccount(ctx, ownResolver, &ownAccount{ID: "a-new2", OrgID: "o9", Name: "x"})
	wantCode(t, "account in a foreign org", err, connect.CodePermissionDenied)
	if f.rows("accounts", "a-new2") != 0 {
		t.Error("a refused account create left a row")
	}

	// An org the caller is in: allowed, and not also filed under the creator.
	if err := f.createAccount(ctx, ownResolver, &ownAccount{ID: "a-new3", OrgID: "o1", Name: "x"}); err != nil {
		t.Fatalf("account in own org: %v", err)
	}
	stored = ownAccount{}
	if err := f.db.Bun().NewSelect().Model(&stored).Where("id = ?", "a-new3").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stored.UserID != "" || stored.OrgID != "o1" {
		t.Errorf("stored owner = user %q org %q, want org o1 only", stored.UserID, stored.OrgID)
	}

	// Several user ids: there is no one to stamp, so an ownerless row is refused.
	multi := withOwnCaller(context.Background(), []string{"u1", "u1-alt"})
	err = f.createAccount(multi, ownResolver, &ownAccount{ID: "a-new4", Name: "x"})
	wantCode(t, "ownerless create with an ambiguous caller", err, connect.CodePermissionDenied)
	if f.rows("accounts", "a-new4") != 0 {
		t.Error("an ambiguous ownerless create left a row")
	}
}

// 5. An update that would move the row out of the caller's ownership is
// refused and rolled back.
func TestOwnedUpdate_MoveToAnotherTenantRollsBack(t *testing.T) {
	f := newOwnFixture(t)
	u1 := withOwnCaller(context.Background(), []string{"u1"})

	err := f.updatePlan(u1, ownResolver, &ownPlan{ID: "p1", ThreadID: "t2", Title: "moved"})
	wantCode(t, "full update to another user's thread", err, connect.CodePermissionDenied)
	if got := f.planThread("p1"); got != "t1" {
		t.Errorf("thread_id = %q after the refused update, want t1 (rolled back)", got)
	}
	if got := f.planTitle("p1"); got != "mine" {
		t.Errorf("title = %q after the refused update, want mine (rolled back)", got)
	}

	err = f.updatePlanMasked(u1, ownResolver, &ownPlan{ID: "p1", ThreadID: "t2", Title: "moved"}, []string{"thread_id", "title"})
	wantCode(t, "masked update to another user's thread", err, connect.CodePermissionDenied)
	if got := f.planThread("p1"); got != "t1" {
		t.Errorf("thread_id = %q after the refused masked update, want t1", got)
	}
	if got := f.planTitle("p1"); got != "mine" {
		t.Errorf("title = %q after the refused masked update, want mine", got)
	}

	// A nonexistent thread answers the same refusal, and also changes nothing.
	err = f.updatePlan(u1, ownResolver, &ownPlan{ID: "p1", ThreadID: "no-such-thread", Title: "moved"})
	wantCode(t, "update to a nonexistent thread", err, connect.CodePermissionDenied)
	if got := f.planThread("p1"); got != "t1" {
		t.Errorf("thread_id = %q after the refused update, want t1", got)
	}

	// Moving between the caller's own threads is fine.
	if err := f.updatePlan(u1, ownResolver, &ownPlan{ID: "p1", ThreadID: "t1b", Title: "mine"}); err != nil {
		t.Fatalf("move within own threads: %v", err)
	}
	if got := f.planThread("p1"); got != "t1b" {
		t.Errorf("thread_id = %q, want t1b", got)
	}
}

// An owned write inside a caller's transaction is refused before it runs
// (B1/B2 of the review): RunTx would join that transaction, so neither the
// SERIALIZABLE check-then-write nor the rollback of a refused write would
// hold. A caller that catches the refusal and commits keeps nothing.
func TestOwnedWrites_RefuseAnAmbientTransaction(t *testing.T) {
	f := newOwnFixture(t)
	u1 := withOwnCaller(context.Background(), []string{"u1"})

	var createErr, updateErr, maskedErr, deleteErr error
	err := f.db.RunTx(u1, orm.ReadCommitted, func(ctx context.Context) error {
		// The unauthorized create and move the post-check would refuse, and
		// a delete of the caller's own row: all refused before any write.
		createErr = f.createPlan(ctx, ownResolver, &ownPlan{ID: "p-amb", ThreadID: "t2"})
		updateErr = f.updatePlan(ctx, ownResolver, &ownPlan{ID: "p1", ThreadID: "t2", Title: "moved"})
		maskedErr = f.updatePlanMasked(ctx, ownResolver, &ownPlan{ID: "p1", ThreadID: "t2"}, []string{"thread_id"})
		deleteErr = f.deletePlan(ctx, ownResolver, "p1")
		return nil // the caller swallows the refusals and commits
	})
	if err != nil {
		t.Fatalf("outer transaction: %v", err)
	}
	for name, e := range map[string]error{"create": createErr, "update": updateErr, "masked update": maskedErr, "delete": deleteErr} {
		wantCode(t, name+" in an ambient transaction", e, connect.CodeFailedPrecondition)
	}
	if f.rows("plans", "p-amb") != 0 {
		t.Error("a create refused inside a committed outer transaction left a row")
	}
	if got := f.planThread("p1"); got != "t1" {
		t.Errorf("thread_id = %q after the outer commit, want t1", got)
	}
	if f.rows("plans", "p1") != 1 {
		t.Error("the delete ran inside the ambient transaction")
	}

	// Reads still join: a Get inside the caller's transaction is scoped.
	err = f.db.RunTx(u1, orm.ReadCommitted, func(ctx context.Context) error {
		if _, err := f.getPlan(ctx, ownResolver, "p1"); err != nil {
			t.Errorf("own get in an ambient transaction: %v", err)
		}
		_, err := f.getPlan(ctx, ownResolver, "p2")
		wantCode(t, "other's get in an ambient transaction", err, connect.CodeNotFound)
		return nil
	})
	if err != nil {
		t.Fatalf("outer read transaction: %v", err)
	}

	// Outside a transaction the same writes behave as before.
	if err := f.createPlan(u1, ownResolver, &ownPlan{ID: "p-own", ThreadID: "t1"}); err != nil {
		t.Fatalf("own create outside a transaction: %v", err)
	}
}

// The same refusal for an explicit transaction handle passed as the
// Transactor with a ctx that carries no transaction: *orm.Tx's RunTx joins
// it at its own level, so the helper refuses before writing, and a caller
// that swallows the refusal and commits keeps nothing.
func TestOwnedWrites_RefuseAnExplicitTransactionHandle(t *testing.T) {
	f := newOwnFixture(t)
	u1 := withOwnCaller(context.Background(), []string{"u1"})

	tx, err := f.db.BeginDetached(context.Background(), orm.ReadCommitted)
	if err != nil {
		t.Fatalf("BeginDetached: %v", err)
	}
	createErr := OwnedCreate(u1, tx, ownResolver, planOwnership, "id", &ownPlan{ID: "p-tx", ThreadID: "t2"},
		func(e *ownPlan) string { return e.ID }, nil,
		func(ctx context.Context, e *ownPlan) error { return f.plans.Create(ctx, tx, e) },
		func(ctx context.Context, opts ...orm.QueryOption) ([]*ownPlan, error) {
			return f.plans.List(ctx, tx, opts...)
		})
	updateErr := OwnedUpdate(u1, tx, ownResolver, planOwnership, "id", "p1", &ownPlan{ID: "p1", ThreadID: "t2", Title: "moved"}, nil,
		func(ctx context.Context, e *ownPlan) error { return f.plans.Update(ctx, tx, e) },
		func(ctx context.Context, opts ...orm.QueryOption) ([]*ownPlan, error) {
			return f.plans.List(ctx, tx, opts...)
		})
	deleteErr := OwnedDelete(u1, tx, ownResolver, planOwnership, "id", "p1", nil,
		func(ctx context.Context, id string) error { return f.plans.Delete(ctx, tx, id) },
		func(ctx context.Context, opts ...orm.QueryOption) ([]*ownPlan, error) {
			return f.plans.List(ctx, tx, opts...)
		})
	if err := tx.Commit(); err != nil { // the caller swallows the refusals and commits
		t.Fatalf("commit: %v", err)
	}
	for name, e := range map[string]error{"create": createErr, "update": updateErr, "delete": deleteErr} {
		wantCode(t, name+" with an explicit *orm.Tx", e, connect.CodeFailedPrecondition)
	}
	if f.rows("plans", "p-tx") != 0 {
		t.Error("a create refused on an explicit transaction left a row after its commit")
	}
	if got := f.planThread("p1"); got != "t1" {
		t.Errorf("thread_id = %q after the commit, want t1", got)
	}
	if f.rows("plans", "p1") != 1 {
		t.Error("the delete ran on the explicit transaction")
	}
}
