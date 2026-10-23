package crud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Reason codes: the machine-readable half of every error this package
// puts on the wire.
//
// # Why they exist
//
// A Connect code and a human message are not enough to route a UI. The
// code is too coarse (three different CRUD failures all arrive as
// InvalidArgument) and the message is not a contract at all — it is
// display copy, and forge reserves the right to reword it. Left with
// only those two, callers reach for the one discriminator that appears
// to work: matching the prose. A real dogfood run produced exactly that,
// `text.includes("already exists")`, on a duplicate-email conflict, which
// is the failure mode forge's own frontend runtime documentation forbids
// (web-runtime/src/errors.ts: key off `error.reason`, never the message).
// The instruction was right and the header did not exist.
//
// Every error returned by this package now carries one of the codes
// below as svcerr.ReasonHeader ("x-forge-error-reason") metadata, which
// connect-go merges into the HTTP response headers for unary calls and
// into the end-stream trailer for streaming ones. The frontend reads it
// as ConnectClientError.reason.
//
// # Design rules these names follow
//
//  1. A reason names the CAUSE, not the Connect code. A reason that
//     merely echoes the code ("already_exists" for CodeAlreadyExists)
//     carries no information the client did not already have, and it is
//     the name that must be SPLIT later when a second cause maps to the
//     same code — breaking every caller keyed on it. Cause names never
//     need splitting; new causes get new names.
//  2. The vocabulary is TOTAL. Every error path here sets a reason,
//     including the unclassified one (ReasonInternal). A frontend that
//     switches on `error.reason` must never fall through to null, or it
//     is back to sniffing prose for whatever the switch missed. This is
//     the property the wire tests assert.
//  3. Reasons are engine-neutral. They describe what the CALLER did
//     wrong (or what the server could not do), not which SQLSTATE
//     postgres returned. "duplicate" outlives a migration to a different
//     database; "unique_violation" is postgres vocabulary leaking into a
//     public contract. The violated constraint's NAME belongs in the
//     MESSAGE, never in the reason: a reason is the stable thing a switch
//     is written against, and constraint names move with migrations.
//  4. Reasons distinguish only what a client can ACT on differently.
//     Where the client's only move is "report it and retry", the reason
//     is ReasonInternal, however the failure arose.
//
// These are a public wire contract. Adding a code is cheap; changing the
// meaning of one is not.
const (
	// ReasonNotFound — the addressed row does not exist (or is
	// soft-deleted). Wire code: NotFound.
	ReasonNotFound = "not_found"

	// ReasonDuplicate — the write collided with a uniqueness constraint:
	// some value the caller supplied is already taken. The message names
	// the constraint the write violated (see pgFailure.identity), which is
	// what tells a person which value to change; a form that wants
	// per-field placement still routes on this code, not on the name.
	// Wire code: AlreadyExists.
	ReasonDuplicate = "duplicate"

	// ReasonReferenceMissing — a create/update pointed at a related
	// record that does not exist. The caller's move is to pick a valid
	// reference. Wire code: FailedPrecondition.
	ReasonReferenceMissing = "reference_missing"

	// ReasonReferenceInUse — a delete was refused because other records
	// still reference this one. The caller's move is to remove the
	// dependents first — a materially different remedy from
	// ReasonReferenceMissing, which is why the two are separate codes
	// even though postgres reports both as SQLSTATE 23503.
	// Wire code: FailedPrecondition.
	ReasonReferenceInUse = "reference_in_use"

	// ReasonRequiredFieldMissing — a required value was absent: a NOT
	// NULL column with no value, or a request message missing the entity
	// it is supposed to carry. Wire code: InvalidArgument.
	ReasonRequiredFieldMissing = "required_field_missing"

	// ReasonConstraintViolated — a value was well-formed and present but
	// outside what the data model permits (a CHECK constraint). Distinct
	// from ReasonInvalidFormat, where the value could not even be parsed.
	// Wire code: InvalidArgument.
	ReasonConstraintViolated = "constraint_violated"

	// ReasonInvalidFormat — a value could not be parsed into its column's
	// type (a malformed UUID, a malformed JSON document).
	// Wire code: InvalidArgument.
	ReasonInvalidFormat = "invalid_format"

	// ReasonUnknownField — an update_mask path names a field the caller
	// may not update: not a column at all, a column the repository owns
	// (forge:version, GENERATED), or a field the wire declares read-only
	// (forge:read-only / forge:computed, see UpdateOp.ReadOnly). One code
	// for all three because the caller's move is the same — drop the path.
	// Wire code: InvalidArgument.
	ReasonUnknownField = "unknown_field"

	// ReasonVersionConflict — the row was modified by another writer
	// between this caller's read and its write, on an entity that declared
	// a `forge:version` column. The caller's move is specific and
	// mechanical, which is why this is not folded into any existing
	// reason: re-read the row, re-apply the change to the fresh values,
	// and write again. It is the one CRUD failure where a blind retry of
	// the SAME request is guaranteed to fail identically.
	// Wire code: Aborted.
	ReasonVersionConflict = "version_conflict"

	// ReasonInvalidPageToken — the supplied page_token did not decode.
	// The caller's move is to restart the listing from page one.
	// Wire code: InvalidArgument.
	ReasonInvalidPageToken = "invalid_page_token"

	// ReasonInvalidOrderBy — the supplied order_by names a column this
	// entity does not declare. Wire code: InvalidArgument.
	ReasonInvalidOrderBy = "invalid_order_by"

	// ReasonPageTokenOrderConflict — a page_token was combined with an
	// order_by that the keyset cursor cannot page. The caller's move is
	// to drop one of the two, which is neither "bad token" nor "bad
	// order" — hence its own code. Wire code: InvalidArgument.
	ReasonPageTokenOrderConflict = "page_token_order_conflict"

	// ReasonInternal — the server failed for a reason it did not
	// classify. Deliberately a single bucket: the client's only move is
	// to report and retry, so splitting it would encode server internals
	// into a client contract. It is also the code that keeps the
	// vocabulary total — no forge error reaches the wire without a
	// reason. Wire code: Internal.
	ReasonInternal = "internal"
)

