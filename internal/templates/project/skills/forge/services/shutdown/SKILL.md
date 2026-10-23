---
name: services/shutdown
description: Graceful shutdown of a forge server — the drain sequence (readiness flip, pre-stop, listener drain, teardown), long-lived streams (SSE, websockets, Connect streams) ended inside the budget instead of holding it, the client contract for a drained Connect stream (OK + x-forge-drained, or Unavailable via RedialOnDrain), websockets closing 1012 through AfterDrain with their cleanup run, the soft "say goodbye" signal, DrainReserve, and using the same pieces from a main that runs its own listener.
---

# Graceful shutdown

`serverkit.Run` owns shutdown. On SIGTERM it runs, inside `SHUTDOWN_TIMEOUT`:

1. **Readiness flips** — `/readyz` answers 503 `{"status":"draining"}`.
2. **Pre-stop pause** (`PRE_STOP_DELAY`) — the process keeps serving while
   load balancers notice. A pod leaves EndpointSlices and cloud LB backends
   asynchronously to SIGTERM; stopping at once drops requests on every rollout.
3. **The listener drains** — it stops accepting, in-flight unary requests
   finish, and long-lived streams end (below).
4. **Workers, then operators stop; `OnShutdown` runs** (close the DB pool here);
   telemetry flushes last.

forge derives `terminationGracePeriodSeconds` from `PRE_STOP_DELAY +
SHUTDOWN_TIMEOUT`, so both are honoured exactly.

Both follow the rule every `serverkit.Config` duration does — **zero is the
default** (5s / 30s), **negative is none** — in config, env and on
`serverkit.Drainer` alike. "None" is a phase that takes no time: `-1s` pre-stop
is no pause; `-1s` shutdown is a window over as it opens (in-flight requests and
streams are cut, teardown runs on an expired context). It is never "unbounded":
the kubelet would end an unbounded shutdown at the grace period anyway.

A main that reads the two variables itself, without the generated config,
uses `drainbudget.FromEnv(os.Getenv)` (`forge/pkg/serverkit/drainbudget`) —
the reader the grace-period renderer uses — and passes the result's
`PreStopDelay` / `ShutdownTimeout` to `serverkit.Config`. Never parse them by
hand: a private copy is how `-1s` came to be read as malformed.

## Long-lived streams

`http.Server.Shutdown` waits for every active request. A stream that never ends
on its own — Server-Sent Events, a websocket, a server-streaming RPC — used to
hold the drain for the whole budget, log `server shutdown error: context
deadline exceeded` at ERROR, and be cut without a status at exit. Every rollout,
every replica.

The listener now owns a drain registry (`serverkit.Streams`) and ends them
inside the budget with cause `svcerr.ErrServerDraining`. Mark a plain-HTTP
streaming route as long-lived:

```go
mux.Handle("/events", serverkit.LongLived(sseHandler))
```

Its request context is then ended **as the drain begins**, after the pre-stop
pause, when this replica is already out of rotation, so the client's reconnect
lands on a live one. `serverkit.Ended(ctx)` tells a drain apart from the client
leaving. Never wrap a unary route: a rollout would cut it short instead of
draining it.

### Connect streaming RPCs

Every `serverkit.NewChain` installs `serverkit.StreamDrainInterceptor()` by
default (drop it only with `serverkit.NoStreamDrain`). It binds every
server-streaming and bidi handler to the drain of the listener serving it
(unary and client-streaming RPCs are drained normally, never cut). **The client contract**
for a stream the drain ends:

| Procedure | Client receives |
|---|---|
| default | every message sent, then **OK** with trailer `x-forge-drained: true` |
| listed in `serverkit.Config.RedialOnDrain` (a procedure `"/pkg.v1.Svc/Method"` or a whole service `"pkg.v1.Svc"`) | every message sent, then **Unavailable** ("server draining…") with the same trailer |

OK is the default because a browser client treats Unavailable as "the network
died" and may abandon every read on that connection; a watch client should
reopen the stream (from its resume cursor) when it ends with the trailer. Use
`RedialOnDrain` for peers that already redial on Unavailable, such as a daemon
on a bidi stream. A handler's own error is never rewritten.

#### A handler parked in `Receive`

A server-side `stream.Receive()` reads the request body, and **no context wakes
it** — not the drain's cancel, not the handler's own. Only the client sending,
the client leaving, or the body being closed does, and Connect closes the body
only after the handler returns. So the listener (`Streams.Install`, inside
every `NewHTTPServer`/`NewListener`) serves each request with a body that can
be ended early, and a stream the drain ends has **both** its context cancelled
and its `Receive` aborted: a bidi handler blocked in `Receive` returns (the
error wraps `svcerr.ErrServerDraining`, so the client still gets the contract
above) instead of pinning the shutdown to the end of the budget.

A handler that reads on a goroutine of its own must stop that goroutine before
it returns — Connect forbids using a stream after its handler has returned, and
a straggling `Receive` races otelconnect's span bookkeeping. Cancelling a
context will not stop it; `serverkit.AbortReceive(ctx)` does:

