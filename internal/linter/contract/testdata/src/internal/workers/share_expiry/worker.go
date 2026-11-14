// Mirrors what `forge scaffold worker share_expiry` emits: a package under
// internal/workers/ whose exported methods are the serverkit.Worker
// surface (Name/Start/Stop), with NO contract.go.
//
// The worker's contract is serverkit.Worker — an interface forge's own
// runtime defines and the supervisor consumes polymorphically — not a
// hand-written Go contract.go. A contract.go here would restate an
// interface the package already satisfies, for a shape forge itself
// scaffolds, so the require-contract rule must skip it.
//
// Before the exemption, a brand-new `forge scaffold worker` left
// `forge lint` red with a finding the author could not fix without editing
// forge's own scaffold. Zero findings expected.
package share_expiry

import "context"

// Worker implements the background processing for share_expiry.
type Worker struct{}

// Name returns the worker's identifier.
func (w *Worker) Name() string { return "share-expiry" }

// Start runs the worker's cycle loop until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) error { return nil }

// Stop is called during graceful shutdown.
func (w *Worker) Stop(ctx context.Context) error { return nil }
