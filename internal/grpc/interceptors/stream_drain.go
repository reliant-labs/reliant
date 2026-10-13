// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/drain"
)

// StreamDrainInterceptor ends streaming RPCs when the server starts shutting
// down, so http.Server.Shutdown is left with only unary requests to drain.
//
// Every streaming handler's context is bound to streams (drain.Streams.Bind):
// when the server's Shutdown runs streams.End, the handler sees its context
// done and returns. A stream the drain ended finishes with an OK status — the
// end of a watch, which is what every streaming RPC this server hosts is:
// StreamUserUpdates and StreamProcessOutput carry a resume cursor, and their
// clients reconnect from it when the stream ends without a terminal event.
//
// OK, not Unavailable, because Unavailable is how the web client learns that
// the network under it died: it logs a stream error and abandons every read
// multiplexed onto the same connection. A drain is neither — the unary reads
// on that connection are being drained normally and will be answered.
//
// Unary RPCs pass through untouched: Shutdown waits for them, and a rollout
// must not cut them short.
type StreamDrainInterceptor struct {
	streams *drain.Streams
}

// NewStreamDrainInterceptor binds streaming handlers to streams.
func NewStreamDrainInterceptor(streams *drain.Streams) *StreamDrainInterceptor {
	return &StreamDrainInterceptor{streams: streams}
}

// WrapUnary leaves unary RPCs alone.
func (i *StreamDrainInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return next
}

// WrapStreamingClient leaves outbound streams alone.
func (i *StreamDrainInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler binds the handler's context to the server's drain and
// turns the cancellation the drain causes into a clean end of stream.
func (i *StreamDrainInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, release := i.streams.Bind(ctx)
		defer release()
		err := next(ctx, conn)
		if drain.Ended(ctx) && isCancellation(err) {
			return nil
		}
		return err
	}
}

// isCancellation reports whether err is how a handler reports that its
// context was cancelled — or no error at all. Any other error is the
// handler's own failure and is returned as it is, drain or not.
func isCancellation(err error) bool {
	return err == nil ||
		errors.Is(err, context.Canceled) ||
		connect.CodeOf(err) == connect.CodeCanceled ||
		drain.IsServerDraining(err)
}