// mapRepoErr is the single repository-error -> client-error chokepoint,
// routed through pkg/svcerr (the prescribed handler convention IS the
// demonstrated one):
//
//   - a missing row (orm.ErrNoRows, or an svcerr.ErrNotFound the
//     repository already classified) maps to CodeNotFound with a clean
//     "<entity>: not found" message;
//   - a postgres constraint violation carries a SQLSTATE that tells the
//     client something actionable — a duplicate, a bad reference, a
//     violated invariant — so it maps to the matching Connect code rather
//     than collapsing to an opaque 500 (a unique-violation surfacing as
//     Internal was the review defect);
//   - EVERYTHING else maps to CodeInternal with safe text AND the original
//     error attached as a server-only cause (svcerr.WithCause). Repository
//     errors carry SQL fragments and driver internals ("sql: no rows in
//     result set", "no such column: ..."), and a connect.Error message
//     is client-visible — raw SQL must never cross the wire. But dropping
//     the error entirely was worse than leaking it: with the table gone,
//     ListItems returned a 500 and the SQLSTATE appeared NOWHERE — not in
//     the response, not in any log line. The trace span this once relied on
//     is not a second copy either: OTEL_EXPORTER_OTLP_ENDPOINT defaults to
//     empty, so in the default configuration that span goes nowhere.
//     The cause now rides through to the logging interceptor, which prints
//     it at ERROR keyed by request id. Sanitize the wire, never the log.
func mapRepoErr(op, entity string, err error) error {
	if errors.Is(err, orm.ErrNoRows) || svcerr.IsNotFound(err) {
		return clientErr(svcerr.NotFound(entity), ReasonNotFound)
	}
	// An optimistic-concurrency conflict on a `forge:version` entity. It is
	// checked BEFORE the not-found branch's neighbours because the repo has
	// already done the work of telling the two apart (see
	// Repo.requireWriteLanded) and this package must not undo that: reported
	// as NotFound, a conflict would tell a client its row was deleted when
	// it was merely edited by someone else. The repo's message already says
	// what to do and names no SQL, so it passes through as written.
	if svcerr.IsAborted(err) {
		return clientErr(err, ReasonVersionConflict)
	}
	// An update_mask path that names no updatable column is the CALLER's
	// mistake, not a server fault: InvalidArgument, with the offending
	// path named and no SQL in the message (the typed error carries only
	// the field name).
	var unknownField *orm.UnknownFieldError
	if errors.As(err, &unknownField) {
		return clientErr(svcerr.InvalidArgument(fmt.Sprintf(
			"%s %s: unknown or immutable update_mask path %q", op, entity, unknownField.Field)),
			ReasonUnknownField)
	}
	// Postgres integrity-constraint violations (SQLSTATE class 23) and
	// data-exception violations (class 22) map to a specific Connect code so
	// the client can react — and name the schema object the write violated,
	// so a person can act on the 400 too. See pgFailure.identity for why the
	// constraint name crosses the wire while the driver's own text does not:
	// the SQLSTATE, the offending values and all driver prose stay
	// server-side (reachable via svcerr.Cause, logged by the interceptor).
	if f, ok := pgFailureOf(err); ok {
		switch f.state {
		case "23505": // unique_violation
			return clientErr(svcerr.AlreadyExists(fmt.Sprintf("%s %s: a record with the same unique value already exists%s", op, entity, f.identity())),
				ReasonDuplicate)
		case "23503": // foreign_key_violation
			// One SQLSTATE, two remedies. On a delete the row being removed
			// is the one still referenced ("remove the dependents first");
			// on any other op the row the caller REFERENCED is the missing
			// one ("pick a valid reference"). The operation is the honest
			// discriminator: a 23503 can only arise on a delete because
			// something still points at the target, and can only arise on an
			// insert/update because the target does not exist. Splitting the
			// reason (while keeping the one message, which covers both) is
			// what lets a UI offer the right next step instead of a shrug.
			if op == "delete" {
				return clientErr(svcerr.FailedPrecondition(fmt.Sprintf("%s %s: a referenced record is missing or still in use%s", op, entity, f.identity())),
					ReasonReferenceInUse)
			}
			return clientErr(svcerr.FailedPrecondition(fmt.Sprintf("%s %s: a referenced record is missing or still in use%s", op, entity, f.identity())),
				ReasonReferenceMissing)
		case "23514": // check_violation
			return clientErr(svcerr.InvalidArgument(fmt.Sprintf("%s %s: a field value violates a constraint%s", op, entity, f.identity())),
				ReasonConstraintViolated)
		case "23502": // not_null_violation
			return clientErr(svcerr.InvalidArgument(fmt.Sprintf("%s %s: a required field is missing%s", op, entity, f.identity())),
				ReasonRequiredFieldMissing)
		case "22P02": // invalid_text_representation (e.g. malformed json/uuid input)
			return clientErr(svcerr.InvalidArgument(fmt.Sprintf("%s %s: a field value has an invalid format%s", op, entity, f.identity())),
				ReasonInvalidFormat)
		}
	}
	// A repository closure is an override seam like Entity and Pack, and an
	// application's own verdict from it — svcerr.PermissionDenied from an
	// owner-scoped Persist — passes through with its code AND message. So
	// does a context cancellation: a client that went away is not a server
	// fault. Both used to reach the fallback below, where the code survived
	// only because svcerr classified a WithCause by its cause (a leak, since
	// closed) and the message never did. svcerr.WrapOr is that decision:
	// classified passes, anything else reads the fallback.
	return wrapOrInternal(err, fmt.Sprintf("%s %s failed", op, entity))
}

