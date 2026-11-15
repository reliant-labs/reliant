// Package activity is forge's typed activity stream: who did what to which
// object, where, and through what. "sean promoted release v1.7.44 in prod via
// cli" is one Event.
//
// An activity event is DECLARED on the rpc, not written by hand at each call
// site:
//
//	rpc PromoteRelease(PromoteReleaseRequest) returns (PromoteReleaseResponse) {
//	  option (forge.v1.method) = {
//	    activity: {
//	      verb: "promote"
//	      target: { kind: "release", id: "release_id", label: "response.release.version" }
//	      env: "to_env"
//	    }
//	  };
//	}
//
// One interceptor ([Interceptor]) reads the declaration from the method
// descriptor and writes the event when the handler succeeds. `forge lint`
// (forgeconv-activity-declared) fails a mutating rpc that declares nothing,
// so a new mutation cannot ship without saying what it records, or saying
// `activity: { none: "<why>" }`. [Resolve] is the one function that
// interprets a declaration; the interceptor and forge's lint both call it, so
// what the lint reports is what the runtime does.
//
// Actors that are not RPCs (a converger, an alert transition, CI) call
// [Record] directly.
//
// # Transactions
//
// By default the interceptor writes after the handler returns successfully,
// in its own statement, best-effort: a failed write is logged and counted and
// never fails the RPC, whose mutation has already committed. It cannot join
// the handler's transaction (that commits before the handler returns, and a
// created object's id exists only in the response), and wrapping the whole
// handler in one would re-run its non-database side effects on every
// serialization retry. The cost is a possible missing row after a crash
// between commit and insert; never a phantom row, because a failed call
// writes nothing.
//
// Where the event must commit or roll back WITH the mutation, declare
// `writer: HANDLER` and call [Record] inside the handler's RunTx. The default
// [Store] writes through *orm.Client, so the insert joins the ctx-carried
// transaction.
//
// # Storage
//
// [Store] persists events to the activity_events table, whose canonical DDL
// is [SchemaSQL]. The table is a migration the project owns, like every
// other table; the read rpc (ListActivity) is project code over [Store.List],
// because who may read which scope is access control.
package activity
