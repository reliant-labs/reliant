package crud

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Owned* scope a generated CRUD op to the caller's rows, from a DECLARED
// ownership policy rather than a hand-written override.
//
// # Why this exists
//
// The Scoped* helpers (scoped.go) take a predicate an override attaches.
// That left every table's access check to be written by hand into the
// owned handlers_crud.go, once per op, and a missed one is one tenant
// reading or writing another's rows. Worse, the common shapes could not be
// expressed by the one-column `forge:owner` reminder at all: a plan owned
// through its thread's chat (plans.thread_id → threads.chat_id →
// chats.user_id), or an account owned by a user OR by any org its caller
// belongs to.
//
// A table now declares its owner in its migration,
//
//	COMMENT ON TABLE plans    IS 'forge:owner=thread_id>chat_id>user_id';
//	COMMENT ON TABLE accounts IS 'forge:owner=user:user_id,org:org_id';
//
// forge resolves that against the applied schema into an Ownership value,
// and the generated op routes every read and write through these helpers.
// Who the caller is, and which orgs they belong to, comes from the
// application through OwnerResolver: forge does not know a project's org
// model, and inventing one is exactly the domain policy it does not own.
//
// # The answers
//
//   - No caller (the resolver says ErrNoCaller), or no resolver wired at
//     all: denied, before any statement runs. The op fails closed.
//   - A row outside the caller's scope: NotFound, the same answer an unknown
//     id gets. "Exists but is not yours" would tell an attacker which ids
//     are real.
//   - A Create or Update whose RESULT the caller would not own (a plan filed
//     under someone else's thread, an account moved to an org the caller is
//     not in): PermissionDenied, and the write is rolled back.
//
// # Why the writes run in one SERIALIZABLE transaction
//
// The check that matters for a write is on the row as it will be STORED,
// and through a join path that cannot be read off the submitted entity —
// it depends on rows of other tables. So the write happens and the row is
// then looked up again under the same scope, inside one transaction that
// the failed lookup rolls back. That one rule covers a direct owner column,
// a path, alternatives, a full replace and an update_mask alike, with no
// per-shape logic to get wrong. SERIALIZABLE also closes the race scoped.go
// documents: a concurrent change of the owner between the check and the
// write aborts this transaction, which RunTx re-runs against the committed
// state.
//
// # Why an owned write refuses to join a caller's transaction
//
// Both guarantees above need the transaction to be the helper's own. Inside
// a caller's transaction, RunTx joins it (pkg/orm) and the requested
// SERIALIZABLE is ignored: at the caller's READ COMMITTED a concurrent owner
// change between the scoped lookup and the PK-only write goes unseen. And the
// helper cannot roll its own write back there (the orm has no savepoints): a
// caller that handles the PermissionDenied and commits would keep the very
// write the refusal was about. The same holds for a Transactor that IS a
// transaction (an explicit *orm.Tx, from BeginDetached or RunTransaction):
// its RunTx is a join too. So Create, Update and Delete answer
// FailedPrecondition, before any statement runs, when ctx already carries a
// transaction or tx is one. Reads (Get, List) need neither guarantee and join as usual. A
// workflow that must write owned rows inside its own transaction checks
// ownership itself in an override (the Scoped* helpers, scoped.go).
//
// A write that names a parent which does not exist fails its foreign key
// before the post-check could run. Answered as such, it would tell the
// caller which parent ids exist; a violation of a path's first foreign key
// therefore answers the same PermissionDenied as a parent that exists but
// is someone else's.
//
// # What the path does not do
//
// It follows the reference, not the parent's liveness: a soft-deleted
// thread still owns its plans for the purpose of scoping. Whether a child
// of a deleted parent should be readable is a product rule, written in an
// override.

// OwnerResolver tells the generated CRUD who the caller is. It is provided
// by the application, wired into each service's Deps as `Owners`.
type OwnerResolver interface {
	// CallerOwners returns the values of kind the caller owns rows through:
	// for "user" typically the caller's one id, for "org" every org they
	// are a member of. kind is whatever the table's `forge:owner=`
	// declaration names; forge attaches no meaning to it. Return
	// ErrNoCaller (wrapped is fine) when the request carries no caller.
	CallerOwners(ctx context.Context, kind string) ([]string, error)
}

// OwnerResolverFunc adapts a function to OwnerResolver.
type OwnerResolverFunc func(ctx context.Context, kind string) ([]string, error)

