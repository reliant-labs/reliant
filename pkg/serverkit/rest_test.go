package serverkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// restEchoService is a service built at test time and registered in
// protoregistry.GlobalFiles, where RESTHandler resolves mounted services, as
// generated code registers a real one:
//
//	service EchoService {
//	  rpc Echo(EchoRequest) returns (EchoResponse) { get: "/v1/echo/{name}" }
//	  rpc Shout(EchoRequest) returns (EchoResponse) { post: "/v1/shout" body: "*" }
//	  rpc Plain(EchoRequest) returns (EchoResponse);  // no REST mapping
//	}
const restEchoService = "forgetest.serverkit.rest.v1.EchoService"

var restEchoOnce = sync.OnceValue(func() protoreflect.ServiceDescriptor {
	str := func(name string, n int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), JsonName: proto.String(name), Number: proto.Int32(n),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		}
	}
	method := func(name string, rule *annotations.HttpRule) *descriptorpb.MethodDescriptorProto {
		m := &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(name),
			InputType:  proto.String(".forgetest.serverkit.rest.v1.EchoRequest"),
			OutputType: proto.String(".forgetest.serverkit.rest.v1.EchoResponse"),
		}
		if rule != nil {
			m.Options = &descriptorpb.MethodOptions{}
			proto.SetExtension(m.Options, annotations.E_Http, rule)
		}
		return m
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("forgetest/serverkit/rest/v1/echo.proto"),
		Package: proto.String("forgetest.serverkit.rest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("EchoRequest"), Field: []*descriptorpb.FieldDescriptorProto{str("name", 1)}},
			{Name: proto.String("EchoResponse"), Field: []*descriptorpb.FieldDescriptorProto{str("message", 1), str("peer", 2)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("EchoService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				method("Echo", &annotations.HttpRule{Pattern: &annotations.HttpRule_Get{Get: "/v1/echo/{name}"}}),
				method("Shout", &annotations.HttpRule{Pattern: &annotations.HttpRule_Post{Post: "/v1/shout"}, Body: "*"}),
				method("Plain", nil),
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		panic(err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		panic(err)
	}
	return fd.Services().ByName("EchoService")
})

// restEchoMux mounts EchoService as a connect-go v1 handler, the way a
// forge service is mounted, behind an interceptor standing in for auth.
// Echo answers "hello <name>" and the caller's address; a name of
// "missing" is a NotFound. /auth/login is a plain route on the same mux.
func restEchoMux(t *testing.T) *http.ServeMux {
	t.Helper()
	svc := restEchoOnce()
	auth := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Header().Get("Authorization") != "Bearer good" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no token"))
			}
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	for i := range svc.Methods().Len() {
		md := svc.Methods().Get(i)
		in, out := md.Input(), md.Output()
		procedure := "/" + string(svc.FullName()) + "/" + string(md.Name())
		mux.Handle(procedure, connect.NewUnaryHandler(procedure,
			func(_ context.Context, req *connect.Request[dynamicpb.Message]) (*connect.Response[dynamicpb.Message], error) {
				name := req.Msg.Get(in.Fields().ByName("name")).String()
				if name == "missing" {
					return nil, connect.NewError(connect.CodeNotFound, errors.New("no such name"))
				}
				res := dynamicpb.NewMessage(out)
				res.Set(out.Fields().ByName("message"), protoreflect.ValueOfString(string(md.Name())+": hello "+name))
				res.Set(out.Fields().ByName("peer"), protoreflect.ValueOfString(req.Peer().Addr))
				return connect.NewResponse(res), nil
			},
			connect.WithSchema(md),
			connect.WithInterceptors(auth),
			connect.WithRequestInitializer(func(_ connect.Spec, msg any) error {
				*msg.(*dynamicpb.Message) = *dynamicpb.NewMessage(in)
				return nil
			}),
		))
	}
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "login page")
	})
	return mux
}

