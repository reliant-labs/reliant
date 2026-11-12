---
name: chain
description: The auth interceptor chain's two caller-shaped seams — audiences (one serverkit chain and listener per kind of caller, enforced from the proto's `audience`) and publishing one rpc with `auth_required` false, including why a public handler sees no claims and how to handle the anonymous case.
---

# Audiences and public rpcs

The chain itself, its order and its slots are in `auth` ("The wiring file + interceptor chain"). This sub-skill covers the two ways a server admits callers other than its signed-in users.

## Audiences: one listener per kind of caller

`ChainDeps.Auth` is a `serverkit.Authenticator` — the auth interceptor plus the **audience** it admits: `serverkit.AuthFor(serverkit.AudienceUser, authInterceptor)` for your signed-in users. A service declares the audience it serves in its proto, defaulting to `user`:

```proto
service ToolsDaemonService {
  option (forge.v1.service) = { audience: "daemon" };  // not reachable with a user token
  rpc Attach(stream AttachRequest) returns (stream AttachResponse);
}
```

A `serverkit.Listener` refuses to start when it mounts a service whose audience differs from its chain's `Auth.Audience()` — and `serverkit.Run` checks the same over a `Server.Chain`. So a daemon credential can never reach a user RPC, even through a hand-written route set. Serving a second kind of caller is a second chain and a second listener, never a second token type inside one chain:

```go
daemonChain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor("daemon", daemonAuth)})
daemon, err := serverkit.NewListener(serverkit.ListenerDeps{
    Config: serverkit.ListenerConfig{Addr: ":9090", ReadTimeout: -1}, // long bidi streams
    Chain:  daemonChain,
    Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
        return components.MountToolsDaemon(mux, cfg, logger, opts...)
    }),
})
```

Run the listeners under `lifecyclekit.Supervise` (`services/shutdown`, "Without `serverkit.Run`").

## Publishing one RPC (per-RPC allow-list)

`auth_required: false` publishes ONE rpc to unauthenticated callers. It is
per-rpc, not a service posture, and the default is closed.

```proto
service Status {
  rpc GetVersion(GetVersionRequest) returns (GetVersionResponse) {
    option (forge.v1.method) = { auth_required: false };  // published deliberately
  }
  rpc GetDetail(GetDetailRequest) returns (GetDetailResponse) {}  // no annotation = closed
}
```

Opening an rpc is not the only way to serve a second audience: forge declares
multiple frontends and multiple services in `forge.yaml`, so separate audiences
can have separate applications against separate rpcs.

Each lands in the generated set as connect's own `…Procedure` constant, matched EXACTLY — never by substring, so a `HealthReport` rpc cannot ride along with the health probes. An unannotated rpc defaults to `auth_required: true`: silence never publishes an endpoint. The gRPC probes are always allowed; they run before anything can authenticate.

**A public handler gets NO claims — not even from a caller who presented a valid token.** The allow-list is checked BEFORE the Authorization header is read, so the validator is not consulted at all. Handle the anonymous case explicitly:

```go
if claims, ok := middleware.ClaimsFromContext(ctx); ok {
    return s.listFor(ctx, claims.UserID) // personalized
}
return s.listPublic(ctx)                 // anonymous — the normal case here
```

Two ways to get this wrong. `middleware.GetUser(ctx)` returns a `CodeUnauthenticated` **error** (it does not panic), so calling it here 401s every anonymous caller and defeats the annotation. And `claims, _ := ClaimsFromContext(ctx)` followed by `claims.UserID` **nil-panics** on every anonymous call — check `ok`.

To personalize for callers who DO present a token, validate it in the handler, or keep the RPC authenticated and add a separate public one. Not `AnonymousOK: true`: that makes auth non-gating service-wide, so `auth_required: true` gates nothing.
