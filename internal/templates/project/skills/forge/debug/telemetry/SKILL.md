---
name: telemetry
description: Local ClickStack telemetry, continuous profiles and live pprof when debugging a forge process.
---

# Local telemetry and profiling
## Observability (ClickStack)

`forge env up dev` runs ClickStack (the `clickstack` compose service: ClickHouse, the HyperDX UI and an OTLP collector) with traces, metrics and logs. It is on by default and off with `_observability = False` in `deploy/kcl/dev/main.k`; see the `observability` skill.

- **HyperDX UI:** the URL `forge env up dev` / `forge env status dev` lists under **Compose services**. No login.
- **Traces:** HyperDX → Search → Traces. Find slow requests, trace cross-service calls.
- **Metrics:** HyperDX → Search → Metrics. Query `rpc.server.call.duration` (the otelconnect RPC edge histogram; attributes `rpc.method`, `rpc.response.status_code`) etc.
- **Logs:** HyperDX → Search → Logs. The collector tails `.forge/logs/<env>/*.log`, so a host process's logs are here and in the file; a log with a `TraceId` links to its trace.
- **Profiles:** continuous profiles are collected locally when enabled; export them with `forge obs profile` (local ClickStack only, `--env <env>` rather than a positional env). Load `forge skill load observability/profiles` for sample types, retention and collection settings. Use `--list` to discover services; no samples is not proof the process used no CPU. For a live process, pprof below remains available.

Host processes are pointed at it by `forge env up`, which sets `OTEL_EXPORTER_OTLP_ENDPOINT` to the loopback OTLP/HTTP port the compose service publishes (`_otlp_http_port` in `dev/main.k`) and `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, unless you set them yourself.

**pprof is always on.** It binds its own listener, separate from the app port, defaulting to `127.0.0.1:6060` — live in every environment and routable from none (no k8s Service, no route, no published port). You never have to redeploy to start profiling:

```bash
go tool pprof http://localhost:6060/debug/pprof/heap    # what is this process holding?
go tool pprof http://localhost:6060/debug/pprof/goroutine
curl -o cpu.pb.gz 'http://localhost:6060/debug/pprof/profile?seconds=30'

kubectl port-forward <pod> 6060:6060                    # in a cluster, then the above
```

Reach for the heap profile the moment a process is being OOMKilled: the cgroup's `memory.stat` tells you HOW MUCH it holds and never WHAT, and the answer only exists while the process is still alive.

Change the address with `PPROF_ADDR` / `--pprof-addr`; the scaffolded `docker-compose.yml` sets `0.0.0.0:6060` for the container's own network namespace (nothing publishes it). Pass `--pprof-addr=` to switch pprof off: an empty `PPROF_ADDR` counts as unset, like every empty config env var, so it keeps the default.
