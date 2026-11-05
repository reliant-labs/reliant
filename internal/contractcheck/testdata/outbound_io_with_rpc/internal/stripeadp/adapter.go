package stripeadp

import (
	"net/http"

	"connectrpc.com/connect"
)

// Force the import to be used so the parser doesn't drop it. The
// real foot-gun is the connect.NewBillingHandler call below.
var _ connect.Code = connect.CodeUnknown

// fake handler stand-in; the real shape is what `protoc-gen-connect-go`
// emits per service. The lint rule looks for the `connect.NewXxxHandler`
// shape, not for a real generated handler.
func registerBilling(mux *http.ServeMux) {
	path, h := connect.NewBillingHandler(nil)
	mux.Handle(path, h)
}