```go
func (s *Service) Connect(ctx context.Context, stream *connect.BidiStream[In, Out]) error {
    if !serverkit.CanAbortReceive(ctx) { // served outside serverkit.NewHTTPServer
        return connect.NewError(connect.CodeInternal, errors.New("stream handler is misconfigured"))
    }
    readerDone := make(chan struct{})
    defer func() { serverkit.AbortReceive(ctx); <-readerDone }() // join before returning
    go func() {
        defer close(readerDone)
        for {
            msg, err := stream.Receive() // returns serverkit.ErrReceiveAborted once aborted
            if err != nil {
                return
            }
            handle(msg)
        }
    }()
    // ... send until done
}
```

The response is untouched: the handler still sends and returns its status.
Unary routes are unaffected — Connect has read their body before the handler
runs, and the drain aborts only the streams it ends. A test that mounts a
handler on a bare `httptest` server gets the same body by wrapping it with
`serverkit.NewStreams().Install(h)`.

A drain is never an incident in the logs: `svcerr.ErrServerDraining` (and
`svcerr.Draining("your words")`, for a transport that tells its peers itself)
is classified `error_class=canceled` and is an expected outcome in every
logging layer — written at INFO with `expected=true`, never forwarded as an
ERROR, and counted in `rpc.server.errors` under `canceled`.

### Websockets (hijacked connections)

`http.Server.Shutdown` neither waits for nor closes a hijacked connection, so
a websocket open at SIGTERM used to be dropped by the process exit — the
browser saw a dead TCP connection, and the handler's deferred cleanup (a PTY,
a session) never ran. Under `serverkit.LongLived` the upgrade's `Hijack`
registers the connection with the drain, and `serverkit.AfterDrain` gives a
handler blocked in a read its goodbye hook:

```go
mux.Handle("/terminal", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        return
    }
    defer conn.Close()
    defer closeSession() // runs on every path, including the drain

    stop := serverkit.AfterDrain(r.Context(), func() {
        deadline := time.Now().Add(2 * time.Second)
        _ = conn.WriteControl(websocket.CloseMessage,
            websocket.FormatCloseMessage(websocket.CloseServiceRestart, "server restarting"), deadline)
        _ = conn.SetReadDeadline(deadline) // returns the read loop
    })
    defer stop()
    for { if _, _, err := conn.ReadMessage(); err != nil { return } }
})))
```

1012 (service restart) is what a browser reconnects on; 1000 reads as "the
session ended". A handler that ignores the drain entirely is not left to the
process exit either: once the reserve is half spent the drain closes its
hijacked connection itself (`Streams.CloseHijacked`), so its read returns and
its deferred cleanup still runs inside the budget.

### Saying goodbye: the soft signal

A stream with something to say before it goes — a final event, a websocket
close frame, a gateway handing its peer to a live replica — asks for the soft
signal at the top of its handler:

