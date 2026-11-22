// Fires the forgeconv-outbound-io-no-rpc rule under connect-go v2. The
// package claims to be outbound-only via `// forge:outbound-io`, then
// registers a Connect RPC service on a *connect.Server.

// forge:outbound-io
//
// stripeadp is an outbound boundary that has, regretfully, also become
// a Connect RPC service. The lint rule should flag adapter.go's call to
// billingv1connect.RegisterBillingServiceHandler.
package stripeadp

import "context"

type Service interface {
	HealthCheck(ctx context.Context) error
}
