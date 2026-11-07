// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// THE SERVE PIPELINE — and, deliberately, nothing else.
//
// Every line below is a DECISION about how this server behaves: whether it
// authenticates, in what order requests pass through the interceptor chain,
// how large a payload it will accept, which origins it answers, what it
// tells a rolling deploy about its own readiness, and what it closes on the
// way down. Those are yours. Change them here.
//
// The steps that are the same in every forge project — building the logger
// and the mux, the migrate ceremony, recording mounts, checking them against
// the inventory for the completeness gate — are in
// github.com/reliant-labs/forge/pkg/serverkit, so this file stays short
// enough to read and improvements to them reach you through a dependency
// bump instead of a re-scaffold.
//
// Serve / ServeSpec are EXPORTED so the command-group subpackages
// (services/, workers/, operators/) consume them. A command composes ITSELF
// by naming its typed Mount + Worker<X>()/Operator<X>() accessors in the
// ServeSpec — there is no string name filter and no re-selection anywhere on
// the run path.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql driver for DATABASE_URL

	fmw "github.com/reliant-labs/forge/pkg/middleware"
	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/validate"

	forgedb "example.com/app/db"
	"example.com/app/internal/app"
	"example.com/app/pkg/config"
	"example.com/app/pkg/middleware"
)

// MountFunc is a TYPED mount: it takes the constructed *app.Components and
// registers a selected set of services on mux, returning their Connect
// paths. Serve receives one of these directly — `(*app.Components).MountAll`
// for the all-services command, `(*app.Components).Mount<Svc>` (a method
// value) for a per-service subcommand. There is NO string selection on this
// path.
type MountFunc = app.MountFunc

// MountNone is a typed mount that registers no services. Worker-only and
// operator-only subcommands pass it to Serve: the component graph still
// constructs everything (cross-component reads stay nil-safe), but no
// Connect routes are mounted — the process is a pure supervisor for its
// named component.
func MountNone(c *app.Components, mux *http.ServeMux, cfg *config.Config, logger *slog.Logger, opts ...connect.HandlerOption) []string {
	_, _, _, _, _ = c, mux, cfg, logger, opts
	return nil
}

// ServeSpec is how a command composes ITSELF onto the shared Serve pipeline:
// it names, by TYPED accessor, exactly the mount + workers + operators this
// process runs.
type ServeSpec struct {
	// Mount registers this process's Connect services onto the mux — a
	// typed (*app.Components).Mount<Svc> / MountAll method value, or
	// MountNone for a worker/operator-only supervisor.
	Mount MountFunc
	// Workers, when non-nil, returns the workers this process supervises.
	// nil => no workers.
	Workers func(*app.Components) []serverkit.Worker
	// Operators, when non-nil, returns the operators this process
	// supervises. Serve derives BOTH the AddOperator markers AND the
	// RunOperators controller rows from the returned slice, so the
	// supervised set and the run set can never diverge.
	Operators func(*app.Components) []app.OperatorEntry
	// RequireComplete runs the boot-time completeness gate (every service in
	// app.Inventory — each forge-managed proto service — must be mounted) —
	// the all-services `server` path sets it; a subset mount leaves it false.
	RequireComplete bool
}

