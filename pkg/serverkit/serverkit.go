package serverkit

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/serverkit/drainbudget"
)

// Server carries the already-composed inputs serverkit runs. Service
// SELECTION — which handlers/workers/operators this process serves — has
// happened ABOVE serverkit: the caller (the generated cmd-server shim)
// builds the mux, mounts the selected services on it, constructs the
// selected workers/operators, and hands the result here. serverkit owns
// only the uniform lifecycle (listener bind, the HTTP edge, health
// probes, worker supervision, operator gating, graceful shutdown) and
// knows nothing about names.
type Server struct {
	// Handler is the fully-composed HTTP handler: the mux with all
	// services already mounted (their Connect interceptors already
	// applied via connect.HandlerOption at mount time), plus any
	// REST-transcoder swap already resolved by the caller, plus any
	// /metrics handle the caller mounted. serverkit wraps it with its
	// OWN edge (CORS/security/request-id from Config + the factory
	// fields below) and routes /healthz + /readyz to its own probes
	// IN FRONT of that edge, but never re-mounts services. Required.
	//
	// A composition root that uses the Mux ergonomics below need not set
	// Handler explicitly: when Handler is nil at Run time and Mux is
	// non-nil, Run uses Mux as the handler. Setting Handler is still the
	// way to install a REST-transcoder swap or any wrapper around the raw
	// mux — set Handler to the wrapped value after mounting.
	Handler http.Handler

	// Mux is the composition root's service mux — the *http.ServeMux that
	// mountkit.RegisterService (or this Server's Mount* helpers) mount
	// onto. It is OPTIONAL ergonomics: a composition root can `srv.Mux =
	// http.NewServeMux()`, mount services through Server.Mount (which both
	// registers on Mux AND records the proto service name for the
	// completeness check), and leave Handler nil — Run falls back to Mux.
	// When the caller needs to wrap the mux (REST swap, an outer handler)
	// it builds Mux, mounts, then sets Handler to the wrapped value; Mux
	// stays the mount target and the recorded-names set is unaffected.
	Mux *http.ServeMux

	// HandlerOpts is the shared []connect.HandlerOption (the interceptor
	// chain from observe.Chain wrapped via connect.WithInterceptors, plus
	// the ReadMaxBytes/SendMaxBytes payload limits) that every service's
	// Register call receives. Carried on the Server so the Mount helpers
	// can thread it through without the caller repeating it per service.
	// Callers that mount via mountkit.RegisterService directly pass it
	// themselves and may leave this nil.
	HandlerOpts []connect.HandlerOption

	// Chain is the interceptor chain the services were mounted with
	// (NewChain). Set, it does two things: the Mount helpers build each
	// service with its HandlerOptions when HandlerOpts is nil, and Run's
	// Listener checks every recorded service's declared
	// `(forge.v1.service).audience` against the chain's Authenticator and
	// refuses to serve a mismatch. nil — a Server mounted with hand-built
	// HandlerOpts — skips the audience check.
	Chain *Chain

	// Logger is the root logger the caller already built (the same one
	// it passed into bootstrap so mount-time logs and run-time logs
	// agree). When nil, serverkit builds one from Config (newLogger) and
	// SetDefaults it. Optional.
	Logger *slog.Logger

	// Workers / Operators are the already-constructed, already-selected
	// supervised components. Selection (the old names filter) happened
	// ABOVE serverkit: the caller passes exactly the workers/operators
	// this process should run. serverkit no longer filters by name.
	Workers   []Worker
	Operators []Operator

	// OnShutdown is the caller's teardown (closing the serving DB pool,
	// flushing app-owned buffers). Run calls it after in-flight HTTP has
	// drained and workers and operators have stopped, so nothing it closes
	// is still in use, and before the telemetry flush. Optional; nil is a
	// no-op.
	OnShutdown func(context.Context) error

	// RunOperators starts the controller-runtime manager and blocks
	// until ctx is done. serverkit calls it in a goroutine when
	// len(Operators) > 0, and on no other condition: the caller already
	// decided whether this process runs a manager by deciding whether to
	// populate Operators, and serverkit does not second-guess that from
	// the environment. Nil + non-empty Operators is a config error.
	// Optional when Operators is empty.
	RunOperators func(ctx context.Context, logger *slog.Logger, healthProbeAddr string) error

	// mounted is the set of fully-qualified proto service names this
	// Server has recorded as MOUNTED, populated by Mounted (which the
	// Mount helpers call, and which a caller mounting through
	// mountkit.RegisterService directly calls itself). It is the input to
	// RequireMounted's completeness check: every DECLARED proto service
	// (walked from protoregistry) must appear here or boot fails. Unexported
	// — callers record via Mounted and read the result via RequireMounted,
	// never by poking the map.
	mounted map[string]struct{}

	// Edge factories: kept as fields (not pure Config) because the
	// concrete middleware still lives in the project's generated
	// pkg/middleware tree and serverkit must not import the project.
	// serverkit owns the GATING (driven by Config: CORSOrigins,
	// SecurityHeaders, Environment); these only supply the wrapper. All
	// optional — nil skips that edge layer.
	CORSMiddleware            func(origins []string, allowCredentials bool) func(http.Handler) http.Handler
	SecurityHeadersMiddleware func(production bool) func(http.Handler) http.Handler
	RequestIDMiddleware       func() func(http.Handler) http.Handler
	HTTPMiddleware            func(http.Handler) http.Handler

	// FlowChecks are the APP-FLOW health assertions this service exposes at
	// GET /flow-health. They are the readiness/liveness analogue for an
	// END-TO-END invariant only this service can assert (it holds the state
	// internally), e.g. "every Ready managed daemon is attached to the
	// gateway". serverkit runs every check on each /flow-health request and
	// returns 200 when ALL pass, 503 when ANY fails, plus a STATUS-ONLY JSON
	// aggregate (per-check name + ok + a terse count summary). It deliberately
	// exposes NO per-entity detail anonymously, so the endpoint is safe to
	// probe from `forge env smoke` (which just curls it) without leaking anything
	// sensitive — gate per-entity detail behind your own auth'd endpoint.
	//
	// Empty (the default) means no /flow-health is mounted, so existing
	// services are unaffected. See FlowCheck + AddFlowCheck.
	FlowChecks []FlowCheck

	// ReadyChecks are the DEPENDENCY probes GET /readyz runs on every
	// request — "can this replica serve right now", as opposed to
	// FlowChecks' "is my end-to-end invariant holding". Leaving this empty
	// makes /readyz report readiness purely on the listener being bound,
	// which is what let a replica with an unreachable database advertise
	// itself as Ready through an entire rollout. Register the pool with
	// AddReadyCheck(DBReadyCheck("database", db)). See readiness.go.
	ReadyChecks []ReadyCheck
}

