package serverkit

// listener.go — an HTTP listener as a component.
//
// A Listener is one address a process serves: its own http.Server (HTTP/1.1,
// HTTP/2 over TLS, h2c), its own timeouts and TLS, its own Streams registry,
// and the routes it mounts through its own Chain. A process may run several —
// a public API listener authenticating users beside a daemon listener
// authenticating daemons — and each drains independently.
//
// Its lifecycle is four methods, the shape lifecyclekit supervises:
//
//	Run(ctx)    bind (if Listen has not) and serve until Drain or ctx ends
//	Drain(ctx)  stop accepting; drain unary requests and long-lived streams
//	            soft-then-hard inside ctx's deadline (DrainHTTP)
//	Close(ctx)  end whatever is left, now
//	Ready(ctx)  nil while bound and not draining
//
// Cancelling Run's ctx before Drain is a hard stop. A graceful stop is Drain,
// then cancel Run's ctx, then Close — the order lifecyclekit.Supervise uses
// and serverkit.Run uses for its one listener.
//
// TRUST BOUNDARY. NewListener mounts its Routes with its Chain's handler
// options and then checks every proto service the routes mounted against the
// chain: a service declaring `option (forge.v1.service) = {audience: "user"}`
// (the default) on a listener whose chain authenticates daemons is a boot
// error. So a daemon credential can never reach a user RPC, even through a
// hand-written route set.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// ListenerConfig is one listener's own block: where it binds and how its
// connections behave. Durations follow Config's rule — zero is the default,
// negative is none. Config.Listener projects a process Config onto it.
type ListenerConfig struct {
	// Addr is the address to bind (":8080", "127.0.0.1:0"). Required.
	Addr string

	// TLSCertPath and TLSKeyPath serve TLS when both are set. The pair is
	// loaded by Listen, before the address is bound: a missing or invalid
	// certificate fails boot, never a listener that already reported Ready.
	TLSCertPath string
	TLSKeyPath  string

	// ReadHeaderTimeout bounds reading a request's headers. Zero is 10s.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading a whole request, body included — over
	// HTTP/2 each stream's request body, so a listener whose clients hold
	// long client or bidi streams sets it negative. Zero is 30s.
	ReadTimeout time.Duration
	// IdleTimeout is how long a keep-alive connection may sit idle. Zero is
	// 120s.
	IdleTimeout time.Duration
	// HTTP2MaxConcurrentStreams caps the streams one HTTP/2 connection may
	// open. Zero is net/http's default.
	HTTP2MaxConcurrentStreams int
	// WriteStallTimeout bounds one write blocked on a client that is not
	// reading (WriteStallGuard). Zero is DefaultWriteStallTimeout.
	WriteStallTimeout time.Duration

	// DrainReserve is the part of the drain budget kept back after the
	// long-lived streams are ended (Streams.DrainReserve). Zero is
	// DefaultDrainReserve.
	DrainReserve time.Duration
	// RedialOnDrain names the procedures or services whose drained streams
	// end Unavailable instead of OK (Streams.RedialOnDrain).
	RedialOnDrain []string
}

// The listener defaults Config.Normalize and ListenerConfig share.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