// clientErr is the single exit from this package to the wire: it maps a
// svcerr-classified error to its *connect.Error AND stamps the
// machine-readable reason code that the frontend routes on.
//
// The two steps are fused into one call on purpose. They were separable
// before, and what happened is that svcerr grew WithReason, web-runtime
// documented `error.reason` as THE thing to key off, CORSMiddleware
// exposed the header cross-origin — and this package, the one that
// produces most of a generated app's errors, never called WithReason
// once. Every classified failure shipped a code and some prose and
// nothing a machine could switch on. Routing every return through a
// function that cannot construct an error without a reason is what makes
// that omission impossible to repeat.
//
// The message is untouched: WithReason only annotates, so the observable
// strings this package's tests pin (see doc.go) are byte-identical.
func clientErr(err error, reason string) error {
	return svcerr.Wrap(svcerr.WithReason(err, reason))
}

// mapPackErr maps a response-projection error (from a shim's Pack — e.g. a
// generated <entity>ToProto that read back an enum value name the current
// proto no longer declares) to a client error. A projection failure is a
// server-side data-integrity problem, so it surfaces as CodeInternal; a
// Pack that already returned a connect.Error passes through unchanged. The
// message ("corrupt enum value %q for column …") carries no SQL/driver text,
// only the offending value and column, so it is safe to surface.
//
// A Pack that returned its own connect.Error keeps its own reason if it
// set one: an application that classified its failure knows more than
// this package's "internal" bucket does, and the branch exists precisely
// to pass the application's verdict through. Only an unreasoned error
// gets stamped, so the vocabulary stays total either way.
//
// That pass-through is keyed on svcerr.IsClassified rather than on
// *connect.Error alone, because Entity and Pack are OVERRIDE seams: an
// application scoping CRUD to its own owner column returns its own rejections
// from them (svcerr.InvalidArgument for a reference it will not accept,
// svcerr.NotFound for a
// row it will not admit exists). Those are bare sentinels, and matching only
// *connect.Error sent every one of them out as CodeInternal — a caller's
// mistake reported as a server fault, from the half of the pipeline whose
// sibling mapRepoErr has understood sentinels all along. The asymmetry was
// invisible: the same svcerr value meant 404 from Fetch and 500 from Entity.
func mapPackErr(err error) error {
	return mapClosureErr(err, "response projection failed")
}

// mapFilterErr maps a ListOp.Filters failure. Filters can fail for one
// reason the other closures cannot: it could not determine WHO is asking,
// so it refused to run a query it would have had to widen to every row.
// That refusal is usually already classified (svcerr.Unauthenticated,
// svcerr.PermissionDenied) and passes through with its own code; an
// unclassified one becomes Internal, and the fallback message says
// "filters" rather than borrowing Pack's "response projection", because
// the two failures happen at opposite ends of the request and a log line
// that names the wrong one sends the reader to the wrong code.
func mapFilterErr(err error) error {
	return mapClosureErr(err, "list filters failed")
}

// mapClosureErr is the shared shim-closure error path: an application's
// own classified rejection passes through verbatim (see mapPackErr for
// why that pass-through keys on IsClassified rather than *connect.Error),
// anything else becomes Internal with safeMsg on the wire and the
// original retained as a server-side cause.
func mapClosureErr(err error, safeMsg string) error {
	return wrapOrInternal(err, safeMsg)
}