func restServe(t *testing.T, h http.Handler, method, target, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:4242"
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func restHandlerForTest(t *testing.T) http.Handler {
	t.Helper()
	mux := restEchoMux(t)
	h := RESTHandler(mux, []string{restEchoService}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if h == nil {
		t.Fatal("RESTHandler returned nil for a mounted, annotated service")
	}
	return h
}

var restAuthed = http.Header{"Authorization": {"Bearer good"}}

// A REST URL reaches the mounted Connect handler: path and body fields
// bind, metadata (the bearer token) and the caller's address cross the
// in-process hop, and the answer is the REST JSON of the response.
func TestRESTHandler_TranscodesToTheMountedHandler(t *testing.T) {
	h := restHandlerForTest(t)
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
		if got.Message != tc.want {
			t.Errorf("%s %s: message %q, want %q", tc.method, tc.target, got.Message, tc.want)
		}
		if got.Peer != "203.0.113.7:4242" {
			t.Errorf("%s %s: the handler saw peer %q, not the REST caller's address", tc.method, tc.target, got.Peer)
		}
	}
}

// The handler's errors are the REST response's: its interceptor chain runs
// (a missing token is a 401), and a handler error keeps its code and message.
func TestRESTHandler_HandlerErrorsKeepTheirCode(t *testing.T) {
	h := restHandlerForTest(t)
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
		if rec.Code != tc.status {
			t.Errorf("GET %s: status %d, want %d (%s)", tc.target, rec.Code, tc.status, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("GET %s: the error body lost the handler's message %q: %s", tc.target, tc.msg, rec.Body)
		}
	}
}

// Everything the mux routes is served by the mux, untouched: the Connect
// URL of every method (including one with no REST mapping) and a plain
// route. Only a request the mux has no route for is a REST request, and one
// matching no REST route is a 404.
func TestRESTHandler_MuxRoutesPassThrough(t *testing.T) {
	h := restHandlerForTest(t)
	connectJSON := http.Header{"Authorization": {"Bearer good"}, "Content-Type": {"application/json"}}
	rec := restServe(t, h, http.MethodPost, "/"+restEchoService+"/Plain", `{"name":"lin"}`, connectJSON)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Plain: hello lin") {
		t.Errorf("Connect call: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := restServe(t, h, http.MethodGet, "/auth/login", "", nil); rec.Code != http.StatusOK || rec.Body.String() != "login page" {
		t.Errorf("a plain mux route: status %d, body %q", rec.Code, rec.Body)
	}
	if rec := restServe(t, h, http.MethodGet, "/v1/nowhere", "", restAuthed); rec.Code != http.StatusNotFound {
		t.Errorf("an unrouted path: status %d, want 404 (%s)", rec.Code, rec.Body)
	}
	if rec := restServe(t, h, http.MethodDelete, "/v1/echo/ada", "", restAuthed); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a REST path under the wrong method: status %d, want 405 (%s)", rec.Code, rec.Body)
	}
}

// Nothing mounted, or a service whose schema is not registered: nil, so the
// caller serves the bare mux. The failure is logged, never swallowed.
func TestRESTHandler_NilKeepsTheBareMux(t *testing.T) {
	mux := restEchoMux(t)
	if h := RESTHandler(mux, nil, nil); h != nil {
		t.Error("nothing mounted: want nil")
	}
	var log bytes.Buffer
	if h := RESTHandler(mux, []string{"forgetest.nowhere.v1.Gone"}, slog.New(slog.NewTextHandler(&log, nil))); h != nil {
		t.Error("an unregistered service: want nil")
	}
	if !strings.Contains(log.String(), "forgetest.nowhere.v1.Gone") {
		t.Errorf("the failure was not logged with the service name: %q", log.String())
	}
	if h := RESTHandler(mux, []string{"/" + restEchoService + "/"}, nil); h == nil {
		t.Error("the path form of a service name was refused")
	}
}

// The in-process hop never takes the process down: a handler that panics
// is a 500 to the forwarding client, and one that writes nothing is a 200.
func TestInProcessTransport_PanicAndEmptyHandlers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		status  int
		bodyErr bool
	}{
		{"panic", func(http.ResponseWriter, *http.Request) { panic("boom") }, http.StatusInternalServerError, true},
		{"empty", func(http.ResponseWriter, *http.Request) {}, http.StatusOK, false},
		{"streamed", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Hop", "1")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "a")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "b")
		}, http.StatusAccepted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, inProcessBaseURL+"/x", nil)
			res, err := inProcessTransport{handler: tc.handler}.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if res.StatusCode != tc.status || (err != nil) != tc.bodyErr {
				t.Fatalf("status %d (want %d), body %q, read error %v", res.StatusCode, tc.status, body, err)
			}
			if tc.name == "streamed" && (string(body) != "ab" || res.Header.Get("X-Hop") != "1") {
				t.Errorf("body %q, header %v", body, res.Header)
			}
		})
	}
}
