// Package topology is forge's release/environment topology report: the
// whole-project document `forge env status --json` prints with no env named,
// and the one schema every reader of that picture shares.
//
// ONE IMPLEMENTATION, TWO GATHERERS. The report is a comparison ACROSS
// environments — prod is 15 releases and ten weeks ahead of staging, and
// carries an image staging does not have — and a comparison assembled by each
// client from its own records is a client that has re-implemented forge's
// data model. So the assembly lives here, pure, and each caller only gathers:
//
//   - the forge CLI gathers from a checkout (deploy/kcl renders, the machine
//     ledger, each env's declared control plane) and calls [Assemble];
//   - a control plane gathers from its own store (environment rows, the
//     current promotion, releases, bundles, rollout and hosted status) and
//     calls the same [Assemble].
//
// Both therefore emit the same document with the same release ordering
// ([SortReleasesNewestFirst]), the same lag arithmetic ([PromotionLag]) and
// the same image-state model ([ImageState]). A report one of them computes is
// forge's report, not a third opinion.
//
// The package is pure: no I/O, no clock, no environment. [Input] carries
// plain records — including the errors a gatherer hit, which become per-row
// notes rather than a failed report — and [Assemble] decides everything else.
//
// THE CONTRACT IS ADDITIVE. New fields may be added (always omitempty, so a
// report that does not set them is byte-identical to one from before they
// existed); no field is renamed or repurposed. A consumer reading
// `environments[].release` keeps working.
//
// UNVERIFIED IS UNKNOWN, NOT GREEN. Unless something compared an image against
// a running system, its state is [NotVerified], the zero value. A consumer must
// paint those cells as unknown. See [ImageState].
package topology