// wrapOrInternal is this package's svcerr.WrapOr: an error the application
// already classified (svcerr.IsClassified) passes with its own code and
// message, and anything else reads Internal safeMsg with the original kept
// as the server-side cause for the logging interceptor. Either way it stamps
// ReasonInternal when the result carries no reason of its own, so the
// vocabulary stays total.
//
// The unclassified error is handed to WrapOr UNWRAPPED. Pre-building a
// *connect.Error here made svcerr.ToConnect find one already present and pass
// its message straight through — which for a driver error is raw SQL, table
// names and whatever the DSN contains.
func wrapOrInternal(err error, safeMsg string) error {
	out := svcerr.WrapOr(err, svcerr.Internal(safeMsg))
	var ce *connect.Error
	if errors.As(out, &ce) && ce.Meta().Get(svcerr.ReasonHeader) == "" {
		ce.Meta().Set(svcerr.ReasonHeader, ReasonInternal)
	}
	return out
}

// pgFailure is the part of a postgres error the APPLICATION authored: the
// SQLSTATE that classifies the failure, and the identity of the schema
// object the write violated.
type pgFailure struct {
	state      string
	constraint string
	column     string
}

// identity renders the schema object postgres named, as a phrase to append
// to a client-visible message — "" when the driver reported neither.
//
// # Why this crosses the wire when driver text does not
//
// The rule this package follows is that a client message contains nothing
// the application did not write. That rule is about DIAGNOSTICS:
// `duplicate key value violates unique constraint ...`, `Key (sku)=(abc)
// already exists.`, `ERROR: relation "accounts" does not exist (SQLSTATE
// 42P01) dsn=postgres://app:s3cr3t@db.internal/prod` — prose composed by
// postgres, quoting row values and connection topology at a caller who may
// be unauthenticated.
//
// A constraint name is none of those things. `prescriptions_expires_after_
// issued_check` is an identifier the application typed into its own
// migration; postgres reports it as a STRUCTURED field of the error rather
// than as text inside the message; and it is the only thing in the failure
// that says WHICH field to fix. Without it the client gets
// "a field value violates a constraint" on a form with eight fields —
// routable by reason code, unactionable by a person.
//
// A NOT NULL violation has no constraint to name, so postgres reports the
// COLUMN instead. Same question, same answer, same treatment.
func (f pgFailure) identity() string {
	switch {
	case f.constraint != "":
		return " (constraint " + f.constraint + ")"
	case f.column != "":
		return " (column " + f.column + ")"
	}
	return ""
}

// pgFailureOf extracts the SQLSTATE and violated-object identity from a
// driver error, independent of which postgres driver produced it. Generated
// apps open their pool through jackc/pgx (errors surface as
// *pgconn.PgError); the library's own embedded-postgres test harness
// (pkg/pgtest) and any lib/pq-based caller surface *pq.Error. Both expose
// the same three fields under different names; handling both keeps the
// constraint mapping correct regardless of the driver behind bun. ok=false
// when the error carries no SQLSTATE (not a postgres error).
func pgFailureOf(err error) (pgFailure, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgFailure{state: pgErr.Code, constraint: pgErr.ConstraintName, column: pgErr.ColumnName}, true
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pgFailure{state: string(pqErr.Code), constraint: pqErr.Constraint, column: pqErr.Column}, true
	}
	return pgFailure{}, false
}

// CreateOp wires the per-RPC concerns of a Create handler.
//
// Create returns the constructed entity in the response. The shim
// supplies:
//
//   - Entity      — proto request -> internal entity constructor.
//   - Persist     — repository call.
//   - Pack        — internal entity -> connect.Response.
//   - EntityLower — lowercase entity name used in the error envelope.
type CreateOp[Req, Resp, Ent any] struct {
	EntityLower string
	// Entity projects the request onto the internal entity. It returns an
	// error for the same reason Pack does, in the opposite direction: a
	// value the request CARRIED that the projection cannot encode (a
	// repeated message onto a jsonb column, say) must fail the call, not
	// store a default and report success. Shims that cannot fail return
	// (entity, nil).
	//
	// It takes ctx because stamping a new row's owner needs the CALLER,
	// and the caller lives on the context (see the package doc's note on
	// scoping seams). Generated shims that read nothing from it declare
	// `_ context.Context`.
	Entity  func(ctx context.Context, req *Req) (Ent, error)
	Persist func(ctx context.Context, entity Ent) error
	// Pack projects the persisted entity onto the response. It returns an
	// error so a corrupt-data projection (e.g. a generated <entity>ToProto
	// that read back an enum value name absent from the current proto)
	// surfaces as CodeInternal instead of silently shipping a degraded
	// message. Shims that cannot fail return (resp, nil).
	Pack func(entity Ent) (*Resp, error)
}

// HandleCreate runs the canonical Create lifecycle:
//
//	build entity -> persist -> pack response.
//
// All error-mapping is fixed; the shim only carries data shape.
func HandleCreate[Req, Resp, Ent any](op CreateOp[Req, Resp, Ent]) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		entity, err := op.Entity(ctx, req.Msg)
		if err != nil {
			return nil, mapPackErr(err)
		}
		if err := op.Persist(ctx, entity); err != nil {
			return nil, mapRepoErr("create", op.EntityLower, err)
		}
		resp, err := op.Pack(entity)
		if err != nil {
			return nil, mapPackErr(err)
		}
		return connect.NewResponse(resp), nil
	}
}

