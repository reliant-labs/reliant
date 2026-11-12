---
name: observability/error-stacks
description: The bounded stack svcerr captures for server faults (Internal, Unknown, DataLoss, Unavailable), where it appears (exception.stacktrace, code.* call site, sfp1 grouping), what never carries one, and how to get a useful one.
---

# Observability: stacks for server faults

An issue for a server fault needs frames, and the log line's own call site (the
interceptor that wrote `rpc failed`) is not one. So `svcerr` captures a bounded
stack — 32 frames, 8 KiB, module-relative paths that survive `-trimpath` — where
a server fault is first built or first wrapped, and only for those:

| kind | stack |
|---|---|
| Internal, Unknown, DataLoss, Unavailable (constructors, or an unrecognised driver error meeting `svcerr.Wrap`) | captured, once, at the earliest wrap |
| every client-class kind, DeadlineExceeded, Aborted, Unimplemented, cancellations, `WithClass(…, ClassUser)` | never |

It rides the ERROR record, the span's exception event and the `log.error` event
as OTel `exception.stacktrace` in Go's traceback layout, and its innermost
application frame becomes `code.function.name` / `code.file.path` /
`code.line.number`, so the fingerprint groups the issue by where the failure began (a record that also carries its cause in `error` with no `exception.type` is further split by that cause; see `observability/query`). It
never reaches the client: it is not in the message, the metadata or the details.

Return `svcerr.Wrap` / `svcerr.WrapOr` from the handler, or build the error with
a constructor: an error first wrapped by the interceptor after the handler has
returned has no application frame, so it reports no stack (the log line says as
much as the stack would). `svcerr.StackOf(err)` is the reader for code that
builds its own telemetry.
