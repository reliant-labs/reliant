package serverkit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// receive_abort_test.go — AbortReceive, and the drain ending a stream parked
// in Receive, over real listeners built by NewHTTPServer.

const (
	chatService       = "test.abort.v1.Chat"
	chatProcedure     = "/" + chatService + "/Chat"
	chatEchoProcedure = "/" + chatService + "/Echo"
	parkedProcedure   = "/" + chatService + "/Parked"
	claimingProcedure = "/" + chatService + "/Claiming"
)

// clientFor speaks proto to a listener NewHTTPServer built.
func clientFor(proto string) *http.Client {
	if proto == "h2c" {
		return h2cClient()
	}
	return &http.Client{}
}

// The property everything else relies on: with the client connected and
// sending nothing, AbortReceive wakes a read parked on the request body —
// which cancelling the request's context does not — and leaves the response
// alone. Over HTTP/2 it closes the body; over HTTP/1.1, where Close would wait
// for the read, it sets a read deadline.
func TestAbortReceive_WakesAReadParkedOnTheRequestBody(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"http1", "h2c"} {
		t.Run(proto, func(t *testing.T) {
			t.Parallel()
			type outcome struct {
				can, aborted, cancelWoke bool
				readErr                  error
			}
			got := make(chan outcome, 1)
			mux := http.NewServeMux()
			mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
				readDone := make(chan error, 1)
				go func() {
					_, err := r.Body.Read(make([]byte, 1))
					readDone <- err
				}()

				// A cancelled context alone leaves the read parked.
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				<-ctx.Done()
				o := outcome{can: serverkit.CanAbortReceive(r.Context())}
				select {
				case <-readDone:
					o.cancelWoke = true
				case <-time.After(100 * time.Millisecond):
				}

				o.aborted = serverkit.AbortReceive(r.Context())
				o.aborted = serverkit.AbortReceive(r.Context()) && o.aborted // idempotent
				select {
				case o.readErr = <-readDone:
				case <-time.After(5 * time.Second):
				}
				got <- o
				_, _ = io.WriteString(w, "bye")
			})
			_, base := serveOwnListener(t, mux, serverkit.NewStreams())

			// The request body is a pipe the test never writes to: a client
			// that is connected and silent.
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			req, err := http.NewRequest(http.MethodPost, base+"/upload", pr)
			if err != nil {
				t.Fatal(err)
			}
			type response struct {
				body string
				err  error
			}
			resps := make(chan response, 1)
			go func() {
				resp, err := clientFor(proto).Do(req)
				if err != nil {
					resps <- response{err: err}
					return
				}
				defer func() { _ = resp.Body.Close() }()
				b, err := io.ReadAll(resp.Body)
				resps <- response{body: string(b), err: err}
			}()

			select {
			case o := <-got:
				if !o.can || !o.aborted {
					t.Fatalf("CanAbortReceive = %v, AbortReceive = %v under NewHTTPServer, want both true", o.can, o.aborted)
				}
				if o.cancelWoke {
					t.Fatal("a cancelled context woke the read; AbortReceive would not be needed")
				}
				if !errors.Is(o.readErr, serverkit.ErrReceiveAborted) {
					t.Fatalf("parked read ended with %v, want ErrReceiveAborted", o.readErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not finish")
			}
			select {
			case r := <-resps:
				if r.err != nil || r.body != "bye" {
					t.Fatalf("response = %q, %v; want \"bye\": the abort touched the response", r.body, r.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no response after the abort")
			}
		})
	}
}

// A Connect handler that reads its stream on a goroutine of its own stops and
// joins that goroutine before it returns, with the client still connected —
// Connect forbids using a stream once its handler has returned — and then
// still answers the client.
func TestAbortReceive_StopsAConnectReaderBeforeTheHandlerReturns(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle(chatProcedure, connect.NewBidiStreamHandler(chatProcedure,
		func(ctx context.Context, stream *connect.BidiStream[wrapperspb.StringValue, wrapperspb.StringValue]) error {
			if !serverkit.CanAbortReceive(ctx) {
				return errors.New("served without an abortable body")
			}
			if _, err := stream.Receive(); err != nil {
				return err
			}
			readerDone := make(chan error, 1)
			go func() {
				for {
					if _, err := stream.Receive(); err != nil {
						readerDone <- err
						return
					}
				}
			}()
			if err := stream.Send(wrapperspb.String("ready")); err != nil {
				return err
			}
			serverkit.AbortReceive(ctx)
			select {
			case err := <-readerDone:
				if !errors.Is(err, serverkit.ErrReceiveAborted) {
					return fmt.Errorf("reader ended with %w, want ErrReceiveAborted", err)
				}
			case <-time.After(5 * time.Second):
				return errors.New("AbortReceive did not wake the reader")
			}
			return stream.Send(wrapperspb.String("bye"))
		}))
	_, base := serveOwnListener(t, mux, serverkit.NewStreams())

	client := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](h2cClient(), base+chatProcedure)
	stream := client.CallBidiStream(context.Background())
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
	if err := stream.Send(wrapperspb.String("hello")); err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream ended with %v after %v, want OK", err, got)
		}
		got = append(got, msg.GetValue())
	}
	if strings.Join(got, ",") != "ready,bye" {
		t.Fatalf("received %v, want [ready bye]", got)
	}
}

