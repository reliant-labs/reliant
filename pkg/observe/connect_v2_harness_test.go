package observe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/structpb"
)

// The connect-go v2 tests run real calls: a connectv2.Server behind the
// interceptors under test, mounted with connecthttp on an httptest server, and
// a connectv2 client. Two procedures: a unary one and a server stream.
const (
	v2Unary  = "/observe.test.v1.V2Service/Do"
	v2Stream = "/observe.test.v1.V2Service/Watch"
)

var (
	v2UnarySpec  = connectv2.Spec{Procedure: v2Unary, StreamType: connectv2.StreamTypeUnary}
	v2StreamSpec = connectv2.Spec{Procedure: v2Stream, StreamType: connectv2.StreamTypeServer}
)

// v2Handlers are the procedures' bodies. A nil unary answers an empty
// message; a nil stream sends two.
type v2Handlers struct {
	unary  func(ctx context.Context) error
	stream func(ctx context.Context, send func() error) error
}

type v2Server struct {
	t      *testing.T
	url    string
	client *connectv2.Client
}

func serveV2(t *testing.T, h v2Handlers, ics ...connectv2.ServerInterceptor) *v2Server {
	t.Helper()
	return serveV2With(t, h, nil, ics...)
}

// serveV2With is serveV2 with connecthttp options on the mount.
func serveV2With(t *testing.T, h v2Handlers, mountOpts []connecthttp.Option, ics ...connectv2.ServerInterceptor) *v2Server {
	t.Helper()
	srv := connectv2.NewServer(ics...)
	srv.Register(
		connectv2.Method{Spec: v2UnarySpec, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			if err := stream.Receive(&structpb.Struct{}); err != nil {
				return err
			}
			if h.unary != nil {
				if err := h.unary(ctx); err != nil {
					return err
				}
			}
			return stream.Send(&structpb.Struct{})
		}},
		connectv2.Method{Spec: v2StreamSpec, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			if err := stream.Receive(&structpb.Struct{}); err != nil {
				return err
			}
			send := func() error { return stream.Send(&structpb.Struct{}) }
			if h.stream != nil {
				return h.stream(ctx, send)
			}
			if err := send(); err != nil {
				return err
			}
			return send()
		}},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv, mountOpts...)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return &v2Server{t: t, url: hs.URL, client: connectv2.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL))}
}

// call makes one unary call carrying header and returns its CallInfo
// (response headers) and error.
func (s *v2Server) call(ctx context.Context, header map[string]string) (*connectv2.CallInfo, error) {
	s.t.Helper()
	ctx, info := connectv2.NewClientContext(ctx)
	for k, v := range header {
		info.RequestHeader().Set(k, v)
	}
	err := s.client.CallUnary(ctx, v2UnarySpec, &structpb.Struct{}, &structpb.Struct{})
	return info, err
}

// watch makes one server-stream call carrying header, reads it to the end,
// and returns its CallInfo, the messages received and the stream's error.
func (s *v2Server) watch(ctx context.Context, header map[string]string) (*connectv2.CallInfo, int, error) {
	s.t.Helper()
	ctx, info := connectv2.NewClientContext(ctx)
	for k, v := range header {
		info.RequestHeader().Set(k, v)
	}
	cs, err := s.client.CallServerStream(ctx, v2StreamSpec, &structpb.Struct{})
	if err != nil {
		return info, 0, err
	}
	defer cs.Close()
	n := 0
	for {
		if err := cs.Receive(&structpb.Struct{}); err != nil {
			if errors.Is(err, io.EOF) {
				return info, n, nil
			}
			return info, n, err
		}
		n++
	}
}

// v2Code is err's v2 code, 0 for nil.
func v2Code(err error) connectv2.Code {
	if err == nil {
		return 0
	}
	return connectv2.CodeOf(err)
}

// v2Message is the wire message of a v2 *connect.Error, "" otherwise.
func v2Message(err error) string {
	var ce *connectv2.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return ""
}

// jsonRecords decodes a JSON log buffer into one map per line.
func jsonRecords(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// recordWithMsg is the one record with msg, failing the test unless exactly
// one has it.
func recordWithMsg(t *testing.T, recs []map[string]any, msg string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, r := range recs {
		if r["msg"] == msg {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one %q record, got %d in %v", msg, len(found), recs)
	}
	return found[0]
}

// markerV2 records, on a shared trace slice, that it ran.
func markerV2(name string, trace *[]string) connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			*trace = append(*trace, name)
			return next(ctx, spec, stream)
		}
	}
}
