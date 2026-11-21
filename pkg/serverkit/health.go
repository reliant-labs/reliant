package serverkit

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/reliant-labs/forge/pkg/observe"
)

// health.go — grpc.health.v1.Health/Check, served as a Connect rpc.
//
// The standard health rpc, through the listener's own chain: unlike /healthz
// and /readyz, which are served in front of the edge and record nothing (a
// kubelet calls them every few seconds), a call to it is a real request of the
// process — traced, timed and logged like any rpc. That is what makes it the
// request `forge env up` sends once a stack is up: it shows the stack's
// telemetry working end to end before the app has served anyone, and it has
// no side effects.
//
// WHAT IT ANSWERS. The process's readiness — the same answer /readyz gives
// (the drain gate and every ready check), asked in-process:
//
//	service ""      SERVING when /readyz answers 200, NOT_SERVING otherwise,
//	                UNKNOWN when the listener was given no readiness to ask
//	any other name  NotFound: serverkit tracks no per-service health, so it
//	                claims none
//
// Watch is not served: nothing forge runs streams health.
//
// A DECLARED PROBE IS LEFT OUT OF REAL TRAFFIC. A call carrying
// observe.HeaderHealthProbe (env up's) has its server span marked
// forge.probe=true (observe.MarkProbeSpans), which release health keeps out
// of real traffic (obsquery.SpanNotProbeSQL), and records no rpc.server.*
// metric. Any other health call — a monitor's — is real traffic. The header
// can hide nothing but a health check.
//
// Auth: generated projects allow /grpc.health.v1.Health/Check and Watch
// unauthenticated (pkg/middleware UnauthenticatedProcedures, pkg/auth's
// built-in skip).

// HealthCheckProcedure is the procedure of the health rpc serverkit mounts.
const HealthCheckProcedure = observe.HealthCheckProcedure

// healthCheck answers grpc.health.v1.Health/Check from ready, the process's
// readiness handler (nil: UNKNOWN).
func healthCheck(ready http.Handler) func(context.Context, *connect.Request[healthpb.HealthCheckRequest]) (*connect.Response[healthpb.HealthCheckResponse], error) {
	return func(ctx context.Context, req *connect.Request[healthpb.HealthCheckRequest]) (*connect.Response[healthpb.HealthCheckResponse], error) {
		if observe.IsHealthProbe(req.Spec().Procedure, req.Header()) {
			observe.MarkProbeSpans(ctx)
		}
		if svc := req.Msg.GetService(); svc != "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("serverkit reports the health of the process (service \"\"), not of "+svc))
		}
		return connect.NewResponse(&healthpb.HealthCheckResponse{Status: readinessStatus(ctx, ready)}), nil
	}
}

// readinessStatus asks ready, in-process, what /readyz would answer.
func readinessStatus(ctx context.Context, ready http.Handler) healthpb.HealthCheckResponse_ServingStatus {
	if ready == nil {
		return healthpb.HealthCheckResponse_UNKNOWN
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
	if err != nil {
		return healthpb.HealthCheckResponse_UNKNOWN
	}
	w := &statusOnly{header: http.Header{}}
	ready.ServeHTTP(w, r)
	if w.status == 0 || w.status == http.StatusOK {
		return healthpb.HealthCheckResponse_SERVING
	}
	return healthpb.HealthCheckResponse_NOT_SERVING
}

// statusOnly is a ResponseWriter that keeps the status and drops the body.
type statusOnly struct {
	header http.Header
	status int
}

func (w *statusOnly) Header() http.Header { return w.header }
func (w *statusOnly) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}
func (w *statusOnly) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

// mountHealth serves the health rpc on mux, built with the listener's chain
// (opts), answering from ready. A project that already mounts it keeps its
// own: ServeMux panics on a pattern registered twice, and that is the one
// panic recovered here.
func mountHealth(mux *http.ServeMux, ready http.Handler, opts ...connect.HandlerOption) (mounted bool) {
	defer func() {
		if recover() != nil {
			mounted = false
		}
	}()
	mux.Handle(HealthCheckProcedure, connect.NewUnaryHandler(HealthCheckProcedure, healthCheck(ready), opts...))
	return true
}

// withHealthRoute sends the health rpc to mux ahead of handler: a REST
// transcoder serves only the project's own services, so the health rpc
// mounted on the mux beneath it would be unreachable through it.
func withHealthRoute(mux *http.ServeMux, handler http.Handler) http.Handler {
	routed := http.NewServeMux()
	routed.Handle(HealthCheckProcedure, mux)
	routed.Handle("/", handler)
	return routed
}

// readinessOf is the readiness handler a listener answers health from: the
// one given, else the Front route on the default readiness path (a Probes
// surface's), else none.
func readinessOf(given http.Handler, front map[string]http.Handler) http.Handler {
	if given != nil {
		return given
	}
	return front[DefaultReadinessPath]
}