// parkInReceive sends one message and then waits in Receive on the handler's
// own goroutine, which no context wakes — a peer that sends only when it has
// something to say.
func parkInReceive(claim bool) func(context.Context, *connect.BidiStream[wrapperspb.StringValue, wrapperspb.StringValue]) error {
	return func(ctx context.Context, stream *connect.BidiStream[wrapperspb.StringValue, wrapperspb.StringValue]) error {
		if claim {
			serverkit.Draining(ctx)
		}
		if err := stream.Send(wrapperspb.String("first")); err != nil {
			return err
		}
		for {
			if _, err := stream.Receive(); err != nil {
				return err
			}
		}
	}
}

// The drain ends a stream parked in Receive: as the drain begins for one that
// did not claim the soft phase, and at the hard deadline for one that did.
// Cancelling its context alone left both pinning the shutdown to the end of
// the budget. Either way the client is told by the drain contract — OK,
// marked drained — not by the abort's read error.
func TestDrain_EndsAStreamParkedInReceive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		procedure string
		claim     bool
	}{
		{"unclaimed", parkedProcedure, false},
		{"claimed", claimingProcedure, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			streams := &serverkit.Streams{DrainReserve: 400 * time.Millisecond}
			mux := http.NewServeMux()
			mux.Handle(tc.procedure, connect.NewBidiStreamHandler(tc.procedure, parkInReceive(tc.claim),
				connect.WithInterceptors(serverkit.StreamDrainInterceptor())))
			httpSrv, base := serveOwnListener(t, mux, streams)

			client := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](h2cClient(), base+tc.procedure)
			stream := client.CallBidiStream(context.Background())
			t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
			if err := stream.Send(wrapperspb.String("hello")); err != nil {
				t.Fatal(err)
			}
			if msg, err := stream.Receive(); err != nil || msg.GetValue() != "first" {
				t.Fatalf("first message = %v, %v", msg, err)
			}

			// The budget's hard deadline is 1.2s - 400ms = 800ms in: a
			// claimed stream is ended there, an unclaimed one at once.
			ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
			defer cancel()
			drained := make(chan error, 1)
			go func() { drained <- serverkit.DrainHTTP(ctx, httpSrv, streams) }()

			ended := make(chan error, 1)
			go func() {
				for {
					if _, err := stream.Receive(); err != nil {
						ended <- err
						return
					}
				}
			}()
			select {
			case err := <-ended:
				if !errors.Is(err, io.EOF) {
					t.Errorf("drained stream ended with %v (code %s), want OK", err, connect.CodeOf(err))
				}
				if got := stream.ResponseTrailer().Get(serverkit.DrainedTrailer); got != "true" {
					t.Errorf("trailer %s = %q, want \"true\"", serverkit.DrainedTrailer, got)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the drain never ended the stream")
			}
			if err := <-drained; err != nil {
				t.Fatalf("DrainHTTP = %v: the parked stream outlived the budget", err)
			}
			if r := streams.Report(); r.StillRunning != 0 {
				t.Fatalf("report = %+v, want nothing still running", r)
			}
		})
	}
}

// A unary route is unaffected: its request body reads in full through the
// abortable body over either protocol, and a request in flight when the drain
// begins still reads its whole body — the drain ends only bound streams.
func TestReceiveAbort_UnaryRouteIsUnaffected(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"http1", "h2c"} {
		t.Run(proto, func(t *testing.T) {
			t.Parallel()
			streams := serverkit.NewStreams()
			inHandler := make(chan struct{})
			mux := http.NewServeMux()
			mux.Handle(chatEchoProcedure, connect.NewUnaryHandler(chatEchoProcedure,
				func(ctx context.Context, req *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
					return connect.NewResponse(wrapperspb.String(req.Msg.GetValue())), nil
				}, connect.WithInterceptors(serverkit.StreamDrainInterceptor())))
			mux.HandleFunc("/slow-upload", func(w http.ResponseWriter, r *http.Request) {
				close(inHandler)
				<-serverkit.Draining(r.Context()) // read only once the drain is underway
				b, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				_, _ = w.Write(b)
			})
			httpSrv, base := serveOwnListener(t, mux, streams)
			cl := clientFor(proto)

			payload := strings.Repeat("x", 64<<10)
			echo := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](cl, base+chatEchoProcedure)
			resp, err := echo.CallUnary(context.Background(), connect.NewRequest(wrapperspb.String(payload)))
			if err != nil || resp.Msg.GetValue() != payload {
				t.Fatalf("unary echo = %d bytes, %v; want the %d-byte payload", len(resp.Msg.GetValue()), err, len(payload))
			}

			uploaded := make(chan string, 1)
			go func() {
				resp, err := cl.Post(base+"/slow-upload", "text/plain", strings.NewReader("whole body"))
				if err != nil {
					uploaded <- err.Error()
					return
				}
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				uploaded <- string(b)
			}()
			<-inHandler
			drained := drainInBackground(httpSrv, streams)
			select {
			case got := <-uploaded:
				if got != "whole body" {
					t.Fatalf("upload in flight during the drain answered %q, want its whole body echoed", got)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("upload in flight during the drain never answered")
			}
			if err := <-drained; err != nil {
				t.Fatalf("DrainHTTP = %v", err)
			}
		})
	}
}

// A request that did not come through Streams.Install has no body
// AbortReceive can end, and says so.
func TestAbortReceive_OutsideInstallReportsFalse(t *testing.T) {
	t.Parallel()
	if serverkit.CanAbortReceive(context.Background()) {
		t.Fatal("CanAbortReceive = true for a context no request was served under")
	}
	if serverkit.AbortReceive(context.Background()) {
		t.Fatal("AbortReceive = true for a context no request was served under")
	}
}
