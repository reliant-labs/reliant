// Package approval holds items an agent proposed until a person approves or
// rejects them, expires the ones nobody decided by their deadline, and hands
// each decided item to the app's own follow-up exactly once.
//
// An agent proposes, a person decides, and the app's code does the work. The
// package owns the state machine and its races; the app owns the domain:
//
//   - the deadline is an input ([Item.ExpiresAt]). Trader expires a held
//     trade at the next market close, growth a draft after a week;
//     [NextOr] and [Within] only build the time.
//   - what happens on approval is the app's [FollowUp]: re-validate and place
//     an order, or start the next workflow. It runs in the same transaction
//     as the state change.
//
// # The flow
//
//	item, err := approvals.Propose(ctx, approval.Item{
//	    Kind: "trade", SubjectRef: "decisions/" + id, Scope: accountID, RunID: runID,
//	    Payload: payloadJSON, Summary: "BUY 10 AAPL",
//	    ExpiresAt: approval.NextOr(clock.NextClose(), now, 8*time.Hour),
//	})
//
// Propose is idempotent on (Scope, Kind, SubjectRef): a retried agent run
// proposes the same subject again and gets the first item back, whatever
// state it has reached.
//
// A person then decides, in a human_only rpc (see below):
//
//	item, err := approvals.Decide(ctx, approval.Decision{ID: req.Id, Approve: true, By: name},
//	    func(ctx context.Context, it approval.Item, d approval.Decision) (string, error) {
//	        if err := revalidate(ctx, it); err != nil {
//	            return "", &approval.Rejection{Reason: err.Error()} // approved, but refused
//	        }
//	        return place(ctx, it) // the order's id, recorded as Outcome
//	    })
//
// Decide does, in order:
//
//  1. Expire first. An item past its deadline becomes expired, DecidedBy
//     records who tried, and it is returned with a nil error; the follow-up
//     does not run. Trader answers "approve at 17:00" with EXPIRED, and so
//     does this.
//  2. Transition with one conditional UPDATE ... WHERE state = 'pending'. Of
//     two concurrent deciders exactly one wins; the other gets
//     FailedPrecondition (svcerr). So the follow-up runs at most once per
//     item, with no process mutex.
//  3. Run the follow-up in the same transaction, when the store is on
//     *orm.Client. An error rolls the transition back, and the item stays
//     pending for another try. A [*Rejection] commits the item as rejected
//     with the reason as Outcome. Only approvals run a follow-up.
//
// ExpireDue expires every pending item past its deadline in one statement
// and returns them; Cancel withdraws pending items (an account retired, a
// draft deleted). Reads are lazy-safe: call ExpireDue at the top of a list
// or briefing rpc, or from a sweeper.
//
// # Side effects outside the database
//
// An order sent to a broker or a workflow started over HTTP cannot be rolled
// back with the transaction. The pattern:
//
//   - the follow-up derives the effect's idempotency key from [Item.ID] (a
//     client order id, an idempotency key on the start call), so doing it
//     twice is harmless;
//
//   - when the effect fails after the decision, the follow-up returns
//     [HandoffIncomplete]. The approval commits with HandedOff false;
//
//   - Redrive re-runs the follow-up for every approved item with HandedOff
//     false, each in its own transaction, until it reports success:
//
//     done, err := approvals.Redrive(ctx, approval.Filter{Scope: accountID}, sameFollowUp)
//
// A crash between the external effect and the commit leaves the item
// approved and not handed off, and Redrive picks it up the same way. Make the
// follow-up idempotent on [Item.ID] and a retry cannot double the effect.
//
// # Who may decide
//
// The package never checks identity. Declare the deciding rpc human_only
// (forge's (forge.v1.method).human_only option, enforced by
// agentrun.HumanOnlyInterceptor and linted by forgeconv-human-only) so an
// agent's key is refused before the handler runs, and pass the person's name
// as [Decision.By]. The package is independent of agentrun; they compose in
// the handler:
//
//	// rpc ApproveItem(ApproveItemRequest) returns (ApproveItemResponse) {
//	//   option (forge.v1.method) = { human_only: true };
//	// }
//	func (s *Service) ApproveItem(ctx context.Context, req *connect.Request[pb.ApproveItemRequest]) (...) {
//	    by := personName(ctx) // from agentrun.RequireHuman, or your claims
//	    item, err := s.approvals.Decide(ctx, approval.Decision{ID: req.Msg.Id, Approve: true, By: by}, s.followUp)
//	    ...
//	}
//
// [Item.RunID] links an item to the agentrun run that proposed it. To record
// the decision in forge's activity stream so it commits with the decision,
// declare `writer: HANDLER` on the rpc and call activity.Record inside the
// follow-up, through the same *orm.Client; the event then commits or rolls
// back with the decision.
//
// # A sweeper
//
// Nothing needs a background job, but one keeps expiry timely for people
// reading a dashboard. Under forge's pkg/lease, only one replica sweeps:
//
//	locker.Run(ctx, "approval-sweeper", func(ctx context.Context) error {
//	    if _, err := approvals.ExpireDue(ctx, ""); err != nil {
//	        return err
//	    }
//	    _, err := approvals.Redrive(ctx, approval.Filter{}, s.followUp)
//	    return err
//	})
//
// # Storage
//
// [Store] persists items to the approvals table, whose canonical DDL is
// [SchemaSQL]. The table is a migration the project owns, like every other
// table (see pkg/activity and pkg/audit); the read rpcs are project code over
// [Store.List] and [Store.Get], because who may read which scope is access
// control. A pending item always has a deadline: expires_at is NOT NULL, and
// Propose rejects a zero one. [WithSchema] names a non-default schema.
// [MemoryStore] has the same behaviour for unit tests.
//
// Decide and Redrive open their transaction through [DB.RunTx], which joins
// a transaction the handler already holds, so the whole rpc can be one
// transaction. Give the follow-up the ctx it receives and write through the
// same *orm.Client, and its writes commit or roll back with the decision.
package approval
