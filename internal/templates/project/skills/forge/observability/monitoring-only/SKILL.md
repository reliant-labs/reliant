---
name: observability/monitoring-only
description: Monitoring for an app forge did not build and does not deploy — any language, any CI. forge obs init, ingest tokens, the OTEL_* variables per language, the attribute contract forge's own apps follow, alert receivers, and an estimate to quote.
applies: any
---

# Monitoring only, any language

For an app forge did **not** build and does **not** deploy: any language, its own CI, its own infrastructure. Nothing in the app changes except three environment variables, and nothing about how it is built or shipped changes at all. Sign-in is required; there is no anonymous path.

**What the team gets:** its traces, logs and metrics in Reliant (dashboards at `https://forge.reliantlabs.io/forge/p/<project>/observability/dashboards?env=<env>`); error grouping into issues with new/regressed detection; `forge obs status|logs|traces|issues|issue|metrics|query|changes` for people and agents; alert routing to Slack, PagerDuty, email or a webhook; and `forge obs check`, a before/after judgement of each new `service.version`, even when no deploy is recorded.

**Honest estimate to quote** (one service, an OTel SDK not yet installed): about 30-60 minutes. Sign in and `forge obs init`: 2 minutes. Add the OTel SDK and auto-instrumentation: 10-30 minutes per language (less when the app already uses OpenTelemetry: then it is only the variables). Store the header as a secret in the deploy system and set the variables: 10 minutes. Verify with `forge obs status <env>`: 5 minutes. Each further service in a known language: about 10 minutes.

## The steps

```bash
forge login                              # a person's sign-in (or RELIANT_TOKEN in CI)
forge obs init prod staging              # registers the envs, mints one ingest token per env, prints the variables
forge obs status prod                    # after the app has sent anything
forge obs receivers create oncall --type slack --secret "$SLACK_WEBHOOK_URL"
```

`forge obs init` works in a repo with no forge files: it offers to write a minimal `forge.yaml` (`name:`, `envs:`, `monitoring: true`; `--write-config` writes it without asking), or takes `--project <name> --no-write` and touches nothing. In a Go repo it also writes `module_path:` from `go.mod`. It refuses an env that `deploy/kcl/<env>/` already declares (`forge env deploy` wires that env's telemetry). `--json` prints a `forge.obs.init/v1` document. The ingest token is printed **once**: store the header in the deploy system's secret store, never in the repo. `forge obs tokens list|create|rotate|revoke` manages the rest of its life: a token per CI system or cluster, `rotate --grace 24h` before expiry (the old one keeps working through the grace), `revoke` for a leaked one (the collector drops it within minutes). Minting, rotating and revoking need the `obs:write` permission (owners and admins hold it).

## The variables (identical in every language)

| Variable | Value |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `https://otlp.reliantapi.com` (what `obs init` prints) |
| `OTEL_EXPORTER_OTLP_HEADERS` | `authorization=Bearer <token>`: a secret |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` |
| `OTEL_SERVICE_NAME` | one per deployable: `checkout-api`, `worker`, `web` |
| `OTEL_RESOURCE_ATTRIBUTES` | `deployment.environment.name=<env>,service.namespace=<project>,service.version=<version>` |

`service.version` is the one value worth wiring with care: set it to what you deploy (a git SHA or release tag, from CI). Release health, the issue inbox's "first seen in" and the version share on every check all key on it. `deployment.environment.name` must equal the forge env name you passed to `obs init`. The organization and environment a span lands in come from the token, never from these attributes, so a mistyped env name mislabels nothing outside its own env.

## Per language

| Language | Install | Run |
|---|---|---|
| Node.js | `npm i @opentelemetry/api @opentelemetry/auto-instrumentations-node` | `node --require @opentelemetry/auto-instrumentations-node/register app.js` |
| Python | `pip install opentelemetry-distro opentelemetry-exporter-otlp && opentelemetry-bootstrap -a install` | `opentelemetry-instrument python app.py` |
| Java / Kotlin | download `opentelemetry-javaagent.jar` | `java -javaagent:opentelemetry-javaagent.jar -jar app.jar` |
| .NET | `dotnet add package OpenTelemetry.AutoInstrumentation` | run with the auto-instrumentation launcher script |
| Ruby | `gem install opentelemetry-sdk opentelemetry-exporter-otlp opentelemetry-instrumentation-all` | `OpenTelemetry::SDK.configure { \|c\| c.use_all }` in an initializer |
| Go | `go.opentelemetry.io/otel`, `otlptracehttp`, `otlpmetrichttp`, `otelhttp` / `otelgrpc` | wrap the handlers; or use `forge/pkg/observe`, which reads the same variables |

Every SDK above reads the variables; nothing forge-specific goes in the code. The ingest token is a server-side secret: never ship it to a browser or a mobile app.

## The contract forge's own apps follow (follow it and every view lights up)

- **Spans:** one SERVER span per inbound request with `http.route` (or `rpc.method`) and the response status; errors set the span status to ERROR and record the exception (`exception.type`, `exception.message`, `exception.stacktrace`). Issues are grouped from those exception events.
- **Logs:** JSON lines with `trace_id` / `span_id` when a span is active (the SDK's log bridge does it), severity in the standard field. Export logs over OTLP (`OTEL_LOGS_EXPORTER=otlp`) when no agent already ships stdout: never both, or every line ships twice.
- **Metrics:** the SDK's HTTP/RPC server duration histograms are enough for the RED dashboards; runtime metrics are a bonus.
- **Never** set attributes under `reliant.*`: the collector deletes them and stamps its own from the token.

## What is different from a forge-deployed app

- `forge obs check` with no deploy recorded judges the newest `service.version` from the first time telemetry showed it: the output says `marker INFERRED`. A canary or a slow rollout moves that moment, so read it as a weaker marker than a recorded deploy.
- `forge env status --signal app`'s runtime checks (`/healthz`, the JSON log shape) are forge-runtime facts; they do not apply.
- Per-env alert rules and their routes come from a deployed env's declaration (`observability/alerts`); a monitoring-only env gets the built-in alerts (new and regressed issues, quota) and the org's receivers.
- `monitoring: {endpoint: https://otel.example.com}` in forge.yaml sends to a self-hosted OTLP backend instead: `obs init` prints the variables for it and mints nothing, and the `forge obs` read verbs cannot query it.

## Where it is declared

`forge.yaml` holds the switch and the env names (`envs:`, `monitoring:`), and the `forge obs` verbs read them through the same model a KCL env lowers into. An env that later moves to `deploy/kcl/<env>/main.k` is KCL's: remove it from `envs:` (declaring the same env's monitoring in both is a load error naming both). `--project` and `--control-plane` on any `forge obs` verb stand in for forge.yaml in a repo with none.
