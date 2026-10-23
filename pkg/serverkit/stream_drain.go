package serverkit

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// stream_drain.go — Connect streaming RPCs end cleanly when the server drains.
//
// # The client contract
//
// A server-streaming or bidi RPC open when this replica drains is ended by the
// drain (streams.go), after the readiness flip and the pre-stop pause, so this
// replica is already out of rotation. Its client receives every message the
// handler sent, and then:
//
//   - by default, a clean end of stream — OK — with the response trailer
//     DrainedTrailer ("x-forge-drained: true"). It means "this replica is going
//     away; the stream did not complete". A watch client reopens the stream
//     (from its resume cursor, if it has one) and reaches a live replica.
//   - for a procedure or service listed in Config.RedialOnDrain, Unavailable
//     with the message of svcerr.ErrServerDraining, and the same trailer. That
//     is the status a peer that redials on Unavailable — a daemon holding a
//     bidi stream to a gateway — already acts on.
//
// OK is the default because Unavailable is how a browser client learns the
// network under it died: connect-web reports it as a stream error and a client
// that multiplexes reads over one connection may abandon all of them. A drain
// is neither — the unary calls on that connection are still being answered.
//
// A handler that asked for serverkit.Draining(ctx) and returned on the soft
// signal chose its own ending (it said goodbye in-band); its status is left as
// it returned it. Unary and client-streaming RPCs are never bound: the drain
// waits for them, and a rollout must not cut them short.

// DrainedTrailer is the response trailer set on a stream the drain ended. A
// client reads it after the stream ends to tell "the server is going away;
// reopen the stream" from "the stream is complete".
const DrainedTrailer = "x-forge-drained"

// StreamDrainInterceptor binds every server-streaming and bidi Connect handler
// to the drain registry of the listener serving it (StreamsFrom), and ends a
// stream the drain ended with the status the client contract above defines.
// It takes no arguments: the registry, and its RedialOnDrain list, come from
// the request context, which NewHTTPServer populates. Put it in the
// interceptor chain inner to logging and metrics, so they record the status
// the client received.
func StreamDrainInterceptor() connect.Interceptor { return &streamDrainInterceptor{} }

type streamDrainInterceptor struct {
	warn sync.Once
}

// WrapUnary leaves unary RPCs alone: the drain waits for them.
func (i *streamDrainInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

// WrapStreamingClient leaves outbound streams alone.
func (i *streamDrainInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler binds the handler's context to the drain and turns the
// cancellation the drain causes into the documented end of stream.
func (i *streamDrainInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		spec := conn.Spec()
		if spec.StreamType != connect.StreamTypeServer && spec.StreamType != connect.StreamTypeBidi {
			return next(ctx, conn)
		}
		streams := StreamsFrom(ctx)
		if streams == nil {
			i.warn.Do(func() {
				slog.WarnContext(ctx, "serverkit.StreamDrainInterceptor: no drain registry installed for this server; streams will not be ended at shutdown",
					"procedure", spec.Procedure, "hint", "serve through serverkit.NewHTTPServer, or wrap the handler with Streams.Install")
			})
			return next(ctx, conn)
		}
		ctx, release := streams.Bind(ctx)
		defer release()
		err := next(ctx, conn)
		if !Ended(ctx) || !isCancellation(err) {
			return err
		}
		conn.ResponseTrailer().Set(DrainedTrailer, "true")
		if streams.redials(spec.Procedure) {
			return svcerr.ToConnect(svcerr.ErrServerDraining)
		}
		return nil
	}
}

// isCancellation reports whether err is how a handler reports that its
// context was cancelled — or no error at all. A Receive the drain aborted
// (receive_abort.go) fails with svcerr.ErrServerDraining, so a handler that
// returns that error is drained too. Any other error is the handler's own
// failure and is returned as it is, drain or not.
func isCancellation(err error) bool {
	return err == nil ||
		errors.Is(err, context.Canceled) ||
		connect.CodeOf(err) == connect.CodeCanceled ||
		svcerr.IsServerDraining(err)
}

// redials reports whether a drained stream of procedure ends Unavailable:
// RedialOnDrain names it, or names its service.
func (s *Streams) redials(procedure string) bool {
	for _, name := range s.RedialOnDrain {
		name = strings.Trim(name, "/")
		if name == "" {
			continue
		}
		if strings.Contains(name, "/") {
			if procedure == "/"+name {
				return true
			}
			continue
		}
		if strings.HasPrefix(procedure, "/"+name+"/") {
			return true
		}
	}
	return false
}
