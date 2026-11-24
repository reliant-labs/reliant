// Fires the forgeconv-outbound-io-no-rpc rule. The package claims to be
// outbound-only via `// forge:outbound-io`, then registers a Connect
// RPC handler — exactly the foot-gun the rule catches.

// forge:outbound-io
//
// stripeadp is an outbound boundary that has, regretfully, also become
// a Connect RPC service. The lint rule should flag adapter.go's call to
// connect.NewBillingHandler.
package stripeadp

import "context"

type Service interface {
	HealthCheck(ctx context.Context) error
}
