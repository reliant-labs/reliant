// Clean fixture: Deps are all interfaces (plus the always-allowed
// Logger and primitive config data). Lint must not fire. No role marker
// here either — a clean package earns silence from its shape, not from
// a label.
package checkout

import "context"

type Service interface {
	Run(ctx context.Context) error
}

type Charger interface {
	Charge(ctx context.Context, userID string, amount int64) (string, error)
}
