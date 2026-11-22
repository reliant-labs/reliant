package serverkit

import (
	"fmt"
	"sort"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/forge/pkg/mountkit"
)

// Mount registers a single forge Connect service onto Server.Mux AND
// records its proto service name for the boot-time completeness check.
//
// It is the composition-root convenience that fuses the two steps the
// explicit-composition shape always does together: mount the handler, then
// remember that this proto service is accounted for. Internally it calls
// mountkit.RegisterService(s.Mux, svc, s.HandlerOpts, ...) — the SAME mount
// mechanism a caller would use by hand — then s.Mounted(connectPath) so
// RequireMounted can verify completeness later.
//
//   - connectPath is the fully-qualified proto service path (e.g.
//     "acme.billing.v1.BillingService") — the DECLARED proto identity
//     RequireMounted walks the registry for. This is recorded as-is; pass
//     the generated ConnectPath constant, not the kebab runtime name.
//   - svc must implement mountkit.Registrar (mountkit panics otherwise —
//     a non-service in the mount loop is a boot-time programming error).
//   - mountOpts forward to mountkit: mountkit.WithHandlerOptions adds Connect
//     handler options to this service alone, after HandlerOpts (so the shared
//     chain wraps them); mountkit.WithHTTPStack wraps its plain-HTTP routes.
//
// Mount panics if Server.Mux is nil: a composition root calling Mount has
// opted into the Mux ergonomics, so a nil Mux is a wiring bug to surface
// loudly at boot, not mount silently nowhere.
//
// A connect-go v2 service (mountkit.RegistrarV2) is mounted with
// mountkit.RegisterServiceV2 through the Server's v2 chain
// (ServerInterceptors and HTTPOptions, else ChainV2's), with the v2 mount
// options (mountkit.WithInterceptorsV2, WithHTTPOptionsV2). A value
// implementing both registrations mounts on the major the Server is
// configured for (v1 when it is configured for both, or neither). A service
// of one major on a Server configured only for the other panics: mounting it
// with no chain would serve it unauthenticated.
func (s *Server) Mount(connectPath string, svc any, mountOpts ...mountkit.Option) {
	if s.Mux == nil {
		panic("serverkit: Server.Mount called with nil Server.Mux — set srv.Mux = http.NewServeMux() before mounting")
	}
	_, isV1 := svc.(mountkit.Registrar)
	_, isV2 := svc.(mountkit.RegistrarV2)
	v1, v2 := s.servesV1(), s.servesV2()
	switch {
	case isV2 && v2 && !(isV1 && v1):
		mountkit.RegisterServiceV2(s.mountV2(), svc, mountOpts...)
	case isV1 && v1:
		mountkit.RegisterService(s.Mux, svc, s.handlerOptions(), mountOpts...)
	case isV2 && v1:
		panic(fmt.Sprintf("serverkit: Server.Mount(%q): %T is a connect-go v2 service (RegisterV2), but this Server is "+
			"configured only for v1 (HandlerOpts / Chain); set ChainV2 or ServerInterceptors to mount it", connectPath, svc))
	case isV1 && v2:
		panic(fmt.Sprintf("serverkit: Server.Mount(%q): %T is a connect-go v1 service (Register), but this Server is "+
			"configured only for v2 (ChainV2 / ServerInterceptors); set Chain or HandlerOpts to mount it", connectPath, svc))
	case isV2 && !isV1:
		mountkit.RegisterServiceV2(s.mountV2(), svc, mountOpts...)
	default:
		// v1, or neither registration — RegisterService panics on the latter.
		mountkit.RegisterService(s.Mux, svc, s.handlerOptions(), mountOpts...)
	}
	s.Mounted(connectPath)
}

// handlerOptions is what the Mount helpers build a service with: HandlerOpts,
// or the Chain's options when HandlerOpts is unset.
func (s *Server) handlerOptions() []connect.HandlerOption {
	if s.HandlerOpts == nil && s.Chain != nil {
		return s.Chain.HandlerOptions()
	}
	return s.HandlerOpts
}

// servesV1 and servesV2 report whether the Server is configured to mount
// services of that connect-go major.
func (s *Server) servesV1() bool { return s.HandlerOpts != nil || s.Chain != nil }
func (s *Server) servesV2() bool { return s.ServerInterceptors != nil || s.ChainV2 != nil }

