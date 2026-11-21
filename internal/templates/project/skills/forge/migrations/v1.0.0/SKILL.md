---
name: v1.0.0
description: Upgrade a project to forge v1.0.0. Only the two v1.0.0 sections that still have a codemod are kept; the rest aged out of the upgrade window. (1) The composition graph upgrade path — `forge fix composition-graph` converts the legacy Infra / OpenInfra / NewComponents / lifecycle.go / ServeSpec composition and the per-service, per-worker and per-operator subcommands to providers, compose.go constructor methods and roots, and lists — writing nothing — every shape it cannot convert without deciding for you; ServeSpec knobs become reachability (control-plane's OperatorListener, NoKubernetesAPI and WorkerLeases are the worked example); deploy binds workloads to roots. Use when internal/app declares `type Infra struct` or `func NewComponents`, or a graph project moves to binary_config. (2) Hand-built Connect errors — a *connect.Error is never redacted, so connect.NewError(code, err) publishes err.Error(); `forge fix connect-errors` rewrites the fixed-message sites to svcerr with the same code and message and lists the rest for svcerr.WrapOr. Use when internal/handlers calls connect.NewError.
version: v1.0.0
automation: fix:composition-graph, fix:connect-errors
detection: grep -qsE '^type Infra struct|^func NewComponents\(' internal/app/*.go && exit 0; test -d internal/handlers && grep -rqs --include='*.go' --exclude='*_gen.go' --exclude='*_test.go' 'connect\.NewError(' internal/handlers && exit 0; exit 1
---

# Upgrading to v1.0.0

The other v1.0.0 sections aged out of forge's two-minor upgrade window and
were deleted with their playbooks. These two stay because each still has a
codemod (`forge fix composition-graph`, `forge fix connect-errors`) that a
project past v1.0.0 can still need. Each section is independent: apply the
ones whose "Use when" matches.

## 1. The composition graph: off Infra, NewComponents and ServeSpec

Use when `internal/app` declares `type Infra struct` or `func NewComponents`
(the legacy composition), or when a graph project moves to `binary_config`.
A project still on the legacy composition generates exactly as before; nothing
migrates it implicitly. Adopt one root at a time (`//forge:root <name> adopt`),
or convert everything with `forge fix composition-graph`, which lists — writing
nothing — each shape it cannot convert without deciding for you. Load
`forge skill load migrations/v1.0.0/composition-graph` for adoption, the
codemod, clearing what it lists, control-plane's ServeSpec knobs as the worked
example, and binding deploy workloads to roots.

## 2. Hand-built Connect errors → `svcerr`

Use when a handler package calls `connect.NewError`: a `*connect.Error` is
never redacted, so `connect.NewError(code, err)` publishes `err.Error()`.
`forge fix connect-errors` (on request) rewrites each site an svcerr spelling
reproduces exactly and lists the rest for `svcerr.WrapOr`;
`forgeconv-handler-connect-error` warns on each. Load
`forge skill load migrations/v1.0.0/connect-errors`.
