---
name: api-connect-v2
description: connect-go v2 (connectrpc.com/connect/v2, the default for a new service project) on both sides — handler and server-streaming shapes, request and response headers through CallInfo, building a client with connect.NewClient + connecthttp.NewTransport, a client interceptor that adds an API key, the server-stream client as a struct value, gzip and the 4 MiB defaults, and per-service send/read caps through MountOptions.
---

# connect-go v2, server and client

`forge project new` puts a service project on **connect-go v2**
(`connectrpc.com/connect/v2`); `--connect v1` keeps v1. After that, go.mod
decides: a direct require of `connectrpc.com/connect/v2` is v2, and
`forge generate` emits v2 stubs. The `api` skill's rules (pb-through, `svcerr`,
`s.deps`) are the same on both majors; this skill is what differs.

The upstream guide is on disk, matching the version you compile against:

```bash
less "$(go list -m -f '{{.Dir}}' connectrpc.com/connect/v2)/docs/v2-guide.md"     # also docs/v2-migration.md
```

## Handlers

There are no `connect.Request[T]`/`connect.Response[T]` wrappers. A unary
handler takes and returns bare messages:

```go
func (s *Service) GetJob(ctx context.Context, req *pb.GetJobRequest) (*pb.GetJobResponse, error) {
    job, err := s.deps.Store.GetJob(ctx, req.GetName())
    if err != nil {
        return nil, svcerr.Wrap(err)
    }
    return &pb.GetJobResponse{Job: job}, nil
}
```

Headers and trailers live on a `*connect.CallInfo` in the context:

```go
info, _ := connect.CallInfoForServerContext(ctx) // always present inside a handler
key := info.RequestHeader().Get("X-Api-Key")
info.ResponseHeader().Set("X-Generation", "42")
```

**Errors.** v2 publishes only a locally built `*connect.Error`; anything else
reaches the client as a bare code with no message. forge's chain ends with
`svcerr.InterceptorV2()`, so return `svcerr` values exactly as on v1. A
hand-built `connect.NewError(code, "msg")` now takes a string, not an error.

## Server streaming

The handler gets a generated stream per rpc that only has `SendHeaders` and
`Send`. There is no `Close`: returning ends the stream (`nil` is a clean end,
an `svcerr` value is the error the client reads).

```go
func (s *Service) WatchJob(ctx context.Context, req *pb.WatchJobRequest,
    stream jobsv1connect.JobsServiceWatchJobServerStream) error {
    if err := stream.SendHeaders(); err != nil { // the client sees the stream open before any event
        return err
    }
    for {
        select {
        case ev := <-events:
            if err := stream.Send(&pb.WatchJobResponse{Event: ev}); err != nil {
                return err
            }
        case <-ctx.Done(): // client went away, or the server is draining
            return nil
        }
    }
}
```

Without the early `SendHeaders`, nothing reaches the client until the first
`Send`, so a quiet stream looks like a hung one.

## Clients

A generated client wraps one `*connect.Client`, built from a transport and
client interceptors. One `*connect.Client` can back every service client for
the same base URL.

| Calling from | Build it with |
|---|---|
| this project's own code (service to service, a worker) | `observe.NewClientStackV2(observe.ClientStackDepsV2{})` then `stack.NewClient(baseURL)`, or `servicecatalog.NewClientV2`. Traced, request id forwarded, 4 MiB read cap lifted, `svcerr.ClientInterceptorV2` installed |
| a test | `_, jobs := testkit.NewConnectClientV2(t, svc.RegisterV2, jobsv1connect.NewJobsServiceClient)`: an httptest server behind `svcerr.InterceptorV2` |
| a public Go SDK that must not import forge | `connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, opts...), interceptors...)` |

