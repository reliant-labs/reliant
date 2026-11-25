// A package that LIVES under internal/workers/ but is not a worker: its
// exported method is an ordinary behavioral surface, not the
// serverkit.Worker lifecycle.
//
// This is the negative half of the worker exemption. The exemption must be
// STRUCTURAL — keyed on the package actually presenting the worker
// lifecycle — rather than a bare `internal/workers/` path blacklist, which
// would silently exempt every future package anyone filed under that
// directory. Finding expected.
package notaworker // want "package notaworker has exported methods but no contract.go"

// Helper carries an ordinary exported method: not Name/Start, so this
// package is not a worker and the rule still applies.
type Helper struct{}

// Compute does domain work and is exactly the kind of behavioral surface
// the require-contract rule exists to put behind an interface.
func (h *Helper) Compute() error { return nil }