// GetOp wires the per-RPC concerns of a Get handler.
type GetOp[Req, Resp, Ent any] struct {
	EntityLower string
	ID          func(req *Req) string
	// Fetch reads the row named by the primary key.
	//
	// The variadic opts carry an ADDITIONAL predicate the caller wants
	// applied in the query — canonically the ownership filter, e.g.
	// orm.WhereEq("company_id", claims.OrgID). It is variadic so an
	// override that has nothing to add passes nothing, and so the
	// predicate composes with whatever the repository already applies
	// (soft-delete scoping, and so on) rather than replacing it.
	//
	// Scoping in the QUERY is what makes "not yours" and "not there"
	// the same answer: a fetch-then-compare returns the row to the
	// handler first, and every branch after that is one an author has
	// to remember to write. HandleGet passes no opts of its own — this
	// parameter exists for the shim and for overrides.
	Fetch func(ctx context.Context, id string, opts ...orm.QueryOption) (Ent, error)
	// Pack projects the fetched entity onto the response; see CreateOp.Pack
	// for why it returns an error.
	Pack func(entity Ent) (*Resp, error)
}

// HandleGet runs fetch -> pack. Repository errors
// are mapped to CodeNotFound; the legacy generator did the same.
func HandleGet[Req, Resp, Ent any](op GetOp[Req, Resp, Ent]) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		entity, err := op.Fetch(ctx, op.ID(req.Msg))
		if err != nil {
			return nil, mapRepoErr("get", op.EntityLower, err)
		}
		resp, err := op.Pack(entity)
		if err != nil {
			return nil, mapPackErr(err)
		}
		return connect.NewResponse(resp), nil
	}
}

// ErrEntityRequired is what an UpdateOp.Entity shim returns when the
// request carries no entity at all. HandleUpdate turns it into
// CodeInvalidArgument naming the field; every OTHER error from the same
// shim is a projection failure and turns into CodeInternal. One return
// value carries both because they are the same question — "did the
// projection produce an entity?" — and a separate `ok bool` alongside an
// error only invites the two to disagree.
var ErrEntityRequired = errors.New("crud: request carries no entity")

// UpdateOp wires the per-RPC concerns of an Update handler. The shim
// supplies an Entity closure that projects the request onto the internal
// entity, returning ErrEntityRequired when the request omitted it.
type UpdateOp[Req, Resp, Ent any] struct {
	EntityLower    string
	EntityFieldLow string // lowercase form of the proto field that holds the entity, e.g. "user"
	// Entity projects the request onto the internal entity; see
	// CreateOp.Entity for why it can fail and why it takes ctx, and
	// ErrEntityRequired for the one failure that is the CALLER's fault
	// rather than the data's.
	Entity func(ctx context.Context, req *Req) (Ent, error)
	// Persist writes the whole row. See GetOp.Fetch for the variadic
	// opts: on an update they are what stops a caller reassigning
	// someone else's row to themselves, because the predicate is
	// evaluated against the STORED row rather than the submitted entity.
	Persist func(ctx context.Context, entity Ent, opts ...orm.QueryOption) error
	// Pack projects the updated entity onto the response; see CreateOp.Pack
	// for why it returns an error.
	Pack func(entity Ent) (*Resp, error)

	// Mask extracts the AIP-134 update_mask paths from the request
	// (req.GetUpdateMask().GetPaths()). nil when the proto's update
	// request has no update_mask field — HandleUpdate then behaves
	// exactly as before this field existed (full replace via Persist).
	Mask func(req *Req) []string

	// PersistMasked writes ONLY the named fields (proto field names ==
	// column names, snake_case). The generator wires it whenever it
	// wires Mask. If Mask is set but PersistMasked is nil and a request
	// arrives with concrete paths, HandleUpdate fails CodeInternal —
	// silently widening a masked write to a full replace is the
	// data-loss bug this hook exists to prevent.
	//
	// The variadic opts mean the same thing they do on Persist.
	PersistMasked func(ctx context.Context, entity Ent, fields []string, opts ...orm.QueryOption) error

	// ReadOnly names the columns a CLIENT may not write through this op:
	// those whose entity wire field is marked `forge:read-only` or
	// `forge:computed`. HandleUpdate refuses an update_mask path naming
	// one with InvalidArgument (reason unknown_field) before anything
	// runs, and the generated Persist keeps the same columns out of a
	// full replace with Preserve — so a client can neither set a
	// server-owned column nor reset it by omitting it.
	//
	// The refusal lives here, on the op, rather than in the repository,
	// because the same repository is how the code that OWNS these
	// columns writes them: a custom RPC's Update<Entity>Masked naming
	// "status" must land, while a client's update_mask naming "status"
	// must not. Only the op knows which of the two is calling.
	//
	// Generated from the proto on every `forge generate`. An override
	// that wants AIP-203's "silently ignore output-only paths" instead
	// can set it to nil and filter in PersistMasked.
	ReadOnly []string
}

