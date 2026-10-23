// Package release is forge's release ledger vocabulary: what a RELEASE is
// (an immutable, content-addressed set of artifacts under a version label),
// what a PROMOTION is (one append-only event binding an environment to a
// release), and the invariants both must hold no matter where they are
// stored.
//
// # One model, two backends
//
// forge records releases and promotions in two places, and they are the
// SAME model:
//
//   - the file backend, inside a project: .forge/releases/<version>.json and
//     one append-only .forge/promotions/<env>.jsonl per environment;
//   - a hosted control plane, over its DeployService RPCs, when an
//     environment declares forge.ControlPlane.
//
// Both backends read and write these types, and both enforce the rules in
// this package. The hosted side additionally enforces them structurally in
// its database (CHECKs, append-only triggers); the Go rules here are the
// ones a directory of files can hold, and they are the acceptance test for
// the hosted side, not a weaker copy of it.
//
// # Why it is public
//
// A hosted ledger is implemented OUTSIDE forge (Reliant's control plane is
// one), and it must speak exactly this vocabulary or the two backends drift
// into two products. Declaring the types once, in an importable package, is
// what makes "the hosted ledger stores what forge cut" a compile-time fact
// rather than a comment in two repos.
//
// # Closed enums, validated on read
//
// [Kind], [Mode], [PromotionKind], [GateStatus], [StageKind] and
// [StageStatus] are CLOSED. An unknown or empty value fails to decode
// rather than defaulting to something plausible — an artifact whose kind
// nobody recognises must not be read as an image, and a promotion whose
// kind is unknown must not be read as a forward move. The earlier "empty
// kind means OCI" convention is gone: every ledger states what it holds.
//
// # Evidence and runs
//
// A [Gate] is one check's result attached to a promotion, and it is
// EVIDENCE, not enforcement: nothing in this package refuses a promotion on
// a gate's status. What a gate buys is attribution — this principal claimed
// this verdict at this time, with a link to the full report — which is the
// most a recorded claim can ever prove. A gate that must BLOCK is checked
// before the entry is written.
//
// A [Run] is the identity of one pipeline attempt, and it is the join key
// that turns scattered ledger rows into a [RunTimeline]. There is no run
// entity: every [Stage] is either derived from the ledger (the cut, the
// promotions, each promotion's rollout) or recorded as a gate (build,
// lint, test, smoke, wait). A parallel event log would be a second source
// of truth free to disagree with the ledger, so there is none.
//
// # One closed set, two parse paths
//
// Enum values arrive from two places, and conflating them is how a ledger
// becomes unreadable:
//
//   - The WRITE path is strict. [ParseGateStatus], [ParseStageKind] and
//     [ParseStageStatus] refuse anything outside the set, so a typo in a
//     --gate flag fails before anything is recorded.
//   - The READ path is lenient where history demands it.
//     [GateStatusFromStored] maps a status written before the set closed to
//     [GateStatusError] and keeps the original on [Gate.RawStatus]. Old
//     entries stay renderable, an uninterpretable claim never renders as a
//     pass, and nothing is silently discarded.
//
// [Gate.Validate] closes the loop: a gate carrying a RawStatus can be read
// but not written, so leniency on read never becomes leniency on write.
package release
