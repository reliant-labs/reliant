package serverkit

import (
	"context"
	"net/http"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/reliant-labs/forge/pkg/observe"
)

// health_v2.go — the health rpc (health.go) on a connect-go v2 server.
//
// The same rpc, the same answers, through the listener's v2 chain: a
// *connect.Server built with the chain's interceptors holding one method,
// grpc.health.v1.Health/Check, mounted with the chain's HTTP options. Only
// the procedure's own pattern goes on the mux — not the per-service subtree
// connecthttp.Mount also registers — so the mux routes exactly what the v1
// handler did (Watch stays unserved), and a project that mounts its own
// health handler keeps it, as on v1.

// healthCheckMethodV2 is the health rpc as a v2 method answering from ready.
func healthCheckMethodV2(ready http.Handler) connectv2.Method {
	return connectv2.Method{
		Spec: connectv2.Spec{
			Procedure:  HealthCheckProcedure,
			StreamType: connectv2.StreamTypeUnary,
			Schema:     healthpb.File_grpc_health_v1_health_proto.Services().ByName("Health").Methods().ByName("Check"),
		},
		Handler: func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			var req healthpb.HealthCheckRequest
			if err := stream.Receive(&req); err != nil {
				return err
			}
			if info, ok := connectv2.CallInfoForServerContext(ctx); ok &&
				observe.IsHealthProbe(spec.Procedure, http.Header{observe.HeaderHealthProbe: info.RequestHeader().Values(observe.HeaderHealthProbe)}) {
				observe.MarkProbeSpans(ctx)
			}
			if svc := req.GetService(); svc != "" {
				return connectv2.NewError(connectv2.CodeNotFound, "serverkit reports the health of the process (service \"\"), not of "+svc)
			}
			return stream.Send(&healthpb.HealthCheckResponse{Status: readinessStatus(ctx, ready)})
		},
	}
}

// mountHealthV2 serves the health rpc on mux through a v2 server built with
// interceptors and mounted with httpOpts (after an unbounded read limit, the
// v1 handler's default). A pattern the project already registered is kept:
// that ServeMux panic is the one recovered, as in mountHealth.
func mountHealthV2(mux *http.ServeMux, ready http.Handler, interceptors []connectv2.ServerInterceptor, httpOpts ...connecthttp.Option) (mounted bool) {
	defer func() {
		if recover() != nil {
			mounted = false
		}
	}()
	server := connectv2.NewServer(dropNilV2(interceptors)...)
	server.Register(healthCheckMethodV2(ready))
	opts := append([]connecthttp.Option{connecthttp.WithReadMaxBytes(0)}, httpOpts...)
	connecthttp.Mount(onlyPattern{mux: mux, pattern: HealthCheckProcedure}, server, opts...)
	return true
}

// onlyPattern is a connecthttp.ServeMux forwarding one pattern to mux and
// dropping the rest.
type onlyPattern struct {
	mux     *http.ServeMux
	pattern string
}

func (o onlyPattern) Handle(pattern string, handler http.Handler) {
	if pattern == o.pattern {
		o.mux.Handle(pattern, handler)
	}
}

func dropNilV2(in []connectv2.ServerInterceptor) []connectv2.ServerInterceptor {
	out := make([]connectv2.ServerInterceptor, 0, len(in))
	for _, ic := range in {
		if ic != nil {
			out = append(out, ic)
		}
	}
	return out
}

// mountHealthOn mounts the health rpc on s.Mux with the chain of the major s
// is configured for: v2 when it is configured only for v2, else v1 (a Server
// configured for neither builds it with no interceptors, as before).
func (s *Server) mountHealthOn(ready http.Handler) bool {
	if s.servesV2() && !s.servesV1() {
		return mountHealthV2(s.Mux, ready, s.serverInterceptorsV2(), s.httpOptionsV2()...)
	}
	return mountHealth(s.Mux, ready, s.handlerOptions()...)
}
