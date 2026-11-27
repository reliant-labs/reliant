package stripeadp

import (
	"context"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"example.com/app/gen/billing/v1/billingv1connect"
)

// A v2 CLIENT of a remote service is outbound I/O — exactly what the
// marker claims. Building one must not fire the rule.
type service struct {
	billing billingv1connect.BillingServiceClient
}

func New(baseURL string) Service {
	client := connect.NewClient(connecthttp.NewTransport(http.DefaultClient, baseURL))
	return &service{billing: billingv1connect.NewBillingServiceClient(client)}
}

func (s *service) HealthCheck(_ context.Context) error {
	return nil
}