// HandleUpdate runs validate-required -> persist -> pack.
//
// AIP-134 update_mask semantics (when op.Mask is wired):
//
//   - mask absent/empty, or containing "*"  → full-object replace via
//     op.Persist. AIP-134 permits full replacement when the behavior is
//     documented — this is that documentation. Callers that want a
//     partial update MUST send a mask.
//   - mask with concrete paths → op.PersistMasked writes only those
//     fields. Paths are proto field names (snake_case, == column names).
//   - a path named in op.ReadOnly → CodeInvalidArgument naming the path,
//     refused before the entity is even built. Nothing is written.
//   - unknown or immutable path → CodeInvalidArgument naming the path
//     (mapped from orm.UnknownFieldError by mapRepoErr).
//
// The response carries the entity as the repository left it. The generated
// repository reads back every column a write did not set (see
// Repo.Update), so on both paths the response reports the STORED value of
// a column the caller could not write — a read-only status, a GENERATED
// total — rather than echoing whatever the request carried for it.
func HandleUpdate[Req, Resp, Ent any](op UpdateOp[Req, Resp, Ent]) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		var paths []string
		masked := false
		if op.Mask != nil {
			var full bool
			paths, full = maskPaths(op.Mask(req.Msg))
			masked = !full
		}
		if masked {
			if err := refuseReadOnlyPaths(op.EntityLower, op.ReadOnly, paths); err != nil {
				return nil, err
			}
		}
		entity, err := op.Entity(ctx, req.Msg)
		if errors.Is(err, ErrEntityRequired) {
			return nil, clientErr(connect.NewError(
				connect.CodeInvalidArgument,
				fmt.Errorf("update %s: %s is required", op.EntityLower, op.EntityFieldLow),
			), ReasonRequiredFieldMissing)
		}
		if err != nil {
			return nil, mapPackErr(err)
		}
		if masked {
			if op.PersistMasked == nil {
				// Wiring bug, not caller error: the generator emits Mask
				// and PersistMasked together. Fail loudly rather than
				// silently rewriting every column.
				return nil, clientErr(connect.NewError(
					connect.CodeInternal,
					fmt.Errorf("update %s: update_mask received but masked persistence is not wired", op.EntityLower),
				), ReasonInternal)
			}
			if err := op.PersistMasked(ctx, entity, paths); err != nil {
				return nil, mapRepoErr("update", op.EntityLower, err)
			}
			resp, err := op.Pack(entity)
			if err != nil {
				return nil, mapPackErr(err)
			}
			return connect.NewResponse(resp), nil
		}
		if err := op.Persist(ctx, entity); err != nil {
			return nil, mapRepoErr("update", op.EntityLower, err)
		}
		resp, err := op.Pack(entity)
		if err != nil {
			return nil, mapPackErr(err)
		}
		return connect.NewResponse(resp), nil
	}
}

// orderKeysetSafe reports whether an ORDER BY is compatible with the PK
// keyset cursor (WHERE pk > cursor ... ORDER BY pk ASC). The cursor yields
// correct, gap-free pages ONLY when the rows are ordered by the primary key
// ascending and nothing else: any other column — or DESC, or a composite
// clause — makes `pk > cursor` page a set sorted one way while advancing by
// another, duplicating and skipping rows across pages. An empty clause means
// the library applies the default PK ASC order, which is safe. An empty
// pkColumn (PK-cursor pagination disabled) is treated as unsafe so no cursor
// is minted.
func orderKeysetSafe(clause string, descending bool, pkColumn string) bool {
	if pkColumn == "" {
		return false
	}
	clause = strings.TrimSpace(clause)
	if clause == "" {
		return true // library defaults to PK ASC
	}
	// A composite (comma-separated) clause is never a single-key cursor.
	if strings.Contains(clause, ",") {
		return false
	}
	fields := strings.Fields(clause)
	if len(fields) == 0 {
		return true
	}
	if !strings.EqualFold(fields[0], pkColumn) {
		return false
	}
	// Direction: req.Descending or an explicit DESC token breaks the
	// ascending `pk > cursor` predicate.
	if descending {
		return false
	}
	if len(fields) == 2 && strings.EqualFold(fields[1], "DESC") {
		return false
	}
	return true
}

// refuseReadOnlyPaths rejects an update_mask that names a column the client
// may not write. Refused, not silently dropped: AIP-203 asks a server to
// ignore output-only mask paths, but a caller who names `status` believes it
// is changing status, and a 200 that changed nothing is exactly the silent
// non-write this op exists to stop. The reason matches the one a
// forge:version path already gets, since the caller's move is the same.
func refuseReadOnlyPaths(entityLower string, readOnly, paths []string) error {
	for _, p := range paths {
		if slices.Contains(readOnly, p) {
			return clientErr(svcerr.InvalidArgument(fmt.Sprintf(
				"update %s: update_mask path %q is read-only", entityLower, p)),
				ReasonUnknownField)
		}
	}
	return nil
}

// maskPaths normalizes update_mask paths: blank entries are dropped, and
// full reports whether the mask requests a full replace (no concrete
// paths, or any "*" entry).
func maskPaths(raw []string) (paths []string, full bool) {
	for _, p := range raw {
		if p == "" {
			continue
		}
		if p == "*" {
			return nil, true
		}
		paths = append(paths, p)
	}
	return paths, len(paths) == 0
}

