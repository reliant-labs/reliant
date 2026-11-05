package serverkit

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// SetupTelemetry installs the process's OpenTelemetry providers from cfg
// (OTLPEndpoint, ServiceName and the standard OTEL_* environment) and returns
// their flush — run it LAST on the way down, so the shutdown's own spans and
// metrics are exported — and the Prometheus /metrics handler, for
// Probes.Metrics. Run calls it itself; a process that supervises its own
// components (lifecyclekit.Supervise) calls it before building them, so every
// instrumented client is created against the installed providers.
func SetupTelemetry(ctx context.Context, cfg Config) (func(context.Context) error, http.Handler, error) {
	instanceID, _ := os.Hostname()
	return observe.Setup(ctx, observeConfig(cfg, instanceID))
}

// Pprof is the pprof endpoint on a listener of its own, as a component: Run
// serves it, Close shuts it down. pprof is never mounted on a public handler
// — its heap, goroutine and profile endpoints can leak memory contents and
// stall the process — so the address is a loopback one by default
// (Config.PprofAddr, 127.0.0.1:6060). It is always on because the answer to
// "what is holding the memory" only exists while the process is alive:
//
//	go tool pprof http://localhost:6060/debug/pprof/heap
//	kubectl port-forward <pod> 6060:6060   # then the line above
//
// A bind failure is logged and the endpoint skipped, never a component
// failure: a debug surface that can take the service down is worse than
// none. The declared-metrics schema rides the same listener.
type Pprof struct {
	addr   string
	logger *slog.Logger
	srv    *http.Server

	mu sync.Mutex
	ln net.Listener
}

// NewPprof builds the pprof endpoint for cfg.PprofAddr; an empty address is
// an endpoint that serves nothing. logger nil is slog.Default().
func NewPprof(cfg Config, logger *slog.Logger) *Pprof {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle(metrics.SchemaPath, metrics.SchemaHandler(metrics.Default(), cfg.ServiceName, schemaVersion(cfg.ServiceVersion)))
	return &Pprof{
		addr:   cfg.PprofAddr,
		logger: logger,
		srv:    &http.Server{Addr: cfg.PprofAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second},
	}
}

// Run serves pprof until ctx ends or Close. A disabled endpoint, or one whose
// address is taken, waits for ctx and returns nil.
func (p *Pprof) Run(ctx context.Context) error {
	if p.addr == "" {
		<-ctx.Done()
		return nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", p.addr)
	if err != nil {
		p.logger.ErrorContext(ctx, "pprof listener unavailable — continuing without it",
			"addr", p.addr, "error", err,
			"hint", "another process is probably bound there; set PPROF_ADDR to a free port, or pass --pprof-addr= to disable pprof")
		<-ctx.Done()
		return nil
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	p.logger.InfoContext(ctx, "pprof server starting", "addr", p.addr)
	errCh := make(chan error, 1)
	go func() { errCh <- p.srv.Serve(ln) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		p.logger.ErrorContext(ctx, "pprof server error", "error", err)
		return nil
	case <-ctx.Done():
		_ = p.srv.Close()
		<-errCh
		return nil
	}
}

// Close shuts the endpoint down within ctx.
func (p *Pprof) Close(ctx context.Context) error {
	p.mu.Lock()
	bound := p.ln != nil
	p.mu.Unlock()
	if !bound {
		return nil
	}
	if err := p.srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
