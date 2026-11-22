package serverkit

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/mountkit"
)

// restEchoMuxV2 is restEchoMux with EchoService as a connect-go v2 service
// mounted through mountkit.MuxV2, behind a v2 interceptor standing in for
// auth.
func restEchoMuxV2(t *testing.T) (*http.ServeMux, []string) {
	t.Helper()
	svc := restEchoOnce()
	auth := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			info, _ := connectv2.CallInfoForServerContext(ctx)
			if info.RequestHeader().Get("Authorization") != "Bearer good" {
				return connectv2.NewError(connectv2.CodeUnauthenticated, "no token")
			}
			return next(ctx, spec, stream)
		}
	}
	mux := http.NewServeMux()
	names := mountkit.NewMuxV2(mux, []connectv2.ServerInterceptor{auth}).Register(func(server *connectv2.Server) {
		for i := range svc.Methods().Len() {
			md := svc.Methods().Get(i)
			in, out := md.Input(), md.Output()
			server.Register(connectv2.Method{
				Spec: connectv2.Spec{
					Procedure:  "/" + string(svc.FullName()) + "/" + string(md.Name()),
					StreamType: connectv2.StreamTypeUnary,
					Schema:     md,
				},
				Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
					req := dynamicpb.NewMessage(in)
					if err := stream.Receive(req); err != nil {
						return err
					}
					name := req.Get(in.Fields().ByName("name")).String()
					if name == "missing" {
						return connectv2.NewError(connectv2.CodeNotFound, "no such name")
					}
					info, _ := connectv2.CallInfoForServerContext(ctx)
					res := dynamicpb.NewMessage(out)
					res.Set(out.Fields().ByName("message"), protoreflect.ValueOfString(string(md.Name())+": hello "+name))
					res.Set(out.Fields().ByName("peer"), protoreflect.ValueOfString(info.PeerAddr))
					return stream.Send(res)
				},
			})
		}
	})
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "login page")
	})
	return mux, names
}

// RESTHandler serves REST over v2-mounted services exactly as over v1 ones:
// it relays each REST call as a Connect call into the mux, so the v2 server's
// chain runs, path and body fields bind, the REST caller's address reaches
// the handler, errors keep their code and message, and Connect URLs and
// plain routes pass through untouched.
func TestRESTHandler_ServesV2MountedServices(t *testing.T) {
	mux, names := restEchoMuxV2(t)
	if len(names) != 1 || names[0] != restEchoService {
		t.Fatalf("MuxV2.Register named %v, want [%s]", names, restEchoService)
	}
	h := RESTHandler(mux, names, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if h == nil {
		t.Fatal("RESTHandler returned nil for a mounted, annotated v2 service")
	}
	for _, tc := range []struct{ method, target, body, want string }{
		{http.MethodGet, "/v1/echo/ada", "", "Echo: hello ada"},
		{http.MethodPost, "/v1/shout", `{"name":"grace"}`, "Shout: hello grace"},
	} {
		rec := restServe(t, h, tc.method, tc.target, tc.body, restAuthed)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d, body %s", tc.method, tc.target, rec.Code, rec.Body)
		}
		var got struct{ Message, Peer string }
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s %s: response is not JSON: %v\n%s", tc.method, tc.target, err, rec.Body)
		}
		if got.Message != tc.want || got.Peer != "203.0.113.7:4242" {
			t.Errorf("%s %s: message %q peer %q, want %q from 203.0.113.7:4242", tc.method, tc.target, got.Message, got.Peer, tc.want)
		}
	}
	for _, tc := range []struct {
		target string
		header http.Header
		status int
		msg    string
	}{
		{"/v1/echo/ada", nil, http.StatusUnauthorized, "no token"},
		{"/v1/echo/missing", restAuthed, http.StatusNotFound, "no such name"},
	} {
		rec := restServe(t, h, http.MethodGet, tc.target, "", tc.header)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("GET %s: status %d body %s, want %d with %q", tc.target, rec.Code, rec.Body, tc.status, tc.msg)
		}
	}
	connectJSON := http.Header{"Authorization": {"Bearer good"}, "Content-Type": {"application/json"}}
	if rec := restServe(t, h, http.MethodPost, "/"+restEchoService+"/Plain", `{"name":"lin"}`, connectJSON); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Plain: hello lin") {
		t.Errorf("Connect call: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := restServe(t, h, http.MethodGet, "/auth/login", "", nil); rec.Code != http.StatusOK || rec.Body.String() != "login page" {
		t.Errorf("a plain mux route: status %d, body %q", rec.Code, rec.Body)
	}
}