// Serve is the canonical server pipeline shared by every server-shaped
// subcommand.
func Serve(ctx context.Context, deps Deps, spec ServeSpec) error {
	cfg, err := config.Load(deps.Cmd)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if verr := config.Validate(cfg); verr != nil {
		return fmt.Errorf("invalid configuration: %w", verr)
	}

	skCfg, err := serverkitConfig(cfg)
	if err != nil {
		return err
	}

	// AUTH POSTURE. AnonymousOK: false is FAIL-CLOSED — a request must
	// present a valid token unless the RPC it names declared
	// `auth_required: false` in its proto, which forge projects into
	// middleware.UnauthenticatedProcedures. The proto is where an endpoint
	// is published as public; nothing here and no environment variable
	// decides it. Flipping this one field to true is the only way this
	// server stops authenticating, and it is a source change.
	authDeps := middleware.AuthDeps{AnonymousOK: false}

	// app.SetupAuth (internal/app/auth.go, yours) builds the authenticator —
	// JWT bearer tokens by default, configured from the typed cfg. Edit
	// SetupAuth to swap validators or add providers: picking an
	// authenticator is code, and only the per-environment values it reads
	// are config. API keys join it below, once the database is open. A nil
	// authenticator leaves Validate unset, which NewAuthInterceptor refuses.
	authenticator, aerr := app.SetupAuth(cfg)
	if aerr != nil {
		return fmt.Errorf("auth setup: %w", aerr)
	}
	if authenticator != nil {
		authDeps.Validate = authenticator.Authenticate
	}

	// Built UP FRONT so a server with no validator REFUSES TO START:
	// NewAuthInterceptor errors when Validate is nil and AuthDeps.ExternalAuth
	// — the code-level opt-out — is not set. An unauthenticated server is
	// therefore a boot failure, never a running process.
	authInterceptor, err := middleware.NewAuthInterceptor(authDeps)
	if err != nil {
		return fmt.Errorf("auth configuration: %w", err)
	}

	// Logger built ONCE and threaded through mount time and run time so the
	// two agree; serverkit also owns OTel setup from skCfg.OTLPEndpoint +
	// skCfg.ServiceName and mounts /metrics on its own edge.
	logger, mux := serverkit.Boot(skCfg)

	// BOOT PROBES. Bind the listener NOW, before any dependency is touched.
	// The database connect below retries for up to DB_CONNECT_TIMEOUT instead
	// of failing the process on the first refused dial (a pooler or primary
	// that comes up seconds after its clients is routine in a rollout), and a
	// process that is waiting must not look crashed: until serverkit.Run takes
	// the address over, /healthz answers 200 and /readyz answers 503 naming
	// the dependency being waited on. Everything that takes ctx from here on
	// reports into it.
	ctx, bootProbes, err := serverkit.StartBootProbes(ctx, skCfg, logger)
	if err != nil {
		return err
	}
	defer bootProbes.Close()

	// Migrate BEFORE constructing components and mounting: a service that
	// starts serving against a schema its migrations have not reached
	// answers requests with errors that look like bugs.
	//
	// forgedb.Source() is the embedded SQL, re-derived in db/source_gen.go
	// (nil until you write your first migration, which migratekit tolerates at
	// boot); the migration LOGIC is migratekit's, so improvements to it arrive
	// through a forge/pkg bump rather than a re-scaffold of this file, which
	// is yours.
	if merr := serverkit.AutoMigrateContext(ctx, skCfg, logger,
		migratekit.AutoMigrate(forgedb.Source(), forgedb.SourceDir)); merr != nil {
		return merr
	}

	// INTERCEPTOR CHAIN — the order requests pass through, outermost first.
	// observe.Chain fixes it: trace → recovery → request-id → logging →
	// tracing → metrics, and then the application interceptors named below:
	// auth → audit → rate-limit → extras (protovalidate last, right before
	// the handler). The trace span is outermost, so a request auth rejects
	// still has a span, and every log line written for it — the "rpc failed"
	// line, the audit event — carries its trace_id. Recovery is next, so a
	// panic anywhere inside still produces a response; auth precedes audit so
	// the audit log knows who called.
	chainDeps := observe.Deps{
		Logger:    logger,
		Auth:      authInterceptor,
		Audit:     fmw.AuditInterceptor(logger, middleware.ClaimsFromContext),
		RateLimit: fmw.RateLimitInterceptor(fmw.RateLimitOptions{Rps: int(cfg.RateLimitRps), Burst: int(cfg.RateLimitBurst)}, middleware.ClaimsFromContext),
		// Success-log sampling, from the typed config (0s, the default, logs
		// every successful RPC). observe reads no environment: this line is
		// how log_success_sample_window reaches the logging interceptor.
		LogOptions: []observe.LogOption{observe.WithSuccessSampling(cfg.LogSuccessSampleWindow.AsDuration())},
	}
	// DRAIN. A server-streaming or bidi RPC never finishes on its own, so at
	// shutdown it would hold the drain for the whole SHUTDOWN_TIMEOUT and then
	// be cut without a status. This binds every one to the listener's drain:
	// it ends OK, with the x-forge-drained trailer, after the pre-stop pause —
	// the client reopens it against a live replica. A procedure whose peer
	// should redial on Unavailable instead goes in skCfg.RedialOnDrain. Inner
	// to logging and metrics, so they record the status the client received.
	chainDeps.Extras = append(chainDeps.Extras, serverkit.StreamDrainInterceptor())
	// protovalidate rejects requests whose buf.validate field rules fail
	// with CodeInvalidArgument BEFORE the handler runs. Appended LAST, so it
	// is the innermost extra: it runs right before the handler, after auth.
	if validateInt, verr := validate.Interceptor(); verr == nil {
		chainDeps.Extras = append(chainDeps.Extras, validateInt)
	} else {
		logger.Error("failed to create protovalidate interceptor", "error", verr)
	}
	// otelconnect goes in Deps.Trace, NOT Extras: Trace is the OUTERMOST slot
	// of the chain, so the span covers auth rejections (exactly the requests
	// you want to see) and every layer's log lines carry its trace_id. Extras
	// run AFTER auth — an interceptor there never sees a rejected call.
	//
	// WithTrustRemote makes the browser's traceparent the PARENT of this span.
	// Without it otelconnect treats every inbound trace context as untrusted
	// and only LINKS to it, so a page's spans and the API spans it caused land
	// in two traces and "show me this click end to end" has no answer. The
	// browser is first-party here. Drop the option if this server is reachable
	// by callers you do not want choosing your trace ids.
	if otelInt, oerr := otelconnect.NewInterceptor(otelconnect.WithTrustRemote()); oerr == nil {
		chainDeps.Trace = otelInt
	} else {
		logger.Error("failed to create otelconnect interceptor", "error", oerr)
	}

	// PAYLOAD CAPS. connect-go documents a ZERO cap as UNLIMITED, and it
	// reads and decodes the whole body BEFORE any interceptor — auth, rate
	// limit, protovalidate — can reject it. So an uncapped handler turns one
	// anonymous request into an OOMKill, and rate limiting cannot help
	// because it counts requests, not bytes. serverkitConfig normalizes
	// skCfg before returning it, so these are guaranteed non-zero here.
	opts := []connect.HandlerOption{
		connect.WithInterceptors(observe.Chain(chainDeps)...),
		connect.WithReadMaxBytes(skCfg.ReadMaxBytes),
		connect.WithSendMaxBytes(skCfg.SendMaxBytes),
	}

	// EXPLICIT COMPOSITION: open owned infra (internal/app/providers.go,
	// yours), then construct the component graph — no by-type injector.
	infra, err := app.OpenInfra(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("open infra: %w", err)
	}

	// API KEYS. Installed HERE, not in SetupAuth, because checking a key
	// reads the database OpenInfra just opened: app.APIKeyValidator
	// (internal/app/auth.go, yours) builds it over infra, so a key check
	// shares the handlers' pool instead of opening its own. The interceptor
	// built above already calls authenticator.Authenticate, and no RPC is
	// mounted until below, so every request sees what is installed. nil —
	// the scaffold's default — leaves API keys disabled (rejected), and
	// bearer tokens unaffected.
	if authenticator != nil {
		authenticator.UseKeyValidator(app.APIKeyValidator(infra))
	}

	components, err := app.NewComponents(infra)
	if err != nil {
		return fmt.Errorf("build components: %w", err)
	}

	srv := serverkit.Server{
		Mux:         mux,
		HandlerOpts: opts,
		Logger:      logger,
		CORSMiddleware: func(origins []string, allowCredentials bool) func(http.Handler) http.Handler {
			// The project's own request/response headers, on top of
			// forge's defaults. The preflight is answered by the CORS
			// layer itself, so a header a browser client sends has to be
			// declared HERE (cors_allow_headers), or every RPC is refused.
			corsOpts := []fmw.CORSOption{
				fmw.WithAllowHeaders(fmw.HeaderList(cfg.CorsAllowHeaders)...),
				fmw.WithExposeHeaders(fmw.HeaderList(cfg.CorsExposeHeaders)...),
			}
			// DEV ONLY: reflect ANY request Origin, so this product's own
			// browser frontend works cross-origin without hand-curating
			// CORS_ORIGINS first (nothing defaults it, and the frontend's
			// dev port is not knowable to the server). Credential-safe —
			// DevCORSMiddleware echoes the concrete request Origin, never a
			// literal "*". NEVER outside dev: a deployed environment with
			// no origins gets no CORS layer at all.
			if cfg.Environment == serverkit.EnvDevelopment {
				return fmw.DevCORSMiddleware(allowCredentials, corsOpts...)
			}
			return fmw.CORSMiddleware(origins, allowCredentials, corsOpts...)
		},
		SecurityHeadersMiddleware: func(production bool) func(http.Handler) http.Handler {
			secCfg := fmw.DefaultSecurityHeadersConfig()
			secCfg.Enabled = true
			secCfg.Production = production
			return fmw.SecurityHeadersMiddleware(secCfg)
		},
		RequestIDMiddleware: fmw.RequestIDMiddleware,
	}

	// READINESS. /readyz is what a rolling deploy believes: it marks this
	// replica Ready, shifts traffic onto it, and moves to the next one. An
	// unconditional /readyz therefore reports Ready with an unreachable
	// database and rolls a broken deployment to 100% behind green probes.
	// Register the pool the HANDLERS serve from — a probe against a second
	// pool stays green while the serving pool is exhausted. Add your own
	// dependency probes with more AddReadyCheck calls.
	srv.AddReadyCheck(serverkit.DBReadyCheck("database", infra.DB))

	// TEARDOWN. The serving pool is the process's longest-lived resource and
	// nothing else closes it. serverkit runs OnShutdown after in-flight HTTP
	// has drained and workers have stopped, so closing here cannot cut off a
	// request that is still running. Add your own teardown to this closure.
	srv.OnShutdown = func(context.Context) error {
		if infra.DB != nil {
			return infra.DB.Close()
		}
		return nil
	}

	// TYPED MOUNT. spec.Mount is a method value over *app.Components — no
	// string selection, no inventory lookup. Each Mount<Svc> returns its
	// Connect path(s), recorded for the completeness gate.
	mounted := spec.Mount(components, srv.Mux, cfg, logger, opts...)
	srv.RecordMounted(mounted)

	// NATIVE SIGN-IN. Mounts /auth/login, /auth/register, /auth/session and
	// /auth/logout — the app's own auth API, which runs the whole OIDC flow
	// server-side so the browser never contacts the identity provider.
	//
	// Gated on configuration rather than a build flag: an app that has no
	// IdP wired (or that authenticates at the edge) mounts nothing and
	// carries no privileged credential. See internal/app/login_broker.go,
	// which is yours to edit or delete.
	if err := app.LoginBrokerConfigured(cfg); err == nil {
		app.RegisterLoginBroker(srv.Mux, cfg, logger)
		logger.Info("native sign-in enabled", "routes", "/auth/login /auth/register /auth/session /auth/logout")
	} else {
		logger.Debug("native sign-in not configured", "reason", err)
	}

	// SELF-COMPOSED SUPERVISION. Each command names, by typed accessor,
	// exactly the workers/operators it runs. For operators we build ONE
	// slice and derive BOTH the AddOperator markers AND the RunOperators
	// controller rows from it, so the two can never diverge.
	if spec.Workers != nil {
		srv.AddWorkers(spec.Workers(components))
	}
	if spec.Operators != nil {
		ops := spec.Operators(components)
		for _, op := range ops {
			srv.AddOperator(op.Marker())
		}
		srv.RunOperators = func(ctx context.Context, logger *slog.Logger, healthProbeAddr string) error {
			return app.RunOperators(ctx, logger, healthProbeAddr, ops)
		}
	}

	// COMPLETENESS GATE: on the all-services path every service the project
	// declares must be mounted before we serve — fail-closed against a
	// half-wired server. A per-service subcommand intentionally mounts a
	// subset, so the gate runs only when the command asks for it.
	if spec.RequireComplete {
		if rmErr := srv.RequireComplete(app.Inventory); rmErr != nil {
			return rmErr
		}
	}

	return serverkit.Run(ctx, skCfg, srv)
}

