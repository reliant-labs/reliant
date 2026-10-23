package serverkit

// deadline.go — the Deadline slot of a Chain: every RPC's time budget.
//
// The type is observe.Deadline: observe.Chain is where the slot is defaulted,
// and NewChain builds on it, so both chains apply the one rule — a nil slot is
// Deadline{}, NoDeadline opts out. serverkit is the name projects spell it by
// (serverkit.Deadline{Procedures: middleware.ProcedureDeadlines}); see
// observe.Deadline for every field.

import "github.com/reliant-labs/forge/pkg/observe"

// Deadline is the Deadline slot of a Chain: the time budget of every RPC and
// the watchdog that reports a slow one. It is observe.Deadline.
type Deadline = observe.Deadline

// SlowCall is one watchdog report of a unary RPC that ran past SlowAfter. It
// is observe.SlowCall.
type SlowCall = observe.SlowCall

// The Deadline slot's defaults.
const (
	// DefaultUnaryDeadline bounds a unary RPC that Deadline.Procedures does
	// not name.
	DefaultUnaryDeadline = observe.DefaultUnaryDeadline
	// DefaultSlowAfter is when the watchdog first reports a unary RPC that is
	// still running.
	DefaultSlowAfter = observe.DefaultSlowAfter
	// DefaultSlowRepeat is how often the watchdog reports it again while it
	// keeps running.
	DefaultSlowRepeat = observe.DefaultSlowRepeat
)
