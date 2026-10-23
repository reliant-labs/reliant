package serverkit

// rest.go — REST URLs over the mounted Connect services (forge.yaml `api.rest`).
//
// vanguard v0.5 serves REST from a connect-go v2 *connect.Server, and forge's
// handlers are connect-go v1 http.Handlers on a mux. The bridge is vanguard's
// own proxy shape: every mounted service is registered on a v2 server as
// vanguard.ForwardService over a Connect client whose transport serves each
// call with the mux, in process. A REST request is decoded by vanguard,
// relayed as a Connect call, and answered by the very handler — interceptor
// chain, auth and all — that answers the Connect URL. Connect, gRPC and
// gRPC-Web requests never touch vanguard.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// RESTHandler serves the REST URLs that the MOUNTED services' google.api.http
// annotations declare, in front of mux. A request mux has a route for —
// every Connect procedure, and anything else registered on it — goes to mux
// unchanged; any other request is matched against the REST routes, and one
// that matches none gets vanguard's 404.
//
// It returns nil when nothing was mounted, and nil when the REST routes
// cannot be built — in both cases the caller keeps serving the bare mux
// rather than serving nothing. That is the deliberate choice: losing REST
// URLs is a degradation, losing Connect is an outage, and REST routes that
// failed to build are not evidence the Connect handlers are broken.
//
// connectPaths are the fully-qualified Connect service names a mount
// returned — the same values the completeness gate records. A path form
// ("/pkg.v1.Service/") is accepted too.
func RESTHandler(mux *http.ServeMux, connectPaths []string, logger *slog.Logger) http.Handler {
	if len(connectPaths) == 0 {
		return nil
	}
	rest, err := newRESTHandler(mux, connectPaths)
	if err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("init vanguard REST handler", "error", err)
		return nil
	}
	return rest
}

func newRESTHandler(mux *http.ServeMux, connectPaths []string) (http.Handler, error) {
	upstream := connectv2.NewClient(connecthttp.NewTransport(
		&http.Client{Transport: inProcessTransport{handler: mux}}, inProcessBaseURL))
	server := connectv2.NewServer()
	for _, p := range connectPaths {
		name := strings.Trim(p, "/")
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			return nil, fmt.Errorf("resolve the schema of service %q: %w", p, err)
		}
		svc, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			return nil, fmt.Errorf("resolve the schema of service %q: it names a %T, not a service", p, desc)
		}
		server.Register(vanguard.ForwardService(upstream, svc)...)
	}
	routes := http.NewServeMux()
	if err := vanguard.Mount(routes, server); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), restOriginKey{}, r)))
	}), nil
}

// inProcessBaseURL is the base URL of the forwarding client. Nothing dials
// it: inProcessTransport serves every request it is handed.
const inProcessBaseURL = "http://serverkit.rest.in-process"

// restOriginKey carries the REST request into the relayed Connect call, so
// the handler sees the real caller's address, TLS state and Host rather
// than the in-process hop's.
type restOriginKey struct{}

// inProcessTransport is an http.RoundTripper that answers each request by
// running handler in this process, streaming its response body back as it
// is written.
type inProcessTransport struct{ handler http.Handler }

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if origin, ok := req.Context().Value(restOriginKey{}).(*http.Request); ok {
		req.RemoteAddr, req.TLS, req.Host = origin.RemoteAddr, origin.TLS, origin.Host
	}
	req.RequestURI = req.URL.RequestURI()
	if req.Body == nil {
		req.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	w := &pipeResponseWriter{header: http.Header{}, body: pw, sent: make(chan struct{})}
	go func() {
		var failed error
		defer func() {
			if p := recover(); p != nil {
				failed = fmt.Errorf("serverkit: REST handler for %s panicked: %v", req.URL.Path, p)
				w.WriteHeader(http.StatusInternalServerError) // unless headers already went
			}
			w.WriteHeader(http.StatusOK) // a handler that wrote nothing answered 200 with no body
			_ = pw.CloseWithError(failed)
		}()
		t.handler.ServeHTTP(w, req)
	}()
	<-w.sent
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.sentHeader,
		Body:          pr,
		ContentLength: -1,
		Request:       req,
	}, nil
}

// pipeResponseWriter is the handler's side of inProcessTransport. Headers
// are fixed at the first WriteHeader (or Write), as on a real connection.
type pipeResponseWriter struct {
	header     http.Header
	body       *io.PipeWriter
	once       sync.Once
	sent       chan struct{}
	status     int
	sentHeader http.Header
}

func (w *pipeResponseWriter) Header() http.Header { return w.header }

func (w *pipeResponseWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status, w.sentHeader = status, w.header.Clone()
		close(w.sent)
	})
}

func (w *pipeResponseWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}

// Flush is a no-op: every Write already reaches the reader.
func (w *pipeResponseWriter) Flush() {}
