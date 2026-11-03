package serverkit

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

// boot.go — probes that answer BEFORE the server is composed.
//
// The generated serve pipeline connects to the database (AutoMigrate, then
// OpenInfra) before it can build the handler Run serves, and Run is what
// binds the listener. So with a database that is not up yet, nothing listens
// and the pod looks identical to a crashed one. StartBootProbes closes that
// window: it binds Config.Addr at the top of boot and answers
//
//	/healthz  200 "ok"        — liveness: the process is alive and booting
//	/readyz   503             — not serving yet; names every dependency
//	                            currently being waited on, and — for a
//	                            composition-graph root — each graph node
//	                            already built (ok) and the ones still under
//	                            construction (detail "building")
//
// Graph construction reaches the probes through Constructing, the one hook a
// builder (graphkit's Progress) needs; the probes know nothing of the graph.
// A node whose constructor blocks — a provider waiting on another process's
// migrations, a dial that retries — is then named in /readyz for the whole
// wait, and logged every bootProgressEvery while it lasts.
//
// Run takes the address over from it (see BootProbes.release) just before
// binding its own listener, so the handoff is invisible to a kubelet probe
// that tolerates a refused connection, which every default probe does.

type bootKey struct{}

// BootProbes is the early probe listener. The zero value is not usable; use
// StartBootProbes. A nil *BootProbes is valid and inert, so callers need no
// guard.
type BootProbes struct {
	srv    *http.Server
	logger *slog.Logger
	addr   string // the Config.Addr bound, which a Listener on it takes over

	mu       sync.Mutex
	pending  map[string]struct{}
	building []constructing // outermost first
	built    []string       // completion order
	progress *time.Timer    // the next "still constructing" log line; nil when idle
	closed   bool
}

// constructing is one graph node whose constructor is running.
type constructing struct {
	name  string
	since time.Time
}

// bootProgressEvery is how long a node may construct before the boot log
// names it, and how often it is named again while it still is: a slow node
// is visible without a line per node on every boot. A variable so a test can
// shorten it.
var bootProgressEvery = 10 * time.Second

// Detail strings of a graph node's /readyz check during boot. Fixed words,
// never an error: the no-leak contract of readyzCheckStatus holds.
const bootDetailBuilding = "building"

// StartBootProbes binds cfg.Addr and serves the boot probes. It returns ctx
// carrying the probes: pass THAT ctx to OpenDB / RetryConnect / AutoMigrate
// (which then report the dependency they wait on) and to Run (which takes the
// listener over). Close it on error paths; it is idempotent.
func StartBootProbes(ctx context.Context, cfg Config, logger *slog.Logger) (context.Context, *BootProbes, error) {
	cfg.Normalize()
	if cfg.Addr == "" {
		return ctx, nil, fmt.Errorf("serverkit.StartBootProbes: Config.Addr is required")
	}
	if err := cfg.Probes.Validate(); err != nil {
		return ctx, nil, fmt.Errorf("serverkit.StartBootProbes: Config.Probes: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return ctx, nil, fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}
	b := &BootProbes{logger: logger, addr: cfg.Addr, pending: map[string]struct{}{}}
	mux := http.NewServeMux()
	for path, h := range cfg.Probes.routes(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeReadyz(w, http.StatusServiceUnavailable, readyzResponse{Status: "not_ready", Checks: b.checks()})
	})) {
		mux.Handle(path, h)
	}
	b.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = b.srv.Serve(ln) }()
	logger.InfoContext(ctx, "boot probes listening — readiness fails until the server is composed",
		"addr", cfg.Addr, "liveness", cfg.Probes.Live, "readiness", cfg.Probes.Ready)
	return context.WithValue(ctx, bootKey{}, b), b, nil
}

// BootProbesFrom returns the probes StartBootProbes put on ctx, or nil.
func BootProbesFrom(ctx context.Context) *BootProbes {
	b, _ := ctx.Value(bootKey{}).(*BootProbes)
	return b
}