```go
soft := serverkit.Draining(ctx) // claims the soft phase
for {
    select {
    case <-soft:
        send(goodbye) // ctx is still live
        return nil
    case ev := <-events:
        send(ev)
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

A stream that asked is left running when the drain begins, and is ended only
when it returns or at the shutdown deadline minus `DrainReserve` (default 5s,
never more than half the budget; `serverkit.Config.DrainReserve`). The reserve
is what the rest of the teardown runs in. A stream that never asked is ended at
once — waiting would only delay its client's reconnect.

A stream that tells its peer how long it has (a gateway asking a daemon to
reconnect elsewhere by a deadline) reads `serverkit.DrainDeadline(ctx)` once
`soft` closes: the exact moment the drain will end it. Never compute it as
"shutdown deadline − `DrainReserve`" — the reserve shrinks to half the budget
when the budget is under twice it, and that arithmetic is then early.

## Reading a shutdown

Each phase writes one INFO line, `msg` starting `drain:`, with `phase` and its
`elapsed`; the same durations are the `forge.server.drain.duration` histogram
(attribute `phase`), exported by the telemetry flush that ends the shutdown:

| `phase` | line | carries |
|---|---|---|
| `readiness` | `drain: readiness flipped to draining` | `pre_stop_delay` |
| `pre_stop` | `drain: pre-stop pause over; draining the listener` | |
| `listener` | `drain: listener drained` | `addr`, `streams_ended`, `streams_claimed`, `streams_hard_ended`, `hijacked_conns_closed`, `streams_still_running` |
| `workers` | `drain: workers stopped` | `workers` |
| `operators` | `drain: operators stopped` (only with operators) | `operators` |
| `teardown` | `drain: teardown done` (OnShutdown, pprof) | |
| `node` | `drain: node stopped` (one per `lifecyclekit.Supervise` node) | `node`, and `drain_error`, `close_error` or `run_still_running` when one went wrong |
| `total` | `drain: complete` | `shutdown_timeout` |

Each line is written by the piece that runs the phase, so a main without
`serverkit.Run` gets the same ones: `Drainer.Begin` writes `readiness` and
`pre_stop`, `DrainHTTP` writes `listener` (one per listener, to its
`Streams.Logger`), `Drainer.Complete` writes `total`; `Run` adds only
`workers`, `operators` and `teardown`. Each takes a `Logger` (nil:
`slog.Default()`) and a `Meter` (nil: the global OpenTelemetry meter) field.

A non-zero `streams_hard_ended` is a stream that asked to say goodbye and did
not finish within the soft phase; `hijacked_conns_closed` or
`streams_still_running` is a handler that ignores its ended context — fix the
handler. A slow `listener` phase with neither is a unary request taking that
long.

## Without `serverkit.Run`

A main that serves more than one listener (a gateway's daemon port beside the
API), or a worker with a health port, builds `serverkit.Listener`s — each with
its own address, TLS, timeouts, `Streams` and chain — and drives their
`Run`/`Drain`/`Close`/`Ready`: graceful is `Drain` (stop accepting, unary
finish, streams soft-then-hard, the `listener` phase line), then cancel `Run`'s
context, then `Close`. Cancelling `Run`'s context first is a hard stop.

```go
l, err := serverkit.NewListener(serverkit.ListenerDeps{
    Config: serverkit.ListenerConfig{Addr: ":9090", RedialOnDrain: []string{"acme.v1.ToolsDaemonService"}},
    Chain:  daemonChain,                       // required; its audience is checked against every mounted service
    Routes: routes,                            // serverkit.Routes: mounts with the chain's handler options
    Front:  map[string]http.Handler{"/healthz": healthz}, // served in front of the chain, no credentials
})
```

`lifecyclekit.Supervise` runs any list of such components. A component
implements whichever of `Run(ctx)`, `Drain(ctx)`, `Close(ctx)` and
`Ready(ctx)` it has (`lifecyclekit.Runner`, `Drainer`, `Closer`, `Readier`);
`lifecyclekit.Component(name, c)` turns one into a `Node`, and
`lifecyclekit.WorkerNode(w)` adapts a `serverkit.Worker` (Run is
`RunContext`/`Start`, Close is `Stop`). List nodes dependencies first:

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()
drainer := serverkit.NewDrainer(cfg)  // readiness gate + PRE_STOP_DELAY + SHUTDOWN_TIMEOUT
pool := lifecyclekit.Node{Name: "db", Close: func(context.Context) error { return db.Close() },
    Ready: func(ctx context.Context) error { return db.PingContext(ctx) }}
// serve drainer.ReadinessHandler(lifecyclekit.ReadyChecks(pool), 0, logger) on a Front "/readyz"
err := lifecyclekit.Supervise(ctx, lifecyclekit.Policy{Drainer: drainer, Flush: otelShutdown},
    pool,
    lifecyclekit.Component("api", apiListener),
    lifecyclekit.Component("gateway", daemonListener), // listed last: drains first
)
```

Start runs in list order, waiting for each node that reports `Ready` before
the next, so a listener accepts traffic only once what it reads is up; the
drainer is marked ready after the last. On `ctx` ending (or a runner failing,
under `Policy.FailurePolicy`): readiness flips and the pre-stop pause runs,
then in REVERSE order each node is drained, has its `Run` context ended and
awaited, and is closed — all inside `SHUTDOWN_TIMEOUT` — then `drain:
complete`, then `Policy.Flush` last. Each node writes a `drain: node stopped`
line (`phase=node`, `node=<name>`). A `Run` gets a context of its own, ended
only after its node drained, so a listener keeps serving through the pre-stop
pause. When one node spends the whole budget, each node after it still gets
250ms for its `Run` to return once cancelled, so only a `Run` that really
ignores its context logs `node did not stop within the shutdown budget`
(`run_still_running`).

`serverkit.Run` is this over one listener plus workers and operators. The
lower-level pieces below are what a Listener is built from, for a main that
keeps its own `http.Server`:

```go
drainer := serverkit.NewDrainer(cfg)                 // readiness gate + budget
drainer.Logger = logger                              // phase lines (nil: slog.Default())
mux.HandleFunc("/readyz", drainer.ReadinessHandler(checks, cfg.ReadinessTimeout, logger))
streams := &serverkit.Streams{Logger: logger}
srv := serverkit.NewHTTPServer(cfg, mux, streams)    // installs streams into every request
// ... serve; mark ready once bound:
drainer.MarkReady()
// on SIGTERM:
drainer.Begin()                                      // 503 "draining", then PRE_STOP_DELAY
ctx, cancel := drainer.ShutdownContext()             // from Background, SHUTDOWN_TIMEOUT
defer cancel()
err := serverkit.DrainHTTP(ctx, srv, streams)        // Shutdown + streams, soft then hard
// ... your own teardown, then, before flushing telemetry:
drainer.Complete()                                   // "drain: complete", the total
```

A bare `srv.Shutdown` still ends the streams that did not claim the soft phase;
`DrainHTTP` adds the reserve-bounded end of the ones that did.
