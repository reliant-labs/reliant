// Package mountkit mounts a forge Connect service onto an http.ServeMux.
//
// # Why a library
//
// Earlier forge versions emitted a per-service Mount closure in the
// generated inventory: each closure called svc.Register(mux, opts...),
// then conditionally svc.RegisterHTTP(...) and svc.RegisterWebhookRoutes(...)
// — the same handful of lines repeated once per service, gated by a
// codegen flag (HasWebhooks). That body is uniform across every
// service and every project, so it belongs in one tested library function
// rather than in N generated closures.
//
// mountkit.RegisterService replaces that closure. It is interface-driven:
// it asks the service value which capabilities it has (required Connect
// registration, optional plain-HTTP routes, optional webhook routes) by
// type-asserting small capability interfaces, and invokes whatever is
// present. No per-service codegen, no hard dependency on any consumer's
// concrete service type.
//
// # What it does NOT do
//
// mountkit adds no interceptors of its own: RegisterService threads the
// caller's opts through to Register, followed by any options the caller
// gave this one mount with WithHandlerOptions (a per-service interceptor on
// top of the shared chain).
//
// mountkit does not build the HTTP middleware stack the optional
// registrars consume — that stack (recovery / logging / audit, e.g.
// forge/pkg/middleware.HTTPStack) is wired from the project's middleware
// package, so the caller constructs it and passes it via WithHTTPStack.
// When no stack is supplied, optional HTTP/webhook routes are mounted
// with an identity (pass-through) stack.
//
// # The boundary with serverkit
//
// serverkit takes an already-composed http.Handler and owns the runtime
// lifecycle; it knows nothing about service names or mounting. mountkit
// sits one level ABOVE serverkit, in the mux-composition step the
// generated cmd-server shim performs before handing the finished handler
// to serverkit.Run. They are deliberately separate packages so neither
// has to import the other's concerns.
//
// # Usage in generated code
//
// The generated cmd-server shim (or the data-only inventory's Mount
// closure, during migration) calls RegisterService once per selected
// service:
//
//	stack := fmw.HTTPStack(logger, middleware.ClaimsFromContext)
//	for _, svc := range selectedServices {
//	    mountkit.RegisterService(mux, svc, opts, mountkit.WithHTTPStack(stack))
//	}
//
// opts is the shared []connect.HandlerOption (observe interceptors,
// read/send limits) already composed once for the whole process.
package mountkit