// Close stops the boot listener. Idempotent; safe on nil.
func (b *BootProbes) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.progress != nil {
		b.progress.Stop()
		b.progress = nil
	}
	b.mu.Unlock()
	_ = b.srv.Close()
}

// Constructing records that a composition-graph node's constructor has
// started: /readyz lists node as building (ok false, detail "building")
// until the returned done is called with the constructor's error, and then
// as built (ok true) when that error is nil. A failed node is dropped — the
// build that called it fails boot with the cause. It satisfies graphkit's
// Progress, so a generated graph reports its construction here
// (graphkit.Builder.ReportTo). Safe on nil, which records nothing.
func (b *BootProbes) Constructing(node string) (done func(err error)) {
	if b == nil {
		return func(error) {}
	}
	b.mu.Lock()
	b.building = append(b.building, constructing{name: node, since: time.Now()})
	if b.progress == nil && !b.closed {
		b.progress = time.AfterFunc(bootProgressEvery, b.logProgress)
	}
	b.mu.Unlock()
	var once sync.Once
	return func(err error) { once.Do(func() { b.constructed(node, err) }) }
}

// constructed ends node's construction: built on a nil err, dropped on any
// other. A node slow enough to have been logged as constructing is logged
// again as it finishes, so the wait the log named has an end.
func (b *BootProbes) constructed(node string, err error) {
	b.mu.Lock()
	var since time.Time
	for i := len(b.building) - 1; i >= 0; i-- {
		if b.building[i].name == node {
			since = b.building[i].since
			b.building = append(b.building[:i], b.building[i+1:]...)
			break
		}
	}
	if err == nil {
		b.built = append(b.built, node)
	}
	closed := b.closed
	b.mu.Unlock()
	if took := time.Since(since); !closed && !since.IsZero() && took >= bootProgressEvery {
		if err != nil {
			b.logger.Info("boot: graph node construction failed", "node", node, "took", took.Round(time.Millisecond))
		} else {
			b.logger.Info("boot: graph node built", "node", node, "took", took.Round(time.Millisecond))
		}
	}
}

// logProgress names the node under construction — the innermost, whose
// constructor is the one actually running — and reschedules itself while
// any node still is.
func (b *BootProbes) logProgress() {
	b.mu.Lock()
	if b.closed || len(b.building) == 0 {
		b.progress = nil
		b.mu.Unlock()
		return
	}
	inner := b.building[len(b.building)-1]
	outer := make([]string, 0, len(b.building)-1)
	for _, c := range b.building[:len(b.building)-1] {
		outer = append(outer, c.name)
	}
	built := len(b.built)
	b.progress = time.AfterFunc(bootProgressEvery, b.logProgress)
	b.mu.Unlock()
	b.logger.Info("boot: still constructing a graph node — readiness fails until it is built",
		"node", inner.name, "for", time.Since(inner.since).Round(time.Second),
		"reached_from", outer, "built", built)
}

// release frees the address for Run's listener. Close, not Shutdown: probe
// connections are one-shot and waiting on them only widens the gap.
func (b *BootProbes) release() { b.Close() }

func (b *BootProbes) pend(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.pending[name] = struct{}{}
	b.mu.Unlock()
}

func (b *BootProbes) clear(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.pending, name)
	b.mu.Unlock()
}

// checks is the boot /readyz body's check list: the graph nodes built so far
// (completion order), then the ones under construction (outermost first),
// then every dependency being waited on (sorted).
func (b *BootProbes) checks() []readyzCheckStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]readyzCheckStatus, 0, len(b.built)+len(b.building)+len(b.pending))
	for _, n := range b.built {
		out = append(out, readyzCheckStatus{Name: n, OK: true})
	}
	for _, c := range b.building {
		out = append(out, readyzCheckStatus{Name: c.name, Detail: bootDetailBuilding})
	}
	names := make([]string, 0, len(b.pending))
	for n := range b.pending {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, readyzCheckStatus{Name: n})
	}
	return out
}