// DeleteOp wires the per-RPC concerns of a Delete handler.
type DeleteOp[Req, Resp any] struct {
	EntityLower string
	ID          func(req *Req) string
	// Persist removes the row named by the primary key. See GetOp.Fetch
	// for why the opts are variadic and what belongs in them: a delete
	// scoped by a WHERE predicate touches no row when the row is not the
	// caller's, and the repository's absent-row check then reports
	// NotFound — the same answer an unknown id gets.
	Persist func(ctx context.Context, id string, opts ...orm.QueryOption) error
	// Pack is optional. When nil, HandleDelete returns the proto's
	// zero-value response (matching the legacy DeleteResponse{} shape).
	Pack func() *Resp
}

// HandleDelete runs persist -> empty response.
//
// A PK that matched no row is a NotFound, not an empty 200: the repository
// reports its not-found error (svcerr.NotFound, errors.Is-true for
// orm.ErrNoRows) and mapRepoErr maps it exactly as it does for Get.
// See crud.requireRowTouched for the reasoning and for the per-entity opt
// out of absent-is-an-error.
func HandleDelete[Req, Resp any](op DeleteOp[Req, Resp]) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		if err := op.Persist(ctx, op.ID(req.Msg)); err != nil {
			return nil, mapRepoErr("delete", op.EntityLower, err)
		}
		var resp *Resp
		if op.Pack != nil {
			resp = op.Pack()
		} else {
			var zero Resp
			resp = &zero
		}
		return connect.NewResponse(resp), nil
	}
}

// ListOp wires the per-RPC concerns of a List handler. Pagination,
// filter, and order-by bookkeeping live in the library; the per-RPC
// shim still provides the per-field filter -> orm.QueryOption mapping
// (this is data, not lifecycle, and reflection-free Go can't generalize
// it without a code-gen table).
type ListOp[Req, Resp, Ent any] struct {
	EntityLower  string
	PkColumnName string // empty disables PK-cursor pagination

	// Columns is the entity's declared column allowlist (the generated
	// db.<Entity>Columns var). User-supplied order_by columns are
	// validated against it — identifier-shape validation alone lets an
	// undeclared column reach the database, where some engines silently
	// treat it as a constant (an ordering no-op).
	Columns []string

	// Pagination knobs. The library applies the same defaults the legacy
	// template did when these are zero.
	HasPagination   bool
	DefaultPageSize int // 0 -> 50
	MaxPageSize     int // 0 -> 100

	// HasOrderBy enables req.Msg.OrderBy / req.Msg.Descending handling
	// via the OrderBy/Descending closures.
	HasOrderBy bool
	OrderBy    func(ctx context.Context, req *Req) (clause string, descending bool)

	// Filters returns extra orm.QueryOption values built from per-field
	// filter logic. The shim implements this as a static sequence of
	// "if req.Msg.X != nil { opts = append(opts, orm.WhereILike(...)) }"
	// statements — same as the legacy template, just lifted into a
	// closure.
	//
	// It is also THE place a per-caller row scope belongs, which is why
	// it takes ctx: the ownership predicate is a WHERE clause like any
	// other, and it must be in the query rather than applied to the
	// returned page, because Count runs over these same opts and a
	// post-filtered page would report a total including rows the caller
	// cannot see.
	//
	// It returns an error so a closure that CANNOT determine the scope
	// can fail closed. Without one, the only way to say "I could not
	// resolve who is asking" is to return no filter — which silently
	// widens the query to every row, the exact failure this seam exists
	// to prevent. HandleList maps the error the way it maps Entity's, so
	// an svcerr sentinel keeps its code and a bare error becomes
	// Internal.
	Filters func(ctx context.Context, req *Req) ([]orm.QueryOption, error)

	// PageToken / PageSize accessors. PageSize is clamped by the
	// library; PageToken is decoded by the library.
	PageToken func(req *Req) string
	PageSize  func(req *Req) int

	// Query runs the repository call. Returns slice + error; the library
	// handles the +1 fetch and trim-to-pageSize.
	Query func(ctx context.Context, opts []orm.QueryOption) ([]Ent, error)

	// Count runs a COUNT over the SAME filters as Query (but WITHOUT the page
	// limit or cursor) to populate the response's total_count. It is wired
	// only when the response message carries a total_count field; nil
	// otherwise, in which case the library passes totalCount = 0 to Pack (the
	// pre-fix behavior). The opts it receives are the filter opts only.
	Count func(ctx context.Context, opts []orm.QueryOption) (int64, error)

	// EntityID extracts the cursor key from the last-of-page entity.
	// Required when HasPagination is true.
	EntityID func(entity Ent) string

	// Pack receives the trimmed item slice, the next page token (empty when
	// no further page), and the total row count matching the filters (0 when
	// Count is not wired). Shim assembles the response with the right
	// repeated-field name. It returns an error so a per-item projection
	// failure (see CreateOp.Pack) surfaces as CodeInternal.
	Pack func(items []Ent, nextPageToken string, totalCount int64) (*Resp, error)
}

