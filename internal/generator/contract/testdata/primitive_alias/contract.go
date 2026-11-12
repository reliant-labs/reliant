package primitive_alias

import "context"

// BalanceCapReason is a named string type. The mock generator must
// resolve `BalanceCapReason` to its underlying primitive kind so the
// zero-value fallback emits `""` rather than the invalid composite
// literal `BalanceCapReason{}`.
type BalanceCapReason string

// AttemptCount is a named int type — same story for the int kind.
type AttemptCount int

// Enabled is a named bool type.
type Enabled bool

// Score is a named float type.
type Score float64

// Service exercises named-primitive aliases on return positions to
// guard against regressions of the `T{}` mock bug for primitive
// underlying named types.
type Service interface {
	DynamicBalanceCap(ctx context.Context) (float64, BalanceCapReason)
	Attempts(ctx context.Context) AttemptCount
	IsEnabled(ctx context.Context) Enabled
	GetScore(ctx context.Context) Score
	Tag(ctx context.Context) SiblingTag
}
