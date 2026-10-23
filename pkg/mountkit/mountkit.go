package mountkit

import (
	"fmt"
	"net/http"

	"connectrpc.com/connect"
)

// Registrar is the REQUIRED capability every mountable forge service
// implements: it registers its Connect handler on the mux. The generated
// service.go's Register method (which calls the protoc-gen-connect-go
// New<Svc>Handler and mux.Handle's the result) satisfies it directly.
//
// A service value that does not implement Registrar cannot be mounted —
// RegisterService panics, because that is a boot-time programming error
// (a non-service was handed to the mount loop), not a runtime condition
// to recover from.
type Registrar interface {
	// Register mounts the service's Connect handler on mux, applying the
	// supplied handler options (the shared interceptor chain + payload
	// limits). RegisterService passes the caller's opts through unchanged.
	Register(mux *http.ServeMux, opts ...connect.HandlerOption)
}

// HTTPRegistrar is an OPTIONAL capability for services that expose plain
// (non-Connect) HTTP routes — OAuth callbacks, hand-rolled REST, webhook
// receivers wired by hand. The signature mirrors the generated
// service.go's RegisterHTTP method exactly:
//
//	RegisterHTTP(mux *http.ServeMux, stack func(http.Handler) http.Handler)
//
// stack is the project's HTTP middleware (recovery / logging / audit,
// e.g. forge/pkg/middleware.HTTPStack) that the route should be wrapped
// in. mountkit does not build the stack — the caller supplies it via
// WithHTTPStack; when omitted, an identity (pass-through) stack is used so
// the route still mounts.
//
// Auth is intentionally NOT part of this stack: plain-HTTP routes commonly
// authenticate differently from Connect RPCs (webhook-signature vs JWT),
// so per-route auth stays the service's responsibility.
type HTTPRegistrar interface {
	RegisterHTTP(mux *http.ServeMux, stack func(http.Handler) http.Handler)
}

// WebhookRegistrar is an OPTIONAL capability for services that own
// forge.yaml-declared webhook routes. forge generates the
// RegisterWebhookRoutes method (in webhook_routes_gen.go) for exactly
// those services; its signature is identical to RegisterHTTP:
//
//	RegisterWebhookRoutes(mux *http.ServeMux, stack func(http.Handler) http.Handler)
//
// It is kept as a SEPARATE capability from HTTPRegistrar (rather than
// folded into it) because the two are generated independently — a service
// may declare webhooks without overriding RegisterHTTP, or vice versa —
// and RegisterService invokes whichever subset the value implements. The
// same caller-supplied stack feeds both.
type WebhookRegistrar interface {
	RegisterWebhookRoutes(mux *http.ServeMux, stack func(http.Handler) http.Handler)
}

// Option configures one service's mount: a RegisterService call, a
// serverkit.Server.Mount, or a generated mount through the owned
// app.MountOptions value (one field per service).
type Option func(*options)

type options struct {
	stack       func(http.Handler) http.Handler
	handlerOpts []connect.HandlerOption
}

// WithHandlerOptions adds Connect handler options to this one mount, AFTER
// the shared chain every service gets (serverkit.Server.HandlerOpts, or the
// opts passed to RegisterService). Repeated calls accumulate in order.
//
// After means inside: connect runs interceptors in the order they were
// added, the first outermost, so the shared chain (recovery, logging, auth…)
// wraps a per-mount interceptor, and a per-mount option overrides a shared
// one where connect lets the last option win (read/send limits). reliant adds
// a machine-wake interceptor to two of its services this way; the other
// services' chains are untouched, because the shared slice is never appended
// to in place.
func WithHandlerOptions(opts ...connect.HandlerOption) Option {
	return func(o *options) { o.handlerOpts = append(o.handlerOpts, opts...) }
}

// Resolved is what one mount resolves to: the handler options its Connect
// handler is built with, and the HTTP stack its plain-HTTP and webhook
// routes are wrapped in.
type Resolved struct {
	HandlerOptions []connect.HandlerOption
	HTTPStack      func(http.Handler) http.Handler
}

// Resolve applies mountOpts to a mount: the shared handler options followed
// by every WithHandlerOptions, in a new slice (shared is never written to,
// so mounts cannot see each other's options), and the WithHTTPStack stack or
// defaultStack when none was given. RegisterService resolves with it, and the
// generated mounts, which register through the service's own methods, call
// it to honour the same options.
func Resolve(shared []connect.HandlerOption, defaultStack func(http.Handler) http.Handler, mountOpts ...Option) Resolved {
	cfg := options{}
	for _, o := range mountOpts {
		if o != nil {
			o(&cfg)
		}
	}
	handlerOpts := make([]connect.HandlerOption, 0, len(shared)+len(cfg.handlerOpts))
	handlerOpts = append(append(handlerOpts, shared...), cfg.handlerOpts...)
	stack := cfg.stack
	if stack == nil {
		stack = defaultStack
	}
	return Resolved{HandlerOptions: handlerOpts, HTTPStack: stack}
}

// WithHTTPStack supplies the HTTP middleware stack passed to the optional
// HTTPRegistrar / WebhookRegistrar capabilities. The same stack feeds
// both. A nil stack is treated as absent (identity pass-through).
//
// When a service implements neither optional capability, the stack is
// never consulted, so callers that mount only pure-Connect services may
// omit it.
func WithHTTPStack(stack func(http.Handler) http.Handler) Option {
	return func(o *options) { o.stack = stack }
}

// identityStack is the pass-through used when no stack was supplied: the
// optional registrars still mount their routes, just without the
// recovery/logging/audit wrap.
func identityStack(next http.Handler) http.Handler { return next }

// RegisterService mounts a single forge Connect service onto mux.
//
// svc MUST implement Registrar; RegisterService asserts it and panics with
// a clear message if absent — handing a value without Register to the
// mount loop is a programming error caught at boot, not a recoverable
// runtime condition. It then calls Register with the caller's shared opts
// followed by this mount's WithHandlerOptions (Resolve). mountkit adds no
// interceptors of its own — see the package doc.
//
// After the required Connect registration, RegisterService type-asserts
// each OPTIONAL capability (HTTPRegistrar, WebhookRegistrar) and invokes
// any the value implements, passing the stack from WithHTTPStack (or an
// identity stack when none was supplied). A service implementing only
// Registrar mounts exactly its Connect handler and nothing else.
func RegisterService(mux *http.ServeMux, svc any, opts []connect.HandlerOption, mountOpts ...Option) {
	if mux == nil {
		panic("mountkit: RegisterService called with nil mux")
	}

	reg, ok := svc.(Registrar)
	if !ok {
		panic(fmt.Sprintf(
			"mountkit: %T does not implement mountkit.Registrar "+
				"(missing Register(*http.ServeMux, ...connect.HandlerOption)); "+
				"it cannot be mounted as a Connect service",
			svc,
		))
	}
	m := Resolve(opts, identityStack, mountOpts...)
	reg.Register(mux, m.HandlerOptions...)

	if h, ok := svc.(HTTPRegistrar); ok {
		h.RegisterHTTP(mux, m.HTTPStack)
	}
	if w, ok := svc.(WebhookRegistrar); ok {
		w.RegisterWebhookRoutes(mux, m.HTTPStack)
	}
}