// CallerOwners implements OwnerResolver.
func (f OwnerResolverFunc) CallerOwners(ctx context.Context, kind string) ([]string, error) {
	return f(ctx, kind)
}

// OwnerKinds is an OwnerResolver from one function per owner kind — the
// usual way to write one, returned from a provider in
// internal/app/providers.go:
//
//	return crud.OwnerKinds{
//		"user": func(ctx context.Context) ([]string, error) {
//			claims, err := middleware.GetUser(ctx)
//			if err != nil {
//				return nil, crud.ErrNoCaller
//			}
//			return []string{claims.UserID}, nil
//		},
//	}, nil
//
// A kind with no function is an Internal error, never an empty scope: a
// table that names a kind the app forgot to resolve is a wiring bug, and
// answering it with "the caller owns nothing" would hide it.
type OwnerKinds map[string]func(ctx context.Context) ([]string, error)

// CallerOwners implements OwnerResolver.
func (k OwnerKinds) CallerOwners(ctx context.Context, kind string) ([]string, error) {
	fn, ok := k[kind]
	if !ok || fn == nil {
		return nil, svcerr.Internal(fmt.Sprintf("crud.OwnerKinds resolves no owner kind %q", kind))
	}
	return fn(ctx)
}

// ErrNoCaller is what an OwnerResolver returns for a request with no
// caller. The Owned* helpers answer it with Unauthenticated.
var ErrNoCaller = errors.New("crud: the request carries no caller")

// Ownership is a table's declared ownership policy: a row belongs to the
// caller when ANY term matches. forge generates one per owned entity from
// its `forge:owner=` declaration.
type Ownership struct {
	// Entity labels the errors ("plan").
	Entity string
	Terms  []OwnerTerm
}

// OwnerTerm is one alternative owner of a row.
type OwnerTerm struct {
	// Kind is handed to OwnerResolver.CallerOwners.
	Kind string
	// Column is a column of the owned table: the owner column itself when
	// Hops is empty, else the foreign key the path starts from.
	Column string
	// Constraint is Column's foreign-key constraint when Hops is non-empty.
	// A write that violates it is refused like any write the caller would
	// not own, so the answer reveals nothing about which parents exist.
	Constraint string
	Hops       []OwnerHop
}

// OwnerHop is one foreign-key step of a path.
type OwnerHop struct {
	// Schema qualifies Table; "" or "public" leaves it unqualified.
	Schema string
	Table  string
	// Key is the column of Table the previous column references.
	Key string
	// Column is read on Table next: the next hop's foreign key, or the
	// owner column on the last hop.
	Column string
}

// Transactor runs fn in a transaction carried by its ctx. orm.Context (a
// service's `DB` dep) satisfies it.
type Transactor interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// callerOwners resolves the caller's values for every kind the policy
// names, once per kind.
func (o Ownership) callerOwners(ctx context.Context, r OwnerResolver) (map[string][]string, error) {
	if r == nil {
		return nil, svcerr.Internal(fmt.Sprintf("%s is owner-scoped but no crud.OwnerResolver is wired (Deps.Owners)", o.Entity))
	}
	vals := make(map[string][]string, len(o.Terms))
	for _, t := range o.Terms {
		if _, done := vals[t.Kind]; done {
			continue
		}
		v, err := r.CallerOwners(ctx, t.Kind)
		if err != nil {
			if errors.Is(err, ErrNoCaller) {
				return nil, svcerr.WithCause(svcerr.Unauthenticated("no caller"), err)
			}
			return nil, err
		}
		vals[t.Kind] = v
	}
	return vals, nil
}

// Scope resolves the caller and returns the predicate that narrows a
// query on the owned table to the caller's rows. A kind the caller has no
// values for drops its term; with none left the predicate matches nothing.
func (o Ownership) Scope(ctx context.Context, r OwnerResolver) (orm.QueryOption, error) {
	vals, err := o.callerOwners(ctx, r)
	if err != nil {
		return nil, err
	}
	return o.predicate(vals), nil
}

