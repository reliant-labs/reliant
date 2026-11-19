// Clean fixture: the package is properly outbound-only. Lint must not fire.

// forge:outbound-io
//
// stripeadp wraps an external billing provider. No Connect RPC handlers
// here — those live in handlers/billing/ and depend on this package's
// Service.
package stripeadp

import "context"

type Service interface {
	HealthCheck(ctx context.Context) error
}
