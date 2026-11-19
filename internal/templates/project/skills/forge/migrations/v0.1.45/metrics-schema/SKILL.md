---
name: migrations/v0.1.45/metrics-schema
description: Adopt the declared metrics registry in a project scaffolded before v0.1.45 — add `<bin> metrics schema`, the owned `internal/metrics/metrics.go`, the `records_created` example in CRUD Create shims, and `forge obs metrics declared --dashboard`. Use when `<bin> metrics schema --json` says "unknown command", `cmd/<bin>/cmd/metrics.go` or `internal/metrics/metrics.go` does not exist, or `GET /debug/metrics/schema` returns 404.
---

# Declared metrics registry (v0.1.45, section 9 of `migrations/v0.1.45`)

Every metric a service emits is now **declared** — name, type, UCUM unit, description, attribute keys (with the closed set of values where there is one), stability and `since` — in `github.com/reliant-labs/forge/pkg/observe/metrics`. The registry returns the standard OpenTelemetry instruments, so recording is unchanged. What it adds: a schema you can dump, and a guard on attributes.

## What changed

- `pkg/observe/metrics`: `Default()` is the process registry. `metrics.Default().Int64Counter(metrics.Spec{...})` (and `Float64Counter`, `Int64UpDownCounter`, `Int64Histogram`, `Float64Histogram`, `Int64Gauge`, `Float64Gauge`) declares and creates in one step; `Describe` lists a metric some library creates. forge's RPC, Go runtime and SQL metrics are declared for you.
- **An attribute the metric did not declare is no longer exported.** Under `ENVIRONMENT=development` it panics at the call; elsewhere the key is dropped, a value outside an enumerated set becomes `_other`, and `forge.metrics.rejected_attributes{metric,reason}` counts it. Existing code that records through a plain `otel.Meter` is untouched; only instruments you move onto the registry are guarded.
- Three places print the schema (`forge.metrics/v1`): `<bin> metrics schema [--json]`, `GET /debug/metrics/schema` on the pprof listener (`PPROF_ADDR`, loopback by default), and `forge obs metrics declared [--binary <path> | --url <addr>]`.
- `forge build` runs the binary it built and writes `bin/observability/metrics-schema.json` (`-o` changes `bin`). A release carries it as `observability/metrics-schema.json`. A cross-compiled binary cannot run on the build host, so no file is written for it, and any stale one is removed.
- `forge obs metrics declared --dashboard` prints a **Custom metrics** HyperDX dashboard drawn from the schema: a counter as a rate, a histogram as p50/p95/p99, a gauge or up-down counter as its last value, one extra panel per enumerated attribute. `forge generate` does not write it (it has no built binary to read); redirect the output into `deploy/observability/dashboards/` to own it.
- The CRUD `Create<Entity>` shim records the example metric: `resp, err := crud.HandleCreate(...)(ctx, req)` then `metrics.RecordCreated(ctx, "<Entity>", err)`.

## Steps

1. `forge generate`. It births `cmd/<bin>/cmd/metrics.go` and `internal/metrics/metrics.go` if absent and never touches an existing file.
2. Existing Create shims keep their one-line delegation (`handlers_crud.go` is yours). To record the example metric from one, replace `return crud.HandleCreate(...)(ctx, req)` with the three lines above and import `<module>/internal/metrics`. Skip this if you do not want the example: delete `recordsCreated` and `RecordCreated` from `internal/metrics/metrics.go`, or keep them unused.
3. Declare your own metrics in `internal/metrics/metrics.go`. Keep each instrument unexported and expose a function that names what happened, so a caller cannot attach an attribute the declaration omits.
4. To move an existing hand-rolled instrument onto the registry, replace `meter.Int64Counter("name", ...)` with `metrics.Default().Int64Counter(metrics.Spec{Name: ..., Unit: ..., Description: ..., Since: ...})` and list every attribute key you record. Run with `ENVIRONMENT=development` once: a missed key panics with the metric and key named.
5. Units must be UCUM: `s`, `ms`, `By`, `1`, or a counted thing in braces (`{order}`). `seconds`, `bytes` and `req/sec` are refused.

## Verify

`go build ./... && go run ./cmd/<bin> metrics schema --json | jq '.metrics[].name'` lists `rpc.server.calls`, the `go.*` metrics and anything you declared. `forge obs metrics declared --binary ./bin/<bin>` prints the same. Against a running service: `curl -s localhost:6060/debug/metrics/schema | jq .schema_version` prints `"forge.metrics/v1"`.
