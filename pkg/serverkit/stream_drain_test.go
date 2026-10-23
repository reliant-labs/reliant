package serverkit_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// stream_drain_test.go — the client contract of StreamDrainInterceptor, over
// a real listener built by NewHTTPServer and drained by DrainHTTP.

const (
	watchService     = "test.drain.v1.Watch"
	updatesProcedure = "/" + watchService + "/Updates"
	tailProcedure    = "/" + watchService + "/Tail"
	failsProcedure   = "/" + watchService + "/Fails"
	byeProcedure     = "/" + watchService + "/Goodbye"
	unaryProcedure   = "/" + watchService + "/Get"
	uploadProcedure  = "/" + watchService + "/Upload"
	daemonService    = "test.drain.v1.Daemon"
	daemonProcedure  = "/" + daemonService + "/Connect"
)

// sendFirst sends a watch's first message, so the client knows the stream is
// open before the test drains the server.
func sendFirst(_ context.Context, _ *connect.Request[emptypb.Empty], stream *connect.ServerStream[wrapperspb.StringValue]) error {
	if err := stream.Send(wrapperspb.String("first")); err != nil {
		return err
	}
	return nil
}

// drainServer serves the given server-stream handlers behind the interceptor
// on a listener of its own.
func drainServer(t *testing.T, streams *serverkit.Streams, routes map[string]func(context.Context, *connect.Request[emptypb.Empty], *connect.ServerStream[wrapperspb.StringValue]) error) (*http.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	opt := connect.WithInterceptors(serverkit.StreamDrainInterceptor())
	for procedure, fn := range routes {
		mux.Handle(procedure, connect.NewServerStreamHandler(procedure, fn, opt))
	}
	return serveOwnListener(t, mux, streams)
}

// blockUntilDone sends one message and holds the stream open until its
// context ends — a watch with nothing more to say.
func blockUntilDone(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[wrapperspb.StringValue]) error {
	if err := sendFirst(ctx, req, stream); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// openWatch opens a server stream and waits for its first message.
func openWatch(t *testing.T, base, procedure string) *connect.ServerStreamForClient[wrapperspb.StringValue] {
	t.Helper()
	client := connect.NewClient[emptypb.Empty, wrapperspb.StringValue](http.DefaultClient, base+procedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("open %s: %v", procedure, err)
	}
	if !stream.Receive() {
		t.Fatalf("%s: no first message: %v", procedure, stream.Err())
	}
	return stream
}

// drainInBackground runs DrainHTTP with a generous budget and reports how it
// went: a stream the drain ends must not hold it anywhere near the budget.
func drainInBackground(httpSrv *http.Server, streams *serverkit.Streams) <-chan error {
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		start := time.Now()
		err := serverkit.DrainHTTP(ctx, httpSrv, streams)
		if err == nil && time.Since(start) > 10*time.Second {
			err = errors.New("drain took " + time.Since(start).String())
		}
		done <- err
	}()
	return done
}

// drainOutcome reads a stream to its end after the drain and returns what the
// client was told. A stream nothing ends fails the test rather than hanging it.
func drainOutcome(t *testing.T, stream *connect.ServerStreamForClient[wrapperspb.StringValue]) (msgs []string, err error, trailer string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for stream.Receive() {
			msgs = append(msgs, stream.Msg().GetValue())
		}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the drain never ended the stream: it is holding the shutdown")
	}
	return msgs, stream.Err(), stream.ResponseTrailer().Get(serverkit.DrainedTrailer)
}

// TestStreamDrainInterceptor_DrainedStreamEndsOK is the #773 contract: a watch
// open when the server drains ends OK, marked drained — not held for the
// whole budget, not cut, and not Unavailable.
func TestStreamDrainInterceptor_DrainedStreamEndsOK(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	httpSrv, base := drainServer(t, streams, map[string]func(context.Context, *connect.Request[emptypb.Empty], *connect.ServerStream[wrapperspb.StringValue]) error{
		updatesProcedure: blockUntilDone,
	})
	stream := openWatch(t, base, updatesProcedure)
	defer func() { _ = stream.Close() }()

	drained := drainInBackground(httpSrv, streams)
	_, err, trailer := drainOutcome(t, stream)
	if err != nil {
		t.Errorf("drained watch ended with %v (code %s), want OK", err, connect.CodeOf(err))
	}
	if trailer != "true" {
		t.Errorf("trailer %s = %q, want \"true\": the client cannot tell a drain from completion", serverkit.DrainedTrailer, trailer)
	}
	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
}

