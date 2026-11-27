package serverkit_test

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
)

const durationProcedure = "/serverkit.json.v1.Svc/SetInterval"

// Every service mounted with a forge chain — v1 (Chain.HandlerOptions) and
// v2 (ChainV2.NewMux) — answers a JSON Duration in Go syntax ("1h") with
// invalid_argument and the protobuf spelling to send ("3600s"), through the
// whole production chain: auth, protovalidate, svcerr and all.
func TestChain_JSONDurationErrorNamesTheSpellingToSend(t *testing.T) {
	const token = "tok"

	v1 := http.NewServeMux()
	v1.Handle(durationProcedure, connect.NewUnaryHandler(durationProcedure,
		func(_ context.Context, req *connect.Request[durationpb.Duration]) (*connect.Response[durationpb.Duration], error) {
			return connect.NewResponse(req.Msg), nil
		}, chainFor(t, "user", token).HandlerOptions()...))

	v2 := http.NewServeMux()
	chainForV2(t, "user", token).NewMux(v2).Register(func(server *connectv2.Server) {
		server.Register(connectv2.Method{
			Spec: connectv2.Spec{Procedure: durationProcedure, StreamType: connectv2.StreamTypeUnary},
			Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
				var req durationpb.Duration
				if err := stream.Receive(&req); err != nil {
					return err
				}
				return stream.Send(&req)
			},
		})
	})

	for name, mux := range map[string]*http.ServeMux{"v1": v1, "v2": v2} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			status, body := postDuration(t, srv, token, `"1h"`)
			var wire struct{ Code, Message string }
			if err := json.Unmarshal([]byte(body), &wire); err != nil {
				t.Fatalf("error body %q: %v", body, err)
			}
			if status != http.StatusBadRequest || wire.Code != "invalid_argument" {
				t.Errorf("\"1h\" answered %d %q, want 400 invalid_argument", status, wire.Code)
			}
			if want := `send "3600s" for "1h"`; !strings.Contains(wire.Message, want) {
				t.Errorf("message %q lacks %q", wire.Message, want)
			}
			if status, body := postDuration(t, srv, token, `"3600s"`); status != http.StatusOK || body != `"3600s"` {
				t.Errorf("\"3600s\" answered %d %s, want 200 \"3600s\"", status, body)
			}
		})
	}
}

func postDuration(t *testing.T, srv *httptest.Server, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+durationProcedure, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
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