// FlowCheck is one named app-flow health assertion mounted at /flow-health.
// Check runs the invariant internally (the service already holds the access)
// and returns a FlowResult: ok = the invariant holds, Summary = a TERSE,
// non-sensitive aggregate ("2 daemons, 0 unattached") safe to expose
// anonymously. A nil error with ok=false is a clean "unhealthy"; a non-nil
// error means the check itself couldn't run (also reported as unhealthy, with
// the error as the summary).
type FlowCheck struct {
	// Name labels the check in the /flow-health JSON (e.g. "daemon-flow").
	Name string
	// Check runs the assertion. It must NOT include per-entity / per-user
	// detail in the returned Summary — that's the public-status contract.
	Check func(ctx context.Context) FlowResult
}

// FlowResult is the outcome of one FlowCheck: whether the invariant holds and
// a terse, non-sensitive aggregate summary.
type FlowResult struct {
	OK      bool
	Summary string
}

// AddFlowCheck registers an app-flow health check surfaced at /flow-health. A
// check with a nil Check func or empty Name is ignored so a composition root
// can pass a conditional builder's result without a guard.
func (s *Server) AddFlowCheck(c FlowCheck) {
	if c.Check == nil || c.Name == "" {
		return
	}
	s.FlowChecks = append(s.FlowChecks, c)
}

// AddWorker appends a constructed worker to Server.Workers. It is pure
// ergonomics for the composition root — `srv.AddWorker(w)` reads cleaner
// than re-slicing srv.Workers — and is equivalent to appending directly.
// A nil worker is ignored so a composition root can pass the result of a
// conditional builder without a guard.
func (s *Server) AddWorker(w Worker) {
	if w == nil {
		return
	}
	s.Workers = append(s.Workers, w)
}

