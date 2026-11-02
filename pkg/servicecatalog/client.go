package servicecatalog

import "connectrpc.com/connect"

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
func NewClient[T any](binding ClientBinding, constructor func(connect.HTTPClient, string, ...connect.ClientOption) T, httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) T {
	return constructor(httpClient, baseURL, opts...)
}