// RedialOnDrain is per procedure or per service: listed streams end
// Unavailable, the rest of the same service still end OK.
func TestStreamDrainInterceptor_RedialOnDrainEndsUnavailable(t *testing.T) {
	t.Parallel()
	streams := &serverkit.Streams{RedialOnDrain: []string{daemonService, tailProcedure}}
	httpSrv, base := drainServer(t, streams, map[string]func(context.Context, *connect.Request[emptypb.Empty], *connect.ServerStream[wrapperspb.StringValue]) error{
		updatesProcedure: blockUntilDone,
		tailProcedure:    blockUntilDone,
		daemonProcedure:  blockUntilDone,
	})
	updates := openWatch(t, base, updatesProcedure)
	tail := openWatch(t, base, tailProcedure)
	daemon := openWatch(t, base, daemonProcedure)

	drained := drainInBackground(httpSrv, streams)
	for name, stream := range map[string]*connect.ServerStreamForClient[wrapperspb.StringValue]{"by procedure": tail, "by service": daemon} {
		_, err, trailer := drainOutcome(t, stream)
		if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Errorf("%s: drained stream ended with %v, want Unavailable", name, err)
		} else if !strings.Contains(err.Error(), "server draining") {
			t.Errorf("%s: message %q does not say the server is draining", name, err.Error())
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

// A handler's own failure is its own: the drain does not rewrite it.
func TestStreamDrainInterceptor_HandlerErrorPassesThrough(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	httpSrv, base := drainServer(t, streams, map[string]func(context.Context, *connect.Request[emptypb.Empty], *connect.ServerStream[wrapperspb.StringValue]) error{
		failsProcedure: func(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[wrapperspb.StringValue]) error {
			if err := sendFirst(ctx, req, stream); err != nil {
				return err
			}
			<-ctx.Done()
			return connect.NewError(connect.CodeDataLoss, errors.New("lost the cursor"))
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

// A handler that claimed the soft phase says goodbye in-band while its
// context is live, and its own ending stands.
func TestStreamDrainInterceptor_ClaimedStreamSaysGoodbye(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	httpSrv, base := drainServer(t, streams, map[string]func(context.Context, *connect.Request[emptypb.Empty], *connect.ServerStream[wrapperspb.StringValue]) error{
		byeProcedure: func(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[wrapperspb.StringValue]) error {
			soft := serverkit.Draining(ctx)
			if err := sendFirst(ctx, req, stream); err != nil {
				return err
			}
			select {
			case <-soft:
				return stream.Send(wrapperspb.String("goodbye"))
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	stream := openWatch(t, base, byeProcedure)
	defer func() { _ = stream.Close() }()
	drained := drainInBackground(httpSrv, streams)
	msgs, err, trailer := drainOutcome(t, stream)
	if err != nil || len(msgs) != 1 || msgs[0] != "goodbye" {
		t.Errorf("claimed stream: messages %v, err %v; want the goodbye and OK", msgs, err)
	}
	if trailer != "" {
		t.Errorf("a stream that ended on its own was marked drained (%q)", trailer)
	}
	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
}

// Unary and client-streaming RPCs are not long-lived: the drain waits for
// them instead of ending them.
func TestStreamDrainInterceptor_LeavesUnaryAndClientStreamsToTheDrain(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	opt := connect.WithInterceptors(serverkit.StreamDrainInterceptor())
	inUnary, inUpload := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	cutShort := make(chan string, 2)

	mux := http.NewServeMux()
	mux.Handle(unaryProcedure, connect.NewUnaryHandler(unaryProcedure,
		func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[wrapperspb.StringValue], error) {
			close(inUnary)
			<-release
			if ctx.Err() != nil {
				cutShort <- "unary"
			}
			return connect.NewResponse(wrapperspb.String("answered")), nil
		}, opt))
	mux.Handle(uploadProcedure, connect.NewClientStreamHandler(uploadProcedure,
		func(ctx context.Context, stream *connect.ClientStream[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			close(inUpload)
			<-release
			for stream.Receive() {
			}
			if ctx.Err() != nil {
				cutShort <- "client stream"
			}
			return connect.NewResponse(wrapperspb.String("stored")), stream.Err()
		}, opt))
	httpSrv, base := serveOwnListener(t, mux, streams)

	unaryDone := make(chan error, 1)
	go func() {
		client := connect.NewClient[emptypb.Empty, wrapperspb.StringValue](http.DefaultClient, base+unaryProcedure)
		_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		unaryDone <- err
	}()
	uploadDone := make(chan error, 1)
	go func() {
		client := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](http.DefaultClient, base+uploadProcedure)
		up := client.CallClientStream(context.Background())
		_ = up.Send(wrapperspb.String("chunk"))
		_, err := up.CloseAndReceive()
		uploadDone <- err
	}()
	<-inUnary
	<-inUpload

	drained := drainInBackground(httpSrv, streams)
	time.Sleep(200 * time.Millisecond) // the drain is underway; nothing may end them
	close(release)
	if err := <-unaryDone; err != nil {
		t.Errorf("unary in flight during the drain = %v, want answered", err)
	}
	if err := <-uploadDone; err != nil {
		t.Errorf("client stream in flight during the drain = %v, want answered", err)
	}
	select {
	case which := <-cutShort:
		t.Errorf("the drain cancelled a %s's context", which)
	default:
	}
	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
}