// serverkitConfig projects this project's typed *config.Config onto the
// vendor-neutral serverkit.Config, so serverkit needs no compile-time
// dependency on pkg/config.
func serverkitConfig(cfg *config.Config) (serverkit.Config, error) {
	skCfg := serverkit.Config{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		// ServiceName is APP IDENTITY (the project name baked into a
		// generated constant), NOT a config field. serverkit reports it on
		// OTel traces/metrics (semconv service.name).
		ServiceName:  ServiceName,
		OTLPEndpoint: cfg.OtlpEndpoint,
		// pprof, ON BY DEFAULT, on its own listener — never the app port.
		// Defaults to 127.0.0.1:6060 (proto/config/v1/config.proto), so it is
		// live in every environment and routable from none: no k8s Service,
		// no route, no published container port. Grab a profile with
		//
		//	go tool pprof http://localhost:6060/debug/pprof/heap
		//	kubectl port-forward <pod> 6060:6060   # then the line above
		//
		// Override with PPROF_ADDR / --pprof-addr; --pprof-addr= turns it off
		// (an empty PPROF_ADDR is unset, so it keeps the default). A failed
		// bind is logged and skipped, never fatal.
		PprofAddr:            cfg.PprofAddr,
		TLSCertPath:          cfg.TlsCertPath,
		TLSKeyPath:           cfg.TlsKeyPath,
		LogFormat:            cfg.LogFormat,
		Environment:          cfg.Environment,
		AutoMigrate:          cfg.AutoMigrate,
		DatabaseURL:          cfg.DatabaseUrl,
		ReadMaxBytes:         int(cfg.ReadMaxBytes),
		SendMaxBytes:         int(cfg.SendMaxBytes),
		SecurityHeaders:      cfg.SecurityHeadersEnabled,
		CORSAllowCredentials: cfg.CorsAllowCredentials,
	}

	if uerr := skCfg.LogLevel.UnmarshalText([]byte(cfg.LogLevel)); uerr != nil {
		return serverkit.Config{}, fmt.Errorf("invalid log level %q: %w", cfg.LogLevel, uerr)
	}

	if cfg.CorsOrigins != "" {
		for _, o := range strings.Split(cfg.CorsOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				skCfg.CORSOrigins = append(skCfg.CORSOrigins, o)
			}
		}
	}

	skCfg.PreStopDelay = cfg.PreStopDelay.AsDuration()
	skCfg.ShutdownTimeout = cfg.ShutdownTimeout.AsDuration()

	skCfg.DBPoolTuning.MaxOpenConns = int(cfg.DbMaxOpenConns)
	skCfg.DBPoolTuning.MaxIdleConns = int(cfg.DbMaxIdleConns)
	skCfg.DBPoolTuning.ConnMaxIdleTime = cfg.DbConnMaxIdleTime.AsDuration()
	skCfg.DBPoolTuning.ConnMaxLifetime = cfg.DbConnMaxLifetime.AsDuration()
	skCfg.DBConnectTimeout = cfg.DbConnectTimeout.AsDuration()

	// Normalize LAST, so nothing downstream can observe a zero-valued
	// cap/timeout/driver. Serve reads several of these fields — the Connect
	// payload caps, the auto-migrate sql.Open driver — BEFORE serverkit.Run
	// is entered, and Run's own normalization is far too late to help those
	// reads: a zero ReadMaxBytes/SendMaxBytes already reached connect as
	// UNLIMITED, and a zero DBDriver already reached sql.Open as `unknown
	// driver ""`. Normalizing here makes that whole class of ordering bug
	// unreachable.
	skCfg.Normalize()

	return skCfg, nil
}
