---
name: observability/product-events/cli
description: Events from a command-line program — pkg/observe/cliev (an anonymous install id, a one-time notice, DO_NOT_TRACK / <APP>_TELEMETRY=off opt-out, always off in CI, `<app> telemetry on|off|status`, a 300 ms batch at exit), the first-run event, and the native ingest key a CLI sends with.
---

# Events from a CLI: `pkg/observe/cliev`

A CLI has no request, no claims and no browser, so `observe.Track` does not fit
it. `cliev` records the same events (the contract in
`observability/product-events`: `event.name` `product.<name>`, `app.client`
`cli`, bounded and PII-guarded `product.<key>` tags) into the same
`product_events` table.

```go
func main() {
	c := cliev.Init(cliev.Config{
		App:      "acme",                 // config dir, ACME_TELEMETRY, the notice
		Version:  version,
		Endpoint: telemetryEndpoint,      // stamped by the release build; "" sends nothing
		Quiet:    hasFlag(os.Args, "--json"),
	})
	if c.FirstRun() {
		c.Track(ctx, cliev.FirstRunEvent("acme"), c.FirstRunAttrs("")...)
	}
	defer cliev.Flush(context.Background())
	// ... run the command; cliev.Track(ctx, "acme_export_finished", slog.Int("files", n))
}
```

What it does for you:

- **Install id.** A random UUID in `<config dir>/acme/telemetry.json`,
  recorded as `visitor.id`. The only identity an unauthenticated CLI sends. A
  signed-in CLI can add the user and org with `observe.WithActor`.
- **Notice.** One line on stderr on the first run, never on a `Quiet` run.
  Nothing is sent until it has been shown once.
- **Opt-out.** `DO_NOT_TRACK=1` (always wins), `ACME_TELEMETRY=off`, or
  `c.SetEnabled(false)` (wire it to `acme telemetry off`), which is remembered.
  Always off on a CI runner (`CI=true`, `GITHUB_ACTIONS` and the other CI
  services' variables; `cliev.InCI`): nothing turns it back on there.
- **Bounds.** One event per name per run, `cliev.MaxEventsPerDay` per install,
  the batch sent once at exit within 300 ms and dropped on failure. Telemetry
  never slows or fails a command.

**Endpoint.** On Reliant hosting, send to the keyed ingest
`https://ingest.reliantlabs.dev/k/<key>/v1/logs` with a **native** ingest key
for the project's environment. A native key is public like a browser key (it
ships in the binary): it admits logs only, needs no Origin, and has stricter
per-key and per-address limits. Bake it in at release (`-ldflags -X`), never
in a development build.

**Tags, not text.** `version`, `os`, `arch`, a command's `noun.verb`, `ok`, a
duration bucket. Never paths, hostnames, project names, flags' values or error
text.
