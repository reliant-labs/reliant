---
name: observability/error-levels
description: Where forge's error-level policy applies (rpc failed, component failures, every hand-written logger.Error through observe.NewErrorClassHandler), how to wrap a log forwarder so it sees the classified level, and how to mark an error's class (svcerr.WithClass, observe.WithErrorClassifier).
---

# Observability: applying the error-level policy

The level table — which `error_class` and code is ERROR, WARN or INFO, and
whose deadline a `DeadlineExceeded` was — is in `observability`, "Error
levels: who has to act". This is how the policy reaches every record.

The policy holds at all three places a record comes from:

1. `rpc failed` / `stream failed` at the RPC edge (`observe.LoggingInterceptor`);
2. component failures on the in-process chain (`observe.LogMiddleware`);
3. **every hand-written `logger.Error`** — `observe.NewErrorClassHandler`
   wraps the process's slog handler (serverkit's `NewLogger` already does) and
   lowers a record carrying a non-server error to the user-error level, adding
   `error_class`. It only ever lowers; it never raises a WARN to ERROR.

**If you forward logs to another sink (or count them), wrap
`observe.NewErrorClassHandler` OUTSIDE that handler**, so it sees the
classified level — the forwarder keeps gating on `level >= ERROR` and never
needs to know about svcerr:

```go
handler := forwardingHandler(serverkit.NewLogger(cfg).Handler()) // forwards >= ERROR
logger := slog.New(observe.NewErrorClassHandler(handler))
```

Marking: most user errors need no marker — return the right `svcerr` kind
(`svcerr.FailedPrecondition`, `svcerr.PlanLimit`, …). Use
`svcerr.WithClass(err, svcerr.ClassUser)` when the CODE and the FAULT disagree
(`Unavailable` is the honest code for "the user's machine is offline", and a
server fault by kind), or when an error never crosses an RPC boundary (a worker
logging a provider's "credential rejected"). `svcerr.WithClass(err,
svcerr.ClassServer)` is the reverse: a 4xx the client must see that is really
our bug. A library error svcerr cannot see into — a workflow engine's
application error that crossed a serialization boundary — is taught with
`observe.WithErrorClassifier(func(error) svcerr.Class)` (return
`svcerr.ClassNone` for "no opinion").
