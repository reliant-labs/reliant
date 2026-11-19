package audit

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// chanStore hands every logged entry to the test; the sink writes off the
// request path, on its own goroutine.
type chanStore struct{ entries chan Entry }

func (s chanStore) Log(_ context.Context, e Entry) error { s.entries <- e; return nil }
func (s chanStore) Query(context.Context, Filter) ([]Entry, error) {
	return nil, nil
}

// InterceptorV2 writes the audit_log row for a real v2 call, failed or not.
func TestInterceptorV2_PersistsEachCall(t *testing.T) {
	const procedure = "/test.v1.EchoService/Echo"
	store := chanStore{entries: make(chan Entry, 4)}
	srv := connectv2.NewServer(InterceptorV2(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil, store))
	srv.Register(connectv2.Method{
		Spec: connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: procedure},
		Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req wrapperspb.StringValue
			if err := stream.Receive(&req); err != nil {
				return err
			}
			if req.GetValue() == "fail" {
				return connectv2.NewError(connectv2.CodePermissionDenied, "nope")
			}
			return stream.Send(wrapperspb.String("ok"))
		},
	})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	client := connectv2.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL))
	spec := connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: procedure}

	next := func() Entry {
		t.Helper()
		select {
		case e := <-store.entries:
			return e
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for the audit row")
		}
		return Entry{}
	}

	if err := client.CallUnary(context.Background(), spec, wrapperspb.String("hi"), &wrapperspb.StringValue{}); err != nil {
		t.Fatal(err)
	}
	if e := next(); e.Procedure != procedure || e.Status != "ok" || e.UserID != "anonymous" || !strings.HasPrefix(e.PeerAddress, "127.0.0.1:") {
		t.Errorf("success row = %+v", e)
	}

	_ = client.CallUnary(context.Background(), spec, wrapperspb.String("fail"), &wrapperspb.StringValue{})
	if e := next(); e.Status != "error" || e.ErrorCode != "permission_denied" || e.ErrorMessage != "permission_denied: nope" {
		t.Errorf("error row = %+v", e)
	}
}

func TestInterceptorV2_NilSafe(t *testing.T) {
	if InterceptorV2(nil, nil, nil) == nil {
		t.Error("InterceptorV2 with a nil logger and store returned nil")
	}
}
