// Copyright (c) 2025 Reliant Labs

package grpc

import (
	"net/http"

	"github.com/klauspost/compress/gzhttp"
	"github.com/klauspost/compress/gzip"
)

// connectStreamingAcceptEncoding is the header a Connect client sends to ask
// for per-message (envelope) compression on a streaming rpc.
const connectStreamingAcceptEncoding = "Connect-Accept-Encoding"

// compressBrowserStreams gzips Connect STREAMING responses at the HTTP layer
// for clients that cannot ask for envelope compression: the browser.
//
// Unary rpcs are already compressed: the Connect unary protocol negotiates on
// the standard Accept-Encoding header, which every browser sends, and
// connect-go compresses every response by default. Streaming rpcs negotiate on
// Connect-Accept-Encoding instead, and @connectrpc/connect-web never sends it
// (it rejects a compressed envelope outright), so every stream to the web app
// went out raw. The chat snapshot is the first message of StreamChatUpdates
// and is mostly JSON text: prod, 2026-11-04, p50 372KB, max 832KB per chat
// open, uncompressed, which on a phone network is seconds before the
// transcript can render.
//
// HTTP Content-Encoding is the layer the browser does speak: fetch decodes it
// transparently, incrementally, under connect-web's envelope reader. gzhttp
// flushes the gzip writer on every http.Flusher.Flush, and connect-go flushes
// after every Send, so each message (and each heartbeat) still reaches the
// client as it is sent.
//
// Scoped to what needs it:
//   - only application/connect+proto and application/connect+json, the
//     Connect streaming content types. A unary response is already
//     Content-Encoded by connect-go (gzhttp also skips any response that
//     already carries one), and native gRPC carries its status in HTTP
//     trailers, which this must not touch;
//   - never when the request sent Connect-Accept-Encoding: a Connect client
//     that negotiates envelope compression gets that instead, rather than
//     both;
//   - only when the request's Accept-Encoding allows gzip (gzhttp's own
//     check), so a client that did not ask gets the bytes it expects.
//
// BestSpeed: a stream holds its gzip writer for its whole life (hours, for
// StreamUserUpdates), so the per-stream state is kept small; on JSON text it
// still removes most of the bytes.
func compressBrowserStreams(next http.Handler) http.Handler {
	wrap, err := gzhttp.NewWrapper(
		gzhttp.ContentTypes([]string{"application/connect+proto", "application/connect+json"}),
		gzhttp.CompressionLevel(gzip.BestSpeed),
		gzhttp.EnableZstd(false),
	)
	if err != nil {
		// Only an invalid option list fails, and this one is constant: a
		// failure is a programming error caught by the tests.
		panic("compressBrowserStreams: " + err.Error())
	}
	compressed := wrap(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(connectStreamingAcceptEncoding) != "" {
			next.ServeHTTP(w, r)
			return
		}
		compressed.ServeHTTP(w, r)
	})
}
