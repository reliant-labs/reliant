// Package releasetimeline answers "what was released to this environment,
// when, by whom, and what changed" from the deploy ledger, and "which release
// was live at time T".
//
// It is the pure half of `forge env releases list|show|at <env>`. The verbs
// live in internal/cli; this package owns the assembly so the timeline, its
// bracketing and its PR extraction are tested without a terminal, a control
// plane or a machine ledger.
//
// THE SHAPE. A [Source] returns [Data]: an environment's promotions
// (OLDEST FIRST, the ledger's own order), the project's releases, the deploy
// attempts (a machine ledger's applies, a control plane's convergences) and
// the activity events that name who promoted and through which channel.
// [Assemble] turns that into a [Timeline] of [Entry] rows, newest first:
//
//   - a promotion row carries its direction (ahead, behind, same, initial,
//     unknown), so a rollback is a row that says "behind", never a separate
//     kind: forge has no undo, and promoting an older release is an ordinary
//     promotion;
//   - a failed or timed-out deploy is its own row, because a deploy that
//     never landed is an event somebody will go looking for;
//   - gates are the promotion's evidence, before and after it was written.
//
// ORDER COMES FROM THE LEDGER, NOT FROM CLOCKS. Which release an environment
// runs is its newest promotion, and whether a move is a rollback depends on
// the promotion before it. Both are facts about the ledger's sequence, so
// Assemble never re-sorts promotions by timestamp (the file ledger stamps to
// the second, and two promotions in one second must keep their order).
//
// DATA SOURCES are declared here, at their consumer, and kept small: the
// machine ledger through [LedgerReader] ([Local]), a control plane through
// [cloud.Caller] ([Cloud], procedure names plus local JSON structs, the way
// cloud.ResolveOrganization does it), and local git through [CommitLister]
// ([Git]) for the commit list behind a recorded range.
//
// WHAT IS NOT HERE. Health verdicts arrive through [Timeline.AttachHealth]:
// the producer (the observability service) is a later phase, and an entry with
// no verdict says nothing, which is different from a healthy one.
//
//forge:not-a-component: a library the forge CLI calls; the server composes nothing from it
package releasetimeline
