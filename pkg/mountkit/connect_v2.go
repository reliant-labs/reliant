package mountkit

// connect_v2.go — mounting connect-go v2 services.
//
// connect-go v2 moved interceptors from the handler to the server: a v2
// generated package registers a service on a *connect.Server
// (Register<Svc>Handler(server, impl)) and connecthttp.Mount puts every
// procedure the server holds on a mux. There is no per-handler option, so the
// v1 shape — one shared []connect.HandlerOption, plus a mount's own options
// appended after it — becomes one server per distinct chain: every service
// mounted with the shared chain alone shares one server, and a mount that
// adds interceptors or HTTP options of its own gets a server of its own, built
// with the shared chain followed by its own. All of them are mounted on the
// same mux, which works because connecthttp.Mount registers one pattern per
// procedure plus a "/pkg.Svc/" subtree per service, and services are disjoint.
//
// Every mount goes through MountV2, which keeps connect-go v1's answer for an
// unknown method of a mounted service: a plain HTTP 404, not v2's
// unimplemented RPC error (see MountV2).
//
// Func values cannot be compared, so two mounts that pass the same per-mount
// interceptor get two servers rather than sharing one. That costs nothing a
// caller can see: each server serves only its own services, through exactly
// the chain v1 would have built for them.

import (
	"fmt"
	"net/http"
	"strings"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// RegistrarV2 is Registrar for a connect-go v2 service: it registers the
// service's methods on server, the way the v2 generated
// Register<Svc>Handler(server, impl) does. The generated v2 service.go's
// RegisterV2 method satisfies it:
//
//	func (s *Service) RegisterV2(server *connect.Server) {
//	    orderv1connect.RegisterOrderServiceHandler(server, s)
//	}
//
// The method is named RegisterV2, not Register, so one type can carry both
// the v1 Register(mux, ...connect.HandlerOption) and the v2 registration
// while a project migrates. RegisterServiceV2 and serverkit.Server.Mount
// prefer it on a v2 mount.
type RegistrarV2 interface {
	RegisterV2(server *connectv2.Server)
}

// WithInterceptorsV2 adds connect-go v2 server interceptors to this one v2
// mount, AFTER the shared chain (MuxV2's interceptors): the shared chain
// wraps them, as WithHandlerOptions' interceptors are wrapped on a v1 mount.
// Repeated calls accumulate in order; nil interceptors are dropped. A mount
// given any gets a *connect.Server of its own. It is the v2 half of
// WithHandlerOptions; a v1 mount (RegisterService, Resolve) refuses it.
func WithInterceptorsV2(interceptors ...connectv2.ServerInterceptor) Option {
	return func(o *options) { o.interceptorsV2 = append(o.interceptorsV2, interceptors...) }
}

// WithHTTPOptionsV2 adds connecthttp options (read and send limits, codecs,
// compression) to this one v2 mount, after MuxV2's: where connecthttp lets the
// last option win, the mount's wins. A mount given any gets a
// *connect.Server of its own. It is the other v2 half of WithHandlerOptions;
// a v1 mount refuses it.
func WithHTTPOptionsV2(opts ...connecthttp.Option) Option {
	return func(o *options) { o.httpOptsV2 = append(o.httpOptsV2, opts...) }
}

// ResolvedV2 is what one v2 mount resolves to: the interceptors and
// connecthttp options it adds on top of MuxV2's shared ones (its own only —
// the shared chain is MuxV2's), and the HTTP stack its plain-HTTP and webhook
// routes are wrapped in.
type ResolvedV2 struct {
	Interceptors []connectv2.ServerInterceptor
	HTTPOptions  []connecthttp.Option
	HTTPStack    func(http.Handler) http.Handler
}

// ResolveV2 is Resolve for a v2 mount: the mount's WithInterceptorsV2 (nil
// dropped) and WithHTTPOptionsV2, in order, and the WithHTTPStack stack or
// defaultStack when none was given. It panics when mountOpts carry
// WithHandlerOptions: a v1 handler option cannot apply to a v2 server, and
// dropping it — an interceptor that authenticates, say — would be silent.
func ResolveV2(defaultStack func(http.Handler) http.Handler, mountOpts ...Option) ResolvedV2 {
	cfg := applyOptions(mountOpts)
	if len(cfg.handlerOpts) > 0 {
		panic("mountkit: mountkit.WithHandlerOptions holds connect-go v1 handler options, which do not apply " +
			"to a v2 mount; use mountkit.WithInterceptorsV2 and mountkit.WithHTTPOptionsV2")
	}
	stack := cfg.stack
	if stack == nil {
		stack = defaultStack
	}
	return ResolvedV2{
		Interceptors: dropNilInterceptors(cfg.interceptorsV2),
		HTTPOptions:  append([]connecthttp.Option(nil), cfg.httpOptsV2...),
		HTTPStack:    stack,
	}
}

// MuxV2 is the mount target of connect-go v2 services: an *http.ServeMux, the
// shared server interceptor chain and connecthttp options every service
// mounted on it gets, and the servers that serve them — one shared by every
// mount that adds nothing of its own, plus one per mount that does. Build it
// with NewMuxV2, mount with RegisterServiceV2 (or Register), and serve Mux().
//
// A MuxV2 is a boot-time builder: it is not safe for concurrent mounting.
type MuxV2 struct {
	mux          *http.ServeMux
	interceptors []connectv2.ServerInterceptor
	httpOpts     []connecthttp.Option

	shared         *connectv2.Server
	sharedSpecs    int                 // specs of shared already mounted
	sharedPatterns map[string]struct{} // mux patterns shared's Mounts registered
	servers        []*connectv2.Server
}

// NewMuxV2 returns the v2 mount target over mux. interceptors are the shared
// chain, outermost first (nil entries dropped — middleware's disabled
// interceptors are nil); httpOpts the shared connecthttp options.
//
// Every server it builds is mounted with connecthttp.WithReadMaxBytes(0)
// ahead of httpOpts: connect-go v1 handlers read requests of any size unless
// told otherwise, v2's default is 4 MiB, and a mount must not start refusing
// what it accepted under v1. A read limit in httpOpts (serverkit.ChainV2
// passes one) or in a mount's WithHTTPOptionsV2 comes later and wins.
//
// It panics on a nil mux, as RegisterService does.
func NewMuxV2(mux *http.ServeMux, interceptors []connectv2.ServerInterceptor, httpOpts ...connecthttp.Option) *MuxV2 {
	if mux == nil {
		panic("mountkit: NewMuxV2 called with nil mux")
	}
	ints := dropNilInterceptors(interceptors)
	opts := make([]connecthttp.Option, 0, len(httpOpts)+1)
	opts = append(append(opts, connecthttp.WithReadMaxBytes(0)), httpOpts...)
	return &MuxV2{
		mux:            mux,
		interceptors:   ints,
		httpOpts:       opts,
		shared:         connectv2.NewServer(ints...),
		sharedPatterns: map[string]struct{}{},
	}
}

// Mux is the mux every server is mounted on: serve it, or hand it to
// serverkit (Server.Mux, RESTHandler).
func (m *MuxV2) Mux() *http.ServeMux { return m.mux }

// Interceptors returns the shared chain, outermost first.
func (m *MuxV2) Interceptors() []connectv2.ServerInterceptor {
	return append([]connectv2.ServerInterceptor(nil), m.interceptors...)
}

// HTTPOptions returns the connecthttp options every server is mounted with,
// the unbounded read limit NewMuxV2 puts first included.
func (m *MuxV2) HTTPOptions() []connecthttp.Option {
	return append([]connecthttp.Option(nil), m.httpOpts...)
}

// Servers returns every server holding a mounted service: the shared one
// first (once anything is registered on it), then each mount's own, in mount
// order.
func (m *MuxV2) Servers() []*connectv2.Server {
	out := make([]*connectv2.Server, 0, len(m.servers)+1)
	if m.sharedSpecs > 0 {
		out = append(out, m.shared)
	}
	return append(out, m.servers...)
}

// Register registers a service on the server its mount's chain selects and
// mounts what it registered on Mux. register is the service's v2 registration
// (a RegistrarV2's RegisterV2, or the generated Register<Svc>Handler closed
// over the impl). A mount whose mountOpts add no interceptors and no HTTP
// options registers on the shared server; one that adds either gets a server
// of its own, built with the shared chain followed by its interceptors and
// mounted with the shared HTTP options followed by its own.
//
// It returns the fully-qualified proto services register put on the server
// ("acme.billing.v1.BillingService"), in the order their first procedure was
// registered — the names a v1 mount returns for the completeness and audience
// checks. A service with no rpcs registers no procedure, so it is not named.
//
// It panics on a WithHandlerOptions mount option (ResolveV2), and connect-go
// and the mux panic on a procedure or pattern registered twice: all boot-time
// programming errors.
func (m *MuxV2) Register(register func(*connectv2.Server), mountOpts ...Option) []string {
	if register == nil {
		panic("mountkit: MuxV2.Register called with a nil register func")
	}
	r := ResolveV2(identityStack, mountOpts...)
	if len(r.Interceptors) == 0 && len(r.HTTPOptions) == 0 {
		register(m.shared)
		specs := collectSpecs(m.shared)
		added := specs[m.sharedSpecs:]
		m.sharedSpecs = len(specs)
		// Mount walks every procedure the shared server holds; the ones an
		// earlier Register mounted are on the mux already.
		MountV2(newPatternsOnly{mux: m.mux, seen: m.sharedPatterns}, m.shared, m.httpOpts...)
		return serviceNames(added)
	}
	ints := make([]connectv2.ServerInterceptor, 0, len(m.interceptors)+len(r.Interceptors))
	ints = append(append(ints, m.interceptors...), r.Interceptors...)
	server := connectv2.NewServer(ints...)
	register(server)
	opts := make([]connecthttp.Option, 0, len(m.httpOpts)+len(r.HTTPOptions))
	opts = append(append(opts, m.httpOpts...), r.HTTPOptions...)
	MountV2(m.mux, server, opts...)
	m.servers = append(m.servers, server)
	return serviceNames(collectSpecs(server))
}

// RegisterServiceV2 is RegisterService for a connect-go v2 service: it mounts
// svc's Connect procedures through m (MuxV2.Register with svc's RegisterV2),
// then invokes the optional HTTPRegistrar and WebhookRegistrar capabilities on
// m.Mux() with the WithHTTPStack stack (identity when none was given). It
// returns the proto services it mounted (see MuxV2.Register).
//
// svc MUST implement RegistrarV2; RegisterServiceV2 panics otherwise, and on a
// nil m — the same boot-time programming errors RegisterService panics on.
func RegisterServiceV2(m *MuxV2, svc any, mountOpts ...Option) []string {
	if m == nil {
		panic("mountkit: RegisterServiceV2 called with nil MuxV2")
	}
	reg, ok := svc.(RegistrarV2)
	if !ok {
		panic(fmt.Sprintf(
			"mountkit: %T does not implement mountkit.RegistrarV2 "+
				"(missing RegisterV2(*connect.Server) from connectrpc.com/connect/v2); "+
				"it cannot be mounted as a connect-go v2 service",
			svc,
		))
	}
	r := ResolveV2(identityStack, mountOpts...)
	names := m.Register(reg.RegisterV2, mountOpts...)
	if h, ok := svc.(HTTPRegistrar); ok {
		h.RegisterHTTP(m.mux, r.HTTPStack)
	}
	if w, ok := svc.(WebhookRegistrar); ok {
		w.RegisterWebhookRoutes(m.mux, r.HTTPStack)
	}
	return names
}

// MountV2 is connecthttp.Mount with connect-go v1's answer for a request to
// an unknown method of a mounted service: a plain HTTP 404 ("404 page not
// found", text/plain), for every protocol and HTTP method, byte-identical to
// the http.NotFound a v1 generated handler's path switch sent. It mounts the
// same per-procedure handlers as connecthttp.Mount; only each service's
// "/pkg.Svc/" subtree pattern is served by http.NotFoundHandler instead of
// connecthttp's unknown-method handler, which answers an RPC-shaped POST with
// an unimplemented error (Connect 501, gRPC grpc-status 12).
//
// The 404 is a contract, not a nicety: a client calling an rpc a live
// service has since retired — an older build against a newer server — tells
// version skew from a real failure by that 404 (connect-es maps it to
// unimplemented too, but deployed clients route on the status). An unknown
// SERVICE is unchanged: no pattern matches, and the mux answers 404 itself.
//
// It also serves JSON with forge's protobuf JSON codec (JSONCodecOptionV2),
// ahead of opts: a Duration sent as "1h" is still refused, with the spelling
// to send ("3600s") in the error. A WithCodecs in opts replaces it.
//
// MuxV2 mounts every server through it. Use it for any v2 server mounted by
// hand.
func MountV2(mux connecthttp.ServeMux, server *connectv2.Server, opts ...connecthttp.Option) {
	all := make([]connecthttp.Option, 0, len(opts)+1)
	all = append(append(all, JSONCodecOptionV2()), opts...)
	connecthttp.Mount(v1UnknownMethod{mux: mux}, server, all...)
}

// v1UnknownMethod is the connecthttp.ServeMux MountV2 mounts through: it
// forwards each procedure pattern as it is and serves each service subtree
// pattern (the one ending in "/"; a procedure never does) with
// http.NotFoundHandler.
type v1UnknownMethod struct{ mux connecthttp.ServeMux }

func (v v1UnknownMethod) Handle(pattern string, handler http.Handler) {
	if strings.HasSuffix(pattern, "/") {
		handler = http.NotFoundHandler()
	}
	v.mux.Handle(pattern, handler)
}

// newPatternsOnly is the connecthttp.ServeMux a shared-server Mount writes
// through: it forwards each pattern the first time it is seen and drops the
// rest, so remounting the shared server adds only what the latest Register
// put on it.
type newPatternsOnly struct {
	mux  *http.ServeMux
	seen map[string]struct{}
}

func (p newPatternsOnly) Handle(pattern string, handler http.Handler) {
	if _, ok := p.seen[pattern]; ok {
		return
	}
	p.mux.Handle(pattern, handler)
	p.seen[pattern] = struct{}{}
}

func collectSpecs(server *connectv2.Server) []connectv2.Spec {
	var out []connectv2.Spec
	for spec := range server.Specs() {
		out = append(out, spec)
	}
	return out
}

// serviceNames maps procedures ("/pkg.v1.Svc/Method") to their services
// ("pkg.v1.Svc"), each once, in first-seen order.
func serviceNames(specs []connectv2.Spec) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, spec := range specs {
		name := strings.TrimPrefix(spec.Procedure, "/")
		if i := strings.LastIndexByte(name, '/'); i > 0 {
			name = name[:i]
		}
		if _, ok := seen[name]; ok || name == "" {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func dropNilInterceptors(in []connectv2.ServerInterceptor) []connectv2.ServerInterceptor {
	out := make([]connectv2.ServerInterceptor, 0, len(in))
	for _, ic := range in {
		if ic != nil {
			out = append(out, ic)
		}
	}
	return out
}

// v2MountOptionsError is the panic a v1 mount raises for v2-only options.
const v2MountOptionsError = "mountkit: mountkit.WithInterceptorsV2 / WithHTTPOptionsV2 configure a connect-go v2 " +
	"mount (RegisterServiceV2, MuxV2.Register), not this v1 mount; use mountkit.WithHandlerOptions"