func (o Ownership) predicate(vals map[string][]string) orm.QueryOption {
	type clause struct {
		sql  string
		args []any
	}
	var clauses []clause
	for _, t := range o.Terms {
		v := vals[t.Kind]
		if len(v) == 0 {
			continue
		}
		sql, args := t.sql(v)
		clauses = append(clauses, clause{sql, args})
	}
	return func(q *bun.SelectQuery) {
		if len(clauses) == 0 {
			q.Where("FALSE")
			return
		}
		q.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			for _, c := range clauses {
				sq = sq.WhereOr(c.sql, c.args...)
			}
			return sq
		})
	}
}

// sql renders one term: `col IN (?)` for a direct owner, a nested IN
// subquery per hop for a path. The outer column stays unqualified, as every
// other generated filter is; each hop gets its own alias so a column of a
// hop never resolves against an outer table.
func (t OwnerTerm) sql(values []string) (string, []any) {
	var b strings.Builder
	args := []any{bun.Ident(t.Column)}
	b.WriteString("? IN (")
	for i, h := range t.Hops {
		alias := fmt.Sprintf("forge_owner_%d", i+1)
		table := h.Table
		if h.Schema != "" && h.Schema != "public" {
			table = h.Schema + "." + h.Table
		}
		b.WriteString("SELECT ?.? FROM ? AS ? WHERE ?.? IN (")
		args = append(args, bun.Ident(alias), bun.Ident(h.Key), bun.Ident(table), bun.Ident(alias), bun.Ident(alias), bun.Ident(h.Column))
	}
	b.WriteString("?")
	args = append(args, orm.ArgList(values))
	b.WriteString(strings.Repeat(")", len(t.Hops)+1))
	return b.String(), args
}

// refusesWrite reports a write error that violated a path's first foreign
// key — a parent that does not exist, answered as one the caller does not
// own.
func (o Ownership) refusesWrite(err error) bool {
	name := orm.ConstraintName(err)
	if name == "" {
		return false
	}
	for _, t := range o.Terms {
		if t.Constraint != "" && t.Constraint == name {
			return true
		}
	}
	return false
}

// refuseAmbient is the fail-closed answer to an owned write called inside a
// caller's transaction, which could neither hold SERIALIZABLE nor roll back
// a refused write (see "Why an owned write refuses to join a caller's
// transaction").
func (o Ownership) refuseAmbient(ctx context.Context, tx Transactor, op string) error {
	why := ""
	switch {
	case orm.InTx(ctx):
		why = "ctx already carries one"
	case isTxHandle(tx):
		why = "the handle it was given is one (*orm.Tx)"
	default:
		return nil
	}
	return svcerr.FailedPrecondition(fmt.Sprintf("%s: an owner-scoped %s runs in a transaction of its own, and %s: "+
		"call it with the database client outside the transaction, or check ownership in an override (crud.Scoped*)", o.Entity, op, why))
}

// isTxHandle reports whether tx is an open transaction rather than a client
// that begins one: *orm.Tx's RunTx joins it, whatever level is asked for.
func isTxHandle(tx Transactor) bool {
	_, ok := tx.(*orm.Tx)
	return ok
}

func (o Ownership) refusal(reason string) error {
	return svcerr.PermissionDenied(fmt.Sprintf("%s: %s", o.Entity, reason))
}

// OwnedList appends the caller's scope to opts. A List op's Query and Count
// both call it, so total_count never counts a row the page would hide.
func OwnedList(ctx context.Context, r OwnerResolver, own Ownership, opts []orm.QueryOption) ([]orm.QueryOption, error) {
	scope, err := own.Scope(ctx, r)
	if err != nil {
		return nil, err
	}
	out := make([]orm.QueryOption, 0, len(opts)+1)
	return append(append(out, opts...), scope), nil
}

// OwnedGet resolves one row by primary key within the caller's scope. A row
// outside it is NotFound, exactly as an unknown id is.
func OwnedGet[Ent any](
	ctx context.Context,
	r OwnerResolver,
	own Ownership,
	pkColumn, id string,
	opts []orm.QueryOption,
	get func(ctx context.Context, id string) (Ent, error),
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) (Ent, error) {
	scoped, err := OwnedList(ctx, r, own, opts)
	if err != nil {
		var zero Ent
		return zero, err
	}
	return ScopedGet(ctx, pkColumn, id, scoped, get, list)
}

