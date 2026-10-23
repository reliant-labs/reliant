package crud

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Scoped* compose a primary-key operation with an ADDITIONAL predicate.
//
// # Why these exist
//
// GetOp.Fetch, DeleteOp.Persist and UpdateOp.Persist take variadic
// orm.QueryOption so an application can attach an ownership predicate to
// a PK-keyed operation. The generated repository delegates those shims
// call — db.Get<Entity>ByID, db.Delete<Entity>, db.Update<Entity> — are
// keyed by PK alone and accept no options, so a shim that simply passed
// its opts along would not compile, and a shim that dropped them would
// build a seam that silently ignores the one thing it was widened for.
// Either way the author writes an owner filter, sees a green build, and
// ships an unscoped RPC — which is the failure this whole seam exists to
// prevent, reintroduced one layer down.
//
// These helpers close that gap using only the functions the generator
// already emits publicly: the list delegate takes options, so a PK
// predicate composed with the caller's predicate is one query.
//
// # What is and is not atomic
//
// ScopedGet is a single SELECT: the ownership predicate is IN the query,
// so a row that is not the caller's is never read, and "not yours" and
// "does not exist" are the same answer — a distinguishable rejection is
// an id oracle.
//
// ScopedDelete and ScopedUpdate cannot be, because the write delegates
// are PK-only. They verify the scope with a SELECT and then write, so a
// concurrent change of the owner column between the two statements is not
// excluded. That race is narrow, and the alternative is worse in a way
// that matters: it is exactly the fetch-then-compare an author would
// otherwise have to remember to write by hand, in every handler,
// correctly. Generated once and named is strictly better than remembered
// five times. An application that needs the check and the write to be
// atomic wraps the closure body in s.deps.DB.RunTx(ctx, orm.Serializable,
// …): both statements then run in one SERIALIZABLE transaction carried by
// ctx, and a concurrent owner change aborts this one and re-runs the check
// against the committed row. It must be orm.Serializable — the level has no
// default — because nothing here locks the row: at orm.ReadCommitted the
// check and the write would each see their own committed snapshot and the
// race would be back.
//
// # Zero options is the untouched path
//
// With no options every helper calls the plain PK delegate, unchanged.
// A shim nobody has scoped therefore issues exactly the query it always
// did — the extra machinery costs nothing until someone uses it.

// scopedLookup runs the PK-plus-predicate SELECT shared by every helper
// below, returning a not-found error (svcerr.NotFound, errors.Is-true for
// orm.ErrNoRows) when no row satisfies both. The list
// delegate applies the repository's own read scoping (soft delete), so
// the composed predicate narrows that scope rather than replacing it.
func scopedLookup[Ent any](
	ctx context.Context,
	pkColumn string,
	id any,
	opts []orm.QueryOption,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) (Ent, error) {
	var zero Ent
	// The PK predicate and the limit go LAST so the caller's options
	// cannot displace them: orm.WithLimit is last-wins.
	q := make([]orm.QueryOption, 0, len(opts)+2)
	q = append(q, opts...)
	q = append(q, orm.WhereEq(pkColumn, id), orm.WithLimit(1))

	rows, err := list(ctx, q...)
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		// The same answer the PK delegate gives a missing row (Repo's
		// notFound): svcerr.NotFound, errors.Is-true for orm.ErrNoRows.
		return zero, fmt.Errorf("%s = %v: %w", pkColumn, id,
			svcerr.WithCause(svcerr.NotFound(entityLabelOf[Ent]()), orm.ErrNoRows))
	}
	return rows[0], nil
}

// ScopedGet resolves one row by primary key, narrowed by opts.
//
// With no opts it delegates to get verbatim. With opts it runs a single
// SELECT carrying both the PK and the caller's predicate; a row that
// exists but does not satisfy the predicate reports the same not-found error
// an unknown id gets (svcerr.NotFound, errors.Is-true for orm.ErrNoRows), which
// HandleGet maps to NotFound — the same answer an unknown id gets.
func ScopedGet[Ent any](
	ctx context.Context,
	pkColumn string,
	id string,
	opts []orm.QueryOption,
	get func(ctx context.Context, id string) (Ent, error),
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) (Ent, error) {
	if len(opts) == 0 {
		return get(ctx, id)
	}
	return scopedLookup(ctx, pkColumn, id, opts, list)
}

// ScopedDelete removes one row by primary key, refusing when opts exclude
// it. See the package-level note above on why the check and the write are
// two statements.
func ScopedDelete[Ent any](
	ctx context.Context,
	pkColumn string,
	id string,
	opts []orm.QueryOption,
	del func(ctx context.Context, id string) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	if len(opts) == 0 {
		return del(ctx, id)
	}
	if _, err := scopedLookup(ctx, pkColumn, id, opts, list); err != nil {
		return err
	}
	return del(ctx, id)
}

// ScopedUpdate writes an entity by primary key, refusing when opts exclude
// the STORED row.
//
// The predicate is evaluated against what is in the database, never
// against the submitted entity: a caller who rewrites the owner column in
// the request they send would otherwise pass a check made against their
// own submission and take ownership of someone else's row.
func ScopedUpdate[Ent any](
	ctx context.Context,
	pkColumn string,
	id string,
	entity Ent,
	opts []orm.QueryOption,
	update func(ctx context.Context, entity Ent) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	if len(opts) == 0 {
		return update(ctx, entity)
	}
	if _, err := scopedLookup(ctx, pkColumn, id, opts, list); err != nil {
		return err
	}
	return update(ctx, entity)
}

// ScopedUpdateMasked is ScopedUpdate for the AIP-134 masked write. The
// mask decides WHICH columns are written; opts decide WHETHER the stored
// row may be written at all, and the two are independent.
func ScopedUpdateMasked[Ent any](
	ctx context.Context,
	pkColumn string,
	id string,
	entity Ent,
	fields []string,
	opts []orm.QueryOption,
	update func(ctx context.Context, entity Ent, fields []string) error,
	list func(ctx context.Context, opts ...orm.QueryOption) ([]Ent, error),
) error {
	if len(opts) == 0 {
		return update(ctx, entity, fields)
	}
	if _, err := scopedLookup(ctx, pkColumn, id, opts, list); err != nil {
		return err
	}
	return update(ctx, entity, fields)
}

// entityLabelOf is the entity a not-found error names for rows of type Ent
// (a model or a pointer to one): its type name, lower-cased, as Repo names it.
func entityLabelOf[Ent any]() string {
	t := reflect.TypeFor[Ent]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return strings.ToLower(t.Name())
}
