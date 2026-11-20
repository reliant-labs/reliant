// Copyright (c) 2025 Reliant Labs

// Package rpcclient builds reliant's connect-go v2 clients, so every one of
// them makes the same two choices:
//
//   - No read limit. connect-go v2 caps a received message at 4 MiB by
//     default; v1, which every reliant client ran on, capped nothing, and
//     reliant moves larger messages than that (attachments, tools-daemon
//     file bodies, workflow YAML, long transcripts). A client that wants a
//     cap passes connecthttp.WithReadMaxBytes in its options; the later
//     option wins.
//   - Error metadata. svcerr.ClientInterceptorV2 runs outermost, so a failed
//     call's error carries the response headers and trailers a v1
//     *connect.Error's Meta() held (svcerr.MetaOf).
//
// Build a generated client from it: reliantv1connect.NewXClient(rpcclient.New(hc, url, nil)).
//
//forge:exclude-contract: a constructor over connect-go; it has no dependencies to seam
package rpcclient

import (
	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// New returns a client for baseURL over hc. opts are the transport's options
// (connecthttp.WithGRPC, a read limit of the caller's own, ...), applied after
// the unlimited read size; interceptors run inside svcerr's.
func New(hc connecthttp.HTTPClient, baseURL string, opts []connecthttp.Option, interceptors ...connect.ClientInterceptor) *connect.Client {
	return connect.NewClient(Transport(hc, baseURL, opts...), append([]connect.ClientInterceptor{svcerr.ClientInterceptorV2()}, interceptors...)...)
}

// Transport is the transport New uses: connecthttp's, with no read limit
// unless opts sets one.
func Transport(hc connecthttp.HTTPClient, baseURL string, opts ...connecthttp.Option) connect.Transport {
	return connecthttp.NewTransport(hc, baseURL, append([]connecthttp.Option{connecthttp.WithReadMaxBytes(0)}, opts...)...)
}
