---
name: observability/tracing-lint
description: forge lint --tracing — the four rules that keep a request in one trace (log-without-ctx with its autofix, raw-http-client, raw-sql-open, contract-uninstrumented), lint.tracing.log_wrappers for your own logging helpers, and adopting the rules in a large codebase.
---

# Observability: keeping a request in one trace (`forge lint --tracing`)

A trace fragments wherever app code steps around the traced constructors.
`forge lint` gates on four such places, and `forge lint --tracing` runs them
alone:

| Rule | Flags | Fix |
|---|---|---|
| `forge-tracing-log-without-ctx` | `slog.Info(…)` / `logger.Info(…)` where a `ctx` (or `r *http.Request`) is in scope: the line has no trace_id | autofixed to `slog.InfoContext(ctx, …)` |
| `forge-tracing-raw-http-client` | `&http.Client{}` with no/nil/default transport, `http.DefaultClient`, `http.Get` | `observe.NewHTTPClient(observe.HTTPClientOptions{…})` |
| `forge-tracing-raw-sql-open` | `sql.Open`, `sqlx.Connect`, a pgx pool with no tracer | `dbtrace.Open(driver, dsn)`; for a native pgx pool, `cfg.ConnConfig.Tracer = dbtrace.NewPgxTracer()` before `pgxpool.NewWithConfig` |
| `forge-tracing-contract-uninstrumented` | an instrumented contract's `pkg.New(…)` used without its decorator, anywhere (`providers.go`, `*_wire.go`, another binary) | autofixed to `pkg.NewInstrumented(…)` — New's signature, decorated |

A logging helper of your own (`logging.Info(msg, args...)`) joins the log rule
through forge.yaml:

```yaml
lint:
  tracing:
    log_wrappers:
      - call: github.com/acme/app/internal/logging.Info
        context_call: log/slog.InfoContext   # default: logging.InfoContext
```

Adopting the rules in a big codebase: set them to `warn` under `lint.rules`,
run `forge lint --tracing --fix` once (it rewrites every log call and every
undecorated contract construction at any severity but off; `forge lint --fix`
rewrites only at error, and without `--fix` nothing is rewritten), fix the
rest, then drop the
`warn` entries.