// OwnedDelete removes one row by primary key when it is the caller's,
// answering NotFound otherwise. The check and the delete share one
// SERIALIZABLE transaction of its own: called inside a caller's transaction
// it answers FailedPrecondition and runs nothing.
func OwnedDelete[Ent any](
	ctx context.Context,
	tx Transactor,
	r OwnerResolver,
	own Ownership,
	pkColumn, id string,
	opts []orm.QueryOption,
	del func(ctx context.Context, id string) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	if err := own.refuseAmbient(ctx, tx, "delete"); err != nil {
		return err
	}
	scoped, err := OwnedList(ctx, r, own, opts)
	if err != nil {
		return err
	}
	return tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if _, err := scopedLookup(ctx, pkColumn, id, scoped, list); err != nil {
			return err
		}
		return del(ctx, id)
	})
}

// OwnedUpdate writes an entity by primary key when the STORED row is the
// caller's (NotFound otherwise), and refuses with PermissionDenied — rolling
// the write back — when the written row would no longer be theirs.
func OwnedUpdate[Ent any](
	ctx context.Context,
	tx Transactor,
	r OwnerResolver,
	own Ownership,
	pkColumn, id string,
	entity Ent,
	opts []orm.QueryOption,
	update func(ctx context.Context, entity Ent) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	return ownedUpdate(ctx, tx, r, own, pkColumn, id, opts, list, func(ctx context.Context) error {
		return update(ctx, entity)
	})
}

// OwnedUpdateMasked is OwnedUpdate for the AIP-134 masked write.
func OwnedUpdateMasked[Ent any](
	ctx context.Context,
	tx Transactor,
	r OwnerResolver,
	own Ownership,
	pkColumn, id string,
	entity Ent,
	fields []string,
	opts []orm.QueryOption,
	update func(ctx context.Context, entity Ent, fields []string) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	return ownedUpdate(ctx, tx, r, own, pkColumn, id, opts, list, func(ctx context.Context) error {
		return update(ctx, entity, fields)
	})
}

func ownedUpdate[Ent any](
	ctx context.Context,
	tx Transactor,
	r OwnerResolver,
	own Ownership,
	pkColumn, id string,
	opts []orm.QueryOption,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
	write func(ctx context.Context) error,
) error {
	if err := own.refuseAmbient(ctx, tx, "update"); err != nil {
		return err
	}
	scope, err := own.Scope(ctx, r)
	if err != nil {
		return err
	}
	pre := append(append(make([]orm.QueryOption, 0, len(opts)+1), opts...), scope)
	return tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if _, err := scopedLookup(ctx, pkColumn, id, pre, list); err != nil {
			return err
		}
		if err := write(ctx); err != nil {
			if own.refusesWrite(err) {
				return own.refusal("the update would take it out of the caller's ownership")
			}
			return err
		}
		if _, err := scopedLookup(ctx, pkColumn, id, []orm.QueryOption{scope}, list); err != nil {
			if errors.Is(err, orm.ErrNoRows) {
				return own.refusal("the update would take it out of the caller's ownership")
			}
			return err
		}
		return nil
	})
}

// OwnedCreate inserts an entity the caller will own, and refuses with
// PermissionDenied — rolling the insert back — one they would not.
//
// stamp, when non-nil, is called with the caller's value for the FIRST
// term's kind when the caller has exactly one (a user id, never a choice
// among several orgs); forge generates it to fill that term's column only
// when the row names no owner at all, so a client need not send its own
// user id, and an org-owned row is not also filed under its creator.
func OwnedCreate[Ent any](
	ctx context.Context,
	tx Transactor,
	r OwnerResolver,
	own Ownership,
	pkColumn string,
	entity Ent,
	id func(Ent) string,
	stamp func(Ent, string),
	create func(ctx context.Context, entity Ent) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	if err := own.refuseAmbient(ctx, tx, "create"); err != nil {
		return err
	}
	vals, err := own.callerOwners(ctx, r)
	if err != nil {
		return err
	}
	if stamp != nil && len(own.Terms) > 0 {
		if v := vals[own.Terms[0].Kind]; len(v) == 1 {
			stamp(entity, v[0])
		}
	}
	scope := own.predicate(vals)
	return tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := create(ctx, entity); err != nil {
			if own.refusesWrite(err) {
				return own.refusal("the caller would not own it")
			}
			return err
		}
		if _, err := scopedLookup(ctx, pkColumn, id(entity), []orm.QueryOption{scope}, list); err != nil {
			if errors.Is(err, orm.ErrNoRows) {
				return own.refusal("the caller would not own it")
			}
			return err
		}
		return nil
	})
}