// serverInterceptorsV2 and httpOptionsV2 are what a v2 service is built
// with: the fields, or ChainV2's when they are unset.
func (s *Server) serverInterceptorsV2() []connectv2.ServerInterceptor {
	if s.ServerInterceptors == nil && s.ChainV2 != nil {
		return s.ChainV2.ServerInterceptors()
	}
	return s.ServerInterceptors
}

func (s *Server) httpOptionsV2() []connecthttp.Option {
	if s.HTTPOptions == nil && s.ChainV2 != nil {
		return s.ChainV2.HTTPOptions()
	}
	return s.HTTPOptions
}

// mountV2 is the MuxV2 v2 services mount through, built once per Mux.
func (s *Server) mountV2() *mountkit.MuxV2 {
	if s.muxV2 == nil || s.muxV2.Mux() != s.Mux {
		s.muxV2 = mountkit.NewMuxV2(s.Mux, s.serverInterceptorsV2(), s.httpOptionsV2()...)
	}
	return s.muxV2
}

// RequireMounted is the boot-time completeness guardrail: it verifies that
// every DECLARED proto service has a mounted handler on this Server, and
// returns a clear error naming any that don't.
//
// # Why this exists
//
// The retired generated DI injector gave a COMPILE-TIME guarantee that you
// wired every service — the generated wiring referenced each one by name,
// so a missing service was a build error. Explicit per-server composition
// trades that for hand-written mount calls, which a human (or a stale
// codegen pass) can forget. RequireMounted restores the guarantee at BOOT
// instead of compile: it walks the proto service descriptors the binary
// links and fails fast if any declared service was never mounted, so a
// forgotten Mount call is a loud startup error rather than a silent 404 in
// production.
//
// # How it walks declarations
//
// It ranges the supplied descriptor source — a protoregistry.Files (use
// protoregistry.GlobalFiles for the process-wide set the binary linked, or
// a scoped *protoregistry.Files in a test) — collecting every
// protoreflect.ServiceDescriptor's full name. Each is compared against the
// names recorded via Server.Mounted (see Mount). Any declared service with
// no recording is reported.
//
// ServeMux does not expose its registered patterns, so completeness is
// checked against EXPLICITLY RECORDED names (Server.Mounted), never by
// introspecting the mux — the recording seam is the source of truth.
//
// Returns nil when every declared service is mounted (including the trivial
// no-services case). The error, when non-nil, names every missing service
// sorted for stable output.
func (s *Server) RequireMounted(src DescriptorSource) error {
	if src == nil {
		return fmt.Errorf("serverkit.RequireMounted: nil descriptor source")
	}

	var missing []string
	src.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			full := string(svcs.Get(i).FullName())
			if _, ok := s.mounted[full]; !ok {
				missing = append(missing, full)
			}
		}
		return true
	})

	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"serverkit.RequireMounted: %d declared proto service(s) have no mounted handler: %v — "+
			"a service was declared in proto but never mounted (a forgotten Server.Mount / "+
			"mountkit.RegisterService call, or a Mounted() recording omitted). Mount it, or "+
			"if it is intentionally not served by this binary, exclude its FileDescriptor from "+
			"the descriptor source passed to RequireMounted",
		len(missing), missing,
	)
}

// DescriptorSource is the minimal proto-descriptor reader RequireMounted
// walks. *protoregistry.Files satisfies it directly, so callers pass
// protoregistry.GlobalFiles (the process-wide linked set) in production and
// a scoped *protoregistry.Files in hermetic tests. Defining the seam as an
// interface (rather than taking *protoregistry.Files concretely) keeps the
// completeness check testable without touching the global registry.
type DescriptorSource interface {
	RangeFiles(func(protoreflect.FileDescriptor) bool)
}

// compile-time assertion that the concrete registry type satisfies the seam.
var _ DescriptorSource = (*protoregistry.Files)(nil)

// RequireMounted is the package-level form of (*Server).RequireMounted, for
// callers who prefer serverkit.RequireMounted(srv, src) reading order. It is
// a thin forward — identical behaviour.
func RequireMounted(srv *Server, src DescriptorSource) error {
	return srv.RequireMounted(src)
}