// AddOperator appends a constructed operator to Server.Operators. Like
// AddWorker it is ergonomics over a direct append; a nil operator is
// ignored. Remember that a non-empty Operators requires Server.RunOperators
// to be set (Run rejects the mismatch).
func (s *Server) AddOperator(o Operator) {
	if o == nil {
		return
	}
	s.Operators = append(s.Operators, o)
}

// Mounted records that the proto service named name (its fully-qualified
// Connect path, e.g. "acme.billing.v1.BillingService") has had its handler
// mounted on this Server. It is the recording seam for the boot-time
// completeness check: the Mount helpers call it after a successful
// RegisterService, and a composition root that mounts through
// mountkit.RegisterService DIRECTLY calls it itself with the same name so
// RequireMounted can later verify nothing declared was left unmounted.
//
// name is the DECLARED proto identity — the same string that appears in the
// proto FileDescriptor's services list and that RequireMounted walks the
// registry for. Recording the kebab runtime name or the procedure path
// instead would make the completeness check compare apples to oranges, so
// callers must pass the fully-qualified proto service name. Empty names are
// ignored.
func (s *Server) Mounted(name string) {
	if name == "" {
		return
	}
	if s.mounted == nil {
		s.mounted = make(map[string]struct{})
	}
	s.mounted[name] = struct{}{}
}

