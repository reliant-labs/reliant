package mountkit_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/reliant-labs/forge/pkg/mountkit"
)

// durationEchoV2 is a v2 service whose one rpc takes and returns a bare
// google.protobuf.Duration, so the JSON body IS the duration string.
type durationEchoV2 struct{}

const durationProcedure = "/test.v1.DurationService/Echo"

func (durationEchoV2) RegisterV2(server *connectv2.Server) {
	spec := connectv2.Spec{Procedure: durationProcedure, StreamType: connectv2.StreamTypeUnary}
	server.Register(connectv2.Method{Spec: spec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		var req durationpb.Duration
		if err := stream.Receive(&req); err != nil {
			return err
		}
		return stream.Send(&req)
	}})
}

// durationEchoV1 is its connect-go v1 twin, built with the handler options
// it is handed.
type durationEchoV1 struct{}

func (durationEchoV1) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.Handle(durationProcedure, connect.NewUnaryHandler(durationProcedure,
		func(_ context.Context, req *connect.Request[durationpb.Duration]) (*connect.Response[durationpb.Duration], error) {
			return connect.NewResponse(req.Msg), nil
		}, opts...))
}

// postJSON sends body to procedure as a Connect unary JSON request and
// returns the HTTP status and the response body.
func postJSON(t *testing.T, srv *httptest.Server, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+durationProcedure, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(out)
}

// A Duration in Go syntax ("1h") is refused, as the protobuf JSON mapping
// requires, with invalid_argument and the spelling to send ("3600s") in the
// message a client reads — through a real v2 mount (MuxV2 → MountV2) and a
// v1 handler built with JSONCodecOption, for both JSON content types. The
// protobuf spelling still round-trips.
func TestJSONCodec_DurationErrorNamesTheSpellingToSend(t *testing.T) {
	v2mux := http.NewServeMux()
	mountkit.RegisterServiceV2(mountkit.NewMuxV2(v2mux, nil), durationEchoV2{})
	v1mux := http.NewServeMux()
	mountkit.RegisterService(v1mux, durationEchoV1{}, []connect.HandlerOption{mountkit.JSONCodecOption()})

	for _, major := range []struct {
		name string
		mux  *http.ServeMux
	}{{"v2", v2mux}, {"v1", v1mux}} {
		srv := httptest.NewServer(major.mux)
		t.Cleanup(srv.Close)
		for _, ct := range []string{"application/json", "application/json; charset=utf-8"} {
			status, body := postJSON(t, srv, ct, `"1h"`)
			var wire struct{ Code, Message string }
			if err := json.Unmarshal([]byte(body), &wire); err != nil {
				t.Fatalf("%s %s: error body %q: %v", major.name, ct, body, err)
			}
			if status != http.StatusBadRequest || wire.Code != "invalid_argument" {
				t.Errorf("%s %s: %d %q, want 400 invalid_argument", major.name, ct, status, wire.Code)
			}
			for _, want := range []string{
				`invalid google.protobuf.Duration value "1h"`,
				`(protobuf JSON durations are decimal seconds with an "s" suffix: send "3600s" for "1h")`,
			} {
				if !strings.Contains(wire.Message, want) {
					t.Errorf("%s %s: message %q lacks %q", major.name, ct, wire.Message, want)
				}
			}

			if status, body := postJSON(t, srv, ct, `"3600s"`); status != http.StatusOK || body != `"3600s"` {
				t.Errorf("%s %s: \"3600s\" answered %d %s, want 200 \"3600s\"", major.name, ct, status, body)
			}
		}
	}
}