// HandleList runs page/order/filter assembly ->
// repository call -> trim -> pack.
func HandleList[Req, Resp, Ent any](op ListOp[Req, Resp, Ent]) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		// Filter opts (the WHERE clauses) are computed ONCE and shared by the
		// list query and the total COUNT. The COUNT must see the filters but
		// NOT the page limit or cursor (total_count is the count across ALL
		// pages), so filters are isolated here and the pagination opts are
		// appended to a separate list-query slice below.
		var filterOpts []orm.QueryOption
		if op.Filters != nil {
			var ferr error
			filterOpts, ferr = op.Filters(ctx, req.Msg)
			if ferr != nil {
				return nil, mapFilterErr(ferr)
			}
		}

		opts := append([]orm.QueryOption{}, filterOpts...)

		// Resolve the requested order FIRST. The keyset cursor pages by
		// `pk > cursor`, which only yields correct, gap-free pages when the
		// result set is ordered by the primary key ascending. Deciding
		// whether a page_token can be honored therefore requires knowing the
		// order before the cursor is applied — hence the up-front resolution.
		var orderClause string
		var orderDesc bool
		if op.HasOrderBy && op.OrderBy != nil {
			orderClause, orderDesc = op.OrderBy(ctx, req.Msg)
			if orderClause != "" {
				if err := orm.ValidateOrderBy(orderClause, op.Columns); err != nil {
					return nil, clientErr(connect.NewError(connect.CodeInvalidArgument, err), ReasonInvalidOrderBy)
				}
			}
		}
		// keysetSafe: the cursor is only valid when the effective order is PK
		// ASC (an empty clause means the library applies exactly that).
		keysetSafe := orderKeysetSafe(orderClause, orderDesc, op.PkColumnName)

		// Pagination clamp + fetch+1 + cursor decode.
		pageSize := 0
		if op.HasPagination {
			pageSize = op.PageSize(req.Msg)
			defSize := op.DefaultPageSize
			if defSize <= 0 {
				defSize = 50
			}
			maxSize := op.MaxPageSize
			if maxSize <= 0 {
				maxSize = 100
			}
			if pageSize <= 0 {
				pageSize = defSize
			}
			if pageSize > maxSize {
				pageSize = maxSize
			}
			opts = append(opts, orm.WithLimit(pageSize+1))

			if tok := op.PageToken(req.Msg); tok != "" {
				cursor, derr := orm.DecodeCursor(tok)
				if derr != nil {
					// Preserve the legacy "invalid page token" wording exactly.
					return nil, clientErr(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page token")), ReasonInvalidPageToken)
				}
				// A page_token minted for PK-keyset pagination cannot be
				// combined with an order_by on a non-PK column (or DESC):
				// paging a set sorted one way while filtering by `pk >
				// cursor` duplicates and skips rows across pages. The two
				// can't both be honored, so reject the combination LOUDLY
				// rather than silently returning wrong pages. (A composite
				// cursor would lift this; until then ordered lists are
				// single-page — see the token-emit guard below.)
				if !keysetSafe {
					return nil, clientErr(connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
						"order_by on a non-primary-key column cannot be combined with a page_token; keyset pagination requires ordering by %q ascending", op.PkColumnName)),
						ReasonPageTokenOrderConflict)
				}
				opts = append(opts, orm.WithWhere(op.PkColumnName, orm.GreaterThan, cursor))
			}
		}

		// (Filter opts were already applied above — they are shared with the
		// total COUNT.)

		// Order-by application. Same semantics as before: the validated
		// user clause when present, else the default PK ASC when pagination
		// is on.
		appliedOrder := false
		if orderClause != "" {
			ord := orm.Asc
			if orderDesc {
				ord = orm.Desc
			}
			opts = append(opts, orm.WithOrderBy(orderClause, ord))
			appliedOrder = true
		}
		if op.HasPagination && !appliedOrder && op.PkColumnName != "" {
			opts = append(opts, orm.WithOrderBy(op.PkColumnName, orm.Asc))
		}

		results, err := op.Query(ctx, opts)
		if err != nil {
			return nil, mapRepoErr("list", op.EntityLower, err)
		}

		var nextPageToken string
		if op.HasPagination && len(results) > pageSize {
			results = results[:pageSize]
			// Only mint a next-page cursor when the order is keyset-safe (PK
			// ASC). For a non-PK / DESC order the cursor would be unusable
			// (and a client sending it back is rejected above), so ordered
			// lists are single-page until composite cursors land. The extra
			// fetched row is still trimmed off either way.
			if keysetSafe && op.EntityID != nil {
				nextPageToken = orm.EncodeCursor(op.EntityID(results[pageSize-1]))
			}
		}

		// Total count over the same filters (never the page limit/cursor), so
		// the client can show real totals/page counts. Wired only when the
		// response carries a total_count field; otherwise 0, as before.
		var totalCount int64
		if op.Count != nil {
			n, cerr := op.Count(ctx, filterOpts)
			if cerr != nil {
				return nil, mapRepoErr("list", op.EntityLower, cerr)
			}
			totalCount = n
		}

		resp, err := op.Pack(results, nextPageToken, totalCount)
		if err != nil {
			return nil, mapPackErr(err)
		}
		return connect.NewResponse(resp), nil
	}
}