```go
import (
    "connectrpc.com/connect/v2"
    "connectrpc.com/connect/v2/connecthttp"
)

// apiKey adds a header to every call this client makes, unary and streaming.
func apiKey(key string) connect.ClientInterceptor {
    return func(next connect.ClientFunc) connect.ClientFunc {
        return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
            if info, ok := connect.CallInfoForClientContext(ctx); ok { // a Client call always attaches one
                info.RequestHeader().Set("X-Api-Key", key)
            }
            return next(ctx, spec)
        }
    }
}

client := connect.NewClient(connecthttp.NewTransport(httpClient, "https://api.example.com"), apiKey(key))
jobs := jobsv1connect.NewJobsServiceClient(client)
res, err := jobs.GetJob(ctx, &pb.GetJobRequest{Name: "a"})
```

Interceptors run in argument order. The outer function runs once, at
`NewClient`: keep per-call state in the function it returns. For headers on ONE
call, or to read response headers after it, attach the CallInfo yourself:

```go
ctx, info := connect.NewClientContext(ctx)
info.RequestHeader().Set("X-Request-Id", id)
res, err := jobs.GetJob(ctx, req)
gen := info.ResponseHeader().Get("X-Generation")
```

On a failed call, `connect.CodeOf(err)` is the code and `errors.As(err, &ce)`
(`*connect.Error`) gives the message. A v2 error carries no metadata;
`svcerr.MetaOf(err).Get(svcerr.ReasonHeader)` reads the reason only on a client
that installed `svcerr.ClientInterceptorV2()`, which forge's builders do.

## The server-stream client is a struct VALUE

v1 returned `*connect.ServerStreamForClient[T]`. v2 returns a generated struct
by value, so a helper cannot `return nil, err`:

```go
func open(ctx context.Context, c jobsv1connect.JobsServiceClient) (jobsv1connect.JobsServiceWatchJobClientStream, error) {
    stream, err := c.WatchJob(ctx, &pb.WatchJobRequest{Name: "a"})
    if err != nil {
        return jobsv1connect.JobsServiceWatchJobClientStream{}, err
    }
    return stream, nil
}

stream, err := open(ctx, jobs)
if err != nil { return err }
defer stream.Close() // idempotent; abandons the stream early
for {
    msg, err := stream.Receive()
    if errors.Is(err, io.EOF) { // a clean end: the server returned nil
        break
    }
    if err != nil {
        return err // the server's error, or a broken stream
    }
    handle(msg)
}
```

Canceling `ctx` also ends the stream. The call itself only sends the request:
a refusal (unauthenticated, not found) usually arrives from the first
`Receive`, so read one message before treating the stream as open.

## Defaults that differ from v1

- **A client reads at most 4 MiB per message** (v1 read any size).
  `connecthttp.WithReadMaxBytes(n)` on `NewTransport` sets it; 0 is unlimited.
  forge's client builders lift it.
- **gzip is on.** `connecthttp` registers gzip on both sides. A client
  advertises it, and a server compresses a response when the client accepts
  gzip and the message is at least `connecthttp.WithCompressMinBytes(n)`.
  `connecthttp.WithCompressors()` with no arguments turns compression off,
  because `WithCompressors` and `WithCodecs` replace the default set.
- **Server caps.** The listener's chain caps every request and response at
  `read_max_bytes` / `send_max_bytes` from config (4 MiB each).

## Per-service options: caps, compression, interceptors

One service that sends big responses gets its own cap; do not raise
`send_max_bytes` for every service. `internal/app/routes_gen.go` has a
`MountOptions` field per service. Set it in `compose.go`, where the route set
is built:

```go
return NewUserRoutes(UserRoutesDeps{ /* … */
    Options: MountOptions{Assignments: []mountkit.Option{mountkit.WithHTTPOptionsV2(
        connecthttp.WithSendMaxBytes(64 << 20),
        connecthttp.WithCompressMinBytes(1 << 10),
    )}},
})
```

A mount's `connecthttp` options apply after the chain's and the last one wins,
so this cap replaces the global one for this service only.
`mountkit.WithInterceptorsV2(...)` adds server interceptors to one service; they
run inside the chain's. A service with options gets its own `*connect.Server` on
the same mux. `forge project libraries mountkit` prints the whole API.