func (c *ListenerConfig) normalize() {
	if c.ReadHeaderTimeout == 0 {
		c.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = defaultReadTimeout
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.WriteStallTimeout == 0 {
		c.WriteStallTimeout = DefaultWriteStallTimeout
	}
	if c.DrainReserve == 0 {
		c.DrainReserve = DefaultDrainReserve
	}
}

// tls reports whether the listener serves TLS.
func (c ListenerConfig) tls() bool { return c.TLSCertPath != "" && c.TLSKeyPath != "" }

// Listener projects the listener half of a process Config — the block
// serverkit.Run's one listener is built from.
func (c Config) Listener() ListenerConfig {
	c.Normalize()
	return ListenerConfig{
		Addr:                      c.Addr,
		TLSCertPath:               c.TLSCertPath,
		TLSKeyPath:                c.TLSKeyPath,
		ReadHeaderTimeout:         c.ReadHeaderTimeout,
		ReadTimeout:               c.ReadTimeout,
		IdleTimeout:               c.IdleTimeout,
		HTTP2MaxConcurrentStreams: c.HTTP2MaxConcurrentStreams,
		WriteStallTimeout:         c.WriteStallTimeout,
		DrainReserve:              c.DrainReserve,
		RedialOnDrain:             append([]string(nil), c.RedialOnDrain...),
	}
}

// Routes is a set of handlers a Listener serves.
type Routes interface {
	// Register mounts every route on mux, building each Connect handler with
	// opts — the listener's chain — and returns the fully qualified proto
	// service names it mounted ("acme.billing.v1.BillingService"), which the
	// listener checks against its chain's audience. Plain HTTP routes (a
	// websocket, a login callback) are mounted too and named nowhere.
	Register(mux *http.ServeMux, opts ...connect.HandlerOption) []string
}

// RoutesFunc adapts a function to Routes — the shape of a generated
// (*app.Components).Mount<Svc> closed over its receiver.
type RoutesFunc func(mux *http.ServeMux, opts ...connect.HandlerOption) []string

// Register calls f.
func (f RoutesFunc) Register(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
	return f(mux, opts...)
}

// ListenerDeps configures a Listener. Config, Chain and Routes are required.
type ListenerDeps struct {
	// Config is the listener's own block: address, TLS, timeouts, drain.
	Config ListenerConfig
	// Chain is the interceptor chain every Connect route is built with, and
	// the audience every mounted service must declare. Required.
	Chain *Chain
	// Routes are what the listener serves. Required.
	Routes Routes
	// Logger receives the listener's lines and its drain phase; nil is
	// slog.Default().
	Logger *slog.Logger
	// Edge wraps the routes' handler — CORS, security headers, a request id.
	// The Front routes bypass it. nil is no edge.
	Edge func(http.Handler) http.Handler
	// Front are routes served IN FRONT of Edge and of the chain, by exact
	// pattern: probes and /metrics, which a kubelet or a scraper reaches
	// without CORS or credentials. "/" is not allowed: it is the routes'.
	Front map[string]http.Handler
	// REST serves a vanguard REST transcoder over the mounted services
	// (forge.yaml api.rest) in place of the bare Connect mux.
	REST bool
}

// Listener is an HTTP listener component. Build it with NewListener.
type Listener struct {
	cfg      ListenerConfig
	logger   *slog.Logger
	audience string
	services []string
	streams  *Streams
	srv      *http.Server

	mu        sync.Mutex
	ln        net.Listener
	serving   bool
	draining  bool
	tlsLoaded bool // Listen loaded cfg's certificate into srv.TLSConfig
}

// NewListener mounts deps.Routes with deps.Chain's handler options and builds
// the listener; it binds nothing until Listen or Run. It refuses — returns an
// error — when a required dep is missing, and when a mounted service declares
// an audience other than the chain's.
func NewListener(deps ListenerDeps) (*Listener, error) {
	if deps.Chain == nil {
		return nil, errors.New("serverkit.NewListener: ListenerDeps.Chain is required: every listener authenticates through a chain (serverkit.NewChain)")
	}
	return newListener(deps)
}

// newListener is NewListener without the Chain requirement: serverkit.Run's
// listener, for a Server whose handlers were mounted with the caller's own
// HandlerOpts, has no Chain to name an audience.
func newListener(deps ListenerDeps) (*Listener, error) {
	var errs []error
	if deps.Config.Addr == "" {
		errs = append(errs, errors.New("ListenerDeps.Config.Addr is required"))
	}
	if deps.Routes == nil {
		errs = append(errs, errors.New("ListenerDeps.Routes is required"))
	}
	if _, ok := deps.Front["/"]; ok {
		errs = append(errs, errors.New(`ListenerDeps.Front may not claim "/": it is the routes' pattern`))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("serverkit.NewListener: %w", errors.Join(errs...))
	}
	cfg := deps.Config
	cfg.normalize()
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	l := &Listener{
		cfg:    cfg,
		logger: logger,
		streams: &Streams{
			DrainReserve:  cfg.DrainReserve,
			RedialOnDrain: cfg.RedialOnDrain,
			Logger:        logger,
		},
	}

	var handler http.Handler
	if hr, ok := deps.Routes.(*handlerRoutes); ok {
		// serverkit.Run's Server: already mounted, served as it is.
		handler, l.services = hr.handler, hr.services
	} else {
		mux := http.NewServeMux()
		var opts []connect.HandlerOption
		if deps.Chain != nil {
			opts = deps.Chain.HandlerOptions()
		}
		l.services = deps.Routes.Register(mux, opts...)
		handler = mux
		if deps.REST {
			if rest := RESTHandler(mux, l.services, logger); rest != nil {
				handler = rest
			}
		}
	}
	if deps.Chain != nil {
		l.audience = deps.Chain.Audience()
		if err := checkAudience(cfg.Addr, l.audience, l.services); err != nil {
			return nil, err
		}
	}
	if deps.Edge != nil {
		handler = deps.Edge(handler)
	}

	top := http.NewServeMux()
	patterns := make([]string, 0, len(deps.Front))
	for p := range deps.Front {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	for _, p := range patterns {
		top.Handle(p, deps.Front[p])
	}
	top.Handle("/", handler)

	l.srv = newHTTPServer(cfg, top, l.streams)
	return l, nil
}

// handlerRoutes is a handler already mounted, with the proto services it
// mounted: serverkit.Run's Server.
type handlerRoutes struct {
	handler  http.Handler
	services []string
}

func (h *handlerRoutes) Register(mux *http.ServeMux, _ ...connect.HandlerOption) []string {
	mux.Handle("/", h.handler)
	return h.services
}

// ServiceAudience is the audience a proto service declares with
// `option (forge.v1.service) = {audience: "..."}` — AudienceUser when it
// declares none.
func ServiceAudience(sd protoreflect.ServiceDescriptor) string {
	if opts := sd.Options(); opts != nil {
		if so, ok := proto.GetExtension(opts, forgepb.E_Service).(*forgepb.ServiceOptions); ok && so.GetAudience() != "" {
			return so.GetAudience()
		}
	}
	return AudienceUser
}

// checkAudience fails when a mounted service is not in the proto registry or
// declares an audience other than the chain's.
func checkAudience(addr, audience string, services []string) error {
	var problems []string
	for _, name := range services {
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s (not in the proto registry, so its audience cannot be checked)", name))
			continue
		}
		sd, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s (a %T, not a service)", name, desc))
			continue
		}
		if got := ServiceAudience(sd); got != audience {
			problems = append(problems, fmt.Sprintf("%s (audience %q)", name, got))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("serverkit: listener %s authenticates audience %q but mounts %s — mount each service on a listener whose chain authenticates its audience, or declare `option (forge.v1.service) = {audience: %q}` on a service that really serves it",
		addr, audience, strings.Join(problems, ", "), audience)
}

// Audience is the audience the listener's chain admits; "" for serverkit.Run's
// listener over a Server with no Chain.
func (l *Listener) Audience() string { return l.audience }

// Services returns the proto services the listener mounted.
func (l *Listener) Services() []string { return append([]string(nil), l.services...) }

// Streams is the listener's registry of long-lived streams.
func (l *Listener) Streams() *Streams { return l.streams }

// Addr is the bound address — the real port when Config.Addr asked for
// ":0" — or nil before Listen.
func (l *Listener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln == nil {
		return nil
	}
	return l.ln.Addr()
}

// Listen binds the listener's address without serving yet, so a caller can
// learn the bound address, or report a bind failure, before anything runs.
// With TLS configured it first loads and validates the certificate pair, so a
// bad one is a Listen error: the listener never binds and never reports
// Ready. Run calls it when it has not been called. Idempotent.
//
// When ctx carries the boot probes (StartBootProbes) bound to the same
// address, Listen takes the address over from them.
func (l *Listener) Listen(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln != nil {
		return nil
	}
	if l.draining {
		return fmt.Errorf("listen %s: the listener is draining", l.cfg.Addr)
	}
	// The certificate is loaded before anything binds, so a bad one fails
	// here — boot — rather than in Run's ServeTLS, after Ready had already
	// reported the listener bound and a supervisor had moved on.
	if l.cfg.tls() && !l.tlsLoaded {
		if err := l.loadTLS(); err != nil {
			return fmt.Errorf("listen %s: %w", l.cfg.Addr, err)
		}
	}
	handoff := false
	if b := BootProbesFrom(ctx); b != nil && b.addr == l.cfg.Addr {
		b.release()
		handoff = true
	}
	ln, err := listenAfterHandoff(ctx, l.cfg.Addr, handoff)
	if err != nil {
		return fmt.Errorf("listen %s: %w", l.cfg.Addr, err)
	}
	l.ln = ln
	l.logger.InfoContext(ctx, "server listening", "addr", l.cfg.Addr, "tls", l.cfg.tls())
	return nil
}

// loadTLS loads the certificate pair into the server's TLS config, which
// ServeTLS then uses as is (it reads the files only when no certificate is
// configured). Called with l.mu held.
func (l *Listener) loadTLS() error {
	cert, err := tls.LoadX509KeyPair(l.cfg.TLSCertPath, l.cfg.TLSKeyPath)
	if err != nil {
		return fmt.Errorf("load TLS certificate (TLSCertPath %q, TLSKeyPath %q): %w", l.cfg.TLSCertPath, l.cfg.TLSKeyPath, err)
	}
	cfg := l.srv.TLSConfig.Clone()
	if cfg == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.Certificates = []tls.Certificate{cert}
	l.srv.TLSConfig = cfg
	l.tlsLoaded = true
	return nil
}

// listenAfterHandoff binds addr. Taking an address over from the boot probes
// can lose a race with the kernel releasing it, so in that case an in-use
// error is retried for a couple of seconds; a first-time bind is not.
func listenAfterHandoff(ctx context.Context, addr string, handoff bool) (net.Listener, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err == nil || !handoff || !addrInUse(err) || time.Now().After(deadline) {
			return ln, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// addrInUse reports whether err is a bind refused because something still
// holds the address, in this OS's spelling (addrInUseErrnos).
func addrInUse(err error) bool {
	for _, errno := range addrInUseErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// Run binds (unless Listen already did) and serves until Drain stops the
// listener — it then returns nil — or ctx ends, which stops it hard (Close).
// A serve failure is returned. Call it once.
func (l *Listener) Run(ctx context.Context) error {
	if err := l.Listen(ctx); err != nil {
		return err
	}
	l.mu.Lock()
	if l.serving {
		l.mu.Unlock()
		return fmt.Errorf("serverkit: listener %s: Run called twice", l.cfg.Addr)
	}
	l.serving = true
	ln, draining := l.ln, l.draining
	l.mu.Unlock()
	if draining {
		return nil
	}

	errCh := make(chan error, 1)
	go func() {
		var err error
		if l.cfg.tls() {
			// Listen loaded the certificate into srv.TLSConfig.
			err = l.srv.ServeTLS(ln, "", "")
		} else {
			err = l.srv.Serve(ln)
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	l.mu.Lock()
	drained := l.draining
	l.mu.Unlock()
	if !drained {
		_ = l.Close(context.WithoutCancel(ctx))
	}
	return <-errCh
}

// Drain stops the listener accepting and drains it inside ctx's deadline: the
// unary requests through http.Server.Shutdown and the long-lived streams
// soft-then-hard (DrainHTTP), writing the "drain: listener drained" phase.
// Run then returns. Draining a listener that never bound does nothing.
func (l *Listener) Drain(ctx context.Context) error {
	l.mu.Lock()
	l.draining = true
	bound := l.ln != nil
	l.mu.Unlock()
	if !bound {
		return nil
	}
	return DrainHTTP(ctx, l.srv, l.streams)
}

// Close ends everything the listener still holds, now: every bound stream,
// every hijacked connection, every open connection. After a completed Drain
// there is nothing left and it returns at once.
func (l *Listener) Close(context.Context) error {
	l.mu.Lock()
	l.draining = true
	l.mu.Unlock()
	l.streams.End()
	l.streams.CloseHijacked()
	return l.srv.Close()
}

// Ready reports nil while the listener is bound and not draining — its half
// of a process's readiness.
func (l *Listener) Ready(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.draining:
		return fmt.Errorf("listener %s is draining", l.cfg.Addr)
	case l.ln == nil:
		return fmt.Errorf("listener %s is not bound", l.cfg.Addr)
	}
	return nil
}
