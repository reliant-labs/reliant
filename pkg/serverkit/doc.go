// Package serverkit runs an already-composed forge server: HTTP/Connect
// listener (HTTP/1.1, HTTP/2, h2c), healthz/readyz probes, the HTTP edge
// (CORS / security-headers / request-id), worker supervisor, operator manager, and graceful
// shutdown sequence.
//
// # Why a library
//
// Earlier forge versions emitted a ~520-line cmd/server.go per project,
// 95% of which was uniform across every project (signal handling,
// listener bind, readiness flip, worker fan-out, shutdown ordering).
// serverkit absorbs those uniform sections so they get one test suite and
// can evolve without touching every downstream project's checkout.
//
// serverkit takes COMPOSED INPUTS and owns only the uniform lifecycle. It
// knows nothing about service SELECTION: which services are mounted, which
// workers/operators run, and how the interceptor chain was built all happen
// ABOVE serverkit, in the project's own serve.go. serverkit receives the
// finished handler (or the mux), the already-selected Workers/Operators,
// and an OnShutdown closure.
//
// Alongside Run, serverkit offers the composition STEPS that every serve.go
// performs identically — Boot, AutoMigrate, RecordMounted, AddWorkers,
// RequireComplete, RESTHandler (see compose.go and rest.go). They are what
// let serve.go be short enough to read as a list of decisions, and what let
// improvements to them reach existing projects through a dependency bump
// rather than a re-scaffold of a file the user owns.
//
// # Usage in the composition root
//
// cmd/<bin>/cmd/serve.go is the project's SCAFFOLD-ONCE composition root:
// forge writes it once and the application owns it from then on. It holds
// the DECISIONS — the fail-closed auth posture, the interceptor order, the
// payload caps, the CORS policy, the readiness set, the teardown — while
// the steps that look the same in every project call into here:
//
//	skCfg, _ := serverkitConfig(cfg)      // project-typed → vendor-neutral, Normalize()d
//	logger, mux := serverkit.Boot(skCfg)  // logger + slog.SetDefault + mux
//	_ = serverkit.AutoMigrate(skCfg, logger, pkgapp.AutoMigrate)
//
//	// … the owner's decisions: auth posture, chain slots, payload caps …
//	chain, _ := serverkit.NewChain(serverkit.ChainDeps{
//	    Auth: serverkit.AuthFor(serverkit.AudienceUser, authInterceptor), …})
//	opts := chain.HandlerOptions()
//
//	srv := serverkit.Server{Mux: mux, Chain: chain, Logger: logger, …}
//	srv.AddReadyCheck(serverkit.DBReadyCheck("database", infra.DB))
//	srv.OnShutdown = func(context.Context) error { return infra.DB.Close() }
//
//	mounted := spec.Mount(components, srv.Mux, cfg, logger, opts...)
//	srv.RecordMounted(mounted)
//	if rest := serverkit.RESTHandler(srv.Mux, mounted, logger); rest != nil {
//	    srv.Handler = rest
//	}
//	srv.AddWorkers(spec.Workers(components))
//	if spec.RequireComplete {
//	    if err := srv.RequireComplete(app.Inventory); err != nil { return err }
//	}
//	return serverkit.Run(ctx, skCfg, srv)
//
// Those helpers (compose.go) are deliberately SEPARATE steps rather than
// one Compose(...) call: the composition root has to be able to reorder and
// replace them — migrate after mounting, skip the readiness pool, wrap the
// mux — and a step usable in only one position is a template with extra
// syntax, not a library.
//
// # The Server value
//
// Server carries everything serverkit runs: the composed Handler, the
// selected Workers/Operators, the RunOperators manager entry point, an
// OnShutdown teardown closure, and the project's edge-middleware factories
// (their concrete implementations live in the project's pkg/middleware, so
// they stay fields rather than collapsing to Config). Config still owns the
// GATING for the edge layers (CORSOrigins, SecurityHeaders, Environment).
//
// # Components: Chain and Listener
//
// The interceptor chain and the HTTP listener are components — structs built
// from their Deps — so a process can hold more than one of each:
//
//   - NewChain(ChainDeps{Auth, …}) is the ordered interceptor stack and the
//     payload caps. Every slot is defaulted except Auth, which is required;
//     an Omit constant (NoAudit, NoTrace, …) drops a default. Auth is an
//     Authenticator: an interceptor plus the audience it admits. The
//     Deadline slot (a Deadline: per-RPC budgets and a slow-call watchdog)
//     runs right before Auth, so the RPC's deadline bounds authentication.
//   - NewListener(ListenerDeps{Config, Chain, Routes, …}) is one address with
//     its own TLS, timeouts, HTTP/2 stream cap and Streams registry. It
//     mounts its Routes with its Chain and refuses to start when a mounted
//     proto service declares an audience (`(forge.v1.service).audience`,
//     default "user") other than the chain's. Its lifecycle is Run, Drain,
//     Close and Ready.
//
// Run is one Listener plus the process policy around it: readiness, the
// pre-stop pause, workers, operators, pprof, teardown and telemetry.
//
// # Lifecycle
//
// Run owns the uniform lifecycle:
//
//  1. Logger init (Server.Logger, or built from Config when nil).
//  2. A tiny top mux routes /healthz + /readyz to serverkit's own probes
//     (IN FRONT of the edge wrap, so probes are never behind CORS/auth)
//     and everything else to Server.Handler.
//  3. CORS, security-headers and request-id are layered over that top mux
//     from Config gating + the Server factory fields. The listener speaks
//     HTTP/1.1, HTTP/2 over TLS and h2c (net/http's own, so its streams
//     drain on shutdown).
//  4. Listener binds, readiness flips true.
//  5. Workers start; the operator manager starts when len(Operators) > 0,
//     which is the caller's decision and the only one.
//  6. SIGINT/SIGTERM → readiness flips to "draining" → pre-stop sleep →
//     the listener drains (Listener.Drain: stop accepting, unary requests
//     finish, long-lived streams end soft-then-hard) → workers stop →
//     operators stop → Server.OnShutdown → pprof shuts down → telemetry
//     flushes.
//
// # Long-lived streams
//
// A stream that never finishes on its own — a server-streaming RPC, an SSE
// response, a websocket — would hold http.Server.Shutdown for the whole
// ShutdownTimeout and then be cut without a status. The listener owns a
// Streams registry (streams.go) that ends them inside the budget instead,
// with cause svcerr.ErrServerDraining: at once when the drain begins, or —
// for a stream that asked for serverkit.Draining(ctx) to say goodbye —
// when it returns or at the deadline minus Config.DrainReserve. Mark an SSE
// or websocket route with serverkit.LongLived; a websocket handler closes
// with 1012 from serverkit.AfterDrain, and one that ignores the drain has its
// hijacked connection closed for it, so its cleanup still runs.
//
// # Without Run
//
// Every piece of the sequence is exported for a main that runs its own
// listener — or several: Listener (the whole listener component), Drainer
// (readiness gate, pre-stop, shutdown budget, /readyz), and, below a
// Listener, NewHTTPServer (an http.Server with its Streams installed),
// Streams, LongLived and DrainHTTP (a listener's drain). A Listener has the
// lifecycle lifecyclekit.Supervise runs, so a main with several listeners
// hands them to it with the Drainer as its policy.
//
// # Worker shutdown contract
//
// Each worker runs in its own goroutine with a per-worker context
// derived from the run lifecycle. On SIGINT/SIGTERM that context is
// cancelled immediately — before the pre-stop drain sleep — so
// long-running cycles get the full shutdown window to unwind; on a
// fatal serve error it is cancelled when the worker-stop phase begins.
// Workers implementing the optional ContextWorker interface run via
// RunContext(ctx), the preferred ctx-aware lifecycle; all other workers
// run via the legacy Start(ctx) with the same per-worker context. The
// supervisor waits for every worker goroutine to return, then calls each
// worker's Stop bounded by Config.ShutdownTimeout. See ContextWorker for
// the full contract.
//
// # What does not belong here
//
// The line is OWNERSHIP, not size. Anything an application author has a
// legitimate reason to change stays in their serve.go: the auth posture,
// what fills each chain slot, the payload caps, the CORS and
// security-header policy, which readiness checks are registered, what
// teardown closes, and which services/workers/operators this process runs.
// Absorbing any of those into serverkit would take a decision away from the
// person who has to answer for it.
//
// Service SELECTION stays above serverkit too: mounting goes through the
// project's own typed Mount<Svc> methods, so selection is compile-time and
// serverkit never sees a service name it has to resolve. Per-component DI
// wiring stays in the generated internal/app. serverkit holds only what is
// identical in every forge project.
package serverkit
