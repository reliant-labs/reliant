package stripeadp

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"example.com/app/gen/billing/v1/billingv1connect"
)

// The v2 shape protoc-gen-connect-go v2 emits per service: register the
// implementation on a *connect.Server, then mount the server.
func registerBilling(mux *http.ServeMux, impl billingv1connect.BillingServiceHandler) {
	server := connect.NewServer()
	billingv1connect.RegisterBillingServiceHandler(server, impl)
	connecthttp.Mount(mux, server)
}
