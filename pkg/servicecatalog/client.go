package servicecatalog

import (
	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ClientBinding describes the existing endpoint's config FIELD, not its value.
// Service is a fully-qualified protobuf service (use the generated service name
// constant); ConfigField is e.g. "shop.config.v1.API.orders_url". This metadata
// grants no authority and changes no HTTP, auth, tracing or retry behavior.
type ClientBinding struct {
	Service     string
	ConfigField string
}

// NewClient calls the original generated Connect constructor without changing
// its HTTPClient, resolved baseURL or option order. T remains its generated
// client interface. Declare this call directly in a composition provider so
// forge generate can inventory it. Dynamic metadata is explicitly unresolved.
// connect-go v2 projects use NewClientV2.
func NewClient[T any](binding ClientBinding, constructor func(connect.HTTPClient, string, ...connect.ClientOption) T, httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) T {
	return constructor(httpClient, baseURL, opts...)
}

// NewClientV2 is NewClient for a connect-go v2 generated client, whose
// constructor takes one *connectv2.Client: it builds that client over
// httpClient and baseURL with httpOpts and interceptors (in order) and calls
// constructor with it. T remains its generated client interface; declare the
// call directly in a composition provider, as NewClient's.
//
// connect-go v2 caps a client's response messages at 4 MiB by default, where
// v1 read any size. For parity with NewClient the cap is lifted
// (connecthttp.WithReadMaxBytes(0)) before httpOpts apply, so a
// WithReadMaxBytes in httpOpts sets the limit.
//
// A v2 error carries no metadata, where v1's Meta() held the response headers
// and trailers, so svcerr.ClientInterceptorV2 runs first, before
// interceptors: svcerr.MetaOf(err) reads the reason (svcerr.ReasonHeader) and
// metadata a server sent with a failed call, as ce.Meta() did in v1.
func NewClientV2[T any](binding ClientBinding, constructor func(*connectv2.Client) T, httpClient connecthttp.HTTPClient, baseURL string, httpOpts []connecthttp.Option, interceptors ...connectv2.ClientInterceptor) T {
	opts := make([]connecthttp.Option, 0, 1+len(httpOpts))
	opts = append(opts, connecthttp.WithReadMaxBytes(0))
	opts = append(opts, httpOpts...)
	ints := make([]connectv2.ClientInterceptor, 0, 1+len(interceptors))
	ints = append(ints, svcerr.ClientInterceptorV2())
	ints = append(ints, interceptors...)
	return constructor(connectv2.NewClient(connecthttp.NewTransport(httpClient, baseURL, opts...), ints...))
}
