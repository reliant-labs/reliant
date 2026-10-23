package serverkit

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
)

// Probes is a process's probe surface as listener Front routes: liveness and
// readiness on Paths (/healthz, /readyz by default) over the process's
// Drainer and its ready checks, /metrics, and any route added with Handle
// (a /flow-health). It is the root's policy, not a component: one per
// process, built before the components, and served IN FRONT of a listener's
// edge so a kubelet or a scraper reaches it without CORS or credentials.
//
// Ready checks may be added after the readiness handler is built — a
// composition graph knows which of its nodes report Ready only once the
// root's closure is constructed, after the listener that serves the probe.
type Probes struct {
	// Drainer is the readiness gate and the stop budget: readiness answers
	// 503 before Drainer.MarkReady and once its drain has begun. Required.
	Drainer *Drainer
	// Metrics is the /metrics handler (observe.Setup's); nil serves none.
	Metrics http.Handler
	// Timeout bounds all of one readiness request's checks together; zero
	// is the serverkit default.
	Timeout time.Duration
	// Logger receives check transitions; nil is slog.Default().
	Logger *slog.Logger
	// Paths are the liveness and readiness paths: the process's
	// Config.Probes, so the boot probes and the probe surface answer on the
	// same ones. Empty is /healthz and /readyz.
	Paths ProbePaths

	mu        sync.RWMutex
	checks    []ReadyCheck
	routes    map[string]http.Handler
	handedOut bool
	front     http.HandlerFunc
	once      sync.Once
}

// Handle adds a route to the probe surface — a health endpoint of the
// process's own, such as /flow-health, served beside the probes and, like
// them, in front of the edge: no CORS, no credentials. So it must be safe to
// answer anonymously.
//
// Add routes before a listener takes the surface (Front): a listener mounts
// its routes once, when it is built. In a composition graph that is any time
// during the root's build for a root whose probes a probe listener serves (it
// is built after the closure), and before the API listener node otherwise.
// A late route, a path the surface already serves, or a path that is not a
// plain "/…" one is an error.
func (p *Probes) Handle(path string, h http.Handler) error {
	if h == nil {
		return fmt.Errorf("serverkit.Probes.Handle(%q): nil handler", path)
	}
	if err := checkRoutePath(path); err != nil {
		return fmt.Errorf("serverkit.Probes.Handle: path %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handedOut {
		return fmt.Errorf("serverkit.Probes.Handle(%q): a listener already took the probe routes; add it before the listener that serves them is built", path)
	}
	if _, taken := p.baseRoutes()[path]; taken {
		return fmt.Errorf("serverkit.Probes.Handle(%q): the probe surface already serves it", path)
	}
	if _, taken := p.routes[path]; taken {
		return fmt.Errorf("serverkit.Probes.Handle(%q): already added", path)
	}
	if p.routes == nil {
		p.routes = map[string]http.Handler{}
	}
	p.routes[path] = h
	return nil
}

// AddReadyCheck adds dependency checks to /readyz. Checks with no Name or no
// Check are dropped. Safe while /readyz is serving.
func (p *Probes) AddReadyCheck(checks ...ReadyCheck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range checks {
		if c.Name == "" || c.Check == nil {
			continue
		}
		p.checks = append(p.checks, c)
	}
}

// ReadyChecks returns the checks /readyz runs.
func (p *Probes) ReadyChecks() []ReadyCheck {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]ReadyCheck(nil), p.checks...)
}

// Front returns the probe routes, for ListenerDeps.Front: liveness and
// readiness on Paths, /metrics when Metrics is set, and every route added
// with Handle. Taking it closes the surface to new routes.
func (p *Probes) Front() map[string]http.Handler {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handedOut = true
	front := p.baseRoutes()
	for path, h := range p.routes {
		front[path] = h
	}
	return front
}

// baseRoutes are the routes the surface serves of its own: the probes and
// /metrics.
func (p *Probes) baseRoutes() map[string]http.Handler {
	front := p.Paths.routes(p.readyz())
	if p.Metrics != nil {
		front[metricsPath] = p.Metrics
	}
	return front
}

// readyz is the /readyz handler: one per Probes, so the per-check
// transition logging sees every request.
func (p *Probes) readyz() http.HandlerFunc {
	p.once.Do(func() {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = defaultReadinessTimeout
		}
		logger := p.Logger
		if logger == nil {
			logger = slog.Default()
		}
		drainer := p.Drainer
		if drainer == nil {
			drainer = NewDrainer(Config{})
		}
		p.front = readyzHandlerFunc(drainer, p.ReadyChecks, timeout, logger)
	})
	return p.front
}

// ProbeListenerDeps are a probe listener's dependencies.
type ProbeListenerDeps struct {
	// Config is the listener's address, TLS and timeouts — the process's
	// server address (Config.Listener()), where a kubelet probes it.
	Config ListenerConfig
	// Probes is the surface it serves. Required.
	Probes *Probes
	// Logger is the listener's logger; nil is slog.Default().
	Logger *slog.Logger
}

// NewProbeListener builds a Listener that serves nothing but the process's
// probe surface (liveness, readiness, /metrics, and Probes.Handle's routes): what a process with no API listener
// — a worker, an operator, a gateway's outbound half — answers its kubelet
// on. It takes the address over from the boot probes like any Listener, has
// no chain and no routes, and is the one listener that never authenticates
// a request, because it serves none.
func NewProbeListener(deps ProbeListenerDeps) (*Listener, error) {
	if deps.Probes == nil {
		return nil, errors.New("serverkit.NewProbeListener: ProbeListenerDeps.Probes is required")
	}
	if err := deps.Probes.Paths.Validate(); err != nil {
		return nil, fmt.Errorf("serverkit.NewProbeListener: Probes.Paths: %w", err)
	}
	return newListener(ListenerDeps{
		Config: deps.Config,
		Routes: RoutesFunc(func(*http.ServeMux, ...connect.HandlerOption) []string { return nil }),
		Logger: deps.Logger,
		Front:  deps.Probes.Front(),
	})
}
