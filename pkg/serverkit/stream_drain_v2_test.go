package serverkit_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// StreamDrainInterceptorV2 keeps StreamDrainInterceptor's client contract on a
// v2 server. The clients here are v1's (openWatch, drainOutcome): what they
// read is the wire a deployed client sees.

// drainServerV2 is drainServer for v2: each procedure a server stream whose
// handler is fn, behind StreamDrainInterceptorV2.
func drainServerV2(t *testing.T, streams *serverkit.Streams, routes map[string]func(ctx context.Context, stream connectv2.ServerStream) error) (*http.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	m := mountkit.NewMuxV2(mux, []connectv2.ServerInterceptor{serverkit.StreamDrainInterceptorV2()})
	m.Register(func(server *connectv2.Server) {
		for procedure, fn := range routes {
			server.Register(connectv2.Method{
				Spec: connectv2.Spec{Procedure: procedure, StreamType: connectv2.StreamTypeServer},
				Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
					if err := stream.Receive(&emptypb.Empty{}); err != nil {
						return err
					}
					if err := stream.Send(wrapperspb.String("first")); err != nil {
						return err
					}
					return fn(ctx, stream)
				},
			})
		}
	})
	return serveOwnListener(t, mux, streams)
}

func blockUntilDoneV2(ctx context.Context, _ connectv2.ServerStream) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestStreamDrainInterceptorV2_DrainedStreamEndsOK(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	httpSrv, base := drainServerV2(t, streams, map[string]func(context.Context, connectv2.ServerStream) error{
		updatesProcedure: blockUntilDoneV2,
	})
	stream := openWatch(t, base, updatesProcedure)
	defer func() { _ = stream.Close() }()

	drained := drainInBackground(httpSrv, streams)
	_, err, trailer := drainOutcome(t, stream)
	if err != nil {
		t.Errorf("drained watch ended with %v, want OK", err)
	}
	if trailer != "true" {
		t.Errorf("trailer %s = %q, want \"true\"", serverkit.DrainedTrailer, trailer)
	}
	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
}

func TestStreamDrainInterceptorV2_RedialOnDrainEndsUnavailable(t *testing.T) {
	t.Parallel()
	streams := &serverkit.Streams{RedialOnDrain: []string{daemonService, tailProcedure}}
	httpSrv, base := drainServerV2(t, streams, map[string]func(context.Context, connectv2.ServerStream) error{
		updatesProcedure: blockUntilDoneV2,
		tailProcedure:    blockUntilDoneV2,
		daemonProcedure:  blockUntilDoneV2,
	})
	updates := openWatch(t, base, updatesProcedure)
	tail := openWatch(t, base, tailProcedure)
	daemon := openWatch(t, base, daemonProcedure)

	drained := drainInBackground(httpSrv, streams)
	for name, stream := range map[string]*connect.ServerStreamForClient[wrapperspb.StringValue]{"by procedure": tail, "by service": daemon} {
		_, err, trailer := drainOutcome(t, stream)
		if connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "server draining") {
			t.Errorf("%s: drained stream ended with %v, want Unavailable saying the server is draining", name, err)
		}
		if trailer != "true" {
			t.Errorf("%s: trailer %s = %q, want \"true\"", name, serverkit.DrainedTrailer, trailer)
		}
		_ = stream.Close()
	}
	if _, err, _ := drainOutcome(t, updates); err != nil {
		t.Errorf("an unlisted procedure of the same service ended with %v, want OK", err)
	}
	_ = updates.Close()
	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
}

func TestStreamDrainInterceptorV2_HandlerErrorPassesThrough(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	httpSrv, base := drainServerV2(t, streams, map[string]func(context.Context, connectv2.ServerStream) error{
		failsProcedure: func(ctx context.Context, _ connectv2.ServerStream) error {
			<-ctx.Done()
			return connectv2.NewError(connectv2.CodeDataLoss, "lost the cursor")
		},
	})
	stream := openWatch(t, base, failsProcedure)
	defer func() { _ = stream.Close() }()
	drained := drainInBackground(httpSrv, streams)
	if _, err, trailer := drainOutcome(t, stream); connect.CodeOf(err) != connect.CodeDataLoss || trailer != "" {
		t.Errorf("handler failure after the drain = %v (trailer %q), want its own DataLoss, unmarked", err, trailer)
	}
	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
}
