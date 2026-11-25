// Package crud provides runtime helpers used by the per-service
// handlers_crud_gen.go file forge generates from a service's CRUD RPCs.
//
// # Pattern
//
// Forge's CRUD generator emits a thin per-RPC shim that delegates to one
// of [HandleCreate], [HandleGet], [HandleList], [HandleUpdate], or
// [HandleDelete]. The shim carries the only things forge can know that
// this library cannot:
//
//   - The RPC signature (req/resp connect types).
//   - The proto -> entity field copy for Create.
//   - The repository call site (db.<Name> function).
//   - The response packing (proto field that holds the entity).
//
// Everything else — error mapping, cursor encoding/decoding, pagination
// clamping, list bookkeeping — moves into this package.
//
// # Connect majors
//
// The lifecycle has no connect type in it, so it is written once, in the
// connect-neutral shape func(ctx, *Req) (*Resp, error): [Create], [Get],
// [Update], [Delete] and [List]. That is exactly the signature of a
// connect-go v2 handler method (v2 generated code is always "simple": bare
// messages, no Request/Response wrappers), so a v2 shim calls them directly:
//
//	func (s *Service) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.GetUserResponse, error) {
//	    return crud.Get(op)(ctx, req)
//	}
//
// The Handle* functions are the same lifecycles behind connect-go v1's
// *connect.Request / *connect.Response, for v1 projects. Neither shape gets a
// V2-suffixed twin: the neutral one has no connect type to version.
//
// Errors are identical in both shapes: svcerr-wrapped *connect.Error values
// (connect-go v1's type) carrying a crud.Reason* reason. A v1 server sends
// them as they are; svcerr.InterceptorV2 re-encodes them for a v2 server with
// the same code, message, details and reason header. A test that calls a v2
// shim directly reads the code with svcerr.CodeV2 (or tdd.AssertConnectErrorV2),
// not connectv2.CodeOf, which does not know the v1 type.
//
// # Scoping seams
//
// This package resolves no principal and injects no WHERE clause: who a
// caller is, and which rows are theirs, is application policy forge does
// not own. What it does own is whether the shapes can EXPRESS that
// policy, and every request-projecting closure therefore takes a
// context.Context, because the caller's claims live there:
//
//   - [ListOp.Filters] — the ownership predicate, in the query. It also
//     returns an error so a closure that cannot determine the scope can
//     fail closed rather than returning no filter, which would widen the
//     query to every row.
//   - [CreateOp.Entity] / [UpdateOp.Entity] — stamping the owner column
//     on a new or rewritten row.
//   - [GetOp.Fetch], [DeleteOp.Persist], [UpdateOp.Persist] and
//     [UpdateOp.PersistMasked] — variadic orm.QueryOption, so a
//     primary-key operation can carry an ownership predicate instead of
//     fetching the row and comparing afterwards.
//
// Before this, four of the five shapes could not see a context at all, so
// scoping a delegated RPC meant abandoning the delegation and hand-writing
// the lifecycle. That cost is what made a declared ownership boundary
// (schemadef's forge:owner) expensive enough to be worth deleting rather
// than satisfying.
//
// # Behavioural fingerprint
//
// The pre-existing per-method generator wrote three observable strings:
//
//   - "<op> <entity_lower>: <wrapped error>" for Create/Get/List/Update/Delete.
//   - "invalid page token" for an undecodable PageToken.
//   - "<op> <entity_lower>: <field> is required" when an Update request
//     has a nil entity field.
//
// All three are preserved verbatim by this package and locked by tests
// in [crud_test.go].
package crud