// MountedNames returns the sorted set of proto service names recorded via
// Mounted. Exposed for diagnostics and tests; the run path uses
// RequireMounted, not this.
func (s *Server) MountedNames() []string {
	out := make([]string, 0, len(s.mounted))
	for n := range s.mounted {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Worker is the runtime contract for a long-running background task.
// The generated WorkerInstance in pkg/app satisfies it directly.
type Worker interface {
	// Name is the worker's stable identifier — used in log lines and
	// the Server.Workers iteration order.
	Name() string

	// Start runs the worker's main loop. It must return when ctx is
	// done. serverkit calls Start once per worker in its own goroutine
	// and never restarts.
	Start(ctx context.Context) error

	// Stop performs graceful shutdown bounded by the supplied ctx
	// (typically Config.ShutdownTimeout). serverkit calls Stop after
	// the worker's Start ctx has been cancelled and the supervisor
	// WaitGroup has drained.
	Stop(ctx context.Context) error
}

// ContextWorker is an OPTIONAL extension of Worker for context-aware
// run loops. When a worker in Server.Workers also
// implements ContextWorker, the supervisor calls RunContext instead of
// Start, passing a per-worker context derived from the run lifecycle.
// Workers that don't implement it keep the legacy Start path unchanged
// — the supervisor type-asserts at fan-out time, so adopting the
// interface is purely additive.
//
// The shutdown contract:
//
//   - ctx is cancelled the moment graceful shutdown begins (SIGINT/
//     SIGTERM received, or a fatal serve error). RunContext must observe
//     ctx.Done() inside its cycle loop — and thread ctx into per-tick
//     work (DB queries, HTTP calls, adapters) — so an in-flight cycle is
//     interrupted rather than running to completion.
//   - RunContext must return promptly after cancellation. The supervisor
//     waits for every worker goroutine to exit before continuing the
//     shutdown sequence, so a worker that ignores ctx stalls shutdown.
//   - Returning nil or ctx.Err() after cancellation is a clean exit —
//     the supervisor does not log context.Canceled as a worker error.
//     Any other error is dispatched on Config.FailurePolicy: FailProcess
//     (the default) terminates the process with the worker's error;
//     Ignore logs and continues. Workers are never restarted in-process
//     — restart is the platform supervisor's job.
//   - Stop is still called afterwards (every WorkerList entry is a
//     Worker), bounded by Config.ShutdownTimeout. For ContextWorker
//     implementations Stop is typically a final-drain no-op since
//     cancellation already unwound the run loop.
//
// Cron-scheduled and continuous workers both fit: a continuous worker
// selects on ctx.Done() in its cycle loop; a cron worker derives each
// tick's context from ctx so scheduled jobs observe shutdown mid-run.
type ContextWorker interface {
	Worker

	// RunContext runs the worker's main loop and returns when ctx is
	// done. See the interface doc for the full shutdown contract.
	RunContext(ctx context.Context) error
}

// FailurePolicy governs what Run does when a supervised background
// component fails: a worker's Start/RunContext returning a
// non-cancellation error, or Server.RunOperators returning an
// error. The zero value is [FailProcess] — fail loud. A pod that
// restarts with a clear error in its termination log is operable; a pod
// that keeps serving HTTP while its workers are silently dead is a
// data-loss incident with a delay timer.
//
// It governs SUPERVISED components only. The pprof side-listener is not one:
// a pprof bind failure is always logged and always survived, under either
// policy. pprof does no work the service owes anyone, and losing the
// service to a busy debug port is a worse outcome than losing the debug
// port.
type FailurePolicy int

const (
	// FailProcess (the zero value / default) cancels the run context on
	// the first component failure: graceful shutdown executes and Run
	// returns the component's error, so the supervisor (Kubernetes,
	// systemd, …) restarts the process loudly.
	FailProcess FailurePolicy = 0

	// Ignore logs the component error and keeps serving — the pre-2026
	// behaviour. Explicit opt-in for deployments where a worker is
	// genuinely best-effort and a dead one must not take down the API.
	Ignore FailurePolicy = 1
)

// Operator is the minimal contract serverkit needs to count the
// supervised operators. The actual controller wiring happens inside
// Server.RunOperators — this interface only carries Name for log lines.
type Operator interface {
	Name() string
}

// DBPoolTuning groups the sql.DB pool knobs: the four ApplyDBPoolTuning
// sets on a pool, and SpareConns, which OpenDB keeps dialled. Values come
// from the project config so operators can tune them per environment
// without recompiling. A zero/empty field leaves the corresponding setting at
// Go's default.
type DBPoolTuning struct {
	// MaxOpenConns caps total open connections. 0 = unlimited.
	MaxOpenConns int

	// MaxIdleConns caps idle connections. 0 = default (2).
	MaxIdleConns int

	// ConnMaxIdleTime is how long an idle connection survives before
	// being closed. Zero disables the limit.
	ConnMaxIdleTime time.Duration

	// ConnMaxLifetime is the absolute lifetime of any connection.
	// Zero disables the limit.
	ConnMaxLifetime time.Duration

	// SpareConns keeps this many connections dialled ahead of need, so
	// when the pool needs a new connection it takes a spare instead of
	// making the request wait for a dial (see spare_conns.go). Capped at
	// MaxOpenConns, and while that is set, spares are dialled only into the
	// room the pool's open connections leave under it. 0 = off,
	// database/sql's own behavior: dial on demand.
	SpareConns int
}

// Config bundles every knob Run reads from the project's *config.Config.
// The generated cmd/server.go shim projects its typed config onto this
// struct so serverkit has no compile-time dependency on the project's
// config package.
//
// Fields with zero values fall back to sensible defaults documented per
// field; Run never returns "missing required field" for any of these.
type Config struct {
	// Addr is the public listener address (e.g. ":8080"). Required.
	Addr string

	// PprofAddr is the side-listener for net/http/pprof, and forge's
	// scaffold sets it ON by default (127.0.0.1:6060) rather than leaving
	// it empty. Never mount pprof on the public Addr — its endpoints can
	// leak memory and stall the process — which is why this is a separate
	// address rather than a path on the main mux.
	//
	// The default is LOOPBACK, and that is what makes always-on defensible:
	// the listener exists in every environment and is routable from none of
	// them (the scaffolded manifests give it no Service, route, or published
	// port). Reach it with:
	//
	//	go tool pprof http://localhost:6060/debug/pprof/heap
	//	kubectl port-forward <pod> 6060:6060   # then the line above
	//
	// Set a wildcard address (0.0.0.0:6060) where a profile genuinely has to
	// cross a container boundary. Empty disables the pprof server entirely.
	//
	// A failed bind is NEVER fatal: Run logs it and serves on without pprof,
	// regardless of FailurePolicy. A debug surface that can kill the process
	// is worse than no debug surface.
	PprofAddr string

	// TLSCertPath and TLSKeyPath enable TLS when both are non-empty.
	// Validation of "both or neither" must happen in the project's
	// config layer before Run is called.
	TLSCertPath string
	TLSKeyPath  string

	// LogLevel is the slog level applied to the root logger. Zero
	// value is slog.LevelInfo.
	LogLevel slog.Level

	// LogFormat selects the slog handler. "text" emits text;
	// anything else (including "" and "json") emits JSON.
	LogFormat string

	// Environment is the deployment environment string. When equal to
	// EnvDevelopment Run emits a loud warning about permissive defaults,
	// suppresses HSTS, and enforces CORS even with an empty CORSOrigins
	// (see CORSEnabled). Every other value — including "" — is treated as
	// a deployed environment: closed by default.
	Environment string

	// OTLPEndpoint is the base OTLP collector endpoint (e.g.
	// "http://localhost:4318"); the wire protocol (http/protobuf by default,
	// grpc) and headers come from OTEL_EXPORTER_OTLP_PROTOCOL / _HEADERS.
	// Empty defers to OTEL_EXPORTER_OTLP_ENDPOINT read by the SDK. serverkit OWNS OpenTelemetry setup: Run
	// calls observe.Setup internally with this endpoint, installs the
	// global trace/metric providers, mounts the Prometheus /metrics handler
	// on its own edge, and flushes the providers during graceful shutdown.
	// Empty means no OTLP exporter is wired — the always-on Prometheus
	// /metrics path still works. The caller no longer builds a cmd/otel.go
	// shim; it projects this (and ServiceName) from its typed config.
	OTLPEndpoint string

	// ServiceName is the logical service name reported on OTel traces and
	// metrics (semconv service.name). It is APP IDENTITY (the project name),
	// not a per-env knob — the caller passes a generated constant. Empty
	// falls back to observe.Setup's "unknown".
	ServiceName string

	// ServiceVersion is reported as semconv service.version (the binary's
	// build version). The "dev" sentinel / empty are treated as "no
	// version" by observe.Setup.
	ServiceVersion string

	// AutoMigrate signals the caller-owned migration step should run.
	// serverkit no longer runs migration itself — the cmd layer reads
	// this flag (plus DatabaseURL/DBDriver/DBPoolTuning below) and runs
	// the migration before calling Run. The fields remain on Config so
	// the projection from the project's typed config stays in one place.
	AutoMigrate bool

	// DatabaseURL is the DSN the caller passes to sql.Open for its
	// migration connection. Required (caller-side) when AutoMigrate is
	// true.
	DatabaseURL string

	// DBDriver is the sql.Open driver name (e.g. "pgx"). Empty
	// defaults to "pgx".
	DBDriver string

	// DBPoolTuning is applied (via ApplyDBPoolTuning) by the caller to
	// its migration connection before the migration runs.
	DBPoolTuning DBPoolTuning

	// DBConnectTimeout is the TOTAL budget for connecting to the database at
	// startup (AutoMigrate and OpenDB). The connect retries with capped
	// exponential backoff and jitter inside it, the process stays up and
	// reports not-ready meanwhile, and only a database still unreachable at
	// the deadline fails the process. Zero falls back to
	// DefaultDBConnectTimeout (5m). The rendered startup probe window must
	// cover it — see v1alpha1.StartupWindowSeconds.
	DBConnectTimeout time.Duration

	// CORSOrigins is the allow-list handed to the CORSMiddleware factory.
	// Each entry must be a full origin (scheme + host + optional port).
	//
	// It does NOT decide whether CORS runs — CORSEnabled does. In
	// development the factory is invoked with an empty list (the project's
	// factory then selects its origin-reflecting dev policy, which ignores
	// the list); in a deployed environment an empty list means no CORS
	// layer at all.
	CORSOrigins []string

	// CORSAllowCredentials toggles Access-Control-Allow-Credentials.
	// The wildcard-origin + credentials combination must be rejected
	// by the project's config validation before Run is called.
	CORSAllowCredentials bool

	// SecurityHeaders enables the OWASP security-header middleware.
	// HSTS is only emitted when Environment != "development".
	SecurityHeaders bool

	// PreStopDelay is the readiness-flip drain pause. Zero falls back
	// to 5s; negative is no pause. Operators tune this to match their
	// load-balancer probe interval — too short causes brief 502s on
	// rollout.
	//
	// PreStopDelay and ShutdownTimeout follow the rule every Config
	// duration does — zero is the default, negative is none — and "none"
	// for both is a phase that takes no time: no pause, and a shutdown
	// window that is over as it opens (in-flight requests and streams are
	// cut, teardown runs on an expired context). It is never "unbounded":
	// forge derives the pod's grace period from the two, and the kubelet
	// would end an unbounded shutdown at it anyway.
	PreStopDelay time.Duration

	// ShutdownTimeout bounds the post-readiness-flip shutdown window
	// (worker Stop, Server.OnShutdown, http.Server.Shutdown all
	// share this budget). Zero falls back to 30s; negative is no window
	// (see PreStopDelay).
	ShutdownTimeout time.Duration

	// DrainReserve is the part of ShutdownTimeout kept back after the
	// long-lived streams are ended (Streams.DrainReserve): a stream that
	// claimed the soft phase (serverkit.Draining) may run until the deadline
	// minus this, and the rest of the teardown — workers, OnShutdown, the
	// telemetry flush — runs in what remains. Zero falls back to
	// DefaultDrainReserve (5s); never more than half the budget.
	DrainReserve time.Duration

	// RedialOnDrain names the Connect procedures ("/pkg.v1.Svc/Method", the
	// generated <Svc><Method>Procedure constant) or whole services
	// ("pkg.v1.Svc", the generated <Svc>Name constant) whose streams the
	// drain ends with Unavailable instead of OK — for a peer that redials on
	// Unavailable, such as a daemon holding a bidi stream. Empty: every
	// drained stream ends OK, with the x-forge-drained trailer. See
	// StreamDrainInterceptor.
	RedialOnDrain []string

	// ReadMaxBytes caps the size of a single Connect request payload.
	// Zero falls back to 4 MiB — but ONLY after Normalize has run. A
	// zero reaching connect.WithReadMaxBytes means UNLIMITED, so any
	// caller that reads this field before Run must Normalize first.
	ReadMaxBytes int

	// SendMaxBytes caps the size of a single Connect response payload.
	// Zero falls back to 4 MiB after Normalize. See ReadMaxBytes for
	// why an un-normalized read is dangerous.
	SendMaxBytes int

	// ReadinessTimeout bounds ALL of a /readyz request's dependency
	// checks together (not each one), so the endpoint's worst case stays
	// inside a kubelet probe's timeoutSeconds no matter how many checks
	// are registered. Zero falls back to 2s.
	ReadinessTimeout time.Duration

	// Probes are the paths the process answers its liveness and readiness
	// probes on — the boot probes, serverkit.Run's listener and a Probes
	// surface alike. Empty is DefaultLivenessPath / DefaultReadinessPath.
	// The scaffold projects them from the liveness_paths / readiness_paths
	// config fields, which the KCL lowering reads too, so the probe a pod
	// is given is the one its process serves.
	Probes ProbePaths

	// ReadHeaderTimeout bounds reading a request's headers (slowloris).
	// Zero falls back to 10s; negative disables it.
	ReadHeaderTimeout time.Duration

	// ReadTimeout bounds reading a whole request, body included — over
	// HTTP/2 it is a deadline on each stream's request body. So it caps how
	// long a client-streaming or bidirectional Connect/gRPC call may keep
	// sending: a server with long-lived client streams sets it negative.
	// Zero falls back to 30s; negative disables it.
	ReadTimeout time.Duration

	// IdleTimeout is how long a keep-alive connection may sit idle (HTTP/1.1
	// and HTTP/2). Zero falls back to 120s; negative disables it, which
	// leaves idle connections open until the client closes them.
	IdleTimeout time.Duration

	// HTTP2MaxConcurrentStreams caps the concurrent streams one HTTP/2
	// connection (TLS or h2c) may open. A client multiplexing many
	// long-lived streams over one connection needs it raised. Zero leaves
	// net/http's default (250 at the time of writing).
	HTTP2MaxConcurrentStreams int

	// WriteStallTimeout bounds how long ONE write to a response may stay
	// blocked on a client that is not reading, after which the response is
	// abandoned (see WriteStallGuard). It is not a cap on how long a response
	// may take: a stream that keeps moving bytes runs as long as it likes.
	// Zero falls back to DefaultWriteStallTimeout; negative disables the
	// guard. A handler that wants a whole-response cap sets one itself with
	// http.NewResponseController(w).SetWriteDeadline — it composes with this.
	WriteStallTimeout time.Duration

	// OperatorHealthProbeAddr is forwarded to Server.RunOperators
	// for projects (like cp-forge) that bind a /healthz + /readyz
	// listener inside the controller manager. Empty string leaves the
	// project's RunOperators to fall back to its own default.
	OperatorHealthProbeAddr string

	// FailurePolicy governs worker Start/RunContext errors and
	// RunOperators errors. Zero value is FailProcess (a component
	// failure terminates the process loudly); set Ignore to restore
	// log-and-continue. See the FailurePolicy doc.
	FailurePolicy FailurePolicy
}

// Normalize projects unset Config fields onto their fallback values. It
// is idempotent, so calling it more than once is free.
//
// Run calls it at entry, but Run is NOT the only reader: the generated
// composition root builds the Connect handler options (payload caps) and
// opens the migration DB connection from the SAME Config *before* Run is
// entered. A field left zero at that point is not "the default" — it is
// the zero value, and for the payload caps connect-go documents zero as
// UNLIMITED, which turns one anonymous request into an OOMKill.
//
// So: whoever reads a Config field outside Run normalizes first. The
// generated projectServerkitConfig does exactly that, as the last step
// before it hands the Config back, which is why no reader downstream of
// it can observe a zero.
func (c *Config) Normalize() {
	if c.PreStopDelay == 0 {
		c.PreStopDelay = defaultPreStopDelay
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
	if c.DrainReserve == 0 {
		c.DrainReserve = DefaultDrainReserve
	}
	if c.ReadinessTimeout == 0 {
		c.ReadinessTimeout = defaultReadinessTimeout
	}
	if c.WriteStallTimeout == 0 {
		c.WriteStallTimeout = DefaultWriteStallTimeout
	}
	if c.ReadHeaderTimeout == 0 {
		c.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = defaultReadTimeout
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.ReadMaxBytes == 0 {
		c.ReadMaxBytes = 4 << 20
	}
	if c.SendMaxBytes == 0 {
		c.SendMaxBytes = 4 << 20
	}
	if c.DBDriver == "" {
		c.DBDriver = "pgx"
	}
	if c.DBConnectTimeout == 0 {
		c.DBConnectTimeout = DefaultDBConnectTimeout
	}
	c.Probes = c.Probes.WithDefaults()
}

// The drain and probe defaults Normalize applies, shared with Drainer so a
// hand-built main and Run agree on the grace arithmetic. The drain pair is
// drainbudget's, the same values pkg/deploy derives the pod's grace period
// from.
const (
	defaultPreStopDelay     = drainbudget.DefaultPreStopDelay
	defaultShutdownTimeout  = drainbudget.DefaultShutdownTimeout
	defaultReadinessTimeout = 2 * time.Second
)

// EnvDevelopment is the one Environment value that unlocks permissive edge
// defaults. It is compared exactly: "dev", "Development" and "" are all
// deployed environments, because a posture this permissive must never be
// reachable by a near-miss or by forgetting to set the variable at all.
const EnvDevelopment = "development"

// IsDevelopment reports whether this Config runs the development posture.
func (c Config) IsDevelopment() bool { return c.Environment == EnvDevelopment }

// CORSEnabled reports whether Run wraps the caller's handler in the CORS
// layer — and therefore whether Server.CORSMiddleware is required.
//
// Two ways to be on, and the second one is the whole point:
//
//   - A non-empty CORSOrigins. An operator naming origins wants them
//     enforced, in every environment.
//   - Development. A freshly scaffolded project has no CORS_ORIGINS value
//     to name — nothing defaults it, and its own frontend's dev port is not
//     knowable to the server — so gating solely on a non-empty list made the
//     generated factory's development branch unreachable: the browser
//     frontend could not call its own backend until someone hand-curated an
//     allow-list. Development enables the layer with an empty list and lets
//     the caller's factory choose the dev policy.
//
// The dev arm is deliberately the ONLY relaxation: a staging or production
// Config with no origins gets no CORS layer, exactly as before.
func (c Config) CORSEnabled() bool { return len(c.CORSOrigins) > 0 || c.IsDevelopment() }
