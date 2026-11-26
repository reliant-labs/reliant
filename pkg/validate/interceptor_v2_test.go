package validate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	unaryProcedure  = "/validate.v1.Svc/Do"
	clientProcedure = "/validate.v1.Svc/Upload"
)

var (
	unarySpec  = connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: unaryProcedure}
	clientSpec = connectv2.Spec{StreamType: connectv2.StreamTypeClient, Procedure: clientProcedure}
)

// serverV2 serves a unary and a client-streaming procedure behind ic,
// counting how often the handler body ran (past its Receive calls).
func serverV2(t *testing.T, ic connectv2.ServerInterceptor, ran *int) *httptest.Server {
	t.Helper()
	server := connectv2.NewServer(ic)
	server.Register(
		connectv2.Method{Spec: unarySpec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req emptypb.Empty
			if err := stream.Receive(&req); err != nil {
				return err
			}
			*ran++
			return stream.Send(&emptypb.Empty{})
		}},
		connectv2.Method{Spec: clientSpec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			for {
				var req emptypb.Empty
				err := stream.Receive(&req)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				*ran++
			}
			return stream.Send(&emptypb.Empty{})
		}},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInterceptorV2_RejectsInvalid(t *testing.T) {
	i := &interceptor{v: fakeValidator{err: errors.New("amount_cents: must be >= 0")}}
	ran := 0
	srv := serverV2(t, i.serverV2, &ran)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	err := client.CallUnary(context.Background(), unarySpec, &emptypb.Empty{}, &emptypb.Empty{})
	var ce *connectv2.Error
	if !errors.As(err, &ce) || ce.Code() != connectv2.CodeInvalidArgument || ce.Message() != "amount_cents: must be >= 0" {
		t.Fatalf("client got %v, want invalid_argument carrying the violation", err)
	}
	if ran != 0 {
		t.Error("handler must NOT run when validation fails")
	}
}

func TestInterceptorV2_PassesValid(t *testing.T) {
	i := &interceptor{v: fakeValidator{}}
	ran := 0
	srv := serverV2(t, i.serverV2, &ran)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	if err := client.CallUnary(context.Background(), unarySpec, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("valid request should pass: %v", err)
	}
	if ran != 1 {
		t.Errorf("handler ran %d times, want 1", ran)
	}
}

func TestInterceptorV2_ValidatesEachStreamedMessage(t *testing.T) {
	i := &interceptor{v: fakeValidator{err: errors.New("bad chunk")}}
	ran := 0
	srv := serverV2(t, i.serverV2, &ran)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	stream, err := client.CallClientStream(context.Background(), clientSpec)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.Send(&emptypb.Empty{})
	_ = stream.CloseSend()
	err = stream.Receive(&emptypb.Empty{})
	if code := connectv2.CodeOf(err); code != connectv2.CodeInvalidArgument {
		t.Fatalf("stream ended with %v (code %v), want invalid_argument", err, code)
	}
	if ran != 0 {
		t.Errorf("handler consumed %d invalid messages, want 0", ran)
	}
}

func TestInterceptorV2_Constructs(t *testing.T) {
	if _, err := InterceptorV2(); err != nil {
		t.Fatalf("InterceptorV2() failed to build a protovalidate validator: %v", err)
	}
}

// TestInterceptorV2_WireMatchesV1 requires the rejection a client receives
// to be byte-identical between the v1 interceptor on a v1 handler and the v2
// interceptor on a v2 server.
func TestInterceptorV2_WireMatchesV1(t *testing.T) {
	i := &interceptor{v: fakeValidator{err: errors.New("amount_cents: must be >= 0")}}
	ran := 0
	v2 := serverV2(t, i.serverV2, &ran)

	mux := http.NewServeMux()
	mux.Handle(unaryProcedure, connect.NewUnaryHandler(unaryProcedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			ran++
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, connect.WithInterceptors(i)))
	v1 := httptest.NewServer(mux)
	t.Cleanup(v1.Close)

	post := func(url string) (int, []byte) {
		resp, err := http.Post(url+unaryProcedure, "application/json", bytes.NewReader([]byte("{}")))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	s1, b1 := post(v1.URL)
	s2, b2 := post(v2.URL)
	if s1 != s2 || !bytes.Equal(b1, b2) {
		t.Fatalf("rejection differs:\n v1 %d %s\n v2 %d %s", s1, b1, s2, b2)
	}
	if s1 != http.StatusBadRequest || ran != 0 {
		t.Fatalf("status %d, handler ran %d times; want 400 and 0", s1, ran)
	}
}
